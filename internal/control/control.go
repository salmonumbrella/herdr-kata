// Package control manages only this installation's worker terminal.
package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func Name(root string) string {
	sum := sha256.Sum256([]byte(root))
	return "herdr-kata-" + hex.EncodeToString(sum[:])[:8]
}
func owns(l state.Layout, t hc.Terminal) bool {
	if t.Labels["kata.worker"] != Name(l.Root) || t.ForegroundPID == 0 {
		return false
	}
	b, err := os.ReadFile(l.PID())
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid != int(t.ForegroundPID) {
		return false
	}
	f, err := os.OpenFile(l.Lock(), os.O_RDWR, 0600)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false
	}
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
func guard(ctx context.Context, h hc.HC, original hc.Terminal, core string) (hc.Terminal, error) {
	current, err := h.CoreID(ctx)
	if err != nil {
		return hc.Terminal{}, err
	}
	if current != core {
		return hc.Terminal{}, errors.New("worker occupant changed")
	}
	t, err := h.Get(ctx, original.Address)
	if err != nil {
		return t, err
	}
	if t.CreatedMs != original.CreatedMs || t.PID != original.PID || t.Labels["kata.worker"] != original.Labels["kata.worker"] {
		return t, errors.New("worker occupant changed")
	}
	return t, nil
}
func Up(ctx context.Context, h hc.HC, l state.Layout, bin, configPath string, prefix []string) (hc.Terminal, error) {
	name := Name(l.Root)
	existing, err := h.Get(ctx, name)
	if err == nil {
		if existing.State == "live" {
			if owns(l, existing) {
				return existing, nil
			}
			return existing, errors.New("existing terminal is not this locked worker")
		}
		if existing.Labels["kata.worker"] != name {
			return existing, errors.New("existing worker name belongs to another terminal")
		}
		core, e := h.CoreID(ctx)
		if e != nil {
			return existing, e
		}
		if _, e = guard(ctx, h, existing, core); e != nil {
			return existing, e
		}
		if e = h.Remove(ctx, existing.Address); e != nil {
			return existing, e
		}
	} else if !errors.Is(err, hc.ErrNotFound) {
		return hc.Terminal{}, err
	}
	launched, err := h.Run(ctx, hc.RunSpec{Name: name, Cwd: l.Root, Labels: map[string]string{"kata.worker": name}, Prefix: prefix, Argv: []string{bin, "--config", configPath, "--state", l.Root, "serve"}})
	if err != nil {
		return hc.Terminal{}, err
	}
	wait, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		t, e := h.Get(wait, launched.Address)
		if e == nil && t.State == "finished" {
			return t, errors.New("worker exited before becoming ready")
		}
		if e == nil && owns(l, t) {
			return t, nil
		}
		select {
		case <-wait.Done():
			return t, fmt.Errorf("worker readiness: %w", wait.Err())
		case <-ticker.C:
		}
	}
}
func Stop(ctx context.Context, h hc.HC, l state.Layout) error {
	t, err := h.Get(ctx, Name(l.Root))
	if errors.Is(err, hc.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if t.Labels["kata.worker"] != Name(l.Root) {
		return errors.New("worker name belongs to another terminal")
	}
	if t.State == "live" && !owns(l, t) {
		return errors.New("terminal is not this locked worker")
	}
	core, err := h.CoreID(ctx)
	if err != nil {
		return err
	}
	if t.State == "live" {
		if _, err = guard(ctx, h, t, core); err != nil {
			return err
		}
		if err = h.Kill(ctx, t.Address); err != nil {
			return err
		}
	}
	fresh, err := guard(ctx, h, t, core)
	if errors.Is(err, hc.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if fresh.State == "live" {
		return hc.ErrLive
	}
	return h.Remove(ctx, t.Address)
}
