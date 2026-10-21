//go:build crashpoint

package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/crashpoint"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestCrashEveryBoundary(t *testing.T) {
	points := []string{"authority.before", "authority.after", "prepared.save.before", "prepared.save.after", "kata.create.before", "kata.create.after", "create-result.save.before", "create-result.save.after", "kata.observe.before", "kata.observe.after", "op-observe-result.save.before", "op-observe-result.save.after", "admitted.save.before", "admitted.save.after", "launch-intent.save.before", "launch-intent.save.after", "herdr.agent.start.before", "herdr.agent.start.after", "identity.save.before", "identity.save.after", "submission-sending.save.before", "submission-sending.save.after", "herdr.agent.prompt.before", "herdr.agent.prompt.after", "submission-accepted.save.before", "submission-accepted.save.after", "result.detected", "result.save.before", "result.save.after", "settlement.save.before", "settlement.save.after", "kata.close.before", "kata.close.after", "op-close-result.save.before", "op-close-result.save.after", "herdr.tab.close.before", "herdr.tab.close.after", "herdr.tab.create.before", "herdr.tab.create.after", "tab-provenance.save.before", "tab-provenance.save.after", "kata.observe.before#2", "kata.observe.after#2", "op-observe-result.save.before#2", "op-observe-result.save.after#2", "kata.comment.before", "kata.comment.after", "op-comment-result.save.before", "op-comment-result.save.after", "kata.label.add.before", "kata.label.add.after", "op-label.add-result.save.before", "op-label.add-result.save.after", "kata.meta.set.before", "kata.meta.set.after", "op-meta.set-result.save.before", "op-meta.set-result.save.after", "recipient.before", "recipient.after", "op-resolve-recipient-result.save.before", "op-resolve-recipient-result.save.after", "kata.notify.before", "kata.notify.after", "op-notify-result.save.before", "op-notify-result.save.after"}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			f := newAdmission(t)
			f.d.Def.Action = defs.Action{Kind: "execute", Prompt: "Inspect"}
			f.d.Workflow = nil
			f.d.WFDef = nil
			h := herdrtest.New(t)
			h.OmitCompletionSeq = true
			scriptCrashActivityTimeouts(h)
			if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.Root, "due.json"), f.d); e != nil {
				t.Fatal(e)
			}
			inspected := false
			worker := crashWorker(t, f.s.layout.Root, h.Path, point, func() error {
				inspected = true
				// Inspect the durable pair while the acknowledged child is parked.
				submission, prompts := Sending, 1
				switch point {
				case "submission-sending.save.before":
					submission, prompts = None, 0
				case "submission-sending.save.after", "herdr.agent.prompt.before":
				default:
					return nil
				}
				barrier, err := f.s.Load(f.uid)
				if err != nil {
					return err
				}
				if barrier.Steps[0].Submission != submission || barrier.PromptsSent != prompts {
					return fmt.Errorf("%s: submission=%s prompts=%d; want %s and %d", point, barrier.Steps[0].Submission, barrier.PromptsSent, submission, prompts)
				}
				return nil
			})
			if !inspected {
				t.Fatal("acknowledged boundary was not inspected")
			}
			var exit *exec.ExitError
			marker, _ := os.ReadFile(filepath.Join(f.s.layout.Root, "crash-point"))
			if string(marker) != point || !errors.As(worker, &exit) || !strings.Contains(exit.Error(), "killed") {
				t.Fatalf("boundary not killed: %v", worker)
			}
			if point == "submission-sending.save.after" || point == "herdr.agent.prompt.before" {
				barrier := loaded(t, f.s, f.uid)
				if barrier.Steps[0].Submission != Sending {
					t.Fatal("crash crossed sending barrier", barrier)
				}
				for _, request := range h.Requests() {
					if request.Method == "agent.prompt" {
						t.Fatal("prompt reached Herdr before the requested crash boundary")
					}
				}
			}
			if e := crashWorker(t, f.s.layout.Root, h.Path, ""); e != nil {
				t.Fatal("restart", e)
			}
			if e := crashWorker(t, f.s.layout.Root, h.Path, ""); e != nil {
				t.Fatal("second restart", e)
			}
			starts, prompts := 0, 0
			for _, req := range h.Requests() {
				switch req.Method {
				case "agent.start":
					starts++
				case "agent.prompt":
					prompts++
				}
			}
			if starts > 1 || prompts > 1 {
				t.Fatalf("duplicated: starts %d prompts %d", starts, prompts)
			}
			r := loaded(t, f.s, f.uid)
			if r.PromptsSent != 1 {
				t.Fatalf("sending barrier counted %d attempts after recovery", r.PromptsSent)
			}
			uncertainReport := point == "kata.notify.before" || point == "kata.label.add.before"
			if !uncertainReport && (r.Inspect != "" || head(r) != nil) {
				t.Fatalf("report did not converge: %+v", r)
			}
			ambiguous := point == "submission-sending.save.after" || point == "herdr.agent.prompt.before"
			if ambiguous || point == "herdr.agent.prompt.after" {
				if r.Phase != Held || r.Outcome != "unknown" || r.Reason != "no_activity" {
					t.Fatalf("unproven attempt did not resolve after activity grace: %+v", r)
				}
			}
			if ambiguous {
				if prompts != 0 || r.Phase != Held || r.Outcome != "unknown" {
					t.Fatalf("ambiguous window: prompts=%d run=%+v", prompts, r)
				}
			} else if prompts != 1 {
				t.Fatalf("known unsubmitted did not converge: prompts=%d run=%+v", prompts, r)
			}
		})
	}
}

