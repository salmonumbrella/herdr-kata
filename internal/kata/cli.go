package kata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
)

type Runner func(ctx context.Context, argv, env []string, dir string, stdin []byte) (stdout, stderr []byte, exit int, err error)
type Starter func(ctx context.Context, argv, env []string, dir string) (Proc, error)

// Wait is called exactly once by Tail, after its output has been drained.
type Proc struct {
	Stdout, Stderr io.Reader
	Wait           func() error
}
type CLI struct {
	cfg       config.Kata
	stateDir  string
	runner    Runner
	starter   Starter
	mu        sync.Mutex
	diagnosis Diagnosis
}

func New(cfg config.Kata, stateDir string, r Runner, s Starter) *CLI {
	cfg.Env = cloneEnv(cfg.Env)
	cfg.SecretEnvs = append([]string(nil), cfg.SecretEnvs...)
	cfg.CredentialNames = append([]string(nil), cfg.CredentialNames...)
	if r == nil {
		r = execRunner
	}
	if s == nil {
		s = execStarter
	}
	return &CLI{cfg: cfg, stateDir: stateDir, runner: r, starter: s}
}

func cloneEnv(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	maps.Copy(dst, src)
	return dst
}

func (c *CLI) Env() []string { env, _ := c.environment(); return env }

func (c *CLI) environment() ([]string, error) {
	credential, err := c.cfg.Credential()
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	credentials := map[string]bool{c.cfg.CredentialEnv: true}
	for _, name := range c.cfg.CredentialNames {
		credentials[name] = true
	}
	secretSources := map[string]bool{}
	for _, name := range c.cfg.SecretEnvs {
		secretSources[name] = true
	}
	for _, pair := range os.Environ() {
		k, v, _ := strings.Cut(pair, "=")
		if credentials[k] || secretSources[k] {
			continue
		}
		switch k {
		case "HOME", "PATH", "USER", "LANG", "LC_ALL", "TZ":
			values[k] = v
		default:
			if strings.HasPrefix(k, "XDG_") {
				values[k] = v
			}
		}
	}
	for k, v := range c.cfg.Env {
		if !credentials[k] {
			values[k] = v
		}
	}
	if k := c.cfg.CredentialEnv; k != "" {
		values[k] = credential
	}
	values["KATA_HTTP_TIMEOUT"] = "45s"
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+values[k])
	}
	return env, nil
}

func (c *CLI) argv(args ...string) []string {
	argv := []string{c.cfg.Bin, "--json", "--project", c.cfg.Project, "--as", c.cfg.Actor, "--teammate", "herdr-kata"}
	if c.cfg.Daemon != "" {
		argv = append(argv, "--daemon", c.cfg.Daemon)
	}
	return append(argv, args...)
}

// hubCallConcurrency caps one-shot Kata calls across every lane in this
// process. Event streams are long-lived and take no slot.
const hubCallConcurrency = 4

var hubCallSlots = make(chan struct{}, hubCallConcurrency)

// A call queued for a slot has not reached Kata. Its one-minute deadline
// starts once it holds a slot.
func (c *CLI) call(ctx context.Context, body []byte, result any, args ...string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case hubCallSlots <- struct{}{}:
	}
	defer func() { <-hubCallSlots }()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	env, err := c.environment()
	if err != nil {
		return err
	}
	out, stderr, exit, err := c.runner(ctx, c.argv(args...), env, c.stateDir, body)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if exit != 0 || err != nil {
		return decodeError(out, stderr, exit, err)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(out, result); err != nil {
		return fmt.Errorf("kata response: %w", err)
	}
	return nil
}

const maxResponse = 16 << 20

// limitWriter continues draining after the cap so a verbose child cannot hang.
type limitWriter struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	n := len(p)
	left := w.limit - w.Len()
	if len(p) > left {
		p = p[:left]
		w.overflow = true
	}
	_, _ = w.Buffer.Write(p)
	return n, nil
}

func execRunner(ctx context.Context, argv, env []string, dir string, stdin []byte) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env, cmd.Dir, cmd.Stdin = env, dir, bytes.NewReader(stdin)
	out, stderr := &limitWriter{limit: maxResponse}, &limitWriter{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	exit := 0
	if e, ok := errors.AsType[*exec.ExitError](err); ok {
		exit = e.ExitCode()
	}
	if out.overflow {
		return nil, stderr.Bytes(), exit, fmt.Errorf("kata stdout exceeds %d bytes", maxResponse)
	}
	return out.Bytes(), stderr.Bytes(), exit, err
}

func execStarter(ctx context.Context, argv, env []string, dir string) (Proc, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	stop, err := configureTailProcess(cmd)
	if err != nil {
		return Proc{}, err
	}
	started := false
	defer func() {
		if !started {
			stop()
		}
	}()
	cmd.Env, cmd.Dir = env, dir
	cmd.WaitDelay = time.Second
	out, err := cmd.StdoutPipe()
	if err != nil {
		return Proc{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = out.Close()
		return Proc{}, err
	}
	if err := cmd.Start(); err != nil {
		_ = out.Close()
		_ = stderr.Close()
		return Proc{}, err
	}
	started = true
	return Proc{Stdout: out, Stderr: stderr, Wait: func() error {
		defer stop()
		return cmd.Wait()
	}}, nil
}

// WithConfig binds a new lane to the same process transport. Diagnosis and
// route state are intentionally not shared between the resulting CLIs.
func (c *CLI) WithConfig(cfg config.Kata, dir string) *CLI { return New(cfg, dir, c.runner, c.starter) }
