package runner

import (
	"context"
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"go.kenn.io/kit/secretref"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCommandSecretEnvironment(t *testing.T) {
	t.Setenv("EXAMPLE_SOURCE", "fixture-secret-only")
	t.Setenv("KATA_AUTH_TOKEN", "fixture-auth-only")
	t.Setenv("EXAMPLE_CREDENTIAL", "fixture-credential-only")
	t.Setenv("KATA_DAEMON", "wrong-route")
	t.Setenv("KATA_SERVER", "wrong-server")
	t.Setenv("KATA_HOME", "/srv/example/catalog")
	f := workflowExecution(t, defs.Step{Key: "check", Kind: "command", Command: `test "$GITHUB_TOKEN" = "$EXAMPLE_SOURCE" && test -z "${KATA_AUTH_TOKEN+x}" && test -z "${EXAMPLE_CREDENTIAL+x}" && test -z "${KATA_DAEMON+x}" && test -z "${KATA_SERVER+x}" && test "$KATA_HOME" = /srv/example/catalog && test "$KATA_PROJECT" = example-project && touch verified`})
	f.m.cfg.Kata.Project = "example-project"
	f.m.cfg.Kata.CredentialEnv = "EXAMPLE_CREDENTIAL"
	f.m.cfg.Secrets = map[string]secretref.Ref{"review": {Env: "EXAMPLE_SOURCE"}}
	f.s.Transition(f.uid, func(r *Run) error { r.JobDef.SecretRefs = map[string]string{"GITHUB_TOKEN": "review"}; return nil })
	r := executeWorkflow(t, f)
	if r.Outcome != "succeeded" {
		t.Fatalf("command secret handoff failed: %s %s", r.Outcome, r.Reason)
	}
	if _, e := os.Stat(filepath.Join(r.Cwd, "verified")); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{f.s.layout.RunFile(f.uid), filepath.Join(f.s.layout.AttemptDir(f.uid, "check", 1), "receipt.json"), filepath.Join(f.s.layout.AttemptDir(f.uid, "check", 1), "output.log")} {
		b, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(b), "fixture-secret-only") {
			t.Fatalf("secret persisted in %s", filepath.Base(p))
		}
	}
}

func TestUnavailableSecretDoesNotStartCommand(t *testing.T) {
	for _, kind := range []string{"env", "mode", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("EXAMPLE_MISSING", "")
			f := workflowExecution(t, defs.Step{Key: "check", Kind: "command", Command: "touch started", Retries: 2})
			source := secretref.Ref{Env: "EXAMPLE_MISSING"}
			if kind != "env" {
				p := filepath.Join(t.TempDir(), "secret")
				if e := os.WriteFile(p, []byte("fixture-secret-only"), 0644); e != nil {
					t.Fatal(e)
				}
				if kind == "symlink" {
					link := p + "-link"
					if e := os.Symlink(p, link); e != nil {
						t.Fatal(e)
					}
					p = link
				}
				source = secretref.Ref{File: p}
			}
			f.m.cfg.Secrets = map[string]secretref.Ref{"review": source}
			f.s.Transition(f.uid, func(r *Run) error { r.JobDef.SecretRefs = map[string]string{"GITHUB_TOKEN": "review"}; return nil })
			r := executeWorkflow(t, f)
			if r.Phase != Held || r.Reason != "secret_unavailable" || r.Steps[0].Attempt != 1 || r.Steps[0].Submission != None || r.Steps[0].Receipt != nil {
				t.Fatalf("unreadable secret was not held before execution: %+v", r)
			}
			if _, e := os.Stat(filepath.Join(r.Cwd, "started")); !os.IsNotExist(e) {
				t.Fatal("command started", e)
			}
			if f.count("tab.create") != 0 || f.count("agent.start") != 0 {
				t.Fatal("Herdr launched")
			}
		})
	}
}

