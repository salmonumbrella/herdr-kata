package attempt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"path/filepath"
	"slices"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/prompt"
)

type Outcome interface{ outcome() }
type Pending struct{}
type Done struct{ Status, Note string }
type Held struct{ Reason string }
type Notify struct{ ID, Message string }

func (Pending) outcome() {}
func (Done) outcome()    {}
func (Held) outcome()    {}
func (Notify) outcome()  {}

func held(st *journal.Store, key string, idx int, reason string) (Outcome, error) {
	return Held{reason}, hold(st, key, idx, reason)
}
func saveAttempt(st *journal.Store, key string, idx int, a journal.Attempt) error {
	return st.Update(key, func(r *journal.Run) error { r.Attempts[idx] = a; return nil })
}
func finish(st *journal.Store, key string, idx int, a journal.Attempt) (Outcome, error) {
	a.Status = a.SettlementStatus
	a.Note = a.SettlementNote
	a.Cleanup = ""
	return Done{a.Status, a.Note}, saveAttempt(st, key, idx, a)
}
func cleanup(ctx context.Context, h hc.HC, st *journal.Store, key string, idx int, a journal.Attempt) (Outcome, error) {
	term, err := guarded(ctx, h, a)
	if errors.Is(err, hc.ErrNotFound) {
		return finish(st, key, idx, a)
	}
	if errors.Is(err, ErrOccupantChanged) {
		return held(st, key, idx, "occupant_changed")
	}
	if err != nil {
		return Pending{}, err
	}
	if term.State == "finished" && a.Cleanup != "remove" {
		a.Cleanup = "remove"
		return Pending{}, saveAttempt(st, key, idx, a)
	}
	switch a.Cleanup {
	case "interrupt":
		a.Cleanup = "kill"
		if err = saveAttempt(st, key, idx, a); err != nil {
			return Pending{}, err
		}
		err = h.Interrupt(ctx, a.Address)
	case "kill":
		if err = h.Kill(ctx, a.Address); err == nil {
			a.Cleanup = "remove"
			err = saveAttempt(st, key, idx, a)
		}
	case "remove":
		if term.State == "live" {
			a.Cleanup = "kill"
			return Pending{}, saveAttempt(st, key, idx, a)
		}
		if err = h.Remove(ctx, a.Address); err == nil {
			return finish(st, key, idx, a)
		}
	}
	return Pending{}, err
}
func accept(ctx context.Context, h hc.HC, st *journal.Store, key string, idx int, a journal.Attempt, status, note, summary, stage string) (Outcome, error) {
	a.SettlementStatus = status
	a.SettlementNote = note
	a.Summary = summary
	if status == "needs-human" {
		a.Status = status
		a.Note = note
		return Done{status, note}, saveAttempt(st, key, idx, a)
	}
	a.Status = "settling"
	a.Cleanup = stage
	if err := saveAttempt(st, key, idx, a); err != nil {
		return Pending{}, err
	}
	return cleanup(ctx, h, st, key, idx, a)
}
func cancelExpiry(ctx context.Context, h hc.HC, st *journal.Store, key string, idx int, a journal.Attempt, kind string, now time.Time) (Outcome, error) {
	lost := "agent_lost"
	if kind == "command" {
		lost = "command_outcome_unknown"
	}
	check := func() (hc.Terminal, Outcome, error) {
		term, err := guarded(ctx, h, a)
		if errors.Is(err, hc.ErrNotFound) {
			if a.Cleanup == "remove" && a.SettlementStatus != "" {
				out, e := finish(st, key, idx, a)
				return term, out, e
			}
			out, e := held(st, key, idx, lost)
			return term, out, e
		}
		if errors.Is(err, ErrOccupantChanged) {
			out, e := held(st, key, idx, "occupant_changed")
			return term, out, e
		}
		if err != nil {
			return term, Pending{}, err
		}
		return term, nil, nil
	}
	term, out, err := check()
	if out != nil {
		return out, err
	}
	if term.State != "finished" {
		now = clock.Current(ctx, now)
		if a.ExpiringAt == nil {
			a.ExpiringAt = new(now)
			if err = saveAttempt(st, key, idx, a); err != nil {
				return Pending{}, err
			}
			if kind == "prompt" {
				if term, out, err = check(); out != nil {
					return out, err
				}
				if term.State == "live" {
					return Pending{}, h.Interrupt(ctx, a.Address)
				}
			}
		}
		if term.State == "live" && kind == "prompt" && now.Before(a.ExpiringAt.Add(10*time.Second)) {
			return Pending{}, nil
		}
		if term, out, err = check(); out != nil {
			return out, err
		}
		if term.State == "live" {
			// Resolve even a lost kill reply from a fresh, identity-matched row.
			_ = h.Kill(ctx, a.Address)
			if term, out, err = check(); out != nil {
				return out, err
			}
		}
	}
	if term.State != "finished" {
		return held(st, key, idx, "timeout")
	}
	if !slices.Contains([]string{"exited", "killed", "signal"}, term.Reason) || term.Reason != "exited" && a.ExpiringAt == nil && a.Cleanup == "" {
		return held(st, key, idx, lost)
	}
	return Done{}, nil
}
func Settle(ctx context.Context, h hc.HC, st *journal.Store, key string, idx int, now time.Time) (Outcome, error) {
	r, err := st.Load(key)
	if err != nil {
		return Pending{}, err
	}
	now = clock.Current(ctx, now)
	a := r.Attempts[idx]
	if a.Hold != "" {
		return Held{a.Hold}, nil
	}
	if a.Cleanup != "" {
		return cleanup(ctx, h, st, key, idx, a)
	}
	if a.Status == "succeeded" || a.Status == "failed" || a.Status == "skipped" || a.Status == "needs-human" {
		return Done{a.Status, a.Note}, nil
	}
	var kind string
	var skip int
	for _, s := range r.Plan.Steps {
		if s.Key == a.Step {
			kind = s.Kind
			skip = s.SkipExit
		}
	}
	lost := "agent_lost"
	if kind == "command" {
		lost = "command_outcome_unknown"
	}
	core, err := h.CoreID(ctx)
	if err != nil {
		return Pending{}, err
	}
	if core != a.CoreID {
		return held(st, key, idx, lost)
	}
	term, err := h.Get(ctx, a.Address)
	if errors.Is(err, hc.ErrNotFound) {
		return held(st, key, idx, lost)
	}
	if err != nil {
		return Pending{}, err
	}
	if a.ExpiringAt != nil || term.State == "live" && !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt) {
		return stop(ctx, h, st, key, idx, a, kind, "timeout", now)
	}
	dir := st.Layout.Attempt(key, a.Step, a.N)
	if kind == "command" {
		if term.State != "finished" {
			return Pending{}, nil
		}
		if term.Reason != "exited" {
			return held(st, key, idx, lost)
		}
		b, e := prompt.ReadRegular(filepath.Join(dir, "receipt.json"), 64*1024)
		var wire struct {
			ExitCode           *int
			StartedAt, EndedAt time.Time
		}
		if e != nil || json.Unmarshal(b, &wire) != nil || wire.ExitCode == nil || *wire.ExitCode < 0 || *wire.ExitCode > 255 || wire.StartedAt.Before(a.LaunchedAt) || wire.StartedAt.IsZero() || wire.EndedAt.Before(wire.StartedAt) {
			return held(st, key, idx, lost)
		}
		receipt := journal.Receipt{ExitCode: *wire.ExitCode, StartedAt: wire.StartedAt, EndedAt: wire.EndedAt}
		if !r.ExpiresAt.IsZero() && receipt.EndedAt.After(r.ExpiresAt) {
			return held(st, key, idx, "timeout")
		}
		note := prompt.ReadNote(filepath.Join(dir, "note.txt"))
		status := "failed"
		if receipt.ExitCode == 0 {
			status = "succeeded"
		} else if skip != 0 && receipt.ExitCode == skip {
			status = "skipped"
			note = "gate_closed"
		}
		a.Receipt = &receipt
		return accept(ctx, h, st, key, idx, a, status, note, "", "remove")
	}
	resultPath := filepath.Join(dir, "result.json")
	resultBytes, resultErr := prompt.ReadRegular(resultPath, 64*1024)
	var result prompt.Result
	if resultErr == nil {
		result, resultErr = prompt.ParseResult(resultBytes, r.RunUID, a.Step, a.N)
	}
	if term.CreatedMs != a.CreatedMs || term.PID != a.PID || term.Labels["kata.nonce"] != a.Nonce {
		return held(st, key, idx, "occupant_changed")
	}
	if (a.TaskStarted == nil || !*a.TaskStarted) && (term.Status == "working" || term.Turn > 0 || resultErr == nil) {
		a.TaskStarted = new(true)
		if err = saveAttempt(st, key, idx, a); err != nil {
			return Pending{}, err
		}
	}
	if term.State == "live" && a.TaskStarted != nil && !*a.TaskStarted && !now.Before(a.LaunchedAt.Add(2*time.Minute)) {
		return stop(ctx, h, st, key, idx, a, kind, "agent_not_started", now)
	}
	if term.State == "finished" {
		if term.Reason != "exited" || resultErr != nil {
			return held(st, key, idx, lost)
		}
		a.Session = result.Session
		return accept(ctx, h, st, key, idx, a, result.Status, result.Note, result.Summary, "remove")
	}
	if term.Status != "blocked" && a.Blocked {
		a.Blocked = false
		if err = saveAttempt(st, key, idx, a); err != nil {
			return Pending{}, err
		}
	}
	if term.Status == "blocked" {
		if !a.Blocked {
			a.Blocked = true
			a.BlockedSpell++
			if err = saveAttempt(st, key, idx, a); err != nil {
				return Pending{}, err
			}
		}
		id := fmt.Sprintf("blocked:%s:%d:%d", a.Step, a.N, a.BlockedSpell)
		if !slices.Contains(r.Notified, id) {
			return Notify{id, "blocked"}, nil
		}
		return Pending{}, nil
	}
	if term.Status == "error" {
		return held(st, key, idx, "agent_error")
	}
	if !term.Reported {
		if resultErr != nil {
			a.ResultDigest = ""
			a.ResultStableSince = nil
			return Pending{}, saveAttempt(st, key, idx, a)
		}
		sum := sha256.Sum256(resultBytes)
		digest := hex.EncodeToString(sum[:])
		if a.ResultDigest != digest || a.ResultStableSince == nil {
			a.ResultDigest = digest
			a.ResultStableSince = new(now)
			return Pending{}, saveAttempt(st, key, idx, a)
		}
		if now.Sub(*a.ResultStableSince) >= 30*time.Second {
			a.Session = result.Session
			return accept(ctx, h, st, key, idx, a, result.Status, result.Note, result.Summary, "interrupt")
		}
		return Pending{}, nil
	}
	if term.Turn == 0 && term.Status == "idle" {
		if a.IdleSince == nil {
			a.IdleSince = new(now)
			return Pending{}, saveAttempt(st, key, idx, a)
		}
		if now.Sub(*a.IdleSince) >= 5*time.Minute {
			return held(st, key, idx, "no_activity")
		}
		return Pending{}, nil
	}
	if a.IdleSince != nil {
		a.IdleSince = nil
		if err = saveAttempt(st, key, idx, a); err != nil {
			return Pending{}, err
		}
	}
	if term.Turn >= 1 && (term.Status == "idle" || term.Status == "done") {
		if resultErr == nil {
			a.Session = result.Session
			return accept(ctx, h, st, key, idx, a, result.Status, result.Note, result.Summary, "kill")
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

// Cancel bypasses normal cleanup so every retained terminal stays inspectable.
func Cancel(ctx context.Context, h hc.HC, st *journal.Store, key string, idx int, now time.Time) (Outcome, error) {
	r, err := st.Load(key)
	if err != nil {
		return Pending{}, err
	}
	now = clock.Current(ctx, now)
	a := r.Attempts[idx]
	if a.Hold != "" {
		return Held{a.Hold}, nil
	}
	if a.Cleanup == "" && slices.Contains([]string{"succeeded", "failed", "skipped"}, a.Status) {
		return Done{a.Status, a.Note}, nil
	}
	if a.Address == "" {
		if a.Nonce == "" {
			return Done{}, nil
		}
		if err = Recover(ctx, h, st, key, idx); err != nil {
			return Pending{}, err
		}
		return Cancel(ctx, h, st, key, idx, now)
	}
	kind := "prompt"
	for _, s := range r.Plan.Steps {
		if s.Key == a.Step {
			kind = s.Kind
		}
	}
	return cancelExpiry(ctx, h, st, key, idx, a, kind, now)
}
