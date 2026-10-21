package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func logsCommand(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("logs", flag.ContinueOnError)
	f.SetOutput(stderr)
	commandUsage(f, stderr, "logs <run> [--follow]")
	follow := f.Bool("follow", false, "follow output until the run ends")
	uid := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		uid = args[0]
		args = args[1:]
	}
	if e := f.Parse(args); e != nil {
		if e == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if uid == "" && f.NArg() == 1 {
		uid = f.Arg(0)
	} else if f.NArg() != 0 {
		uid = ""
	}
	if !validLogRunID(uid) {
		f.Usage()
		return 2
	}
	if _, e := state.LayoutPresent(state.OSFS{}, state.Layout{Root: state.DefaultRoot()}); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	if e := streamRunLogs(ctx, state.Layout{Root: state.DefaultRoot()}, uid, *follow, stdout); e != nil {
		fmt.Fprintln(stderr, controlError(state.Layout{Root: state.DefaultRoot()}, e.Error()))
		return 1
	}
	return 0
}
func streamRunLogs(ctx context.Context, l state.Layout, uid string, follow bool, out io.Writer) error {
	seen := map[string][]byte{}
	headers := map[string]bool{}
	displayed := ""
	corruptPolls := 0
	for first := true; ; first = false {
		if !first {
			timer := time.NewTimer(200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		// Read-only commands must never quarantine or otherwise change worker state.
		var r runner.Run
		if e := state.ReadJSON(state.OSFS{}, l.RunFile(uid), 4<<20, &r); e != nil {
			var corrupt *state.CorruptError
			if follow && errors.As(e, &corrupt) && corruptPolls < 5 {
				corruptPolls++
				continue
			}
			return e
		}
		corruptPolls = 0
		if (r.Version != 1 && r.Version != 2) || r.UID != uid {
			return errors.New("invalid run identity")
		}
		for _, step := range r.Steps {
			if step.Key == "" || step.Key == "." || step.Key == ".." || strings.ContainsAny(step.Key, "/\\\x00") || step.Attempt < 1 || step.Attempt > 1000 {
				return errors.New("invalid step identity")
			}
			for attempt := 1; attempt <= step.Attempt; attempt++ {
				path := filepath.Join(l.AttemptDir(uid, step.Key, attempt), "output.log")
				info, e := os.Lstat(l.AttemptDir(uid, step.Key, attempt))
				if os.IsNotExist(e) {
					continue
				}
				if e != nil {
					return e
				}
				if !info.IsDir() {
					return errors.New("attempt is not a directory")
				}
				if !headers[path] {
					if _, e := fmt.Fprintf(out, "[%s · attempt %d]\n", step.Key, attempt); e != nil {
						return e
					}
					headers[path] = true
					displayed = path
				}
				info, e = os.Lstat(path)
				if os.IsNotExist(e) {
					continue
				}
				if e != nil {
					return e
				}
				if !info.Mode().IsRegular() {
					return errors.New("output log is not a regular file")
				}
				f, e := os.Open(path)
				if e != nil {
					return e
				}
				data, e := io.ReadAll(io.LimitReader(f, (2<<20)+1))
				f.Close()
				if e != nil {
					return e
				}
				if len(data) > 2<<20 {
					return errors.New("output log exceeds size limit")
				}
				old := seen[path]
				if bytes.Equal(old, data) {
					continue
				}
				delta := data
				if bytes.HasPrefix(data, old) {
					delta = data[len(old):]
				} else {
					// The capped log's tail moves in place. Show its current tail when
					// bytes have been replaced, including the visible truncation marker.
					marker := []byte("\n[output truncated]\n")
					if i := bytes.Index(data, marker); i >= 0 {
						delta = data[i:]
					}
				}
				if displayed != path {
					if _, e = fmt.Fprintf(out, "[%s · attempt %d]\n", step.Key, attempt); e != nil {
						return e
					}
					displayed = path
				}
				if _, e = out.Write(delta); e != nil {
					return e
				}
				seen[path] = data
			}
		}
		if !follow || r.EndedAt != nil || r.Phase == runner.Settled || r.Phase == runner.HandedOff {
			return nil
		}
	}
}

func validLogRunID(uid string) bool {
	if len(uid) != 26 || uid[0] > '7' {
		return false
	}
	for _, r := range uid {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", r) {
			return false
		}
	}
	return true
}
