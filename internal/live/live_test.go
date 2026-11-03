//go:build live

// Package live exercises the operator CLI against disposable real runtimes.
package live

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/control"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// run never logs a native agent's transcript or credentials.
func run(t *testing.T, cwd string, input []byte, bin string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, bin, args...)
	c.Dir = cwd
	c.Stdin = bytes.NewReader(input)
	b, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", filepath.Base(bin), args[0], err, b)
	}
	return b
}

func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func download(t *testing.T, root, version string) string {
	t.Helper()
	base := "https://releases.herdr.dev/core/" + version
	b := run(t, root, nil, "curl", "-fsSL", base+"/manifest.json")
	var manifest struct {
		Targets map[string]struct{ URL, SHA256 string }
	}
	if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	osName := map[string]string{"linux": "linux", "darwin": "macos"}[runtime.GOOS]
	target := manifest.Targets[osName+"-"+arch]
	if target.URL == "" || !strings.HasPrefix(target.URL, base+"/") {
		t.Fatal("no official release target for this platform")
	}
	dir := filepath.Join(root, version)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "core.tar.gz")
	run(t, root, nil, "curl", "-fsSL", "-o", archive, target.URL)
	b, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != target.SHA256 {
		t.Fatal("official hc archive checksum mismatch")
	}
	run(t, dir, nil, "tar", "-xzf", archive)
	t.Logf("official hc %s SHA-256 verified: %s", version, target.SHA256)
	return filepath.Join(dir, "herdr-core")
}

type fixture struct {
	t                                                          *testing.T
	root, work, bin, coreBin, kataBin, config, session, coreID string
	h                                                          hc.HC
	store                                                      *journal.Store
}

func TestCodexSandboxSelection(t *testing.T) {
	for _, mode := range []string{"", "workspace-write", "danger-full-access"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HERDR_KATA_LIVE_CODEX_SANDBOX", mode)
			want := mode
			if want == "" {
				want = "workspace-write"
			}
			if got := codexSandbox(); got != want {
				t.Fatalf("sandbox %q, want %q", got, want)
			}
		})
	}
}

func codexSandbox() string {
	if mode := os.Getenv("HERDR_KATA_LIVE_CODEX_SANDBOX"); mode != "" {
		return mode
	}
	return "workspace-write"
}

func newFixture(t *testing.T, bin, coreBin, auth string) *fixture {
	t.Helper()
	root, err := os.MkdirTemp("", "hk3-")
	if err != nil {
		t.Fatal(err)
	}
	// Restore every original selector only after teardown, including on failure.
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(key, "KATA_") || strings.HasPrefix(key, "HC_") || strings.HasPrefix(key, "XDG_") || slices.Contains([]string{"CODEX_HOME", "CLAUDE_CONFIG_DIR", "PI_CODING_AGENT_DIR", "OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_MANAGED_BY_NPM"}, key) {
			t.Setenv(key, "")
			os.Unsetenv(key)
		}
	}
	for k, v := range map[string]string{"HOME": filepath.Join(root, "home"), "XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_STATE_HOME": filepath.Join(root, "state"), "XDG_RUNTIME_DIR": filepath.Join(root, "runtime"), "KATA_HOME": filepath.Join(root, "kata"), "CODEX_HOME": filepath.Join(root, "codex")} {
		t.Setenv(k, v)
		if err := os.MkdirAll(v, 0700); err != nil {
			t.Fatal(err)
		}
	}
	session := filepath.Base(root)
	t.Setenv("HC_SESSION", session)
	kataBin, err := exec.LookPath("kata")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, root: root, work: filepath.Join(root, "work"), bin: bin, coreBin: coreBin, kataBin: kataBin, config: filepath.Join(root, "plugin.toml"), session: session}
	f.store = journal.New(state.Layout{Root: filepath.Join(root, "plugin-state")})
	if err := os.Mkdir(f.work, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "kata", "config.toml"), []byte("[[daemon]]\nname=\"fixture\"\nlocal=true\n"))
	if auth != "" {
		b, err := os.ReadFile(auth)
		if err != nil {
			t.Fatal("native CLI login unavailable")
		}
		write(t, filepath.Join(root, "codex", "auth.json"), b)
	}
	f.h = hc.New(coreBin, session)
	info, err := f.h.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.coreID = info.CoreID
	f.verifyCore()
	t.Cleanup(f.cleanup)
	f.kata(nil, "init")
	return f
}