func TestPromptSecretEnvironmentAndRotation(t *testing.T) {
	for _, mode := range []string{"", `{"agent_mode":"fresh"}`} {
		t.Run(mode, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "secret")
			if e := os.WriteFile(source, []byte("fixture-first"), 0600); e != nil {
				t.Fatal(e)
			}
			f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first", Options: []byte(mode)}, defs.Step{Key: "last", Kind: "prompt", Prompt: "last", Options: []byte(mode), After: []string{"first"}})
			f.m.cfg.Kata.Project = "example-project"
			f.m.cfg.Kata.Daemon = "example-route"
			f.m.cfg.Kata.CredentialEnv = "EXAMPLE_CREDENTIAL"
			f.m.cfg.Kata.Env = map[string]string{"KATA_SERVER": "https://hub.example", "KATA_AUTH_TOKEN": "fixture-auth", "EXAMPLE_CREDENTIAL": "fixture-credential"}
			f.m.cfg.Secrets = map[string]secretref.Ref{"review": {File: source}}
			f.s.Transition(f.uid, func(r *Run) error { r.JobDef.SecretRefs = map[string]string{"GITHUB_TOKEN": "review"}; return nil })
			if e := f.m.launch(t.Context(), f.uid, 0); e != nil {
				t.Fatal(e)
			}
			first := loaded(t, f.s, f.uid)
			// A bound result and settled occupant authorize moving to the next step.
			f.s.Transition(f.uid, func(r *Run) error {
				r.Steps[0].Status = "succeeded"
				r.Steps[0].Result = &ResultRef{Version: 1, Run: r.UID, Step: "first", Attempt: 1, Status: "succeeded"}
				return nil
			})
			if e := os.WriteFile(source, []byte("fixture-second"), 0600); e != nil {
				t.Fatal(e)
			}
			if e := f.m.launch(t.Context(), f.uid, 1); e != nil {
				t.Fatal(e)
			}
			last := loaded(t, f.s, f.uid)
			if AgentReference(first).TabID == AgentReference(last).TabID || AgentReference(first).AgentName == AgentReference(last).AgentName {
				t.Fatal("secret steps reused an agent")
			}
			var values []string
			for _, req := range f.h.Requests() {
				if req.Method == "tab.create" {
					var p struct {
						Env map[string]string `json:"env"`
					}
					if e := json.Unmarshal(req.Params, &p); e != nil {
						t.Fatal(e)
					}
					if p.Env["KATA_PROJECT"] != "example-project" || p.Env["KATA_DAEMON"] != "example-route" || p.Env["KATA_SERVER"] != "https://hub.example" {
						t.Fatal("prompt lost configured routing")
					}
					if _, set := p.Env["KATA_AUTH_TOKEN"]; set {
						t.Fatal("prompt received auth token")
					}
					if _, set := p.Env["EXAMPLE_CREDENTIAL"]; set {
						t.Fatal("prompt received credential")
					}
					values = append(values, p.Env["GITHUB_TOKEN"])
				}
			}
			if !reflect.DeepEqual(values, []string{"fixture-first", "fixture-second"}) {
				t.Fatalf("secret handoff: %q", values)
			}
			b, e := os.ReadFile(f.s.layout.RunFile(f.uid))
			if e != nil {
				t.Fatal(e)
			}
			if strings.Contains(string(b), "fixture-first") || strings.Contains(string(b), "fixture-second") {
				t.Fatal("secret persisted")
			}
		})
	}
}
func TestPromptUnavailableSecretDoesNotLaunch(t *testing.T) {
	t.Setenv("EXAMPLE_MISSING", "")
	f := newExecution(t)
	f.m.cfg.Secrets = map[string]secretref.Ref{"review": {Env: "EXAMPLE_MISSING"}}
	f.s.Transition(f.uid, func(r *Run) error { r.JobDef.SecretRefs = map[string]string{"GITHUB_TOKEN": "review"}; return nil })
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "secret_unavailable" || r.Steps[0].Attempt != 1 || r.Steps[0].Submission != None || f.count("tab.create") != 0 || f.count("agent.start") != 0 {
		t.Fatalf("prompt was not held before launch: %s %s", r.Phase, r.Reason)
	}
}

