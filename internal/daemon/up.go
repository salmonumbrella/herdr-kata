// Package daemon owns the detached supervisor and its locked execution worker.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/version"
)

type UpResult string

const (
	WrongSession     UpResult = "wrong-session"
	Started          UpResult = "started"
	AlreadyRunning   UpResult = "already-running"
	UpgradeRequested UpResult = "upgrade-requested"
)

type Upgrade struct {
	Exe string `json:"exe"`
}

func exists(p string) bool { _, err := os.Lstat(p); return !errors.Is(err, fs.ErrNotExist) }
func Up(ctx context.Context, cfg config.Config, l state.Layout, self string) (UpResult, error) {
	if herdr.SessionFromSocket(herdr.SocketPath(cfg.Session)) != cfg.Session {
		return WrongSession, nil
	}
	if err := os.MkdirAll(l.Root, 0700); err != nil {
		return "", err
	}
	gate, ok, err := state.TryLock(filepath.Join(l.Root, "up.lock"))
	if err != nil {
		return "", err
	}
	if !ok {
		return AlreadyRunning, nil
	}
	defer gate.Unlock() //nolint:errcheck // Lock cleanup remains best-effort.
	if _, err := state.LayoutPresent(state.OSFS{}, l); err != nil {
		return "", err
	}
	if err := removeStageTemps(l); err != nil {
		return "", err
	}
	exe, err := stage(l, self)
	if err != nil {
		return "", err
	}
	if err := prune(l, exe); err != nil {
		return "", err
	}
	held, err := state.Held(l.ServeLock())
	if err != nil {
		return "", err
	}
	workerHeld, err := heldExisting(l.WorkerLock())
	if err != nil {
		return "", err
	}
	if (held || workerHeld) && exists(l.StopFile()) {
		return "", fmt.Errorf("runtime is stopping; retry up after drain")
	}
	if (held || workerHeld) && credentialRepairReady(cfg, l) {
		if err := Stop(l); err != nil {
			return "", err
		}
		held, workerHeld = false, false
	}
	if held {
		var info ServeInfo
		if err := state.ReadJSON(state.OSFS{}, l.ServeInfo(), 4096, &info); err != nil {
			return "", err
		}
		if info.Exe == exe {
			return AlreadyRunning, nil
		}
		if err := state.WriteJSON(state.OSFS{}, l.UpgradeFile(), Upgrade{exe}); err != nil {
			return "", err
		}
		return UpgradeRequested, nil
	}
	if workerHeld {
		var previous ServeInfo
		err := state.ReadJSON(state.OSFS{}, l.ServeInfo(), 4096, &previous)
		if err != nil || previous.Exe != exe {
			if err := state.WriteJSON(state.OSFS{}, l.UpgradeFile(), Upgrade{exe}); err != nil {
				return "", err
			}
		}
	} else {
		// A stopped supervisor can leave an unconsumed candidate. This up
		// invocation selects its own staged build, rather than that old request.
		if err := os.Remove(l.UpgradeFile()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	if err := os.Remove(l.StopFile()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(l.LogFile()), 0700); err != nil {
		return "", err
	}
	log, err := os.OpenFile(l.LogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer log.Close()
	null, err := os.Open(os.DevNull)
	if err != nil {
		return "", err
	}
	defer null.Close()
	cmd := exec.Command(exe, "serve")
	cmd.Env = childEnv(l)
	cmd.Stdin = null
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() { _ = cmd.Wait() }()
	// Hold the starter gate through the bounded lock handoff, including slow cold starts.
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return Started, nil
		case <-timer.C:
			return Started, nil
		case <-ticker.C:
			// Serve publishes its PID only after acquiring serve.lock. Probing
			// the lock here can steal it from the child during startup.
			var ready ServeInfo
			if state.ReadJSON(state.OSFS{}, l.ServeInfo(), 4096, &ready) == nil && ready.PID == cmd.Process.Pid {
				return Started, nil
			}
		}
	}
}
func stage(l state.Layout, self string) (string, error) {
	id, err := version.BuildID(self)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(l.BinDir(), "herdr-kata-"+id)
	if info, err := os.Lstat(dest); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			return "", fmt.Errorf("staged build is not a regular executable")
		}
		existing, e := version.BuildID(dest)
		if e != nil || existing != id {
			return "", fmt.Errorf("staged build differs: %s", dest)
		}
		return dest, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(l.BinDir(), 0700); err != nil {
		return "", err
	}
	src, err := os.Open(self)
	if err != nil {
		return "", err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(l.BinDir(), ".stage-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err = io.Copy(tmp, src); err != nil {
		return "", err
	}
	if err = tmp.Chmod(0700); err != nil {
		return "", err
	}
	if err = tmp.Sync(); err != nil {
		return "", err
	}
	copied, err := version.BuildID(tmp.Name())
	if err != nil {
		return "", err
	}
	if copied != id {
		return "", fmt.Errorf("executable changed during staging")
	}
	// Link is an atomic, no-overwrite publication under the starter gate.
	if err = os.Link(tmp.Name(), dest); err != nil {
		return "", err
	}
	if err = (state.OSFS{}).SyncDir(l.BinDir()); err != nil {
		return "", err
	}
	return dest, nil
}
func childEnv(l state.Layout) []string { return append(os.Environ(), "HERDR_PLUGIN_STATE_DIR="+l.Root) }
func Stop(l state.Layout) error {
	if !exists(l.Root) {
		return nil
	}
	if err := state.WriteJSON(state.OSFS{}, l.StopFile(), struct {
		Version int `json:"version"`
	}{1}); err != nil {
		return err
	}
	for {
		serveHeld, err := heldExisting(l.ServeLock())
		if err != nil {
			return err
		}
		workerHeld, err := heldExisting(l.WorkerLock())
		if err != nil {
			return err
		}
		if !serveHeld && !workerHeld {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Only Up stages binaries, under up.lock. An abandoned temporary cannot be an
// in-use executable: publication always gives it the immutable build name.
func removeStageTemps(l state.Layout) error {
	entries, err := os.ReadDir(l.BinDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".stage-") && e.Type().IsRegular() {
			if err := os.Remove(filepath.Join(l.BinDir(), e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// A later up can replace a hook-started supervisor whose inherited credential
// was empty. Healthy lanes are left running; file credentials also self-repair
// on the next route probe because they are resolved for each Kata child.
func credentialRepairReady(cfg config.Config, l state.Layout) bool {
	problems, err := readStartupErrors(l)
	if err != nil || len(problems) == 0 {
		return false
	}
	status, err := ReadStatus(l, cfg)
	if err != nil {
		return false
	}
	for _, lane := range status.Projects {
		if lane.Route != nil && lane.Route.Allowed {
			return false
		}
	}
	for _, entry := range cfg.ProjectEntries() {
		if entry.CredentialEnv == "" || !strings.Contains(problems[entry.Name], "credential_") {
			continue
		}
		value, err := cfg.View(entry).Kata.Credential()
		if err == nil && value != "" {
			return true
		}
	}
	return false
}
