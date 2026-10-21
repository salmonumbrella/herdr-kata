package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/version"
)

type ServeInfo struct {
	PID       int       `json:"pid"`
	WorkerPID int       `json:"worker_pid"`
	Version   string    `json:"version"`
	Exe       string    `json:"exe"`
	StartedAt time.Time `json:"started_at"`
}
type serveOptions struct{ poll, pingEvery, goneAfter, backoff, maxBackoff, resetAfter time.Duration }

func Serve(ctx context.Context, l state.Layout, exe string) error {
	return serve(ctx, l, exe, serveOptions{time.Second, 30 * time.Second, 2 * time.Minute, time.Second, 5 * time.Minute, 10 * time.Minute})
}
func serve(ctx context.Context, l state.Layout, exe string, o serveOptions) error {
	if err := os.MkdirAll(l.Root, 0700); err != nil {
		return err
	}
	var lock *state.Lock
	var ok bool
	var err error
	if inherited := os.Getenv("HERDR_KATA_SERVE_LOCK_FD"); inherited != "" {
		fd, parseErr := strconv.Atoi(inherited)
		if parseErr != nil {
			return parseErr
		}
		lock, err = state.AdoptLock(l.ServeLock(), fd)
		ok = err == nil
		if unsetErr := os.Unsetenv("HERDR_KATA_SERVE_LOCK_FD"); unsetErr != nil {
			return unsetErr
		}
	} else {
		lock, ok, err = state.TryLock(l.ServeLock())
	}
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("supervisor already running")
	}
	defer lock.Unlock() //nolint:errcheck // Lock cleanup remains best-effort.
	info := ServeInfo{PID: os.Getpid(), Version: version.String(), Exe: exe, StartedAt: time.Now().UTC()}
	save := func() error { return state.WriteJSON(state.OSFS{}, l.ServeInfo(), info) }
	if err := save(); err != nil {
		return err
	}
	log, err := NewLogWriter(l.LogFile())
	if err != nil {
		return err
	}
	defer log.Close()
	null, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer null.Close()
	var cmd *exec.Cmd
	var done chan error
	var launched time.Time
	backoff := o.backoff
	next := time.Now()
	lastPing := time.Time{}
	unreachable := time.Time{}
	stopChild := func() {
		if cmd != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			<-done
			cmd = nil
		}
	}
	defer stopChild()
	ticker := time.NewTicker(o.poll)
	defer ticker.Stop()
	for {
		now := time.Now()
		if ctx.Err() != nil || exists(l.StopFile()) {
			return nil
		}
		if now.Sub(lastPing) >= o.pingEvery {
			lastPing = now
			pctx, cancel := context.WithTimeout(ctx, min(5*time.Second, o.pingEvery))
			_, err := (&herdr.Client{Path: herdr.SocketPath("")}).Ping(pctx)
			cancel()
			if err == nil {
				unreachable = time.Time{}
			} else if unreachable.IsZero() {
				unreachable = now
			} else if now.Sub(unreachable) >= o.goneAfter {
				return nil
			}
		}
		if done != nil {
			select {
			case err := <-done:
				fmt.Fprintf(log, "worker exited: %v\n", err)
				cmd = nil
				done = nil
				info.WorkerPID = 0
				if err := save(); err != nil {
					return err
				}
				if exists(l.StopFile()) || ctx.Err() != nil {
					return nil
				}
				if exists(l.UpgradeFile()) {
					if err := applyUpgrade(l, exe, lock); err != nil {
						return err
					}
					next = now
					continue
				}
				if err == nil {
					return nil
				}
				if now.Sub(launched) >= o.resetAfter {
					backoff = o.backoff
				}
				next = now.Add(backoff)
				backoff = min(o.maxBackoff, 2*backoff)
			default:
			}
		}
		if cmd == nil && !now.Before(next) {
			held, err := state.Held(l.WorkerLock())
			if err != nil {
				return err
			}
			if !held {
				if exists(l.UpgradeFile()) {
					if err := applyUpgrade(l, exe, lock); err != nil {
						return err
					}
				}
				// An upgrade with an orphan must first start a worker only after the orphan
				// has drained; the fresh worker notices the upgrade and exits cleanly.
				cmd = exec.Command(exe, "worker")
				cmd.Env = childEnv(l)
				cmd.Stdin = null
				cmd.Stdout = log
				cmd.Stderr = log
				if err := cmd.Start(); err != nil {
					cmd = nil
					next = now.Add(backoff)
					backoff = min(o.maxBackoff, 2*backoff)
					fmt.Fprintf(log, "worker start: %v\n", err)
				} else {
					launched = now
					info.WorkerPID = cmd.Process.Pid
					done = make(chan error, 1)
					child := cmd
					go func() { done <- child.Wait() }()
					if err := save(); err != nil {
						return err
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func validateStaged(l state.Layout, exe string) error {
	if !stringsHasBuildPath(l, exe) {
		return fmt.Errorf("upgrade executable outside staging directory")
	}
	id, err := version.BuildID(exe)
	if err != nil {
		return err
	}
	if exe != filepath.Join(l.BinDir(), "herdr-kata-"+id) {
		return fmt.Errorf("upgrade build ID differs")
	}
	i, err := os.Lstat(exe)
	if err != nil {
		return err
	}
	if !i.Mode().IsRegular() || i.Mode()&0111 == 0 {
		return fmt.Errorf("upgrade executable is not a regular executable")
	}
	return nil
}
func stringsHasBuildPath(l state.Layout, exe string) bool { return filepath.Dir(exe) == l.BinDir() }

func applyUpgrade(l state.Layout, exe string, lock *state.Lock) error {
	var u Upgrade
	if err := state.ReadJSON(state.OSFS{}, l.UpgradeFile(), 4096, &u); err != nil {
		return err
	}
	if err := validateStaged(l, u.Exe); err != nil {
		return err
	}
	if err := os.Remove(l.UpgradeFile()); err != nil {
		return err
	}
	if err := (state.OSFS{}).SyncDir(l.Root); err != nil {
		return err
	}
	if u.Exe == exe {
		return nil
	}
	fd, err := lock.Inherit()
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	env := append(childEnv(l), "HERDR_KATA_SERVE_LOCK_FD="+strconv.Itoa(fd))
	return syscall.Exec(u.Exe, []string{u.Exe, "serve"}, env)
}
