package attempt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/prompt"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// stop preserves the decision before any signal and requires stop proof before
// freeing capacity. ExpiringAt also resumes termination saved by older workers.
func stop(ctx context.Context, h hc.HC, st *journal.Store, key string, idx int, a journal.Attempt, kind, reason string, now time.Time) (Outcome, error) {
	lost := "agent_lost"
	if kind == "command" {
		lost = "command_outcome_unknown"
	}
	check := func() (hc.Terminal, Outcome, error) {
		t, err := guarded(ctx, h, a)
		if errors.Is(err, hc.ErrNotFound) {
			out, e := held(st, key, idx, lost)
			return t, out, e
		}
		if errors.Is(err, ErrOccupantChanged) {
			out, e := held(st, key, idx, "occupant_changed")
			return t, out, e
		}
		if err != nil {
			return t, Pending{}, err
		}
		return t, nil, nil
	}
	term, out, err := check()
	if out != nil {
		return out, err
	}
	observeStartup := func() (bool, error) {
		if a.StopReason != "" || reason != "agent_not_started" {
			return false, nil
		}
		active := term.Status == "working" || term.Turn > 0
		if !active {
			r, e := st.Load(key)
			if e != nil {
				return false, e
			}
			b, e := prompt.ReadRegular(filepath.Join(st.Layout.Attempt(key, a.Step, a.N), "result.json"), 64*1024)
			if e == nil {
				_, e = prompt.ParseResult(b, r.RunUID, a.Step, a.N)
				active = e == nil
			}
		}
		if !active {
			return false, nil
		}
		a.TaskStarted = new(true)
		return true, saveAttempt(st, key, idx, a)
	}
	if active, e := observeStartup(); active || e != nil {
		return Pending{}, e
	}
	if a.StopReason == "" {
		if kind == "prompt" {
			text, complete, e := h.Read(ctx, a.Address, 200)
			if e != nil {
				return Pending{}, e
			}
			text += "\n"
			if !complete {
				text += "[terminal output incomplete]\n"
			}
			if e = state.WriteFile(filepath.Join(st.Layout.Attempt(key, a.Step, a.N), "output.log"), []byte(text)); e != nil {
				return Pending{}, e
			}
		}
		term, out, err = check()
		if out != nil {
			return out, err
		}
		if active, e := observeStartup(); active || e != nil {
			return Pending{}, e
		}
		a.StopReason = reason
		first := a.ExpiringAt == nil
		if first {
			a.ExpiringAt = new(clock.Current(ctx, now))
		}
		if err = saveAttempt(st, key, idx, a); err != nil {
			return Pending{}, err
		}
		if kind == "prompt" && first && term.State == "live" {
			if _, out, err = check(); out != nil {
				return out, err
			}
			return Pending{}, h.Interrupt(ctx, a.Address)
		}
	}
	if term.State == "live" {
		if kind == "prompt" && clock.Current(ctx, now).Before(a.ExpiringAt.Add(10*time.Second)) {
			return Pending{}, nil
		}
		if term, out, err = check(); out != nil {
			return out, err
		}
		if term.State == "live" {
			// Even a lost kill reply can be resolved by a matching finished row.
			_ = h.Kill(ctx, a.Address)
			if term, out, err = check(); out != nil {
				return out, err
			}
		}
	}
	if term.State != "finished" {
		return held(st, key, idx, a.StopReason)
	}
	artifact := "result.json"
	if kind == "command" {
		artifact = "receipt.json"
	}
	_, err = os.Lstat(filepath.Join(st.Layout.Attempt(key, a.Step, a.N), artifact))
	if err == nil {
		return held(st, key, idx, a.StopReason)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Pending{}, err
	}
	return accept(ctx, h, st, key, idx, a, "failed", "", "", "remove")
}