func TestResumeSecretHoldKeepsAttempt(t *testing.T) {
	t.Setenv("EXAMPLE_SOURCE", "")
	f := newAdmission(t)
	f.m.cfg.Secrets = map[string]secretref.Ref{"review": {Env: "EXAMPLE_SOURCE"}}
	f.d.Def.SecretRefs = map[string]string{"GITHUB_TOKEN": "review"}
	f.d.WFDef.Steps = []defs.Step{{Key: "main", Kind: "command", Command: `test -n "$GITHUB_TOKEN" && touch resumed`, Retries: 2}}
	target, e := f.m.pin()
	if e != nil {
		t.Fatal(e)
	}
	r, e := f.m.prepare(f.d, f.uid, "occurrence", target)
	if e != nil {
		t.Fatal(e)
	}
	r.Phase = Admitted
	r.Admitted = new(true)
	r.IssueUID = "issue"
	r.IssueRef = "project#task"
	r.IssueOwned = false
	if e = f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	if e = f.m.RestoreGates(); e != nil {
		t.Fatal(e)
	}
	if e = f.m.Execute(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r = loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "secret_unavailable" {
		t.Fatalf("not held: %s %s", r.Phase, r.Reason)
	}
	f.k.comment = func(context.Context, string, string, string) error { return nil }
	f.k.notify = func(context.Context, string, string, string) error { return nil }
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open", Author: "operator"}, nil
	}
	k := &resumeKata{admissionKata: f.k, ready: []kata.IssueView{{UID: "issue"}}}
	f.m.k = k
	f.q.k = k
	for head(loaded(t, f.s, f.uid)) != nil {
		deliver(t, f.q, f.uid)
	}
	req := Request{Version: 1, UID: "secret-resume", Kind: "resume", RunUID: f.uid}
	// An unchanged unavailable source must refuse before consuming authorization.
	f.start(t)
	if e = f.m.Resume(t.Context(), req); e == nil {
		t.Fatal("resume accepted unreadable secret")
	}
	r = loaded(t, f.s, f.uid)
	if len(r.ConsumedResumes) != 0 || r.Steps[0].Attempt != 1 {
		t.Fatal("failed resume consumed an attempt")
	}
	t.Setenv("EXAMPLE_SOURCE", "fixture-secret-only")
	if e = f.m.Resume(t.Context(), req); e != nil {
		t.Fatal(e)
	}
	if e = f.m.Execute(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r = loaded(t, f.s, f.uid)
	if r.Phase != Settled || r.Outcome != "succeeded" || r.Steps[0].Attempt != 1 || r.Steps[0].Reason != "" || len(r.PastAttempts) != 0 {
		t.Fatalf("resume consumed attempt: %s %s attempt %d", r.Phase, r.Reason, r.Steps[0].Attempt)
	}
	if _, e = os.Stat(filepath.Join(r.Cwd, "resumed")); e != nil {
		t.Fatal(e)
	}
}

func TestUnreadableSecretDoesNotConsumeRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if e := os.WriteFile(path, []byte("fixture-secret-only"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("EXAMPLE_SECRET_FILE", path)
	f := workflowExecution(t, defs.Step{Key: "check", Kind: "command", Command: `chmod 0644 "$EXAMPLE_SECRET_FILE"; exit 7`, Retries: 2})
	f.m.cfg.Secrets = map[string]secretref.Ref{"review": {File: path}}
	f.s.Transition(f.uid, func(r *Run) error { r.JobDef.SecretRefs = map[string]string{"GITHUB_TOKEN": "review"}; return nil })
	r := executeWorkflow(t, f)
	if r.Phase != Held || r.Reason != "secret_unavailable" || r.Steps[0].Attempt != 1 || r.Steps[0].Receipt == nil || r.Steps[0].Receipt.ExitCode != 7 {
		t.Fatalf("unavailable secret consumed retry: %s %s attempt=%d", r.Phase, r.Reason, r.Steps[0].Attempt)
	}
}

func TestCommandConfiguredRoutingOverridesInherited(t *testing.T) {
	spec := commandSpec(t, `test "$KATA_DAEMON" = example-route && test "$KATA_SERVER" = https://hub.example && test "$KATA_PROJECT" = example-project && test "$HERDR_KATA_RUN" = run-1 && test -z "${EXAMPLE_CREDENTIAL+x}"`)
	spec.Env = append(spec.Env, "KATA_DAEMON=wrong", "KATA_PROJECT=wrong", "KATA_SERVER=wrong", "EXAMPLE_CREDENTIAL=fixture-credential")
	spec.Daemon = "example-route"
	spec.Project = "example-project"
	spec.KataEnv = map[string]string{"KATA_SERVER": "https://hub.example"}
	spec.Secrets = map[string]string{"HERDR_KATA_RUN": "wrong", "EXAMPLE_CREDENTIAL": "fixture-secret"}
	spec.CredentialEnv = "EXAMPLE_CREDENTIAL"
	receipt, e := RunCommand(t.Context(), spec)
	if e != nil || receipt.ExitCode != 0 {
		t.Fatalf("routing or final credential fence failed: exit %d error %v", receipt.ExitCode, e)
	}
}

func TestSharedDaemonCannotReplaceLaneRouting(t *testing.T) {
	spec := commandSpec(t, `test -z "${KATA_DAEMON+x}"`)
	spec.Env = append(spec.Env, "KATA_DAEMON=wrong-inherited-route")
	spec.KataEnv = map[string]string{"KATA_DAEMON": "shared-route"}
	if receipt, e := RunCommand(t.Context(), spec); e != nil || receipt.ExitCode != 0 {
		t.Fatal("shared daemon replaced missing lane daemon", e)
	}
	f := newExecution(t)
	f.m.cfg.Kata.Env = spec.KataEnv
	f.launch(t)
	for _, req := range f.h.Requests() {
		if req.Method == "tab.create" {
			var p struct {
				Env map[string]string `json:"env"`
			}
			if e := json.Unmarshal(req.Params, &p); e != nil {
				t.Fatal(e)
			}
			if _, ok := p.Env["KATA_DAEMON"]; ok {
				t.Fatal("tab inherited shared daemon")
			}
		}
	}
}
func TestSecretLoopHoldThenResetResumesSamePrompt(t *testing.T) {
	testPreflightLoopReset(t, false)
}

func TestEnvironmentLoopHoldThenResetRewritesFrozenPrompt(t *testing.T) {
	testPreflightLoopReset(t, true)
}

func testPreflightLoopReset(t *testing.T, frozen bool) {
	t.Helper()
	t.Setenv("EXAMPLE_SOURCE", "")
	f := heldResumeFixture(t)
	h := herdrtest.New(t)
	f.m.h = &herdr.Client{Path: h.Path}
	f.m.cfg.Secrets = map[string]secretref.Ref{"review": {Env: "EXAMPLE_SOURCE"}}
	f.s.Transition(f.uid, func(r *Run) error {
		r.Phase = Admitted
		r.EndedAt = nil
		r.Outcome = ""
		r.Reason = ""
		r.ActiveStep = "make"
		r.JobDef.SecretRefs = map[string]string{"GITHUB_TOKEN": "review"}
		r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "make", Kind: "prompt", Prompt: "work"}, {Key: "check", Kind: "command", Command: "true", After: []string{"make"}, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":3}}`)}}}
		r.Steps = []StepState{{Key: "make", Kind: "prompt", Attempt: 2, Submission: None, LoopRerun: true}, {Key: "check", Kind: "command", Attempt: 2, Submission: None}}
		r.Loop = LoopState{Used: 1, Rejection: "old diagnosis", Comparable: true}
		return nil
	})
	if frozen {
		t.Setenv("EXAMPLE_SOURCE", "fixture-secret-only")
		if e := f.m.writePrompt(loaded(t, f.s, f.uid), 0); e != nil {
			t.Fatal(e)
		}
		f.m.environment = leakingLaunch{}
	}
	if _, e := f.m.executePrompt(t.Context(), f.uid, 0); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	wantReason := "secret_unavailable"
	if frozen {
		wantReason = "herdr_env_unclean"
	}
	if r.Reason != wantReason {
		t.Fatal(r.Reason)
	}
	prompt := filepath.Join(f.s.layout.AttemptDir(f.uid, "make", 2), "prompt.md")
	if frozen {
		b, e := os.ReadFile(prompt)
		if e != nil || !strings.Contains(string(b), "old diagnosis") {
			t.Fatal("fixture did not retain the unsent pre-crash prompt", e)
		}
	} else if _, e := os.Stat(prompt); !os.IsNotExist(e) {
		t.Fatal("unavailable source froze prompt before launch", e)
	}
	t.Setenv("EXAMPLE_SOURCE", "fixture-secret-only")
	f.m.environment = cleanLaunch{}
	f.start(t)
	if e := f.m.Resume(t.Context(), Request{UID: "reset-loop", RunUID: f.uid, ResetLoops: true}); e != nil {
		t.Fatal(e)
	}
	h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method == "agent.prompt" {
			if e := WriteResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, "make", 2), "result.json"), &Result{Version: 1, Run: f.uid, Step: "make", Attempt: 2, Status: "succeeded", Summary: "Verified the resumed maker after repairing its fixture source."}); e != nil {
				t.Error(e)
			}
		}
	}
	result, e := f.m.executePrompt(t.Context(), f.uid, 0)
	if e != nil || result == nil || result.Status != "succeeded" {
		saved := loaded(t, f.s, f.uid)
		t.Fatalf("reset invalidated unsent prompt: result=%v err=%v phase=%s reason=%s", result, e, saved.Phase, saved.Reason)
	}
	r = loaded(t, f.s, f.uid)
	if r.Steps[0].Attempt != 2 || r.Loop.Used != 0 || r.Loop.Rejection != "" {
		t.Fatal("reset consumed unsent attempt or kept loop context")
	}
	b, e := os.ReadFile(prompt)
	if e != nil || strings.Contains(string(b), "old diagnosis") {
		t.Fatal("prompt retained stale loop context", e)
	}
}

