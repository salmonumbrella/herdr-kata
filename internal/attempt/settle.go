package attempt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
func accept(ctx context.Context, h hc.HC, st *journal.Store, key string, idx int, a journal.Attempt, status, note, stage string) (Outcome, error) {
	a.SettlementStatus = status
	a.SettlementNote = note
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
func expire(ctx context.Context, h hc.HC, st *journal.Store, key string, idx int, a journal.Attempt, kind string, now time.Time) (Outcome, error) {
	term, err := guarded(ctx, h, a)
	if errors.Is(err, ErrOccupantChanged) {
		return held(st, key, idx, "occupant_changed")
	}
	if err != nil {
		return Pending{}, err
	}
	if term.State == "finished" {
		return held(st, key, idx, "timeout")
	}
	if a.ExpiringAt == nil {
		a.ExpiringAt = new(now)
		if err = saveAttempt(st, key, idx, a); err != nil {
			return Pending{}, err
		}
		if kind == "prompt" {
			return Pending{}, h.Interrupt(ctx, a.Address)
		}
	}
	if kind == "prompt" && now.Before(a.ExpiringAt.Add(10*time.Second)) {
		return Pending{}, nil
	}
	// Revalidate again immediately before the destructive operation.
	if err = Guard(ctx, h, a); errors.Is(err, ErrOccupantChanged) {
		return held(st, key, idx, "occupant_changed")
	}
	if err != nil {
		return Pending{}, err
	}
	if err = h.Kill(ctx, a.Address); err != nil {
		return Pending{}, err
	}
	return held(st, key, idx, "timeout")
}
func Settle(ctx context.Context, h hc.HC, st *journal.Store, key string, idx int, now time.Time) (Outcome, error) {
	r, err := st.Load(key)
	if err != nil {
		return Pending{}, err
	}
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
		return expire(ctx, h, st, key, idx, a, kind, now)
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
		var receipt journal.Receipt
		if e != nil || json.Unmarshal(b, &receipt) != nil || receipt.StartedAt.Before(a.LaunchedAt) || receipt.StartedAt.IsZero() || receipt.EndedAt.Before(receipt.StartedAt) {
			return held(st, key, idx, lost)
		}
		if !r.ExpiresAt.IsZero() && receipt.EndedAt.After(r.ExpiresAt) {
			return held(st, key, idx, "timeout")
		}
		note := ""
		b, e = prompt.ReadRegular(filepath.Join(dir, "note.txt"), 282)
		if e == nil {
			note = strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
			if !prompt.ValidNote(note) {
				return held(st, key, idx, "invalid_note")
			}
		} else if !os.IsNotExist(e) {
			return held(st, key, idx, "invalid_note")
		}
		status := "failed"
		if receipt.ExitCode == 0 {
			status = "succeeded"
		} else if skip != 0 && receipt.ExitCode == skip {
			status = "skipped"
			note = "gate_closed"
		}
		a.Receipt = &receipt
		return accept(ctx, h, st, key, idx, a, status, note, "remove")
	}
	resultPath := filepath.Join(dir, "result.json")
	resultBytes, resultErr := prompt.ReadRegular(resultPath, 64*1024)
	var result prompt.Result
	if resultErr == nil {
		result, resultErr = prompt.ParseResult(resultBytes, r.RunUID, a.Step, a.N)
	}
	if term.State == "finished" {
		if term.Reason != "exited" || resultErr != nil {
			return held(st, key, idx, lost)
		}
		return accept(ctx, h, st, key, idx, a, result.Status, result.Note, "remove")
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
			return accept(ctx, h, st, key, idx, a, result.Status, result.Note, "interrupt")
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
			return accept(ctx, h, st, key, idx, a, result.Status, result.Note, "kill")
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
