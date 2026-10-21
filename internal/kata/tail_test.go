//go:build linux || darwin

package kata

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// A killed worker cannot cancel its context. An idle tail must stop without
// writing another event to its now-broken stdout pipe.
func TestEventTailStopsWithWorker(t *testing.T) {
	if mode := os.Getenv("HK_TEST_KATA_PARENT_HELPER"); mode != "" {
		os.Exit(eventTailParentHelper(mode))
	}
	testEventTailStopsWithWorker(t, "worker")
}

func eventTailParentHelper(mode string) int {
	switch mode {
	case "worker", "worker-pipe", "worker-cancel":
		argv := []string{os.Args[0], "-test.run=^TestEventTailStopsWithWorker$"}
		env := append(os.Environ(), "HK_TEST_KATA_PARENT_HELPER=stream")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if mode == "worker-cancel" {
			go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
		}
		var wait func() error
		if mode == "worker" {
			proc, err := execStarter(ctx, argv, env, "")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
			wait = proc.Wait
		} else {
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			stop, err := configureTailPipe(cmd)
			if err != nil {
				return 2
			}
			defer stop()
			cmd.Env = env
			if err := cmd.Start(); err != nil {
				stop()
				return 2
			}
			wait = cmd.Wait
		}
		_ = wait()
		return 0
	case "stream":
		conn, err := net.Dial("tcp", os.Getenv("HK_TEST_KATA_PARENT_ADDRESS"))
		if err != nil {
			return 3
		}
		defer conn.Close()
		fmt.Fprintln(conn, os.Getpid())
		// Only process termination closes this connection while the test holds it.
		// An idle tail never writes to its broken stdout after worker death.
		_, _ = io.Copy(io.Discard, conn)
		return 0
	}
	return 4
}

func TestTailPipeStopsWithWorker(t *testing.T) {
	testEventTailStopsWithWorker(t, "worker-pipe")
}

func TestTailPipeCancelStopsChild(t *testing.T) {
	testEventTailStopsWithWorker(t, "worker-cancel")
}

func testEventTailStopsWithWorker(t *testing.T, mode string) {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	helper := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestEventTailStopsWithWorker$")
	helper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	helper.Env = append(os.Environ(), "HK_TEST_KATA_PARENT_HELPER="+mode, "HK_TEST_KATA_PARENT_ADDRESS="+listener.Addr().String())
	var control io.WriteCloser
	if mode == "worker-cancel" {
		control, err = helper.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = control.Close() })
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	joined := false
	t.Cleanup(func() {
		if !joined {
			_ = helper.Process.Kill()
			_ = helper.Wait()
		}
	})
	conn, err := listener.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	var pid int
	if _, err := fmt.Fscanln(reader, &pid); err != nil {
		t.Fatal(err)
	}
	child, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Kill(); _ = child.Release() })
	if err := child.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("tail exited before worker death:", err)
	}
	if control == nil {
		if err := helper.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		_ = helper.Wait()
		joined = true
	} else if err := control.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatal("Kata event subprocess survived worker stop:", err)
	}
	if !joined {
		err := helper.Wait()
		joined = true
		if err != nil {
			t.Fatal("worker failed to reap tail:", err)
		}
	}
}