type rotateAfterRetryFS struct {
	state.FS
	runFile, source string
}

func (f rotateAfterRetryFS) Rename(old, new string) error {
	if e := f.FS.Rename(old, new); e != nil {
		return e
	}
	if new == f.runFile {
		var r Run
		if e := state.ReadJSON(f.FS, new, 4<<20, &r); e != nil {
			return e
		}
		if r.Steps[0].Attempt == 2 && r.Steps[0].Submission == None {
			return os.Chmod(f.source, 0644)
		}
	}
	return nil
}
func TestRetryUsesSecretResolvedBeforeAttemptAllocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if e := os.WriteFile(path, []byte("fixture-secret-only"), 0600); e != nil {
		t.Fatal(e)
	}
	f := workflowExecution(t, defs.Step{Key: "check", Kind: "command", Command: `if [ ! -f once ]; then touch once; exit 7; fi; test -n "$GITHUB_TOKEN" && touch verified`, Retries: 1})
	f.m.cfg.Secrets = map[string]secretref.Ref{"review": {File: path}}
	f.s.Transition(f.uid, func(r *Run) error { r.JobDef.SecretRefs = map[string]string{"GITHUB_TOKEN": "review"}; return nil })
	f.s.fsys = rotateAfterRetryFS{FS: f.s.fsys, runFile: f.s.layout.RunFile(f.uid), source: path}
	r := executeWorkflow(t, f)
	if r.Outcome != "succeeded" || r.Steps[0].Attempt != 2 {
		t.Fatalf("retry re-resolved after consuming its attempt: %s %s attempt=%d", r.Phase, r.Reason, r.Steps[0].Attempt)
	}
	if _, e := os.Stat(filepath.Join(r.Cwd, "verified")); e != nil {
		t.Fatal(e)
	}
}
