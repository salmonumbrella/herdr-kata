package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdrcli"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/lockfile"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"github.com/salmonumbrella/herdr-kata/internal/workflow"
)

const policyProjectUID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
const policyIssueUID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"

type policyCall struct {
	Args []string `json:"args"`
	Body string   `json:"body"`
}
type policyHerdrState struct {
	Panes                              map[string]int
	Session                            string
	Calls                              [][]string
	Env                                map[string]string
	Name, Kind                         string
	Started                            bool
	PromptStatus                       string
	AgentStatus, WaitTabID, WaitPaneID string
	PromptResults                      []string
	Workspaces                         map[string]string
	Tabs                               map[string]map[string]string
	NextWorkspace                      int
	NextTab                            int
	PaneID, TabID, WorkspaceID         string
}

func policyWrite(t *testing.T, path string, v any) {
	t.Helper()
	raw, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
}

func policyProduct(t *testing.T) (*store.Store, string, string) {
	t.Helper()
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	kataDir, herdrDir := t.TempDir(), t.TempDir()
	name := "command"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(kataDir, name)
	if raw, e := exec.Command("go", "build", "-o", bin, "../../internal/katacli/testdata/command").CombinedOutput(); e != nil {
		t.Fatalf("fixture build: %v %s", e, raw)
	}
	raw, e := os.ReadFile(bin)
	if e != nil {
		t.Fatal(e)
	}
	herdrBin := filepath.Join(herdrDir, name)
	if e = os.WriteFile(herdrBin, raw, 0700); e != nil {
		t.Fatal(e)
	}
	for dir, mode := range map[string]string{kataDir: "execution-policy", herdrDir: "herdr"} {
		if e = os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0600); e != nil {
			t.Fatal(e)
		}
	}
	policyWrite(t, filepath.Join(herdrDir, "herdr.json"), policyHerdrState{Panes: map[string]int{"w1:p9": 123}, Session: "example-session"})
	t.Setenv("HERDR_BIN_PATH", herdrBin)
	policyWrite(t, filepath.Join(stateDir(), "workspace.json"), map[string]string{"workspace_id": "w1", "label": "Herdr Kata"})
	cfg := store.NativeConfig{Client: katacli.Client{Executable: bin, Target: katacli.Target{Server: "http://127.0.0.1:7777", Project: "spoke-project", Workspace: t.TempDir(), Actor: "worker", Teammate: "launch"}}, Binding: store.NativeBinding{ProjectUID: policyProjectUID, Checkouts: map[string]string{"primary": t.TempDir()}}}
	if e = store.WriteNativeConfig(stateDir(), cfg); e != nil {
		t.Fatal(e)
	}
	s, e := openStore()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s, kataDir, herdrDir
}

func policyJob(t *testing.T, s *store.Store, j store.Job) store.Job {
	t.Helper()
	draft, e := s.Native.JobDraft(j)
	if e != nil {
		t.Fatal(e)
	}
	if uid, err := katacli.NormalizeUID(j.Ref); err == nil && s.Native.Binding.ProjectUID == policyProjectUID {
		draft.Definition = policyExistingIssueDefinition(t, draft.Definition, uid)
	}
	if _, e = s.Native.Save(t.Context(), draft); e != nil {
		t.Fatal(e)
	}
	saved, e := s.Job(t.Context(), draft.UID)
	if e != nil {
		t.Fatal(e)
	}
	return *saved
}

func policyExistingIssueDefinition(t *testing.T, raw json.RawMessage, uid string) json.RawMessage {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := katacli.Decode(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["issue"] = policyJSON(t, map[string]string{"kind": "existing", "uid": uid})
	return policyJSON(t, doc)
}

func policyCalls(t *testing.T, dir string) []policyCall {
	t.Helper()
	f, e := os.Open(filepath.Join(dir, "policy-calls.jsonl"))
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	var out []policyCall
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 1024), 2<<20)
	for scan.Scan() {
		var call policyCall
		if e = json.Unmarshal(scan.Bytes(), &call); e != nil {
			t.Fatal(e)
		}
		out = append(out, call)
	}
	if e = scan.Err(); e != nil {
		t.Fatal(e)
	}
	return out
}

