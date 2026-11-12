// Package attempt implements guarded hc launch, recovery and settlement.
package attempt

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
)

var ErrOccupantChanged = errors.New("occupant_changed")

func Name(executor, run, step string, n int, nonce string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{executor, run, step, strconv.Itoa(n), nonce}, "|")))
	return "hk-" + hex.EncodeToString(sum[:])[:29]
}
func LaunchEnv(unset []string, extra map[string]string) []string {
	set := map[string]string{"PATH": os.Getenv("PATH"), "HOME": os.Getenv("HOME")}
	for _, e := range os.Environ() {
		k, v, _ := strings.Cut(e, "=")
		if strings.HasPrefix(k, "XDG_") {
			set[k] = v
		}
	}
	maps.Copy(set, extra)
	return hc.EnvPrefix(unset, set)
}
func hold(st *journal.Store, key string, idx int, reason string) error {
	return st.Update(key, func(r *journal.Run) error {
		a := &r.Attempts[idx]
		a.Status = "held"
		a.Hold = reason
		a.Cleanup = ""
		return nil
	})
}
func matches(t hc.Terminal, a journal.Attempt) bool {
	if t.Name != a.Name || !slices.Equal(t.Argv, append(slices.Clone(a.Spec.Prefix), a.Spec.Argv...)) {
		return false
	}
	for k, v := range a.Spec.Labels {
		if t.Labels[k] != v {
			return false
		}
	}
	return true
}
func Launch(ctx context.Context, h hc.HC, st *journal.Store, key string, idx int, spec hc.RunSpec) error {
	r, err := st.Load(key)
	if err != nil {
		return err
	}
	a := r.Attempts[idx]
	if a.Address != "" {
		return nil
	}
	if a.Nonce != "" {
		return Recover(ctx, h, st, key, idx)
	}
	if len(spec.Argv) == 0 || !filepath.IsAbs(spec.Argv[0]) {
		return fmt.Errorf("launch requires absolute argv[0]")
	}
	core, err := h.CoreID(ctx)
	if err != nil {
		return err
	}
	var entropy [8]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return err
	}
	a.Nonce = hex.EncodeToString(entropy[:])
	a.CoreID = core
	a.Name = Name(spec.Labels["kata.executor"], r.RunUID, a.Step, a.N, a.Nonce)
	a.LaunchedAt = clock.Now(ctx)
	a.TaskStarted = new(false)
	spec.Name = a.Name
	spec.Labels = maps.Clone(spec.Labels)
	if spec.Labels == nil {
		spec.Labels = make(map[string]string)
	}
	spec.Labels["kata.run"] = r.RunUID
	spec.Labels["kata.step"] = a.Step
	spec.Labels["kata.attempt"] = strconv.Itoa(a.N)
	spec.Labels["kata.nonce"] = a.Nonce
	a.Spec = spec
	a.Status = "launching"
	if err = st.Update(key, func(r *journal.Run) error { r.Attempts[idx] = a; return nil }); err != nil {
		return err
	}
	launched, err := h.Run(ctx, spec)
	if err != nil {
		return err
	}
	term, err := h.Get(ctx, launched.Address)
	if err != nil {
		return err
	}
	if !matches(term, a) {
		return hold(st, key, idx, "launch_ambiguous")
	}
	return adopt(st, key, idx, term)
}
func adopt(st *journal.Store, key string, idx int, t hc.Terminal) error {
	return st.Update(key, func(r *journal.Run) error {
		a := &r.Attempts[idx]
		a.Address = t.Address
		a.CreatedMs = t.CreatedMs
		a.PID = t.PID
		a.Status = "running"
		if t.Status == "working" || t.Turn > 0 {
			a.TaskStarted = new(true)
		}
		return nil
	})
}
func Recover(ctx context.Context, h hc.HC, st *journal.Store, key string, idx int) error {
	r, err := st.Load(key)
	if err != nil {
		return err
	}
	a := r.Attempts[idx]
	if a.Address != "" {
		return nil
	}
	if a.Nonce == "" || a.CoreID == "" || a.Name == "" || a.Spec.Labels["kata.nonce"] != a.Nonce {
		return hold(st, key, idx, "launch_ambiguous")
	}
	core, err := h.CoreID(ctx)
	if err != nil {
		return err
	}
	found, err := h.List(ctx, true, map[string]string{"kata.nonce": a.Nonce})
	if err != nil {
		return err
	}
	if len(found) == 0 {
		if core != a.CoreID || strings.HasPrefix(r.Detail, "cancel requested: request:") {
			return hold(st, key, idx, "launch_unproven")
		}
		if !r.ExpiresAt.IsZero() && !clock.Now(ctx).Before(r.ExpiresAt) {
			return hold(st, key, idx, "timeout")
		}
		if err = st.Update(key, func(r *journal.Run) error { r.Attempts[idx].Nonce = ""; return nil }); err != nil {
			return err
		}
		return Launch(ctx, h, st, key, idx, a.Spec)
	}
	if len(found) != 1 || core != a.CoreID {
		return hold(st, key, idx, "launch_ambiguous")
	}
	term, err := h.Get(ctx, found[0])
	if err != nil {
		return err
	}
	if !matches(term, a) {
		return hold(st, key, idx, "launch_ambiguous")
	}
	return adopt(st, key, idx, term)
}
func guarded(ctx context.Context, h hc.HC, a journal.Attempt) (hc.Terminal, error) {
	core, err := h.CoreID(ctx)
	if err != nil {
		return hc.Terminal{}, err
	}
	if core != a.CoreID {
		return hc.Terminal{}, ErrOccupantChanged
	}
	term, err := h.Get(ctx, a.Address)
	if err != nil {
		if errors.Is(err, hc.ErrNotFound) {
			return term, fmt.Errorf("%w: %w", ErrOccupantChanged, err)
		}
		return term, err
	}
	if term.CreatedMs != a.CreatedMs || term.PID != a.PID || (a.Nonce != "" && term.Labels["kata.nonce"] != a.Nonce) || (a.Nonce == "" && (term.Status == "idle" || term.Status == "done") && term.ForegroundPID != a.ForegroundPID) {
		return term, ErrOccupantChanged
	}
	return term, nil
}
func Guard(ctx context.Context, h hc.HC, a journal.Attempt) error {
	_, err := guarded(ctx, h, a)
	return err
}
