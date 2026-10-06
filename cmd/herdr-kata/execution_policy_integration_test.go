package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"github.com/salmonumbrella/herdr-kata/internal/workflow"
)

// Only Kata is real here. Herdr's public process/prompt boundary is an owned
// executable oracle, so integration cannot touch a user's live workspace.
func realPolicyHerdr(t *testing.T) string {
	t.Helper()
	if os.Getenv("KATA_NATIVE_TEST_BINARY") == "" {
		t.Skip("requires explicit isolated branch binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "herdr")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if raw, err := exec.Command("go", "build", "-o", bin, "../../internal/katacli/testdata/command").CombinedOutput(); err != nil {
		t.Fatalf("Herdr oracle build: %s %v", raw, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("herdr"), 0600); err != nil {
		t.Fatal(err)
	}
	policyWrite(t, filepath.Join(dir, "herdr.json"), policyHerdrState{Panes: map[string]int{"w1:p9": 123}, Session: "example-session"})
	t.Setenv("HERDR_BIN_PATH", bin)
	return dir
}

func TestRealExecutionPolicyReducedHistoryUsesSelectedNumericProject(t *testing.T) {
	realPolicyHerdr(t)
	c, s := realNativeProduct(t)
	j := consumerShellJob(t, s)
	// The synthetic fixture issue must never escape into the real producer.
	var issue struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
	}
	if err := c.Create(t.Context(), "Inspect workspace", "Ordinary local execution", "real-history-issue", &issue); err != nil {
		realNativeFailure(t, err)
	}
	j.Ref = issue.Issue.UID
	j.NativeDefinition = policyExistingIssueDefinition(t, j.NativeDefinition, j.Ref)
	at := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	j.Schedule = store.ScheduleOnce
	j.RunAt = &at
	draft, err := s.Native.JobDraft(j)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Native.Save(t.Context(), draft); err != nil {
		realNativeFailure(t, err)
	}
	if _, err := c.Definitions(t.Context(), "job", false); err != nil {
		realNativeFailure(t, err)
	}
	if snapshot, err := s.Native.Refresh(t.Context()); err != nil || snapshot.Offline {
		t.Fatalf("reduced producer definitions unavailable: %+v %v", snapshot, err)
	}
	mapped, err := s.Job(t.Context(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for i := 0; i < 2; i++ {
		run, err := Execute(t.Context(), s, *mapped, "scheduled")
		if err != nil || run == nil || run.Outcome != runner.OutcomeDone {
			t.Fatalf("real execute: %+v %v", run, err)
		}
		ids[run.RunID] = true
	}
	if len(ids) != 2 {
		t.Fatal("same linked issue executions reused a run UID")
	}
	if effects, err := os.ReadFile(filepath.Join(j.CWD, "consumer-count")); err != nil || string(effects) != "xx" {
		t.Fatalf("independent same-occurrence process effects: %q %v", effects, err)
	}
	// Join owned observation delivery before examining real producer evidence.
	deadline := time.Now().Add(8 * time.Second)
	for {
		var wire struct {
			Runs []json.RawMessage `json:"runs"`
		}
		if err := c.Runs(t.Context(), "", "", &wire); err != nil {
			realNativeFailure(t, err)
		}
		complete := 0
		for _, raw := range wire.Runs {
			var r struct{ UID, Status string }
			json.Unmarshal(raw, &r)
			if ids[r.UID] && r.Status == "succeeded" {
				complete++
			}
		}
		if complete == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("two final reduced observations not published: %s", policyJSON(t, wire))
		}
		time.Sleep(20 * time.Millisecond)
	}
	page, err := c.RunHistory(t.Context(), s.Native.Binding.ProjectUID, "")
	if err != nil || len(page.Runs) != 2 {
		t.Fatalf("actual reduced numeric-project history refused: %+v %v", page, err)
	}
	for _, r := range page.Runs {
		if !ids[r.UID] || r.IssueUID != issue.Issue.UID || r.Actor != c.Target.Actor || r.Teammate != c.Target.Teammate || r.Status != "succeeded" || r.OccurrenceKey != at.Format(time.RFC3339Nano) {
			t.Fatalf("real reduced identity/evidence mapping: %+v", r)
		}
	}
	if _, err := c.RunHistory(t.Context(), policyProjectUID, ""); err == nil {
		t.Fatal("foreign project UID accepted the selected producer's rows")
	}
}

func TestRealExecutionPolicyPersistentKeepsLiveConversationThroughLostLogReply(t *testing.T) {
	herdrDir := realPolicyHerdr(t)
	c, s := realNativeProduct(t)
	policyWrite(t, filepath.Join(stateDir(), "workspace.json"), map[string]string{"workspace_id": "w1", "label": "Herdr Kata"})
	j := policyJob(t, s, store.Job{Name: "Persistent inspect", Kind: "codex", Prompt: "Inspect workspace", CWD: c.Target.Workspace, Persistent: true, KeepContext: true, Timeout: time.Second})
	// Forward to the actual accepted producer, discarding one successful reply.
	shimDir := t.TempDir()
	shim := filepath.Join(shimDir, "kata")
	if runtime.GOOS == "windows" {
		shim += ".exe"
	}
	if raw, err := exec.Command("go", "build", "-o", shim, "../../internal/katacli/testdata/command").CombinedOutput(); err != nil {
		t.Fatalf("forward oracle: %s %v", raw, err)
	}
	for name, value := range map[string]string{"mode": "forward", "forward-binary": c.Executable, "lose-action": "run observe", "lose-reply": "armed"} {
		if err := os.WriteFile(filepath.Join(shimDir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s.Native.Client.Executable = shim
	var runs []*runner.Run
	for i := 0; i < 2; i++ {
		r, err := Execute(t.Context(), s, j, "manual")
		if err != nil || r == nil || r.Outcome != runner.OutcomeDone {
			t.Fatalf("persistent prompt withheld by ordinary logger: %+v %v", r, err)
		}
		runs = append(runs, r)
		// Own the external oracle ordering: the first successful ordinary
		// receipt must be lost before the second independent prompt starts.
		if i == 0 {
			deadline := time.Now().Add(5 * time.Second)
			for {
				var d NativeDelivery
				if statefs.ReadJSON(nativeDeliveryPath(r.RunDir), 262144, &d) == nil && d.Error != "" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("real first accepted receipt was not lost")
				}
				time.Sleep(10 * time.Millisecond)
			}
		} else {
			deadline := time.Now().Add(5 * time.Second)
			for {
				var d NativeDelivery
				if statefs.ReadJSON(nativeDeliveryPath(r.RunDir), 262144, &d) == nil && d.Pending == nil && d.Revision == 2 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("second independent ordinary observation did not settle")
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
	}
	policyAssertLiveConversation(t, policyHerdr(t, herdrDir))
	if runs[0].RunID == runs[1].RunID {
		t.Fatal("persistent runs shared a UID")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var pending NativeDelivery
	path := nativeDeliveryPath(runs[0].RunDir)
	if err := statefs.ReadJSON(path, 262144, &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Pending == nil || pending.Unsent == nil || pending.Error == "" {
		t.Fatalf("lost actual receipt discarded bounded evidence: %+v", pending)
	}
	// Same frozen ordinary DTO replays; successor uses acknowledged revision.
	os.Remove(filepath.Join(shimDir, "lose-reply"))
	deliverNativeObservations(t.Context(), *c, path)
	pending = NativeDelivery{}
	if err := statefs.ReadJSON(path, 262144, &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Pending != nil || pending.Unsent != nil || pending.Error != "" || pending.Revision != 2 {
		t.Fatalf("real same-UID observation recovery: %+v", pending)
	}
	page, err := c.RunHistory(t.Context(), s.Native.Binding.ProjectUID, "")
	if err != nil || len(page.Runs) != 2 {
		t.Fatalf("persistent shared evidence: %+v %v", page, err)
	}
	for _, r := range page.Runs {
		if r.Status != "succeeded" || r.JobUID != j.ID {
			t.Fatalf("wrong persistent evidence: %+v", r)
		}
	}
}

func TestRealExecutionPolicyFrozenReferenceResumeAfterSharedEditsAndDeletion(t *testing.T) {
	realPolicyHerdr(t)
	c, s := realNativeProduct(t)
	var issue struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
	}
	if err := c.Create(t.Context(), "Inspect workspace", "Saved recovery", "frozen-reference-issue", &issue); err != nil {
		realNativeFailure(t, err)
	}
	draft, err := workflow.NativeDraft(workflow.Workflow{NativeName: "Saved inspection", Steps: []store.Step{{ID: "first", Run: "printf x >> frozen-count"}, {ID: "second", Run: "test -f ready && printf '%s' \"$HERDR_KATA_INPUT|$HERDR_KATA_REF\" > frozen-values"}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	fd, err := s.Native.Save(t.Context(), draft)
	if err != nil {
		realNativeFailure(t, err)
	}
	j := policyJob(t, s, store.Job{Name: "Saved inspection", CWD: c.Target.Workspace, Workflow: fd.UID, Input: "retained input", Timeout: time.Second})
	var body map[string]json.RawMessage
	katacli.Decode(j.NativeDefinition, &body)
	body["issue"] = policyJSON(t, map[string]any{"kind": "existing", "uid": issue.Issue.UID})
	jdraft, err := katacli.NewDraft("job", j.ID, j.Name, policyJSON(t, body), j.NativeEventUID)
	if err != nil {
		t.Fatal(err)
	}
	jd, err := s.Native.Save(t.Context(), jdraft)
	if err != nil {
		realNativeFailure(t, err)
	}
	j2, err := s.Job(t.Context(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Execute(t.Context(), s, *j2, "manual")
	if err != nil || r == nil || r.Outcome == runner.OutcomeDone {
		t.Fatalf("recovery setup: %+v %v", r, err)
	}
	rec, err := s.Run(t.Context(), r.RunID)
	if err != nil {
		t.Fatal(err)
	}
	contextPath := filepath.Join(rec.RunDir, "native-context.json")
	frozen, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatal(err)
	}
	var changed map[string]json.RawMessage
	katacli.Decode(fd.Definition, &changed)
	changed["steps"] = policyJSON(t, []map[string]any{{"key": "replacement", "kind": "command", "command": "printf rejected > changed-definition"}})
	edit, err := katacli.NewDraft("workflow", fd.UID, fd.Name, policyJSON(t, changed), fd.DefinitionEventUID)
	if err != nil {
		t.Fatal(err)
	}
	fd2, err := s.Native.Save(t.Context(), edit)
	if err != nil {
		realNativeFailure(t, err)
	}
	body["action"] = policyJSON(t, map[string]any{"kind": "execute", "prompt": "Changed prompt"})
	edit, err = katacli.NewDraft("job", jd.UID, jd.Name, policyJSON(t, body), jd.DefinitionEventUID)
	if err != nil {
		t.Fatal(err)
	}
	jd2, err := s.Native.Save(t.Context(), edit)
	if err != nil {
		realNativeFailure(t, err)
	}
	if err := s.Native.Delete(t.Context(), "job", jd2); err != nil {
		realNativeFailure(t, err)
	}
	if err := s.Native.Delete(t.Context(), "workflow", fd2); err != nil {
		realNativeFailure(t, err)
	}
	if err := os.WriteFile(filepath.Join(j.CWD, "ready"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := resumeWorkflowRun(s, rec.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.Run(t.Context(), rec.ID)
	if err != nil || got.Outcome != "done" || got.Ref != issue.Issue.UID || got.Input != "retained input" {
		t.Fatalf("immutable reference recovery: %+v %v", got, err)
	}
	count, _ := os.ReadFile(filepath.Join(j.CWD, "frozen-count"))
	values, _ := os.ReadFile(filepath.Join(j.CWD, "frozen-values"))
	if string(count) != "x" || string(values) != "retained input|"+issue.Issue.UID {
		t.Fatalf("frozen commands/input/ref changed: count=%q values=%q", count, values)
	}
	after, _ := os.ReadFile(contextPath)
	if string(after) != string(frozen) {
		t.Fatal("resume rewrote local intent")
	}
	if _, err := os.Stat(filepath.Join(j.CWD, "changed-definition")); !os.IsNotExist(err) {
		t.Fatal("new definition executed")
	}
	bad := *got
	bad.Ref = policyIssueUID
	if _, err := runWorkflow(t.Context(), s, j, bad, workflowOpts{}); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("reference override accepted: %v", err)
	}
}

func TestRealExecutionPolicyOrdinaryPlanningDatesDriveLocalSweep(t *testing.T) {
	realPolicyHerdr(t)
	c, s := realNativeProduct(t)
	var issue struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
	}
	if err := c.Create(t.Context(), "Scheduled inspection", "Ordinary source dates", "real-date-issue", &issue); err != nil {
		realNativeFailure(t, err)
	}
	for key, value := range map[string]string{"scheduled_on": "2000-01-01", "timezone": "Asia/Tokyo", "deadline_on": "2100-01-01"} {
		if err := c.Call(t.Context(), []string{"meta", "set", issue.Issue.UID, key, value}, nil, new(any)); err != nil {
			realNativeFailure(t, err)
		}
	}
	dates, err := c.PlanningDates(t.Context(), s.Native.Binding.ProjectUID, issue.Issue.UID)
	if err != nil || dates.ScheduledOn == nil || dates.ScheduledOn.Instant.Format(time.RFC3339Nano) != "1999-12-31T15:00:00Z" || dates.ScheduledOn.Timezone != "Asia/Tokyo" {
		t.Fatalf("actual native civil projection: %+v %v", dates, err)
	}
	j := consumerShellJob(t, s)
	var body map[string]json.RawMessage
	katacli.Decode(j.NativeDefinition, &body)
	body["trigger"] = policyJSON(t, map[string]any{"kind": "issue-scheduled", "issue_uid": issue.Issue.UID, "timezone": "UTC"})
	body["issue"] = policyJSON(t, map[string]any{"kind": "existing", "uid": issue.Issue.UID})
	draft, err := katacli.NewDraft("job", j.ID, j.Name, policyJSON(t, body), j.NativeEventUID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Native.Save(t.Context(), draft); err != nil {
		realNativeFailure(t, err)
	}
	if err := s.SetEnabled(t.Context(), j.ID, true); err != nil {
		realNativeFailure(t, err)
	}
	d := &daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
	d.sweep(t.Context())
	d.wg.Wait()
	first, err := s.LastRun(t.Context(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := runner.LoadNativeContext(first.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if proof.IssueUID != issue.Issue.UID || !strings.Contains(proof.Occurrence, "1999-12-31T15:00:00Z") || !strings.Contains(proof.Occurrence, "Asia/Tokyo") {
		t.Fatalf("native projection was retimed locally: %+v", proof)
	}
	if err := c.Call(t.Context(), []string{"meta", "set", issue.Issue.UID, "priority_hint", "inspect"}, nil, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	d.sweep(t.Context())
	d.wg.Wait()
	// Moving scheduled_on to the future holds ordinary execution; a deadline
	// alone never caused the first successful run to be withheld.
	if err := c.Call(t.Context(), []string{"meta", "set", issue.Issue.UID, "scheduled_on", "2100-01-01"}, nil, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	d.sweep(t.Context())
	d.wg.Wait()
	if err := c.Call(t.Context(), []string{"meta", "unset", issue.Issue.UID, "scheduled_on"}, nil, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	d.sweep(t.Context())
	d.wg.Wait()
	if raw, _ := os.ReadFile(filepath.Join(j.CWD, "consumer-count")); string(raw) != "x" {
		t.Fatalf("same/moved/cleared actual source launched: %q", raw)
	}
	if err := c.Call(t.Context(), []string{"meta", "set", issue.Issue.UID, "scheduled_on", "2001-01-01"}, nil, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	d.sweep(t.Context())
	d.wg.Wait()
	last, err := s.LastRun(t.Context(), j.ID)
	if err != nil || last.ID == first.ID {
		t.Fatalf("changed actual source did not allocate a new local run: %+v %v", last, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(j.CWD, "consumer-count")); string(raw) != "xx" {
		t.Fatalf("changed actual source process effects: %q", raw)
	}
}