func policyAssertNoHandshake(t *testing.T, calls []policyCall) {
	t.Helper()
	for _, c := range calls {
		for i, a := range c.Args {
			if a != "cron" || i+1 >= len(c.Args) {
				continue
			}
			args := c.Args[i+1:]
			if args[0] == "authority" || args[0] == "due" || args[0] == "advance" || len(args) > 1 && args[0] == "run" && args[1] != "observe" && args[1] != "show" && args[1] != "list" {
				t.Errorf("product attempted retired execution command: %v", args)
			}
		}
	}
}

func policyHerdr(t *testing.T, dir string) policyHerdrState {
	t.Helper()
	raw, e := os.ReadFile(filepath.Join(dir, "herdr.json"))
	if e != nil {
		t.Fatal(e)
	}
	var state policyHerdrState
	if e = json.Unmarshal(raw, &state); e != nil {
		t.Fatal(e)
	}
	return state
}

func policyAssertLiveConversation(t *testing.T, state policyHerdrState) {
	t.Helper()
	starts, prompts := 0, 0
	for _, args := range state.Calls {
		if len(args) < 2 {
			continue
		}
		switch args[0] + " " + args[1] {
		case "agent start":
			starts++
		case "agent prompt":
			prompts++
		case "pane close", "tab close", "workspace close":
			t.Errorf("persistent conversation was forcibly closed: %v", args)
		}
	}
	if starts != 1 || prompts != 2 || !state.Started {
		t.Errorf("wanted one live conversation and two prompts; starts=%d prompts=%d live=%v", starts, prompts, state.Started)
	}
	if len(state.PromptResults) != 2 || state.PromptResults[0] == state.PromptResults[1] {
		t.Errorf("successive commands did not receive separate result destinations: %v", state.PromptResults)
	}
	for _, path := range state.PromptResults {
		raw, e := os.ReadFile(path)
		if e != nil || !strings.Contains(string(raw), `"status":"ok"`) {
			t.Errorf("prompt did not produce successful result at %s: %s %v", path, raw, e)
		}
	}
}

// Catches native Execute retaining a central admission gate or bypassing the
// upstream live-session reuse path. The logger rejects every observation.
func TestExecutionPolicyNativePersistentReusesLiveConversation(t *testing.T) {
	s, kataDir, herdrDir := policyProduct(t)
	j := policyJob(t, s, store.Job{Name: "Inspect", Kind: "codex", Model: "example-model", Prompt: "Inspect workspace", CWD: s.Native.Binding.Checkouts["primary"], Schedule: store.ScheduleManual, Persistent: true, KeepContext: true, Timeout: time.Second})
	// Positive control exercises the existing real Runner through the same
	// public CLI fixture, so absent product prompts cannot be blamed on it.
	controlDir := t.TempDir()
	r := runner.Runner{Herdr: herdrcli.New(), Store: s, StateDir: controlDir}
	control := runner.FromStore(j)
	control.WorkspaceID = "w1"
	for i := 0; i < 2; i++ {
		uid, e := katacli.NewUID()
		if e != nil {
			t.Fatal(e)
		}
		run, e := r.Execute(t.Context(), control, uid)
		if e != nil || run.Outcome != runner.OutcomeDone {
			t.Fatalf("upstream fixture control failed: %+v %v", run, e)
		}
	}
	policyAssertLiveConversation(t, policyHerdr(t, herdrDir))
	if t.Failed() {
		t.FailNow()
	}
	t.Log("upstream control: one live agent start, two prompts, two successful result destinations, no close")
	if e := s.DeleteJobSession(t.Context(), j.ID); e != nil {
		t.Fatal(e)
	}
	policyWrite(t, filepath.Join(herdrDir, "herdr.json"), policyHerdrState{Panes: map[string]int{"w1:p9": 123}, Session: "example-session"})
	var ids []string
	for i := 0; i < 2; i++ {
		run, e := Execute(t.Context(), s, j, "manual")
		if e != nil || run == nil || run.Outcome != runner.OutcomeDone {
			t.Errorf("native manual invocation %d must run despite absent grants/logger: run=%+v err=%v", i+1, run, e)
		}
		if run != nil {
			if _, e = katacli.NormalizeUID(run.RunID); e != nil {
				t.Errorf("persistent invocation did not allocate canonical run UID: %q", run.RunID)
			}
			ids = append(ids, run.RunID)
		}
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Errorf("persistent invocations need distinct run UIDs: %v", ids)
	}
	policyAssertLiveConversation(t, policyHerdr(t, herdrDir))
	policyAssertNoHandshake(t, policyCalls(t, kataDir))
}

