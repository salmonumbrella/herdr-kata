// Package defs freezes portable definitions into immutable execution plans.
package defs

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

type Plan struct {
	JobUID, JobEventUID, WorkflowUID, WorkflowEventUID string
	Name, WorkflowName, About                          string
	Steps                                              []Step
	Issue                                              IssuePolicy
	Overlap, Catchup                                   string
	Grace, Timeout                                     time.Duration
	CheckoutPath, Input                                string
}
type IssuePolicy struct {
	Kind, UID, Title, Body string
	DeadlineOffsetSeconds  *int64
}
type Step struct {
	Key, Kind, Text string
	After           []string
	Retries         int
	Argv            []string
	SkipExit        int
}

func text(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func option(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func unsupported(m map[string]any) []string {
	var reasons []string
	for _, key := range []string{"on_fail", "findings"} {
		if _, ok := m[key]; ok {
			reasons = append(reasons, "loops_unsupported")
		}
	}
	if option(m, "on_failure") == "park" {
		reasons = append(reasons, "park_unsupported")
	}
	if mode, ok := m["agent_mode"]; ok && mode != "fresh" {
		reasons = append(reasons, "agent_mode_unsupported")
	}
	if _, ok := m["run_parent"]; ok {
		reasons = append(reasons, "run_parent_unsupported")
	}
	if _, ok := m["secret_refs"]; ok {
		reasons = append(reasons, "secrets_unsupported")
	}
	return reasons
}

func Freeze(job kata.CronJob, wfs map[string]kata.CronWorkflow, cfg config.Config, project string, look func(string) (string, error)) (Plan, []string) {
	d := job.Definition
	p := Plan{JobUID: job.UID, JobEventUID: job.DefinitionEventUID, Name: job.Name, Overlap: d.Overlap, Catchup: d.Catchup, Timeout: cfg.Limits.DefaultTimeout, Input: option(d.Options, "input")}
	reasons := unsupported(d.Options)
	if err := sched.Validate(d.Trigger, job.CreatedAt); err != nil {
		reasons = append(reasons, "invalid_trigger")
	}
	if adapter, ok := d.Options["adapter"]; ok && adapter != "herdr-kata" {
		reasons = append(reasons, fmt.Sprintf("adapter: %v", adapter))
	}
	if d.Action.Kind == "notify" {
		reasons = append(reasons, "notify_unsupported")
		if d.Issue != nil && d.Issue.Kind == "per-run" {
			reasons = append(reasons, "per-run notify belongs to the hub")
		}
	}
	if len(d.SecretRefs) > 0 {
		reasons = append(reasons, "secrets_unsupported")
	}
	if d.Issue != nil {
		i := d.Issue
		p.Issue = IssuePolicy{Kind: i.Kind, UID: text(i.UID), Title: text(i.Title), Body: text(i.Body)}
		if i.DeadlineOffsetSeconds != nil {
			p.Issue.DeadlineOffsetSeconds = new(*i.DeadlineOffsetSeconds)
		}
		if i.ScheduledOffsetSeconds != nil {
			reasons = append(reasons, "scheduled_offset_unsupported")
		}
	}
	if d.TimeoutSeconds != nil {
		if *d.TimeoutSeconds > int64(cfg.Limits.MaxTimeout/time.Second) {
			p.Timeout = cfg.Limits.MaxTimeout
		} else {
			p.Timeout = time.Duration(*d.TimeoutSeconds) * time.Second
		}
	}
	if p.Timeout > cfg.Limits.MaxTimeout {
		p.Timeout = cfg.Limits.MaxTimeout
	}
	if d.GraceSeconds != nil {
		p.Grace = time.Duration(*d.GraceSeconds) * time.Second
	}
	p.CheckoutPath = cfg.Checkouts[text(d.CheckoutKey)]
	if !filepath.IsAbs(p.CheckoutPath) {
		reasons = append(reasons, "checkout not configured: "+text(d.CheckoutKey))
	}
	var steps []kata.WorkflowStep
	if d.Action.WorkflowUID != nil {
		wf, ok := wfs[*d.Action.WorkflowUID]
		if !ok || wf.DeletedAt != nil {
			reasons = append(reasons, "workflow not found: "+*d.Action.WorkflowUID)
		} else {
			p.WorkflowUID = wf.UID
			p.WorkflowEventUID = wf.DefinitionEventUID
			p.WorkflowName = wf.Name
			p.About = text(wf.Definition.About)
			reasons = append(reasons, unsupported(wf.Definition.Options)...)
			steps = wf.Definition.Steps
		}
	} else if d.Action.Prompt != nil {
		steps = []kata.WorkflowStep{{Key: "main", Kind: "prompt", Prompt: d.Action.Prompt}}
	}
	agent := option(d.Options, "agent")
	if agent == "" {
		agent = "default"
	}
	commands := false
	for _, projectCfg := range cfg.Projects {
		if projectCfg.Name == project {
			commands = projectCfg.Commands
		}
	}
	for _, s := range steps {
		reasons = append(reasons, unsupported(s.Options)...)
		frozen := Step{Key: s.Key, Kind: s.Kind, After: slices.Clone(s.After)}
		if s.Retries != nil {
			frozen.Retries = int(*s.Retries)
		}
		switch s.Kind {
		case "prompt":
			frozen.Text = text(s.Prompt)
			profile := agent
			if override := option(s.Options, "agent"); override != "" {
				profile = override
			}
			argv := slices.Clone(cfg.Profiles[profile].Argv)
			if len(argv) == 0 {
				reasons = append(reasons, "profile not configured: "+profile)
			} else {
				resolved, err := look(argv[0])
				if err != nil || !filepath.IsAbs(resolved) {
					reasons = append(reasons, "profile executable unavailable: "+profile)
				} else {
					argv[0] = resolved
					frozen.Argv = argv
				}
			}
		case "command":
			frozen.Text = text(s.Command)
			if !commands {
				reasons = append(reasons, "commands disabled for project")
			}
			if raw, ok := s.Options["skip_exit"]; ok {
				n, err := exitCode(raw)
				if err != nil || n < 1 || n > 255 {
					reasons = append(reasons, "skip_exit must be an exit code 1–255")
				} else {
					frozen.SkipExit = n
				}
			}
		default:
			reasons = append(reasons, "unsupported step kind: "+s.Kind)
		}
		p.Steps = append(p.Steps, frozen)
	}
	// Preserve deterministic reason order while dropping duplicate scope findings.
	unique := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if !slices.Contains(unique, reason) {
			unique = append(unique, reason)
		}
	}
	return p, unique
}

func exitCode(v any) (int, error) {
	switch n := v.(type) {
	case json.Number:
		return strconv.Atoi(n.String())
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		if n == float64(int(n)) {
			return int(n), nil
		}
	}
	return 0, fmt.Errorf("not an integer")
}
