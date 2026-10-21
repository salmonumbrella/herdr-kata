package defs

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// AutomaticAuthorWarning explains why a supported job has no automatic fires.
const AutomaticAuthorWarning = "automatic scheduling disabled: original author does not match lane actor"

type Reader interface {
	ListJobs(context.Context) ([]kata.Job, error)
	ListWorkflows(context.Context) ([]kata.Workflow, error)
}
type Snapshot struct {
	Version   int             `json:"version"`
	Jobs      []JobEntry      `json:"jobs"`
	Workflows []kata.Workflow `json:"workflows"`
}

type JobEntry struct {
	IssueRefs          map[string]string `json:"issue_refs,omitempty"`
	ProjectUID         string            `json:"-"`
	Project            string            `json:"project,omitempty"`
	Definition         json.RawMessage   `json:"definition,omitempty"`
	DeletedAt          *time.Time        `json:"deleted_at,omitempty"`
	Trigger            *Trigger          `json:"trigger,omitempty"`
	CreatedAt          time.Time         `json:"created_at,omitempty"`
	UpdatedAt          time.Time         `json:"updated_at,omitzero"`
	UID                string            `json:"uid"`
	Name               string            `json:"name"`
	DefinitionEventUID string            `json:"definition_event_uid"`
	Owner              Owner             `json:"owner"`
	Supported          bool              `json:"supported"`
	Reasons            []string          `json:"reasons,omitempty"`
	Warnings           []string          `json:"warnings,omitempty"`
	LastSkip           string            `json:"last_skip,omitempty"`
	Waiting            string            `json:"waiting,omitempty"`
}
type Cache struct {
	mu      sync.Mutex
	refresh sync.Mutex
	cfg     config.Config
	k       Reader
	fs      state.FS
	l       state.Layout
	entries []JobEntry
	// The rows of the latest successful refresh, as Kata returned them.
	listed    bool
	jobs      []kata.Job
	workflows []kata.Workflow
}

func NewCache(c config.Config, k Reader, fs state.FS, l state.Layout) *Cache {
	return &Cache{cfg: c, k: k, fs: fs, l: l}
}
func (c *Cache) Refresh(ctx context.Context) error {
	return c.refreshDefinitions(ctx, "")
}

// RefreshForJob updates the full snapshot while reporting only the selected job.
func (c *Cache) RefreshForJob(ctx context.Context, job string) error {
	return c.refreshDefinitions(ctx, job)
}