// Catches native scheduling's central Due requirement and single-occurrence
// exclusion. Explicit invocations share a once time/issue but execute twice.
func TestExecutionPolicySameOccurrenceRunsWithoutLogAcknowledgment(t *testing.T) {
	if _, e := exec.LookPath("sh"); e != nil {
		t.Skip("shell workflow needs installed sh")
	}
	s, kataDir, _ := policyProduct(t)
	draft, e := workflow.NativeDraft(workflow.Workflow{NativeName: "Inspect", Steps: []store.Step{{ID: "inspect", Run: "printf x >> launch-count"}}}, "", "")
	if e != nil {
		t.Fatal(e)
	}
	fd, e := s.Native.Save(t.Context(), draft)
	if e != nil {
		t.Fatal(e)
	}
	at := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	j := policyJob(t, s, store.Job{Name: "Inspect", Kind: "codex", Model: "example-model", CWD: s.Native.Binding.Checkouts["primary"], Workflow: fd.UID, Schedule: store.ScheduleOnce, RunAt: &at, Ref: policyIssueUID})
	var ids []string
	for i := 0; i < 2; i++ {
		run, err := Execute(t.Context(), s, j, "scheduled")
		if err != nil || run == nil || run.Outcome != runner.OutcomeDone {
			t.Errorf("same-occurrence invocation %d must execute without Due/log ack: run=%+v err=%v", i+1, run, err)
		}
		if run != nil {
			if _, err = katacli.NormalizeUID(run.RunID); err != nil {
				t.Errorf("execution did not allocate canonical run UID: %q", run.RunID)
			}
			ids = append(ids, run.RunID)
		}
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Errorf("independent same-occurrence executions collapsed: %v", ids)
	}
	raw, e := os.ReadFile(filepath.Join(j.CWD, "launch-count"))
	if e != nil || string(raw) != "xx" {
		t.Errorf("wanted two actual shell side effects, got %q err=%v", raw, e)
	}
	observed := map[string]bool{}
	calls := policyCalls(t, kataDir)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		count := 0
		for _, c := range calls {
			for i, a := range c.Args {
				if a == "cron" && i+2 < len(c.Args) && c.Args[i+1] == "run" && c.Args[i+2] == "observe" {
					count++
				}
			}
		}
		if count >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
		calls = policyCalls(t, kataDir)
	}
	for _, c := range calls {
		for i, a := range c.Args {
			if a == "cron" && i+3 < len(c.Args) && c.Args[i+1] == "run" && c.Args[i+2] == "observe" {
				observed[c.Args[i+3]] = true
			}
		}
	}
	if len(observed) != 2 {
		t.Errorf("logger outage must retain two separately attributed run writes; attempted UIDs=%v", observed)
	}
	policyAssertNoHandshake(t, calls)
}

