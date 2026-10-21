package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
)

func settledAgent(a herdr.Agent) bool { return a.Status == "idle" || a.Status == "done" }
func readyAgent(a herdr.Agent) bool {
	return settledAgent(a) && a.InteractiveReady && !a.LaunchPending
}
func (m *Manager) matches(ref HerdrRef, a herdr.Agent) bool {
	id, e := herdr.ReadServerID(m.h.Path)
	return e == nil && id == ref.ServerID && id != (herdr.ServerID{}) && a.Name == ref.AgentName && a.PaneID == ref.PaneID && a.TerminalID == ref.TerminalID && a.TabID == ref.TabID && a.WorkspaceID == ref.WorkspaceID && (ref.Session == nil || a.Session == nil || *ref.Session == *a.Session)
}
func (m *Manager) hold(uid, reason string) error { return m.holdStopped(uid, reason, false) }
func (m *Manager) holdStopped(uid, reason string, stopped bool, parkedStep ...string) error {
	_, e := m.store.Transition(uid, func(r *Run) error {
		r.Phase = Held
		r.Outcome = "unknown"
		r.Reason = reason
		if len(parkedStep) > 0 {
			r.ActiveStep = parkedStep[0]
		}
		r.SlotReleased = r.SlotReleased || stopped
		return nil
	})
	return e
}
func (m *Manager) occupant(ctx context.Context, ref HerdrRef) (herdr.Agent, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a, e := m.h.AgentGet(ctx, ref.AgentName)
	if e != nil {
		return a, e
	}
	pane, e := m.h.PaneGet(ctx, ref.PaneID)
	if e != nil {
		return a, e
	}
	if !m.matches(ref, a) || pane.PaneID != ref.PaneID || pane.TabID != ref.TabID || pane.WorkspaceID != ref.WorkspaceID || pane.TerminalID != ref.TerminalID || (ref.Session != nil && pane.Session != nil && *ref.Session != *pane.Session) || (a.Session != nil && pane.Session != nil && *a.Session != *pane.Session) {
		return a, errUnproven
	}
	return a, nil
}

var errUnproven = errors.New("unproven Herdr occupant")

// errNoSession is an unproven occupant whose agent never reported a Herdr
// session, as with a Codex profile that turns its hooks off.
var errNoSession = fmt.Errorf("%w: agent reported no Herdr session", errUnproven)

// switchUnproven names the hold for an agent switch that proof refused.
func switchUnproven(e error) string {
	if errors.Is(e, errNoSession) {
		return "agent_switch_unproven: agent reported no Herdr session"
	}
	return "agent_switch_unproven"
}

// Session identity can appear after the first turn. Capture it once from a
// matching runtime observation; an existing identity is never replaced.
func (m *Manager) saveSession(r *Run, a herdr.Agent) error {
	if !m.matches(*AgentReference(r), a) {
		return errUnproven
	}
	if AgentReference(r).Session == nil && a.Session != nil {
		session := *a.Session
		AgentReference(r).Session = &session
	}
	return nil
}

func (m *Manager) observeSession(uid string, a herdr.Agent) error {
	r, e := m.store.Load(uid)
	if e != nil {
		return e
	}
	if !m.matches(*AgentReference(r), a) {
		return errUnproven
	}
	if AgentReference(r).Session != nil || a.Session == nil {
		return nil
	}
	_, e = m.store.Transition(uid, func(r *Run) error { return m.saveSession(r, a) })
	return e
}

func (m *Manager) launch(ctx context.Context, uid string, indices ...int) (err error) {
	index := 0
	if len(indices) > 0 {
		index = indices[0]
	}
	return m.launchPrepared(ctx, uid, index, nil)
}