// Crash workers use an advancing clock. An activity slice must expire without
// changing stale idle into working or spending wall time waiting on the fake.
func scriptCrashActivityTimeouts(h *herdrtest.Server) {
	h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "agent.wait" {
			return
		}
		var p struct{ Until []string }
		if e := json.Unmarshal(req.Params, &p); e != nil {
			panic(e)
		}
		if slices.Contains(p.Until, "working") {
			h.Fail("agent.wait", herdr.Error{Code: "timeout"})
		}
	}
}

func crashWorker(t *testing.T, root, socket, point string, beforeKill ...func() error) error {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCrashRecoveryWorker$", "-test.timeout=0")
	cmd.Env = append(os.Environ(), "HK_CRASH_ROOT="+root, "HK_CRASH_SOCKET="+socket, "HK_CRASH_POINT="+point, "GORACE=atexit_sleep_ms=0")
	if strings.Contains(point, "comment") || strings.Contains(point, "label") || strings.Contains(point, "meta.set") || strings.Contains(point, "recipient") || strings.Contains(point, "notify") {
		cmd.Env = append(cmd.Env, "HK_CRASH_HANDOFF=1")
	}
	// The child parks at its boundary; only the parent requests termination.
	// A self-SIGKILL can return before delivery, and Goexit unwinds the runner.
	ready, signal, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	defer signal.Close()
	hold, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	defer release.Close()
	cmd.ExtraFiles = []*os.File{signal, hold}
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err = cmd.Start(); err != nil {
		return err
	}
	_ = signal.Close()
	_ = hold.Close()
	if point != "" {
		reached := make(chan error, 1)
		go func() {
			line, e := bufio.NewReader(ready).ReadString('\n')
			if e == nil && strings.TrimSuffix(line, "\n") != point {
				e = fmt.Errorf("wrong crash boundary: %q", line)
			}
			reached <- e
		}()
		select {
		case err = <-reached:
			if err == nil {
				for _, inspect := range beforeKill {
					if err = inspect(); err != nil {
						break
					}
				}
			}
			if err == nil {
				err = cmd.Process.Kill()
			}
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if err != nil {
		_ = cmd.Process.Kill()
	}
	e := cmd.Wait()
	if err != nil {
		t.Logf("crash worker exit: %v; wait: %v\nstdout:\n%s\nstderr:\n%s", cmd.ProcessState, e, out.String(), stderr.String())
		return fmt.Errorf("crash boundary handshake: %w", err)
	}
	if e != nil && !strings.Contains(e.Error(), "killed") {
		t.Logf("crash worker exit: %v; wait: %v\nstdout:\n%s\nstderr:\n%s", cmd.ProcessState, e, out.String(), stderr.String())
	}
	return e
}
func TestCrashRecoveryWorker(t *testing.T) {
	root := os.Getenv("HK_CRASH_ROOT")
	if root == "" {
		return
	}
	f := newAdmission(t)
	f.s = NewStore(state.OSFS{}, state.Layout{Root: root})
	f.d = sched.Due{}
	if e := state.ReadJSON(f.s.fsys, filepath.Join(root, "due.json"), 64<<10, &f.d); e != nil {
		t.Fatal(e)
	}
	f.k.run = func(_ context.Context, uid string) (kata.Run, error) {
		var r kata.Run
		e := state.ReadJSON(f.s.fsys, filepath.Join(root, "remote-run.json"), 64<<10, &r)
		if os.IsNotExist(e) {
			e = &kata.Error{Code: "not_found"}
		}
		return r, e
	}
	var remoteMu sync.Mutex
	f.k.observe = func(_ context.Context, uid string, o kata.Observation) (kata.Run, bool, error) {
		remoteMu.Lock()
		defer remoteMu.Unlock()
		var current kata.Run
		state.ReadJSON(f.s.fsys, filepath.Join(root, "remote-run.json"), 64<<10, &current)
		if current.Revision == o.ExpectedRevision+1 && current.Status == o.Status {
			return current, true, nil
		}
		r := kata.Run{UID: uid, Revision: o.ExpectedRevision + 1, Status: o.Status}
		return r, false, state.WriteJSON(f.s.fsys, filepath.Join(root, "remote-run.json"), r)
	}
	f.k.close = func(_ context.Context, req kata.CloseReq) error {
		issue := crashIssue(root)
		issue.Status = "closed"
		return state.WriteJSON(state.OSFS{}, filepath.Join(root, "remote-issue.json"), issue)
	}
	f.k.comment = func(context.Context, string, string, string) error { return nil }
	f.k.notify = func(context.Context, string, string, string) error { return nil }
	f.k.show = func(context.Context, string) (kata.IssueView, error) { return crashIssue(root), nil }
	f.k.meta = func(_ context.Context, _ string, key, value string, cond kata.Cond) (int64, error) {
		issue := crashIssue(root)
		if cond.IfMatch != strconv.FormatInt(issue.Revision, 10) {
			return 0, &kata.Error{Code: "conflict"}
		}
		data, _ := json.Marshal(value)
		issue.Metadata[key] = data
		issue.Revision++
		return issue.Revision, state.WriteJSON(state.OSFS{}, filepath.Join(root, "remote-issue.json"), issue)
	}
	f.k.notify = func(_ context.Context, _, to, msg string) error {
		issue := crashIssue(root)
		key := "notify." + base64.RawURLEncoding.EncodeToString([]byte(to))
		data, _ := json.Marshal(map[string]string{"message": msg, "from": "worker", "teammate": "herdr-kata"})
		issue.Metadata[key] = data
		return state.WriteJSON(state.OSFS{}, filepath.Join(root, "remote-issue.json"), issue)
	}
	boundary := &crashKata{admissionKata: f.k, root: root}
	h := &herdr.Client{Path: os.Getenv("HK_CRASH_SOCKET")}
	f.q = NewQueue(boundary, f.s, f.g, RealClock{})
	f.m = NewManager(f.cfg, boundary, h, f.s, f.q, f.g, &activityClock{at: time.Now()})
	// Crash boundaries exercise lifecycle replay; the fake starts no shell.
	f.m.environment = cleanLaunch{}
	f.m.activityGrace = time.Second
	var hookMu sync.Mutex
	hits := map[string]int{}
	crashpoint.Hook = func(point string) {
		hookMu.Lock()
		hits[point]++
		n := hits[point]
		hookMu.Unlock()
		wanted := os.Getenv("HK_CRASH_POINT")
		if point == wanted || point+"#"+strconv.Itoa(n) == wanted {
			if e := os.WriteFile(filepath.Join(root, "crash-point"), []byte(wanted), 0600); e != nil {
				panic(e)
			}
			ready := os.NewFile(3, "crash-boundary")
			if _, e := fmt.Fprintln(ready, wanted); e != nil {
				panic(e)
			}
			_ = ready.Close()
			// Wait for the parent externally without unwinding locks or runner
			// completion. A bare select can trigger Go's deadlock detector.
			hold := os.NewFile(4, "parent-held-crash-boundary")
			var b [1]byte
			_, e := hold.Read(b[:])
			panic(fmt.Sprintf("parent released crash boundary without killing worker: %v", e))
		}
		if point == "herdr.agent.prompt.after" {
			r, e := f.s.Load(f.uid)
			if e != nil {
				panic(e)
			}
			var step StepState
			for _, candidate := range r.Steps {
				if candidate.Kind == "prompt" && candidate.Status == "" && candidate.Submission == Sending {
					step = candidate
					break
				}
			}
			res := &Result{Version: 1, Run: f.uid, Step: step.Key, Attempt: step.Attempt, Status: "succeeded", Summary: "Inspected the task and verified all focused checks passed successfully.", Evidence: []string{"test:go test ./internal/run"}}
			if os.Getenv("HK_CRASH_HANDOFF") == "1" {
				res.Status = "needs-human"
				res.Evidence = nil
				if strings.Contains(os.Getenv("HK_CRASH_POINT"), "label") {
					res.Status = "succeeded"
				}
			}
			if e = WriteResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(r.UID, step.Key, step.Attempt), "result.json"), res); e != nil {
				panic(e)
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		e := f.q.Run(ctx)
		done <- e
		cancel()
	}()
	if e := f.m.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Load(f.uid); missing(e) {
		if _, e = f.m.Admit(ctx, f.d); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.m.HandleRequests(ctx, nil); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	if r.Phase == Running || r.Phase == Admitted {
		t.Fatal("not recovered", r)
	}
	// Allow the queue to finish settlement and exercise each result save.
	for head(r) != nil && r.Inspect == "" {
		if e := f.q.wait(ctx, f.uid, head(r).ID); e != nil {
			r = loaded(t, f.s, f.uid)
			if r.Inspect == "" {
				t.Fatal(e, r)
			}
		}
		r = loaded(t, f.s, f.uid)
	}
	cancel()
	if e := <-done; e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if point := os.Getenv("HK_CRASH_POINT"); point != "" {
		data, _ := json.Marshal(r)
		t.Fatalf("crash boundary %s was not reached; final journal: %s", point, data)
	}
}

type crashKata struct {
	*admissionKata
	root string
}

func (k *crashKata) LabelAdd(_ context.Context, _, label string) error {
	issue := crashIssue(k.root)
	issue.Labels = append(issue.Labels, label)
	return state.WriteJSON(state.OSFS{}, filepath.Join(k.root, "remote-issue.json"), issue)
}
func crashIssue(root string) kata.IssueView {
	var issue kata.IssueView
	if e := state.ReadJSON(state.OSFS{}, filepath.Join(root, "remote-issue.json"), 64<<10, &issue); os.IsNotExist(e) {
		issue = kata.IssueView{UID: "issue", QualifiedID: "project#task", Status: "open", Author: "operator", Revision: 7, Metadata: map[string]json.RawMessage{}}
	}
	return issue
}

func TestCrashWorkflowSecondPrompt(t *testing.T) {
	points := []string{"submission-sending.save.before#3", "submission-sending.save.after#3", "herdr.agent.prompt.before#2", "herdr.agent.prompt.after#2", "submission-accepted.save.before#3", "submission-accepted.save.after#3", "result.detected#2", "result.save.before#2", "result.save.after#2"}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			f := newAdmission(t)
			f.d.Def.Action = defs.Action{Kind: "execute", WorkflowUID: "workflow"}
			f.d.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "first", Kind: "prompt", Prompt: "First"}, {Key: "check", Kind: "command", Command: "printf once >> marker", After: []string{"first"}}, {Key: "last", Kind: "prompt", Prompt: "Last", After: []string{"check"}}}}
			f.d.Workflow = &kata.Workflow{UID: "workflow", DefinitionEventUID: "workflow-event"}
			h := herdrtest.New(t)
			h.OmitCompletionSeq = true
			scriptCrashActivityTimeouts(h)
			if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.Root, "due.json"), f.d); e != nil {
				t.Fatal(e)
			}
			killed := crashWorker(t, f.s.layout.Root, h.Path, point)
			var exit *exec.ExitError
			marker, _ := os.ReadFile(filepath.Join(f.s.layout.Root, "crash-point"))
			if string(marker) != point || !errors.As(killed, &exit) || !strings.Contains(exit.Error(), "killed") {
				t.Fatal("boundary not killed", point, killed)
			}
			if point == "submission-sending.save.after#3" || point == "herdr.agent.prompt.before#2" {
				barrier := loaded(t, f.s, f.uid)
				if barrier.Steps[2].Submission != Sending {
					t.Fatal("crash crossed second sending barrier", barrier)
				}
				prompts := 0
				for _, request := range h.Requests() {
					if request.Method == "agent.prompt" {
						prompts++
					}
				}
				if prompts != 1 {
					t.Fatal("second prompt reached Herdr before the requested crash boundary", prompts)
				}
			}
			for range 2 {
				if e := crashWorker(t, f.s.layout.Root, h.Path, ""); e != nil {
					t.Fatal("restart", e)
				}
			}
			starts := 0
			prompts := map[string]int{}
			for _, req := range h.Requests() {
				if req.Method == "agent.start" {
					starts++
				}
				if req.Method == "agent.prompt" {
					var p struct{ Text string }
					json.Unmarshal(req.Params, &p)
					prompts[p.Text]++
				}
			}
			total := 0
			for _, n := range prompts {
				if n != 1 {
					t.Fatal("duplicate prompt", prompts)
				}
				total += n
			}
			r := loaded(t, f.s, f.uid)
			b, e := os.ReadFile(filepath.Join(r.Cwd, "marker"))
			if starts != 1 || total > 2 || e != nil || string(b) != "once" {
				t.Fatal("duplicate workflow effect", starts, prompts, string(b), e)
			}
			ambiguous := point == "submission-sending.save.after#3" || point == "herdr.agent.prompt.before#2"
			if ambiguous || point == "herdr.agent.prompt.after#2" {
				if r.Reason != "no_activity" {
					t.Fatal("unproven prompt did not resolve after activity grace", r)
				}
			}
			if ambiguous {
				if total != 1 || r.Phase != Held || r.Outcome != "unknown" {
					t.Fatal(r)
				}
			} else if point == "herdr.agent.prompt.after#2" {
				// The prompt landed, but the killed worker has not copied a report.
				if total != 2 || r.Phase != Held || r.Outcome != "unknown" {
					t.Fatal(r)
				}
			} else if total != 2 || r.Phase != Settled || r.Outcome != "succeeded" {
				t.Fatal(r)
			}
		})
	}
}

