package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func commandSpec(t *testing.T, command string) CommandSpec {
	t.Helper()
	dir := t.TempDir()
	return CommandSpec{Run: "run-1", Step: "check", Attempt: 1, Command: command,
		Cwd: dir, AttemptDir: filepath.Join(dir, "steps", "check", "1"),
		RunDir: dir, Project: "project", ReportBin: "/bin/report", Env: os.Environ()}
}

func readReceipt(t *testing.T, spec CommandSpec) Receipt {
	t.Helper()
	var receipt Receipt
	if err := state.ReadJSON(state.OSFS{}, filepath.Join(spec.AttemptDir, "receipt.json"), 4096, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

type commandOutputCloser func() error

func (f commandOutputCloser) Close() error { return f() }

func TestCommandDrainedOutputWinsExpiredTimer(t *testing.T) {
	for _, delayed := range []bool{false, true} {
		t.Run(fmt.Sprint(delayed), func(t *testing.T) {
			drained := make(chan error, 1)
			expired := make(chan time.Time)
			close(expired)
			if !delayed {
				drained <- nil
			}
			reader := commandOutputCloser(func() error {
				// The copier already observed EOF, but was descheduled before
				// delivering its result. Closing the reader cannot change that.
				if delayed {
					drained <- nil
				}
				return nil
			})
			if err := waitCommandOutput(drained, reader, expired); err != nil {
				t.Fatal("completed output became uncertain", err)
			}
		})
	}
}

func TestCommandOpenOutputIsUncertain(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	// Keep the writer open, as an escaped descendant would. The timeout must
	// close the reader, join its copier and preserve the unknown outcome.
	drained := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, reader); drained <- err }()
	expired := make(chan time.Time)
	close(expired)
	if err := waitCommandOutput(drained, reader, expired); !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatal("open output was considered complete", err)
	}
}

