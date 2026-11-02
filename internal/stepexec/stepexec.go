// Package stepexec records ordinary command exits and bounded raw output.
package stepexec

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

const headLimit = 1024 * 1024
const tailLimit = 256 * 1024

type output struct {
	file  *os.File
	head  int
	tail  []byte
	total int
	err   error
}

func (o *output) Write(b []byte) (int, error) {
	n := len(b)
	o.total += n
	if o.head < headLimit {
		take := min(n, headLimit-o.head)
		_, err := o.file.Write(b[:take])
		if err != nil {
			o.err = err
			return 0, err
		}
		o.head += take
		b = b[take:]
	}
	if len(b) > 0 {
		o.tail = append(o.tail, b...)
		if len(o.tail) > tailLimit {
			o.tail = bytes.Clone(o.tail[len(o.tail)-tailLimit:])
		}
	}
	return n, nil
}
func (o *output) close() error {
	if o.err != nil {
		_ = o.file.Close()
		return o.err
	}
	if o.total > headLimit+tailLimit {
		if _, err := o.file.WriteString("\n[output truncated]\n"); err != nil {
			_ = o.file.Close()
			return err
		}
	}
	if _, err := o.file.Write(o.tail); err != nil {
		_ = o.file.Close()
		return err
	}
	if err := o.file.Sync(); err != nil {
		_ = o.file.Close()
		return err
	}
	return o.file.Close()
}
func Run(dir string, argv []string) int {
	if len(argv) == 0 || state.EnsureDir(dir) != nil {
		return 2
	}
	// A reused attempt directory cannot lend an old receipt to a new process.
	if err := os.Remove(filepath.Join(dir, "receipt.json")); err != nil && !os.IsNotExist(err) {
		return 1
	}
	f, err := os.OpenFile(filepath.Join(dir, "output.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return 1
	}
	log := &output{file: f}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	tee := io.MultiWriter(os.Stdout, log)
	cmd.Stdout = tee
	cmd.Stderr = tee
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	started := time.Now().UTC()
	if err = cmd.Start(); err != nil {
		_ = log.close()
		return 1
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	signalled := false
	for {
		select {
		case sig := <-signals:
			signalled = true
			_ = syscall.Kill(-cmd.Process.Pid, sig.(syscall.Signal))
		case err = <-waited:
			// Conservatively suppress a receipt for any signal already delivered.
			select {
			case <-signals:
				signalled = true
			default:
			}
			ended := time.Now().UTC()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					_ = log.close()
					return 1
				}
				code = exit.ExitCode()
				if code < 0 {
					signalled = true
					code = 128 + int(exit.Sys().(syscall.WaitStatus).Signal())
				}
			}
			if err = log.close(); err != nil {
				return 1
			}
			if !signalled {
				if err = state.WriteJSON(filepath.Join(dir, "receipt.json"), journal.Receipt{ExitCode: code, StartedAt: started, EndedAt: ended}); err != nil {
					return 1
				}
			}
			if signalled && code == 0 {
				return 128
			}
			return code
		}
	}
}