func TestSubmitNotReadyRejectedBeforeInput(t *testing.T) {
	f := newRecovery(t)
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	crashpoint.Hook = func(p string) {
		if p == "herdr.agent.prompt.before" {
			a, _ := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
			a.InteractiveReady = false
			f.h.SetAgent(a)
		}
	}
	defer func() { crashpoint.Hook = nil }()
	f.submit(t)
	if got := loaded(t, f.s, f.uid).Steps[0].Submission; got != Rejected {
		t.Fatalf("agent_not_ready recorded as %s; the prompt is never resent", got)
	}
	crashpoint.Hook = nil
	a, err := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
	if err != nil {
		t.Fatal(err)
	}
	a.InteractiveReady = true
	f.h.SetAgent(a)
	f.submit(t)
	if got := loaded(t, f.s, f.uid).Steps[0].Submission; got != Accepted || f.count("agent.prompt") != 2 {
		t.Fatal("ready retry not accepted exactly once", got, f.count("agent.prompt"))
	}
	f.submit(t)
	if f.count("agent.prompt") != 2 {
		t.Fatal("accepted prompt resent")
	}
}

func TestCrashFreshAgentLaunch(t *testing.T) {
	for _, point := range []string{"fresh-launch-intent.save.before", "fresh-launch-intent.save.after", "herdr.agent.start.before#2", "herdr.agent.start.after#2", "submission-sending.save.after#2", "herdr.agent.prompt.after#2"} {
		t.Run(point, func(t *testing.T) {
			f := newAdmission(t)
			f.d.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "first", Kind: "prompt", Prompt: "First"}, {Key: "fresh", Kind: "prompt", Prompt: "Fresh", After: []string{"first"}, Options: json.RawMessage(`{"agent_mode":"fresh"}`)}}}
			h := herdrtest.New(t)
			h.OmitCompletionSeq = true
			scriptCrashActivityTimeouts(h)
			if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.Root, "due.json"), f.d); e != nil {
				t.Fatal(e)
			}
			killed := crashWorker(t, f.s.layout.Root, h.Path, point)
			var exit *exec.ExitError
			marker, _ := os.ReadFile(filepath.Join(f.s.layout.Root, "crash-point"))
			if string(marker) != point || !errors.As(killed, &exit) || !strings.Contains(exit.Error(), "killed") {
				t.Fatalf("boundary not killed: %s %v", point, killed)
			}
			for range 2 {
				if e := crashWorker(t, f.s.layout.Root, h.Path, ""); e != nil {
					t.Fatal("restart", e)
				}
			}
			counts := map[string]int{}
			for _, req := range h.Requests() {
				if req.Method == "agent.start" || req.Method == "agent.prompt" {
					var p struct{ Name, Target string }
					json.Unmarshal(req.Params, &p)
					if p.Name == "" {
						p.Name = p.Target
					}
					counts[req.Method+":"+p.Name]++
				}
			}
			for key, n := range counts {
				if n > 1 {
					t.Fatal("duplicated", key, n)
				}
			}
			r := loaded(t, f.s, f.uid)
			if r.Steps[1].Herdr == nil || r.Steps[1].Herdr.AgentName == r.Herdr.AgentName || r.Herdr.AgentName == "" {
				t.Fatal("provenance lost", r)
			}
			if point == "submission-sending.save.after#2" && r.Phase != Held {
				t.Fatal("ambiguous fresh submission replayed", r)
			}
		})
	}
}

func (k *crashKata) Ready(context.Context) ([]kata.IssueView, error) {
	return []kata.IssueView{crashIssue(k.root)}, nil
}
