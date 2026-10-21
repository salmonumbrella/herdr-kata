package runner

import (
	"context"
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"time"
)

func (m *Manager) proof(ctx context.Context, ref HerdrRef) (herdr.Agent, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if ref.Session == nil {
		return herdr.Agent{}, errNoSession
	}
	if ref.Session.Source == "" || ref.Session.Kind == "" || ref.Session.Value == "" || ref.TerminalID == "" {
		return herdr.Agent{}, errUnproven
	}
	tab, e := m.h.TabGet(ctx, ref.TabID)
	if e != nil {
		return herdr.Agent{}, e
	}
	if tab.PaneCount != 1 || tab.TabID != ref.TabID || tab.WorkspaceID != ref.WorkspaceID {
		return herdr.Agent{}, errUnproven
	}
	a, e := m.occupant(ctx, ref)
	if e != nil {
		return a, e
	}
	pane, e := m.h.PaneGet(ctx, ref.PaneID)
	if e != nil {
		return a, e
	}
	if a.Session == nil || *a.Session != *ref.Session || pane.Session == nil || *pane.Session != *ref.Session {
		return a, errUnproven
	}
	return a, nil
}
func (m *Manager) cleanup(ctx context.Context, uid string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Serialize cleanup with release through the same journal lock. Re-read
	// under that lock before any destructive RPC.
	lock := m.store.lock(uid)
	lock.Lock()
	defer lock.Unlock()
	r, e := m.store.load(uid)
	if e != nil {
		return e
	}

	if m.h == nil {
		return nil
	}
	var errorsFound []error
	seen := map[string]bool{}
	for _, ref := range agentReferences(r) {
		if ref.TabID == "" || seen[ref.TabID] {
			continue
		}
		seen[ref.TabID] = true
		if e := m.cleanupAgent(ctx, r, ref); e != nil {
			errorsFound = append(errorsFound, e)
		}
	}
	return errors.Join(errorsFound...)
}
func (m *Manager) cleanupAgent(ctx context.Context, r *Run, ref HerdrRef) error {
	phase := string(r.Phase)
	if r.Phase == Settled && r.Outcome == "succeeded" {
		a, e := m.proof(ctx, ref)
		if e == nil && settledAgent(a) {
			return m.h.TabClose(ctx, ref.TabID)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		phase = "unproven"
	} else if r.Phase == Settled {
		phase = r.Outcome
	}
	if _, e := m.occupant(ctx, ref); e != nil {
		return nil
	}
	return m.h.PaneReportMetadata(ctx, ref.PaneID, map[string]string{"kata_ref": r.IssueRef, "run": r.UID, "phase": phase})
}
