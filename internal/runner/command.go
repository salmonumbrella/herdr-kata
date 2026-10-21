package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/salmonumbrella/herdr-kata/internal/crashpoint"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// CommandSpec describes one attempt. The caller saves sending before calling
// RunCommand, then copies its receipt into run.json in a serialized transition.
// Env is the worker environment; RunDir is the run root, not AttemptDir.
type CommandSpec struct {
	Run, Step                  string
	Attempt                    int
	Command, Cwd, AttemptDir   string
	Input, Previous            string
	Env                        []string
	Secrets                    map[string]string
	CredentialEnv              string
	CredentialEnvs             []string
	KataEnv                    map[string]string
	Daemon                     string
	RunDir, ReportBin, Project string
	Deadline                   time.Time
}

// ErrCommandGroupAlive means Wait returned without proof that all descendants
// stopped. It is an unknown outcome, even when the shell exited zero.
var ErrCommandGroupAlive = errors.New("command process group still exists")

// RunCommand runs exactly one attempt. Nonzero exits return an *exec.ExitError
// and a durable receipt. Cancellation and uncertain descendants are unknown
// outcomes, not retryable exits. Start failures never produce a receipt.
func RunCommand(ctx context.Context, spec CommandSpec) (Receipt, error) {
	if !spec.Deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, spec.Deadline)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if spec.Run == "" || spec.Step == "" || spec.Attempt < 1 || spec.Command == "" || spec.Cwd == "" || spec.AttemptDir == "" {
		return Receipt{}, errors.New("command requires identity, positive attempt, command, cwd and attempt directory")
	}
	if err := os.MkdirAll(spec.AttemptDir, 0700); err != nil {
		return Receipt{}, err
	}
	f, err := os.OpenFile(filepath.Join(spec.AttemptDir, "output.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Receipt{}, err
	}
	defer f.Close()
	log := &commandLog{file: f}
	cmd := exec.Command("sh", "-c", spec.Command)
	cmd.Dir, cmd.Env = spec.Cwd, commandEnv(spec)
	cmd.SysProcAttr = commandProcAttr()
	reader, writer, err := os.Pipe()
	if err != nil {
		return Receipt{}, err
	}
	defer reader.Close()
	defer writer.Close()
	cmd.Stdout, cmd.Stderr = writer, writer
	receipt := Receipt{Version: 1, Run: spec.Run, Step: spec.Step, Attempt: spec.Attempt, StartedAt: time.Now().UTC()}
	if err := cmd.Start(); err != nil {
		return Receipt{}, err
	}
	receipt.PGID = cmd.Process.Pid
	started := receipt
	started.Uncertain = true
	if err := state.WriteJSON(state.OSFS{}, filepath.Join(spec.AttemptDir, "process.json"), started); err != nil {
		_ = syscall.Kill(-receipt.PGID, syscall.SIGKILL)
		waitErr := cmd.Wait()
		receipt.EndedAt, receipt.Uncertain, receipt.GroupAlive = time.Now().UTC(), true, !GroupGone(receipt.PGID)
		return receipt, errors.Join(err, waitErr)
	}
	_ = writer.Close()
	drained := make(chan error, 1)
	go func() {
		crashpoint.Hit("command.output.start")
		_, err := io.Copy(log, reader)
		crashpoint.Hit("command.output.drained")
		drained <- err
	}()
	type waitResult struct {
		err       error
		uncertain bool
	}
	waited := make(chan waitResult, 1)
	go func() {
		result := waitResult{err: cmd.Wait()}
		// Keep draining independently: Cmd.Wait can hide ErrWaitDelay behind
		// an ExitError, and an escaped descendant can outlive the group.
		timer := time.NewTimer(time.Second)
		drainErr := waitCommandOutput(drained, reader, timer.C)
		result.uncertain = drainErr != nil
		result.err = errors.Join(result.err, drainErr)
		timer.Stop()
		waited <- result
	}()
	var result waitResult
	select {
	case result = <-waited:
	case <-ctx.Done():
		receipt.Interrupted = true
		_ = syscall.Kill(-receipt.PGID, syscall.SIGTERM)
		timer := time.NewTimer(10 * time.Second)
		select {
		case result = <-waited:
			if !GroupGone(receipt.PGID) {
				// The shell can exit on TERM while descendants ignore it.
				<-timer.C
				_ = syscall.Kill(-receipt.PGID, syscall.SIGKILL)
			}
		case <-timer.C:
			_ = syscall.Kill(-receipt.PGID, syscall.SIGKILL)
			result = <-waited
		}
		timer.Stop()
	}
	err = result.err
	if receipt.Interrupted {
		err = errors.Join(ctx.Err(), err)
	}
	receipt.EndedAt, receipt.ExitCode = time.Now().UTC(), -1
	if cmd.ProcessState != nil {
		receipt.ExitCode = cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	receipt.Uncertain = result.uncertain || cmd.ProcessState == nil || err != nil && !receipt.Interrupted && !errors.As(err, &exitErr)
	receipt.GroupAlive = !GroupGone(receipt.PGID)
	if receipt.GroupAlive {
		err = errors.Join(err, ErrCommandGroupAlive)
	}
	// The output copier is joined, so these writes cannot race with it.
	err = errors.Join(err, log.finish(&receipt))
	err = errors.Join(err, state.WriteJSON(state.OSFS{}, filepath.Join(spec.AttemptDir, "receipt.json"), receipt))
	return receipt, err
}

func waitCommandOutput(drained <-chan error, reader io.Closer, timeout <-chan time.Time) error {
	select {
	case err := <-drained:
		return err
	case <-timeout:
		// All writers may have exited while the copier was descheduled
		// before reading. A hung-up pipe can be drained to EOF safely.
		if pipe, ok := reader.(*os.File); !ok || !commandOutputClosed(pipe) {
			_ = reader.Close()
		}
		crashpoint.Hit("command.output.timeout")
		// EOF may already have been observed while this goroutine or the
		// copier was descheduled. Only an interrupted drain is uncertain.
		if err := <-drained; err != nil {
			return exec.ErrWaitDelay
		}
		return nil
	}
}

func commandOutputClosed(reader *os.File) bool {
	raw, err := reader.SyscallConn()
	if err != nil {
		return false
	}
	closed := false
	err = raw.Control(func(fd uintptr) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			_, err := unix.Poll(fds, 0)
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			closed = err == nil && fds[0].Revents&unix.POLLHUP != 0
			return
		}
	})
	return err == nil && closed
}