func TestCommandExitZero(t *testing.T) {
	spec := commandSpec(t, `printf '%s\n' "$PWD"; printf error >&2`)
	before := time.Now()
	receipt, err := RunCommand(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Version != 1 || receipt.Run != spec.Run || receipt.Step != spec.Step || receipt.Attempt != 1 || receipt.ExitCode != 0 || receipt.PGID <= 0 || receipt.Interrupted || receipt.StartedAt.Before(before) || receipt.EndedAt.Before(receipt.StartedAt) {
		t.Fatalf("receipt=%+v", receipt)
	}
	if got := readReceipt(t, spec); !reflect.DeepEqual(got, receipt) {
		t.Fatalf("saved=%+v returned=%+v", got, receipt)
	}
	cwd, err := filepath.EvalSymlinks(spec.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join(spec.AttemptDir, "output.log"))
	if err != nil || string(log) != cwd+"\nerror" {
		t.Fatalf("log=%q err=%v", log, err)
	}
	if !GroupGone(receipt.PGID) || GroupGone(0) || GroupGone(-1) {
		t.Fatal("invalid group disappearance proof")
	}
}

func TestCommandNonzero(t *testing.T) {
	spec := commandSpec(t, "exit 17")
	receipt, err := RunCommand(context.Background(), spec)
	if err == nil || receipt.ExitCode != 17 || receipt.Interrupted || !GroupGone(receipt.PGID) {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if readReceipt(t, spec).ExitCode != 17 {
		t.Fatal("nonzero exit receipt missing")
	}
}

func TestCommandHeadTailCap(t *testing.T) {
	// Distinct head, middle and tail catch dropping the tail or keeping only
	// the most recent output. Five MiB also exceeds typical pipe capacity.
	spec := commandSpec(t, `head -c 1048576 /dev/zero | tr '\000' H; head -c 3932160 /dev/zero | tr '\000' M; head -c 262144 /dev/zero | tr '\000' T`)
	if _, err := RunCommand(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join(spec.AttemptDir, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(log) > (1<<20)+(256<<10)+128 || !strings.HasPrefix(string(log), strings.Repeat("H", 1<<20)) || !strings.HasSuffix(string(log), strings.Repeat("T", 256<<10)) || !strings.Contains(string(log[1<<20:len(log)-(256<<10)]), "truncated") {
		t.Fatalf("incorrect head/tail retention: %d bytes", len(log))
	}
}

func TestCommandEnvDropsKataCredential(t *testing.T) {
	spec := commandSpec(t, `printf '%s|%s|%s|%s|%s|%s|%s' "${KATA_AUTH_TOKEN-unset}" "${CUSTOM_CREDENTIAL-unset}" "$KEEP" "$HERDR_KATA_RUN" "$HERDR_KATA_RUN_DIR" "$HERDR_KATA_REPORT" "$KATA_PROJECT"`)
	spec.Env = []string{"KATA_AUTH_TOKEN=secret", "CUSTOM_CREDENTIAL=secret", "KEEP=present", "HERDR_KATA_RUN=wrong"}
	spec.CredentialEnv = "CUSTOM_CREDENTIAL"
	if _, err := RunCommand(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join(spec.AttemptDir, "output.log"))
	want := "unset|unset|present|run-1|" + spec.RunDir + "|/bin/report|project"
	if err != nil || string(log) != want {
		t.Fatalf("log=%q want=%q err=%v", log, want, err)
	}
}

func TestCommandCancelledBeforeStart(t *testing.T) {
	spec := commandSpec(t, "touch marker")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunCommand(ctx, spec); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(spec.Cwd, "marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command ran: %v", err)
	}
}

func TestCommandLogVisibleBeforeExit(t *testing.T) {
	spec := commandSpec(t, `printf started; while [ ! -f finish ]; do sleep 0.01; done; printf finished`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := RunCommand(ctx, spec); done <- err }()
	for {
		data, _ := os.ReadFile(filepath.Join(spec.AttemptDir, "output.log"))
		if string(data) == "started" {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("command exited before visible output: %v", err)
		case <-ctx.Done():
			t.Fatal("output was not drained before command exit")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := os.Stat(filepath.Join(spec.AttemptDir, "receipt.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt written before Wait: %v", err)
	}
	if err := os.WriteFile(filepath.Join(spec.Cwd, "finish"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCommandCloseEvidence(t *testing.T) {
	steps := []CommandStepResult{{Command: "go test ./...", Receipt: &Receipt{Version: 1, ExitCode: 0}}, {Command: "go vet ./...", Receipt: &Receipt{Version: 1, ExitCode: 0}}}
	message := CommandCloseMessage("checks", "run-1", "manual:run-1", steps)
	if message != "Workflow checks: 2 command steps exited 0 (run run-1, occurrence manual:run-1)" {
		t.Fatalf("message=%q", message)
	}
	evidence := CommandEvidence(steps)
	if !reflect.DeepEqual(evidence, []string{"test:go test ./...", "test:go vet ./..."}) || ValidateClose("done", message, evidence) != nil {
		t.Fatalf("evidence=%v", evidence)
	}
	for _, receipt := range []*Receipt{nil, {Version: 1, ExitCode: 1}, {Version: 1, ExitCode: 0, Interrupted: true}} {
		if got := CommandCloseMessage("checks", "run-1", "occ", []CommandStepResult{{Command: "check", Receipt: receipt}}); got != "" {
			t.Fatalf("invented success: %q", got)
		}
	}
}

func TestCommandUncertainReceiptNeverCloses(t *testing.T) {
	for _, reason := range []string{"deadline", "descendants"} {
		t.Run(reason, func(t *testing.T) {
			r := commandFixture()
			r.Steps[0].Receipt.Interrupted = reason == "deadline"
			r.Steps[0].Receipt.GroupAlive = reason == "descendants"
			ops := SettleOps(r, nil, settleTime)
			for _, op := range ops {
				if op.Kind == "close" {
					t.Fatal("uncertain command closed the issue")
				}
			}
			_, payload := settlePayload(t, ops, "observe")
			if payload.Observation.Status != "unknown" {
				t.Fatalf("observation=%+v", payload.Observation)
			}
		})
	}
}

func TestCommandEvidenceBoundedAndUnique(t *testing.T) {
	command := strings.Repeat("界<&\n", 20000)
	steps := []CommandStepResult{{Command: command, Receipt: &Receipt{Version: 1}}, {Command: command, Receipt: &Receipt{Version: 1}}}
	evidence := CommandEvidence(steps)
	encoded, err := json.Marshal(evidence)
	message := CommandCloseMessage(strings.Repeat("界<&\n", 20000), "run-1", "occ", steps)
	encodedMessage, _ := json.Marshal(message)
	if err != nil || len(encoded) > 64<<10 || len(encodedMessage) > 64<<10 || len(evidence) != 1 || !utf8.ValidString(evidence[0]) || !strings.Contains(message, "2 command steps exited 0") || ValidateClose("done", message, evidence) != nil {
		t.Fatalf("invalid bounded evidence: count=%d encoded=%d message=%d err=%v", len(evidence), len(encoded), len(encodedMessage), err)
	}
}

func FuzzCommandEvidence(f *testing.F) {
	f.Add("go test ./...")
	f.Add(strings.Repeat("界<&\n", 20000))
	f.Add(strings.Repeat(" ", 70000) + "true")
	f.Fuzz(func(t *testing.T, command string) {
		if !utf8.ValidString(command) || strings.TrimSpace(command) == "" {
			return
		}
		steps := []CommandStepResult{{Command: command, Receipt: &Receipt{Version: 1}}}
		evidence := CommandEvidence(steps)
		encoded, err := json.Marshal(evidence)
		if err != nil || len(encoded) > 64<<10 || len(evidence) != 1 || !utf8.ValidString(evidence[0]) {
			t.Fatalf("invalid command evidence: %v", evidence)
		}
	})
}

type commandFaultFile struct {
	*os.File
	syncErr, closeErr error
}

func (f commandFaultFile) Sync() error  { return errors.Join(f.File.Sync(), f.syncErr) }
func (f commandFaultFile) Close() error { return errors.Join(f.File.Close(), f.closeErr) }

func TestCommandLogDurabilityFailureUnknown(t *testing.T) {
	for _, failure := range []string{"sync", "close"} {
		t.Run(failure, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "output")
			if err != nil {
				t.Fatal(err)
			}
			fault := commandFaultFile{File: f}
			if failure == "sync" {
				fault.syncErr = syscall.EIO
			} else {
				fault.closeErr = syscall.EIO
			}
			log := commandLog{file: fault}
			receipt := Receipt{Version: 1}
			if err := log.finish(&receipt); !errors.Is(err, syscall.EIO) {
				t.Fatalf("err=%v", err)
			}
			path := filepath.Join(t.TempDir(), "receipt.json")
			if err := state.WriteJSON(state.OSFS{}, path, receipt); err != nil {
				t.Fatal(err)
			}
			var saved Receipt
			if err := state.ReadJSON(state.OSFS{}, path, 4096, &saved); err != nil {
				t.Fatal(err)
			}
			if !saved.Uncertain || CommandCloseMessage("checks", "run-1", "occ", []CommandStepResult{{Command: "check", Receipt: &saved}}) != "" {
				t.Fatal("log durability error was lost from the receipt")
			}
		})
	}
}

func TestCommandLaneEnvironment(t *testing.T) {
	spec := commandSpec(t, `printf '%s|%s|%s|%s|%s' "$KATA_PROJECT" "${KATA_DAEMON-unset}" "${KATA_SERVER-unset}" "${TOKEN_A-unset}" "${TOKEN_B-unset}"`)
	spec.Env = []string{"PATH=/usr/bin:/bin", "KATA_PROJECT=wrong-project", "KATA_DAEMON=wrong-daemon", "KATA_SERVER=wrong-server", "TOKEN_A=secret-a", "TOKEN_B=secret-b"}
	spec.CredentialEnvs = []string{"TOKEN_A", "TOKEN_B"}
	spec.Project = "example-project"
	receipt, e := RunCommand(t.Context(), spec)
	if e != nil || receipt.ExitCode != 0 {
		t.Fatal(receipt, e)
	}
	b, e := os.ReadFile(filepath.Join(spec.AttemptDir, "output.log"))
	if e != nil || string(b) != "example-project|unset|unset|unset|unset" {
		t.Fatal(string(b), e)
	}
}

func TestCommandCloseMessageSingularStep(t *testing.T) {
	got := CommandCloseMessage("checks", "run-1", "manual:run-1", []CommandStepResult{{Command: "true", Receipt: &Receipt{Version: 1, ExitCode: 0}}})
	if got != "Workflow checks: 1 command step exited 0 (run run-1, occurrence manual:run-1)" {
		t.Fatal(got)
	}
}