func (c *Cache) refreshDefinitions(ctx context.Context, job string) error {
	c.refresh.Lock()
	defer c.refresh.Unlock()
	jobs, err := c.k.ListJobs(ctx)
	if err != nil {
		return err
	}
	workflows, err := c.k.ListWorkflows(ctx)
	if err != nil {
		return err
	}
	listedJobs, listedWorkflows := slices.Clone(jobs), slices.Clone(workflows)
	wf := map[string]*WorkflowDef{}
	workflowErrors := map[string]string{}
	for _, w := range workflows {
		if w.DeletedAt == nil {
			if d, e := DecodeWorkflow(w.Definition); e == nil {
				wf[w.UID] = &d
			} else {
				workflowErrors[w.UID] = "workflow decode: " + e.Error()
			}
		}
	}
	// Preserve operator-visible decisions across definition refresh and restart.
	previous, _ := ReadSnapshot(c.fs, c.l.DefsSnapshot())
	oldEntries := map[string]JobEntry{}
	for _, e := range previous.Jobs {
		oldEntries[e.UID] = e
	}
	entries := make([]JobEntry, 0, len(jobs))
	for _, j := range jobs {
		e := JobEntry{CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, Definition: snapshotDefinition(j.Definition), DeletedAt: j.DeletedAt, UID: j.UID, Name: j.Name, DefinitionEventUID: j.DefinitionEventUID}
		d, err := DecodeJob(j.Definition)
		if err != nil {
			e.Reasons = []string{err.Error()}
		} else {
			e.Trigger = &d.Trigger
			owner, support := Classify(d, wf[d.Action.WorkflowUID], c.cfg)
			e.Owner = owner
			e.Supported = support.Supported
			e.Reasons = support.Reasons
			if reason := workflowErrors[d.Action.WorkflowUID]; reason != "" {
				e.Reasons = append(e.Reasons, reason)
			}
			e.Warnings = support.Warnings
			if j.DeletedAt != nil {
				e.Supported = false
				e.Reasons = append(e.Reasons, "deleted")
			}
		}
		if old, ok := oldEntries[j.UID]; ok && old.DefinitionEventUID == j.DefinitionEventUID {
			e.LastSkip = old.LastSkip
			e.Waiting = old.Waiting
		}
		if e.Supported && e.Owner == Adapter && e.Trigger != nil && e.Trigger.Kind != "manual" && (c.cfg.Kata.Actor == "" || j.Author != c.cfg.Kata.Actor) {
			e.Warnings = append(e.Warnings, AutomaticAuthorWarning)
		}
		entries = append(entries, e)
	}
	for i := range workflows {
		workflows[i].Definition = snapshotDefinition(workflows[i].Definition)
	}
	if err := c.definitionReferences(ctx, entries, previous.Jobs); err != nil {
		return err
	}
	if err := state.WriteJSON(c.fs, c.l.DefsSnapshot(), Snapshot{Version: 2, Jobs: entries, Workflows: workflows}); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	old := map[string]string{}
	for _, e := range c.entries {
		old[e.UID] = e.DefinitionEventUID
	}
	for _, e := range entries {
		if job != "" && e.UID != job && e.Name != job {
			continue
		}
		if e.Trigger != nil && e.Owner == Ignored {
			continue
		}
		if old[e.UID] != e.DefinitionEventUID && len(e.Reasons) > 0 {
			slog.Warn("unsupported definition", "job", e.UID, "reasons", e.Reasons)
		}
	}
	c.entries = entries
	c.listed, c.jobs, c.workflows = true, listedJobs, listedWorkflows
	return nil
}

// Listing returns the jobs and workflows of the latest successful refresh,
// or lists them from Kata before the first one.
func (c *Cache) Listing(ctx context.Context) ([]kata.Job, []kata.Workflow, error) {
	c.mu.Lock()
	if c.listed {
		defer c.mu.Unlock()
		return slices.Clone(c.jobs), slices.Clone(c.workflows), nil
	}
	c.mu.Unlock()
	jobs, err := c.k.ListJobs(ctx)
	if err != nil {
		return nil, nil, err
	}
	workflows, err := c.k.ListWorkflows(ctx)
	return jobs, workflows, err
}
func (c *Cache) Snapshot() []JobEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]JobEntry, len(c.entries))
	for i, e := range c.entries {
		if e.Trigger != nil {
			t := *e.Trigger
			e.Trigger = &t
		}
		e.IssueRefs = maps.Clone(e.IssueRefs)
		e.Reasons = append([]string(nil), e.Reasons...)
		e.Warnings = append([]string(nil), e.Warnings...)
		out[i] = e
	}
	return out
}

// RecordDecision saves visibility only; checkpoints remain the scheduling authority.
func (c *Cache) RecordDecision(job, reason string, dropped bool) error {
	c.refresh.Lock()
	defer c.refresh.Unlock()
	snap, err := ReadSnapshot(c.fs, c.l.DefsSnapshot())
	if err != nil {
		return err
	}
	changed := false
	for i := range snap.Jobs {
		e := &snap.Jobs[i]
		if e.UID != job {
			continue
		}
		before := *e
		if dropped {
			e.LastSkip = reason
			e.Waiting = ""
		} else {
			e.Waiting = reason
		}
		changed = e.LastSkip != before.LastSkip || e.Waiting != before.Waiting
	}
	if !changed {
		return nil
	}
	if err = state.WriteJSON(c.fs, c.l.DefsSnapshot(), snap); err != nil {
		return err
	}
	c.mu.Lock()
	c.entries = snap.Jobs
	c.mu.Unlock()
	return nil
}