func commandEnv(spec CommandSpec) []string {
	runEnv := map[string]string{"HERDR_KATA_RUN": spec.Run, "HERDR_KATA_RUN_DIR": spec.RunDir,
		"HERDR_KATA_REPORT": spec.ReportBin, "KATA_PROJECT": spec.Project, "HERDR_KATA_INPUT": spec.Input, "HERDR_KATA_PREVIOUS": spec.Previous, "HERDR_KATA_NOTE_FILE": filepath.Join(spec.AttemptDir, "note.txt")}
	blocked := map[string]bool{"KATA_AUTH_TOKEN": true}
	if spec.CredentialEnv != "" {
		blocked[spec.CredentialEnv] = true
	}
	for _, key := range spec.CredentialEnvs {
		blocked[key] = true
	}
	values := map[string]string{}
	for _, item := range spec.Env {
		key, value, ok := strings.Cut(item, "=")
		if !ok || blocked[key] {
			continue
		}
		if key == "KATA_PROJECT" || key == "KATA_DAEMON" || key == "KATA_SERVER" {
			continue
		}
		values[key] = value
	}
	maps.Copy(values, spec.KataEnv)
	maps.Copy(values, spec.Secrets)
	// Runtime routing overrides shared selectors. A lane without a daemon
	// intentionally leaves it absent, even when the worker inherited one.
	delete(values, "KATA_DAEMON")
	if spec.Daemon != "" {
		values["KATA_DAEMON"] = spec.Daemon
	}
	maps.Copy(values, runEnv)
	for key := range blocked {
		delete(values, key)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}

	return env
}

// CommandStepResult pairs a frozen command with its recorded receipt.
type CommandStepResult struct {
	Command string
	Receipt *Receipt
}

func CommandCloseMessage(workflowName, runUID, occKey string, steps []CommandStepResult) string {
	if len(steps) == 0 {
		return ""
	}
	for _, step := range steps {
		if !commandSucceeded(step.Receipt) {
			return ""
		}
	}
	// Reserve room for the receipt facts even when a definition name is huge.
	budget := maxSummaryBytes / 4
	noun := "steps"
	if len(steps) == 1 {
		noun = "step"
	}
	return fmt.Sprintf("Workflow %s: %d command %s exited 0 (run %s, occurrence %s)", capText(workflowName, budget), len(steps), noun, capText(runUID, budget), capText(occKey, budget))
}

// CommandEvidence returns successful commands as bounded CLI test evidence.
// Duplicate commands collapse because Kata rejects duplicate evidence values.
func CommandEvidence(steps []CommandStepResult) []string {
	var commands []string
	for _, step := range steps {
		if commandSucceeded(step.Receipt) && strings.TrimSpace(step.Command) != "" {
			commands = append(commands, step.Command)
		}
	}
	if len(commands) == 0 {
		return nil
	}
	var evidence []string
	budget := maxSummaryBytes / len(commands)
	for _, command := range commands {
		command = capText(strings.TrimSpace(command), max(0, budget-8))
		if strings.TrimSpace(command) != "" {
			evidence = appendCommandEvidence(evidence, []string{"test:" + command})
		}
	}
	return evidence
}

func commandSucceeded(receipt *Receipt) bool {
	return receipt != nil && receipt.Version == 1 && receipt.ExitCode == 0 && receipt.definite()
}

// definite excludes interruption and uncertain process or output completion.
func (r *Receipt) definite() bool {
	return !r.Interrupted && !r.GroupAlive && !r.Uncertain
}