func (m *Manager) launchPrepared(ctx context.Context, uid string, index int, secrets map[string]string) (err error) {
	callerCtx := ctx
	r, e := m.selectAgent(ctx, uid, index)
	if e != nil {
		if errors.Is(e, errUnproven) {
			return m.hold(uid, switchUnproven(e))
		}
		return e
	}
	remaining := r.ExpiresAt.Sub(m.clock.Now())
	if remaining <= 0 {
		return m.hold(uid, "timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, min(80*time.Second, remaining))
	defer cancel()
	defer func() {
		if errors.Is(err, context.DeadlineExceeded) && callerCtx.Err() == nil {
			err = m.hold(uid, "launch_timeout")
		}
	}()
	if m.h == nil {
		return errors.New("herdr client unavailable")
	}
	// Resolve before creating any pane. Recovering a possibly submitted
	// occupant must still follow the saved launch intent below.
	if AgentReference(r).PaneID == "" && secrets == nil {
		secrets, e = resolveSecrets(m.cfg, r.JobDef)
		if e != nil {
			return m.holdSecret(uid, index, e)
		}
	}
	provenServer, e := m.checkEnvironment(ctx, r.Cwd, AgentReference(r).ReportBin)
	if e != nil {
		if callerCtx.Err() != nil {
			return callerCtx.Err()
		}
		return m.holdEnvironment(uid, index, e)
	}
	// Persist the complete unsent prompt after preflight but before any agent
	// can start. Possibly submitted attempts keep their frozen artifact.
	if r.Steps[index].Submission == None || r.Steps[index].Submission == Rejected {
		if e = m.writePrompt(r, index); e != nil {
			return m.holdStopped(uid, "invalid_prompt", prelaunchStopped(r))
		}
	}
	if AgentReference(r).PaneID == "" {
		ws, e := m.ensureWorkspace(ctx)
		if e != nil {
			return e
		}
		if ws.ServerID != provenServer {
			return m.holdEnvironment(uid, index, errors.New("herdr server changed after environment probe"))
		}
		report := AgentReference(r).ReportBin
		if report == "" {
			report, e = os.Executable()
			if e != nil {
				return e
			}
		}
		env := m.tabEnvironment(uid, m.store.layout.RunDir(uid), report, secrets)
		label := fmt.Sprintf("%s · %.8s", r.Job.Name, uid)
		tab, pane, e := m.h.TabCreate(ctx, ws.WorkspaceID, label, r.Cwd, env)
		if e != nil {
			return e
		}
		r, e = m.store.Transition(uid, func(r *Run) error {
			*AgentReference(r) = HerdrRef{ServerID: ws.ServerID, WorkspaceID: ws.WorkspaceID, TabID: tab.TabID, PaneID: pane.PaneID, TerminalID: pane.TerminalID, ReportBin: report, HasSubmitted: new(bool)}
			return nil
		})
		if e != nil {
			return e
		}
	}
	// Query a saved launch intent before ever attempting another start.
	if AgentReference(r).LaunchIntent {
		a, e := m.occupant(ctx, *AgentReference(r))
		if e == nil {
			return m.waitReady(ctx, uid, a, index)
		}
		if !herdrCode(e, "agent_not_found") {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return m.hold(uid, "launch_unproven")
		}
		// Also query by pane: a foreign name must not be mistaken for an empty pane.
		a, e = m.h.AgentGet(ctx, AgentReference(r).PaneID)
		if e == nil {
			if !m.matches(*AgentReference(r), a) {
				return m.hold(uid, "launch_unproven")
			}
			return m.waitReady(ctx, uid, a, index)
		}
		if !herdrCode(e, "agent_not_found") {
			return m.hold(uid, "launch_unproven")
		}
	}
	shellDeadline := m.clock.Now().Add(10 * time.Second)
	shellCtx, shellCancel := context.WithTimeout(ctx, 10*time.Second)
	defer shellCancel()
	for {
		p, e := m.h.PaneProcessInfo(shellCtx, AgentReference(r).PaneID)
		if e != nil {
			return e
		}
		if p.ShellPID != nil && p.ForegroundProcessGroupID != nil && *p.ShellPID == *p.ForegroundProcessGroupID {
			break
		}
		if !m.clock.Now().Before(shellDeadline) {
			return m.hold(uid, "pane_busy")
		}
		if e = m.pause(shellCtx, 300*time.Millisecond); e != nil {
			return e
		}
	}
	id, e := herdr.ReadServerID(m.h.Path)
	if e != nil || id != provenServer {
		return m.holdEnvironment(uid, index, errors.New("herdr server changed after environment probe"))
	}
	if id != AgentReference(r).ServerID {
		return m.hold(uid, "launch_unproven")
	}
	pane, e := m.h.PaneGet(ctx, AgentReference(r).PaneID)
	if e != nil {
		return e
	}
	if AgentReference(r).TerminalID == "" || pane.PaneID != AgentReference(r).PaneID || pane.TerminalID != AgentReference(r).TerminalID || pane.TabID != AgentReference(r).TabID || pane.WorkspaceID != AgentReference(r).WorkspaceID {
		return m.hold(uid, "launch_unproven")
	}
	r, e = m.store.Transition(uid, func(r *Run) error {
		AgentReference(r).AgentName = agentName(r, index)
		AgentReference(r).LaunchIntent = true
		AgentReference(r).LaunchedAt = m.clock.Now().UTC()
		r.Phase = Running
		if r.StartedAt == nil {
			now := m.clock.Now()
			r.StartedAt = &now
		}
		return nil
	})
	if e != nil {
		return e
	}
	if e = m.h.PaneReportMetadata(ctx, AgentReference(r).PaneID, map[string]string{"project": m.cfg.Kata.Project, "kata_ref": r.IssueRef, "run": uid, "phase": "running"}); e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("run pane metadata unavailable", "run", uid, "error", e)
	}
	busyDeadline := m.clock.Now().Add(10 * time.Second)
	startCtx, startCancel := context.WithTimeout(ctx, 10*time.Second)
	defer startCancel()
	for {
		_, profile := StepProfile(r, index)
		a, e := m.h.AgentStart(startCtx, AgentReference(r).AgentName, profile.Kind, AgentReference(r).PaneID, profile.Args)
		if e == nil {
			if !m.matches(*AgentReference(r), a) {
				return m.hold(uid, "launch_unproven")
			}
			return m.waitReady(ctx, uid, a, index)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if herdrCode(e, "agent_pane_busy") && m.clock.Now().Before(busyDeadline) {
			if e = m.pause(startCtx, 300*time.Millisecond); e != nil {
				return e
			}
			continue
		}
		// Name collision and lost replies are reconciled by both name and pane.
		startErr := e
		a, e = m.occupant(ctx, *AgentReference(r))
		if e != nil {
			if herdrCode(e, "agent_not_found") && m.failedStartStopped(ctx, *AgentReference(r)) {
				reason := "agent_start_failed"
				if he, ok := errors.AsType[*herdr.Error](startErr); ok {
					reason += ": " + he.Code
				}
				return m.holdStopped(uid, reason, true)
			}
			return m.hold(uid, "launch_unproven")
		}
		byPane, e := m.h.AgentGet(ctx, AgentReference(r).PaneID)
		if e != nil || !m.matches(*AgentReference(r), byPane) {
			return m.hold(uid, "launch_unproven")
		}
		return m.waitReady(ctx, uid, a, index)
	}
}

// An absent agent alone cannot release a possibly-live slot. Require absence
// by pane, unchanged provenance and the saved shell in the foreground too.
func (m *Manager) failedStartStopped(ctx context.Context, ref HerdrRef) bool {
	if _, e := m.h.AgentGet(ctx, ref.PaneID); !herdrCode(e, "agent_not_found") {
		return false
	}
	id, e := herdr.ReadServerID(m.h.Path)
	if e != nil || id == (herdr.ServerID{}) || id != ref.ServerID {
		return false
	}
	pane, e := m.h.PaneGet(ctx, ref.PaneID)
	if e != nil || pane.PaneID != ref.PaneID || pane.TabID != ref.TabID || pane.WorkspaceID != ref.WorkspaceID || pane.TerminalID != ref.TerminalID {
		return false
	}
	p, e := m.h.PaneProcessInfo(ctx, ref.PaneID)
	return e == nil && p.PaneID == ref.PaneID && p.ShellPID != nil && p.ForegroundProcessGroupID != nil && *p.ShellPID == *p.ForegroundProcessGroupID
}
func (m *Manager) waitReady(ctx context.Context, uid string, a herdr.Agent, indices ...int) (err error) {
	index := 0
	if len(indices) > 0 {
		index = indices[0]
	}
	callerCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	defer func() {
		if errors.Is(err, context.DeadlineExceeded) && callerCtx.Err() == nil {
			err = m.hold(uid, "launch_timeout")
		}
	}()
	until := m.clock.Now().Add(60 * time.Second)
	for {
		r, e := m.store.Load(uid)
		if e != nil {
			return e
		}
		if !m.matches(*AgentReference(r), a) {
			return m.hold(uid, "launch_unproven")
		}
		if a.Status == "blocked" {
			return m.blockedStep(uid, index, true)
		}
		if readyAgent(a) {
			_, e = m.store.Transition(uid, func(r *Run) error {
				if a.Session != nil {
					AgentReference(r).Session = a.Session
				}
				r.Phase = Running
				return nil
			})
			return e
		}
		if !m.clock.Now().Before(until) || !m.clock.Now().Before(r.ExpiresAt) {
			return m.hold(uid, "launch_timeout")
		}
		if e = m.pause(ctx, 500*time.Millisecond); e != nil {
			return e
		}
		a, e = m.occupant(ctx, *AgentReference(r))
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if herdrCode(e, "agent_not_found") && m.failedStartStopped(ctx, *AgentReference(r)) {
				return m.holdStopped(uid, "agent_start_failed", true)
			}
			return m.hold(uid, "launch_unproven")
		}
	}
}