func (f *fixture) verifyCore() {
	f.t.Helper()
	path := filepath.Join(f.root, "state", "herdr-core", f.session, "core.json")
	b, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal("scratch core state path unavailable: ", err)
	}
	var v struct{ ID string }
	if json.Unmarshal(b, &v) != nil || v.ID != f.coreID {
		f.t.Fatal("scratch core state identity mismatch")
	}
	info, err := f.h.Info(context.Background())
	if err != nil || info.CoreID != f.coreID {
		f.t.Fatal("scratch core changed; refusing terminal operation")
	}
}
func (f *fixture) kata(input []byte, args ...string) []byte {
	f.t.Helper()
	prefix := []string{"--workspace", f.work, "--project", "example-project", "--daemon", "fixture", "--as", "worker", "--teammate", "herdr-kata", "--json"}
	return run(f.t, f.work, input, f.kataBin, append(prefix, args...)...)
}
func (f *fixture) cli(args ...string) []byte {
	f.t.Helper()
	f.verifyCore()
	return run(f.t, f.work, nil, f.bin, append([]string{"--config", f.config, "--state", f.store.Layout.Root}, args...)...)
}
func (f *fixture) configure(argv []string) {
	f.t.Helper()
	quoted := make([]string, len(argv))
	for i, s := range argv {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	text := fmt.Sprintf("executor=\"live-worker\"\nhc_session=%q\nhc_bin=%q\n[kata]\nbin=%q\ndaemon=\"fixture\"\n[kata.env]\nKATA_HOME=%q\n[[projects]]\nname=\"example-project\"\nactor=\"worker\"\ncommands=true\n[checkouts]\nexample-project=%q\n[profiles.default]\nargv=[%s]\n[limits]\ndefault_timeout=\"10m\"\nmax_timeout=\"10m\"\n", f.session, f.coreBin, f.kataBin, filepath.Join(f.root, "kata"), f.work, strings.Join(quoted, ","))
	write(f.t, f.config, []byte(text))
}
func (f *fixture) createJob(uid string, action map[string]any) {
	f.t.Helper()
	body := map[string]any{"name": "Scratch task", "definition": map[string]any{"version": 1, "kind": "job", "enabled": true, "trigger": map[string]any{"kind": "manual"}, "action": action, "issue": map[string]any{"kind": "per-run", "title": "Scratch execution"}, "checkout_key": "example-project", "catchup": "all", "overlap": "forbid", "options": map[string]any{"adapter": "herdr-kata"}}}
	b, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	f.kata(b, "cron", "job", "create", "--uid", uid, "--file", "-")
}
func (f *fixture) await(uid string, predicate func(journal.Run) bool) journal.Run {
	f.t.Helper()
	deadline := time.NewTimer(11 * time.Minute)
	defer deadline.Stop()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	var last journal.Run
	for {
		runs, err := f.store.All()
		if err != nil && !os.IsNotExist(err) {
			f.t.Fatal(err)
		}
		for _, r := range runs {
			if r.JobUID == uid {
				last = r
				if predicate(r) {
					return r
				}
			}
		}
		if last.Phase == "held" {
			if dir := os.Getenv("HERDR_KATA_LIVE_DIAGNOSTICS"); dir != "" {
				f.verifyCore()
				for _, a := range last.Attempts {
					text, _, err := f.h.Read(context.Background(), a.Address, 200)
					if err == nil {
						write(f.t, filepath.Join(dir, "native-output.txt"), []byte(text))
					}
				}
			}
			f.t.Fatalf("scratch run held: %s (%s)", last.Hold, last.Detail)
		}
		select {
		case <-deadline.C:
			f.t.Fatalf("scratch run timed out: phase=%s hold=%s attempts=%d", last.Phase, last.Hold, len(last.Attempts))
		case <-poll.C:
		}
	}
}
func (f *fixture) assertEvidence(r journal.Run, closed bool) {
	f.t.Helper()
	// Wire names have underscores; decode those explicitly.
	var response struct {
		Run struct {
			Status    string
			IssueUID  string `json:"issue_uid"`
			Revision  int
			StartedAt *time.Time `json:"started_at"`
			EndedAt   *time.Time `json:"ended_at"`
		}
	}
	b := f.kata(nil, "cron", "run", "show", r.RunUID)
	if err := json.Unmarshal(b, &response); err != nil {
		f.t.Fatal(err)
	}
	if response.Run.Status != "succeeded" || response.Run.Revision != 2 || response.Run.IssueUID != r.IssueUID || response.Run.StartedAt == nil || response.Run.EndedAt == nil {
		f.t.Fatal("Kata run evidence missing or incorrect")
	}
	var issue struct {
		Issue struct {
			Status string
			UID    string
		}
	}
	if err := json.Unmarshal(f.kata(nil, "show", r.IssueUID), &issue); err != nil {
		f.t.Fatal(err)
	}
	if issue.Issue.UID != r.IssueUID || (closed && issue.Issue.Status != "closed") || (!closed && issue.Issue.Status != "open") {
		f.t.Fatalf("scratch issue status=%s closed expected=%v", issue.Issue.Status, closed)
	}
}
func (f *fixture) cleanup() {
	f.t.Helper()
	f.verifyCore()
	// This fresh session contains only the worker and attempts this test created.
	ids, err := f.h.List(context.Background(), true, nil)
	if err != nil {
		f.t.Error(err)
		return
	}
	for _, id := range ids {
		f.verifyCore()
		term, err := f.h.Get(context.Background(), id)
		if err != nil {
			f.t.Error(err)
			return
		}
		if term.Labels["kata.worker"] != control.Name(f.store.Layout.Root) && term.Labels["kata.executor"] != "live-worker" {
			f.t.Error("foreign terminal in scratch core; refusing teardown")
			return
		}
		if term.State == "live" {
			if err := f.h.Kill(context.Background(), id); err != nil {
				f.t.Error(err)
				return
			}
		}
		if err := f.h.Remove(context.Background(), id); err != nil {
			f.t.Error(err)
			return
		}
	}
	run(f.t, f.work, nil, f.kataBin, "daemon", "stop", "--json")
	f.verifyCore()
	run(f.t, f.work, nil, f.coreBin, "server", "stop", "--json")
	if err := os.RemoveAll(f.root); err != nil {
		f.t.Error(err)
	}
	f.t.Log("scratch terminals, Kata daemon and core stopped; directories removed")
}

func TestLive(t *testing.T) {
	if os.Getenv("HERDR_KATA_LIVE") != "1" {
		t.Skip("set HERDR_KATA_LIVE=1 for isolated live checks")
	}
	root, err := os.MkdirTemp("", "hk3-tools-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Clean(filepath.Join(cwd, "../.."))
	bin := filepath.Join(root, "herdr-kata")
	run(t, repo, nil, "go", "build", "-o", bin, "./cmd/herdr-kata")
	installed, err := exec.LookPath("hc")
	if err != nil {
		t.Fatal(err)
	}
	authHome := os.Getenv("CODEX_HOME")
	if authHome == "" {
		authHome = filepath.Join(os.Getenv("HOME"), ".codex")
	}
	auth := filepath.Join(authHome, "auth.json")
	script := filepath.Join(cwd, "testdata", "scripted-agent.sh")
	for _, version := range []string{"installed", "0.1.16", "0.1.15"} {
		t.Run(version, func(t *testing.T) {
			coreBin := installed
			if version != "installed" {
				coreBin = download(t, root, version)
			}
			t.Run("zero-config", func(t *testing.T) { zeroConfigJobs(t, bin, coreBin, script) })
			t.Run("scripted", func(t *testing.T) {
				f := newFixture(t, bin, coreBin, "")
				f.configure([]string{"/bin/sh", script})
				uid := "01J00000000000000000000010"
				f.createJob(uid, map[string]any{"kind": "execute", "prompt": "Complete the scratch task."})
				f.cli("up")
				f.cli("run", uid)
				r := f.await(uid, func(r journal.Run) bool { return r.Phase == "reported" })
				f.assertEvidence(r, true)
				if len(r.Attempts) != 1 || r.Attempts[0].Status != "succeeded" {
					t.Fatal("scripted attempt did not succeed exactly once")
				}
				t.Log("scripted agent: finished/exited with valid result, no OSC 7501")
			})
			t.Run("gated-workflow", func(t *testing.T) {
				f := newFixture(t, bin, coreBin, "")
				f.configure([]string{"/bin/sh", script})
				workflow := []byte(`{"name":"Scratch gate","definition":{"version":1,"steps":[{"key":"gate","kind":"command","command":"exit 7","options":{"skip_exit":7}},{"key":"work","kind":"prompt","prompt":"Should be skipped","after":["gate"]}]}}`)
				wid := "01J00000000000000000000012"
				f.kata(workflow, "cron", "workflow", "create", "--uid", wid, "--file", "-")
				uid := "01J00000000000000000000010"
				f.createJob(uid, map[string]any{"kind": "execute", "workflow_uid": wid})
				f.cli("up")
				f.cli("run", uid)
				r := f.await(uid, func(r journal.Run) bool { return r.Phase == "reported" })
				f.assertEvidence(r, false)
				if len(r.Attempts) != 1 || r.Attempts[0].Status != "skipped" {
					t.Fatal("closed gate launched dependent step")
				}
				if _, err := os.Stat(filepath.Join(f.work, "launches")); !os.IsNotExist(err) {
					t.Fatal("dependent prompt launched")
				}
			})
			t.Run("worker-recovery", func(t *testing.T) {
				f := newFixture(t, bin, coreBin, "")
				f.configure([]string{"/bin/sh", script})
				write(t, filepath.Join(f.work, "wait"), nil)
				uid := "01J00000000000000000000010"
				f.createJob(uid, map[string]any{"kind": "execute", "prompt": "Complete after the scratch wait file is removed."})
				f.cli("up")
				f.cli("run", uid)
				r := f.await(uid, func(r journal.Run) bool { return len(r.Attempts) == 1 && r.Attempts[0].Address != "" })
				f.verifyCore()
				worker, err := f.h.Get(context.Background(), control.Name(f.store.Layout.Root))
				if err != nil {
					t.Fatal(err)
				}
				if err := f.h.Kill(context.Background(), worker.Address); err != nil {
					t.Fatal(err)
				}
				f.cli("up")
				if err := os.Remove(filepath.Join(f.work, "wait")); err != nil {
					t.Fatal(err)
				}
				got := f.await(uid, func(r journal.Run) bool { return r.Phase == "reported" })
				f.assertEvidence(got, true)
				b, err := os.ReadFile(filepath.Join(f.work, "launches"))
				if err != nil || strings.TrimSpace(string(b)) != r.RunUID || len(got.Attempts) != 1 || got.Attempts[0].Nonce != r.Attempts[0].Nonce || got.Attempts[0].Address != r.Attempts[0].Address {
					t.Fatal("worker restart repeated launch")
				}
			})
			t.Run("real-codex", func(t *testing.T) {
				if os.Getenv("HERDR_KATA_LIVE_CODEX") != "1" {
					t.Skip("set HERDR_KATA_LIVE_CODEX=1 for native Codex")
				}
				codex, err := exec.LookPath("codex")
				if err != nil {
					t.Fatal(err)
				}
				f := newFixture(t, bin, coreBin, auth)
				f.configure([]string{codex, "--no-daemon", "--model", "gpt-6.1-sol", "--sandbox", codexSandbox(), "exec", "--skip-git-repo-check", "--ignore-user-config", "--add-dir", f.root})
				uid := "01J00000000000000000000010"
				f.createJob(uid, map[string]any{"kind": "execute", "prompt": "This is an isolated live test. Write completed.txt containing exactly 'complete' in the current directory and read it back. Then use the exact report command below with status succeeded and summary 'Scratch file checked'. Close the scratch issue using the exact Kata prefix below and evidence reviewed-paths:completed.txt. Do only this task. Do not inspect any other directory or account. If a permission request is denied, report needs-human and stop."})
				f.cli("up")
				f.cli("run", uid)
				r := f.await(uid, func(r journal.Run) bool { return r.Phase == "reported" })
				f.assertEvidence(r, true)
				b, err := os.ReadFile(filepath.Join(f.work, "completed.txt"))
				if err != nil || strings.TrimSpace(string(b)) != "complete" || len(r.Attempts) != 1 {
					t.Fatal("native Codex task output missing")
				}
				a := r.Attempts[0]
				row := "finished/exited with valid result"
				if a.ResultStableSince != nil {
					row = "never-reported with result stable for 30s"
				} else if a.TurnEndedAt != nil {
					row = "reported idle/done turn with valid result"
				}
				t.Logf("real Codex gpt-6.1-sol settlement row: %s; outcome=%s", row, r.Outcome)
			})
		})
	}
}
