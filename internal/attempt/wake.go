package attempt

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/prompt"
)

// Wake binds the borrowed terminal before typing and never cleans it up.
func Wake(ctx context.Context, h hc.HC, st *journal.Store, key string, idx int, now time.Time) (Outcome, error) {
	r, err := st.Load(key)
	if err != nil {
		return Pending{}, err
	}
	now = clock.Current(ctx, now)
	a := r.Attempts[idx]
	if a.Hold != "" {
		return Held{a.Hold}, nil
	}
	if a.Status == "succeeded" || a.Status == "failed" || a.Status == "needs-human" {
		return Done{a.Status, a.Note}, nil
	}
	if a.Address == "" {
		if !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt) {
			a.StopReason = "not_delivered"
			a.SettlementStatus = "failed"
			return finish(st, key, idx, a)
		}
		ids, e := h.List(ctx, false, map[string]string{"herdr-kata.agent": r.Plan.Agent})
		if e != nil {
			return Pending{}, e
		}
		if len(ids) != 1 {
			return Pending{}, nil
		}
		t, e := h.Get(ctx, ids[0])
		if errors.Is(e, hc.ErrNotFound) {
			return Pending{}, nil
		}
		if e != nil {
			return Pending{}, e
		}
		if t.State != "live" || !t.Reported || (t.Status != "idle" && t.Status != "done") || t.Labels["herdr-kata.agent"] != r.Plan.Agent {
			return Pending{}, nil
		}
		core, e := h.CoreID(ctx)
		if e != nil {
			return Pending{}, e
		}
		a.CoreID, a.Address, a.CreatedMs, a.PID, a.ForegroundPID, a.Turn = core, t.Address, t.CreatedMs, t.PID, t.ForegroundPID, t.Turn
		a.Status = "launching"
		a.TaskStarted = new(false)
		if e = saveAttempt(st, key, idx, a); e != nil {
			return Pending{}, e
		}
		var turn uint64
		if t.Status == "done" {
			var latest hc.Terminal
			latest, e = h.Get(ctx, a.Address)
			if errors.Is(e, hc.ErrNotFound) {
				e = hc.ErrNotSent
			} else if e == nil && (latest.State != "live" || !latest.Reported || latest.Status != "done" || latest.Address != a.Address || latest.CreatedMs != a.CreatedMs || latest.PID != a.PID || latest.ForegroundPID != a.ForegroundPID || latest.Labels["herdr-kata.agent"] != r.Plan.Agent) {
				e = hc.ErrNotSent
			}
		}
		if e == nil {
			turn, e = h.Send(ctx, a.Address, prompt.Line(filepath.Join(st.Layout.Attempt(key, a.Step, a.N), "prompt.md")), t.Status)
		} else {
			// No send was attempted, so a failed recheck can safely return to queue.
			recheckErr := e
			a.Address, a.CoreID, a.Status = "", "", "queued"
			a.CreatedMs, a.PID, a.ForegroundPID, a.Turn = 0, 0, 0, 0
			if e = saveAttempt(st, key, idx, a); e != nil {
				return Pending{}, e
			}
			if errors.Is(recheckErr, hc.ErrNotSent) {
				return Pending{}, nil
			}
			return Pending{}, recheckErr
		}
		if errors.Is(e, hc.ErrNotSent) {
			a.Address = ""
			a.CoreID = ""
			a.CreatedMs = 0
			a.PID = 0
			a.ForegroundPID = 0
			a.Turn = 0
			a.Status = "queued"
			return Pending{}, saveAttempt(st, key, idx, a)
		}
		if e != nil {
			return held(st, key, idx, "wake_uncertain")
		}
		a.Status = "running"
		a.Turn = turn
		return Pending{}, saveAttempt(st, key, idx, a)
	}
	if a.Status == "launching" {
		return held(st, key, idx, "wake_uncertain")
	}
	core, err := h.CoreID(ctx)
	if err != nil {
		return Pending{}, err
	}
	if core != a.CoreID {
		return held(st, key, idx, "agent_lost")
	}
	t, err := h.Get(ctx, a.Address)
	if errors.Is(err, hc.ErrNotFound) {
		return held(st, key, idx, "agent_lost")
	}
	if err != nil {
		return Pending{}, err
	}
	if t.CreatedMs != a.CreatedMs || t.PID != a.PID || ((t.Status == "idle" || t.Status == "done") && t.ForegroundPID != a.ForegroundPID) {
		return held(st, key, idx, "occupant_changed")
	}
	if !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt) {
		return held(st, key, idx, "timeout")
	}
	if t.Status == "error" {
		return held(st, key, idx, "agent_error")
	}
	if out, e := blocked(st, key, idx, r, &a, t); out != nil || e != nil {
		return out, e
	}
	if (a.TaskStarted == nil || !*a.TaskStarted) && (t.Status == "working" || t.Turn > a.Turn) {
		a.TaskStarted = new(true)
		if err = saveAttempt(st, key, idx, a); err != nil {
			return Pending{}, err
		}
	}
	if t.Reported && t.Turn > a.Turn && (t.Status == "idle" || t.Status == "done") {
		b, e := prompt.ReadRegular(filepath.Join(st.Layout.Attempt(key, a.Step, a.N), "result.json"), 64*1024)
		if e == nil {
			var result prompt.Result
			result, e = prompt.ParseResult(b, r.RunUID, a.Step, a.N)
			if e == nil {
				a.SettlementStatus, a.SettlementNote, a.Summary = result.Status, result.Note, result.Summary
				return finish(st, key, idx, a)
			}
		}
		if a.TurnEndedAt == nil {
			a.TurnEndedAt = new(now)
			return Pending{}, saveAttempt(st, key, idx, a)
		}
		if now.Sub(*a.TurnEndedAt) >= 10*time.Second {
			return held(st, key, idx, "no_result")
		}
	} else if a.TurnEndedAt != nil {
		a.TurnEndedAt = nil
		return Pending{}, saveAttempt(st, key, idx, a)
	}
	return Pending{}, nil
}