func TestExecutionPolicyDaemonRequiresLocalActivation(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("shell requires sh")
	}
	s, kataDir, _ := policyProduct(t)
	draft, err := workflow.NativeDraft(workflow.Workflow{NativeName: "Inspect", Steps: []store.Step{{ID: "inspect", Run: "printf x >> activation-count"}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	fd, err := s.Native.Save(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	j := policyJob(t, s, store.Job{Name: "Inspect", Workflow: fd.UID, CWD: s.Native.Binding.Checkouts["primary"], Enabled: true, Schedule: store.ScheduleOnce, RunAt: &at})
	var shared map[string]json.RawMessage
	if err := json.Unmarshal(j.NativeDefinition, &shared); err != nil {
		t.Fatal(err)
	}
	shared["enabled"] = json.RawMessage(`true`)
	portable, err := json.Marshal(shared)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := katacli.NewDraft("job", j.ID, j.Name, portable, j.NativeEventUID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Native.Save(t.Context(), replacement); err != nil {
		t.Fatal(err)
	}
	currentJob, err := s.Job(t.Context(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	j = *currentJob
	d := daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
	d.sweep(t.Context())
	d.wg.Wait()
	path := filepath.Join(j.CWD, "activation-count")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("shared enabled auto-activated local scheduler: %v", err)
	}
	if err := nativeCmd([]string{"activate", j.ID}); err != nil {
		t.Fatalf("explicit local activation failed: %v", err)
	}
	d.sweep(t.Context())
	d.wg.Wait()
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "x" {
		t.Fatalf("activated scheduler did not execute: %q %v", raw, err)
	}
	if err := nativeCmd([]string{"deactivate", j.ID}); err != nil {
		t.Fatal(err)
	}
	current, err := s.Native.Client.Definition(t.Context(), "job", j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.DefinitionEventUID != j.NativeEventUID || string(current.Definition) != string(j.NativeDefinition) {
		t.Fatal("local activation edited shared definition")
	}
	policyAssertNoHandshake(t, policyCalls(t, kataDir))
}

func TestExecutionPolicyDirectWorkflowAndSavedResume(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("shell requires sh")
	}
	s, kataDir, _ := policyProduct(t)
	draft, err := workflow.NativeDraft(workflow.Workflow{NativeName: "Inspect", Steps: []store.Step{{ID: "first", Run: "printf x >> resume-count"}, {ID: "second", Run: "test -f ready"}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	fd, err := s.Native.Save(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := katacli.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	cwd := s.Native.Binding.Checkouts["primary"]
	j := store.Job{ID: fd.UID, Workflow: fd.UID, CWD: cwd}
	rec := store.Run{ID: uid, JobID: fd.UID, Workflow: fd.UID, Trigger: "manual", RunDir: runDirFor(uid), Input: "retained direct input", Ref: policyIssueUID}
	run, err := runWorkflow(t.Context(), s, j, rec, workflowOpts{})
	if err != nil || run == nil || run.Outcome == runner.OutcomeDone {
		t.Fatalf("native direct workflow must execute first step before local failed result: %+v %v", run, err)
	}
	if err := s.Native.Delete(t.Context(), "workflow", fd); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "ready"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"reference", "input"} {
		bad := rec
		if field == "reference" {
			bad.Ref = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
		} else {
			bad.Input = "changed direct input"
		}
		if _, err := runWorkflow(t.Context(), s, j, bad, workflowOpts{}); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("direct saved %s change accepted: %v", field, err)
		}
	}
	if err := resumeWorkflowRun(s, uid); err != nil {
		t.Fatalf("saved workflow snapshot did not resume after shared deletion: %v", err)
	}
	got, err := s.Run(t.Context(), uid)
	if err != nil || got.Outcome != "done" || got.Ref != rec.Ref || got.Input != rec.Input {
		t.Fatalf("resume result %+v %v", got, err)
	}
	raw, err := os.ReadFile(filepath.Join(cwd, "resume-count"))
	if err != nil || string(raw) != "x" {
		t.Fatalf("completed step relaunched: %q %v", raw, err)
	}
	c, err := runner.LoadNativeContext(runDirFor(uid))
	if err != nil {
		t.Fatal(err)
	}
	if c.Job != nil || c.Workflow == nil || c.Workflow.DefinitionEventUID != fd.DefinitionEventUID {
		t.Fatalf("direct workflow fabricated job or lost winner: %+v", c)
	}
	policyAssertNoHandshake(t, policyCalls(t, kataDir))
}

func TestExecutionPolicyAdHocRunOnceHasLocalHistory(t *testing.T) {
	s, kataDir, herdrDir := policyProduct(t)
	out, err := captureStdout(t, func() error {
		return runOnce([]string{"--prompt", "Inspect workspace", "--kind", "codex", "--cwd", s.Native.Binding.Checkouts["primary"], "--timeout", "1s"})
	})
	if err != nil {
		t.Fatalf("local ad-hoc prompt refused: %v", err)
	}
	var result struct {
		RunID   string `json:"run_id"`
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if _, err := katacli.NormalizeUID(result.RunID); err != nil || result.Outcome != "done" {
		t.Fatalf("ad-hoc identity/outcome: %+v %v", result, err)
	}
	if got, err := s.Run(t.Context(), result.RunID); err != nil || got.Outcome != "done" {
		t.Fatalf("local history missing: %+v %v", got, err)
	}
	c, err := runner.LoadNativeContext(runDirFor(result.RunID))
	if err != nil {
		t.Fatalf("ad-hoc local snapshot missing: %v", err)
	}
	if c.Job != nil || c.Workflow != nil || c.Runtime.Prompt != "Inspect workspace" {
		t.Fatalf("ad-hoc snapshot fabricated shared reference: %+v", c)
	}
	state := policyHerdr(t, herdrDir)
	if len(state.PromptResults) != 1 {
		t.Fatalf("ad-hoc prompt absent: %+v", state)
	}
	for _, call := range policyCalls(t, kataDir) {
		for _, a := range call.Args {
			if a == "observe" {
				t.Fatal("ad-hoc prompt fabricated shared observation")
			}
		}
	}
	policyAssertNoHandshake(t, policyCalls(t, kataDir))
}

func TestExecutionPolicyWorkflowCLIAndBoardUseNativeWorkflow(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("shell requires sh")
	}
	s, kataDir, herdrDir := policyProduct(t)
	draft, err := workflow.NativeDraft(workflow.Workflow{NativeName: "Inspect", Steps: []store.Step{{ID: "inspect", Run: "printf x >> route-count"}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	fd, err := s.Native.Save(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	cwd := s.Native.Binding.Checkouts["primary"]
	if _, err := captureStdout(t, func() error { return workflowRun([]string{fd.UID, "--cwd", cwd}) }); err != nil {
		t.Fatalf("native CLI workflow did not execute: %v", err)
	}
	t.Chdir(cwd)
	if err := startWorkflowFromBoard(s, fd.UID, ""); err != nil {
		t.Fatalf("native board workflow did not execute: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(cwd, "route-count"))
	if err != nil || string(raw) != "xx" {
		t.Fatalf("CLI/board effects: %q %v", raw, err)
	}
	runs, err := s.Runs(t.Context(), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("CLI/board run count: %d", len(runs))
	}
	state := policyHerdr(t, herdrDir)
	for _, rec := range runs {
		if rec.Space == "" {
			t.Fatal("CLI/board native workflow omitted space")
		}
		if _, ok := state.Workspaces[rec.Space]; ok {
			t.Fatal("completed CLI/board workspace remained open")
		}
	}
	policyAssertNoHandshake(t, policyCalls(t, kataDir))
}

func TestNativeDeliveryFreezesPendingAndRefusesRetarget(t *testing.T) {
	s, kataDir, _ := policyProduct(t)
	uid, err := katacli.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	path := nativeDeliveryPath(t.TempDir())
	identity := NativeDelivery{Version: 1, TargetKey: nativeTargetKey(s.Native.Client.Target), ProjectUID: s.Native.Binding.ProjectUID, Actor: s.Native.Client.Target.Actor, Teammate: s.Native.Client.Target.Teammate, RunUID: uid}
	first := katacli.RunObservation{Status: "running", Summary: json.RawMessage(`{"version":1}`)}
	if err := queueNativeObservation(t.Context(), path, identity, first); err != nil {
		t.Fatal(err)
	}
	frozen, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var original NativeDelivery
	if err := json.Unmarshal(frozen, &original); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Status = "failed"
	if err := queueNativeObservation(t.Context(), path, identity, second); err != nil {
		t.Fatal(err)
	}
	second.Status = "succeeded"
	if err := queueNativeObservation(t.Context(), path, identity, second); err != nil {
		t.Fatal(err)
	}
	deliverNativeObservations(t.Context(), *s.Native.Client, path)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got NativeDelivery
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(original.Pending)
	b, _ := json.Marshal(got.Pending)
	if string(a) != string(b) || got.Unsent == nil || got.Unsent.Status != "succeeded" || got.Error == "" || got.Attempts != 1 {
		t.Fatalf("pending mutated/lost through failed delivery: %+v", got)
	}
	before := len(policyCalls(t, kataDir))
	for _, field := range []string{"actor", "project", "workspace", "server", "teammate"} {
		wrong := *s.Native.Client
		switch field {
		case "actor":
			wrong.Target.Actor = "different-worker"
		case "project":
			wrong.Target.Project = "other-project"
		case "workspace":
			wrong.Target.Workspace = t.TempDir()
		case "server":
			wrong.Target.Server = "http://127.0.0.1:7778"
		case "teammate":
			wrong.Target.Teammate = "other-worker"
		}
		deliverNativeObservations(t.Context(), wrong, path)
		if after := len(policyCalls(t, kataDir)); after != before {
			t.Fatalf("saved observation sent under changed %s", field)
		}
	}
}

func TestNativeDeliveryRemoteReplyDoesNotHoldExecutionQueue(t *testing.T) {
	s, kataDir, _ := policyProduct(t)
	uid, err := katacli.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	path := nativeDeliveryPath(t.TempDir())
	identity := NativeDelivery{Version: 1, TargetKey: nativeTargetKey(s.Native.Client.Target), ProjectUID: s.Native.Binding.ProjectUID, Actor: s.Native.Client.Target.Actor, Teammate: s.Native.Client.Target.Teammate, RunUID: uid}
	first := katacli.RunObservation{Status: "running", Summary: json.RawMessage(`{"version":1}`)}
	if err := queueNativeObservation(t.Context(), path, identity, first); err != nil {
		t.Fatal(err)
	}
	pause := filepath.Join(kataDir, "observe-paused")
	if err := os.WriteFile(pause, nil, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); deliverNativeObservations(t.Context(), *s.Native.Client, path) }()
	defer func() { os.Remove(pause); <-done }()
	observed := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		for _, call := range policyCalls(t, kataDir) {
			for _, arg := range call.Args {
				if arg == "observe" {
					observed = true
				}
			}
		}
		if observed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !observed {
		t.Fatal("fixture never entered paused ordinary write")
	}
	second := first
	second.Status = "succeeded"
	queued := make(chan error, 1)
	go func() { queued <- queueNativeObservation(t.Context(), path, identity, second) }()
	select {
	case err := <-queued:
		if err != nil {
			t.Fatalf("remote reply withheld execution-side queue: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("execution-side queue waited for remote acknowledgment")
	}
}

func TestNativeDeliveryLostAcceptedReplyUsesFrozenDTOThenAcknowledgedRevision(t *testing.T) {
	s, kataDir, _ := policyProduct(t)
	if err := os.WriteFile(filepath.Join(kataDir, "observe-mode"), []byte("ack-loss"), 0600); err != nil {
		t.Fatal(err)
	}
	uid, err := katacli.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	path := nativeDeliveryPath(t.TempDir())
	identity := NativeDelivery{Version: 1, TargetKey: nativeTargetKey(s.Native.Client.Target), ProjectUID: s.Native.Binding.ProjectUID, Actor: s.Native.Client.Target.Actor, Teammate: s.Native.Client.Target.Teammate, RunUID: uid}
	initial := katacli.RunObservation{JobUID: policyIssueUID, DefinitionEventUID: policyProjectUID, Status: "running", Summary: json.RawMessage(`{"version":1}`)}
	final := initial
	final.Status = "succeeded"
	if err := queueNativeObservation(t.Context(), path, identity, initial); err != nil {
		t.Fatal(err)
	}
	if err := queueNativeObservation(t.Context(), path, identity, final); err != nil {
		t.Fatal(err)
	}
	deliverNativeObservations(t.Context(), *s.Native.Client, path)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pending NativeDelivery
	if err := json.Unmarshal(raw, &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Revision != 0 || pending.Pending == nil || pending.Pending.ExpectedRevision != 0 || pending.Unsent == nil || pending.Error == "" {
		t.Fatalf("lost reply lost pending intent: %+v", pending)
	}
	deliverNativeObservations(t.Context(), *s.Native.Client, path)
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var acknowledged NativeDelivery
	if err := json.Unmarshal(raw, &acknowledged); err != nil {
		t.Fatal(err)
	}
	if acknowledged.Revision != 2 || acknowledged.Pending != nil || acknowledged.Unsent != nil || acknowledged.Error != "" {
		t.Fatalf("replay/next ordinary revision failed: %+v", acknowledged)
	}
	var bodies []string
	for _, call := range policyCalls(t, kataDir) {
		for _, arg := range call.Args {
			if arg == "observe" {
				bodies = append(bodies, call.Body)
			}
		}
	}
	if len(bodies) != 3 || bodies[0] != bodies[1] {
		t.Fatalf("retry did not freeze exact pending DTO: %v", bodies)
	}
	var last katacli.RunObservation
	if err := json.Unmarshal([]byte(bodies[2]), &last); err != nil {
		t.Fatal(err)
	}
	if last.ExpectedRevision != 1 || last.Status != "succeeded" {
		t.Fatalf("successor bypassed predecessor acknowledgment: %+v", last)
	}
}

func TestNativeExecutionQueueSurvivesShortLocalWriteContention(t *testing.T) {
	path := nativeDeliveryPath(t.TempDir())
	lock, err := lockfile.Acquire(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() { time.Sleep(100 * time.Millisecond); lock.Release(); close(released) }()
	defer func() { <-released }()
	identity := NativeDelivery{Version: 1, TargetKey: "example-target", ProjectUID: policyProjectUID, Actor: "worker", RunUID: policyIssueUID}
	if err := queueNativeObservation(t.Context(), path, identity, katacli.RunObservation{Status: "running", Summary: json.RawMessage(`{"version":1}`)}); err != nil {
		t.Fatalf("ordinary local writer contention refused execution-side intent: %v", err)
	}
}

func TestExecutionPolicyMissingCheckoutCannotFallBackToCurrentDirectory(t *testing.T) {
	s, _, herdrDir := policyProduct(t)
	j := policyJob(t, s, store.Job{Name: "Inspect", Kind: "codex", Prompt: "Inspect workspace", CWD: s.Native.Binding.Checkouts["primary"], Schedule: store.ScheduleManual, Timeout: time.Second})
	var body map[string]json.RawMessage
	if err := json.Unmarshal(j.NativeDefinition, &body); err != nil {
		t.Fatal(err)
	}
	body["checkout_key"] = json.RawMessage(`"unmapped"`)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := katacli.NewDraft("job", j.ID, j.Name, raw, j.NativeEventUID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Native.Save(t.Context(), replacement); err != nil {
		t.Fatal(err)
	}
	current, err := s.Job(t.Context(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(t.Context(), s, *current, "manual"); err == nil {
		t.Fatal("unmapped native checkout silently executed in current directory")
	}
	if state := policyHerdr(t, herdrDir); len(state.PromptResults) != 0 || state.Started {
		t.Fatalf("missing mapping started an agent: %+v", state)
	}
}

func TestNativeDeliveryStoreCloseCancelsAndJoinsWriter(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("shell requires sh")
	}
	s, kataDir, _ := policyProduct(t)
	pause := filepath.Join(kataDir, "observe-paused")
	if err := os.WriteFile(pause, nil, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(pause)
	draft, err := workflow.NativeDraft(workflow.Workflow{NativeName: "Inspect", Steps: []store.Step{{ID: "inspect", Run: "true"}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	fd, err := s.Native.Save(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	j := policyJob(t, s, store.Job{Name: "Inspect", Workflow: fd.UID, CWD: s.Native.Binding.Checkouts["primary"]})
	run, err := executeNative(t.Context(), s, j, "manual")
	if err != nil {
		t.Fatal(err)
	}
	path := nativeDeliveryPath(run.RunDir)
	observed := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		for _, call := range policyCalls(t, kataDir) {
			for _, arg := range call.Args {
				if arg == "observe" {
					observed = true
				}
			}
		}
		if observed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !observed {
		t.Fatal("ordinary writer never entered paused remote operation")
	}
	// Closing the local owner cancels remote work and joins its final local write.
	// It must not wait for the remote acknowledgment, nor leave a writer behind.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var d NativeDelivery
	if err := statefs.ReadJSON(path, 262144, &d); err != nil {
		t.Fatal(err)
	}
	if d.Pending == nil || d.Error == "" || d.NextRetry == "" {
		// Own the pre-fix worker while preserving the failing assertion.
		time.Sleep(4 * time.Second)
		t.Fatalf("store closed with unjoined delivery: pending=%v error=%q retry=%q", d.Pending != nil, d.Error, d.NextRetry)
	}
}
