package main

import (
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/flow"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func consumerShellJob(t *testing.T, s *store.Store) store.Job {
	t.Helper()
	draft, err := flow.NativeDraft(flow.Flow{NativeName: "Inspect", Steps: []store.Step{{ID: "inspect", Run: "printf x >> consumer-count"}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	fd, err := s.Native.Save(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	return policyJob(t, s, store.Job{Name: "Inspect", CWD: s.Native.Binding.Checkouts["primary"], Flow: fd.UID, Ref: policyIssueUID, Schedule: store.ScheduleManual, Timeout: time.Second})
}

func TestConsumerOrdinaryIssueReadiness(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	for _, tc := range []struct {
		name        string
		metadata    map[string]any
		status, uid string
		hold        bool
	}{
		{name: "future scheduled metadata", metadata: map[string]any{"scheduled_on": "2100-01-01T00:00:00Z"}, hold: true},
		{name: "ordinary dependency exclusion", metadata: map[string]any{}, hold: true},
		{name: "someday bool", metadata: map[string]any{"someday": true}, hold: true},
		{name: "someday string", metadata: map[string]any{"someday": "true"}, hold: true},
		{name: "past civil", metadata: map[string]any{"scheduled_on": "2000-01-01T12:00", "timezone": "America/New_York"}},
		{name: "deadline alone", metadata: map[string]any{"deadline_on": "2100-01-01T00:00:00Z", "timezone": "invalid/zone"}},
		{name: "closed", status: "closed", hold: true},
		{name: "wrong identity", uid: "01ARZ3NDEKTSV4RRFFQ69G5FAX", hold: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uid := tc.uid
			if uid == "" {
				uid = policyIssueUID
			}
			status := tc.status
			if status == "" {
				status = "open"
			}
			policyWrite(t, filepath.Join(dir, "issues.json"), map[string]any{policyIssueUID: map[string]any{"uid": uid, "status": status, "metadata": tc.metadata}})
			// Supplied independently from the issue payload, as the public daemon
			// computes eligibility using its own timezone and recurrence settings.
			eligible := []map[string]any{}
			if !tc.hold {
				eligible = append(eligible, map[string]any{"uid": policyIssueUID, "project_uid": policyProjectUID})
			}
			policyWrite(t, filepath.Join(dir, "ready.json"), map[string]any{"issues": eligible})
			marker := filepath.Join(j.CWD, "consumer-count")
			os.Remove(marker)
			run, err := Execute(t.Context(), s, j, "manual")
			raw, _ := os.ReadFile(marker)
			if tc.hold {
				if err == nil || len(raw) != 0 {
					t.Errorf("deferred/invalid issue launched: err=%v effects=%q", err, raw)
				}
			} else if err != nil || run == nil || run.Outcome != runner.OutcomeDone || string(raw) != "x" {
				t.Errorf("ready issue did not launch: run=%+v err=%v effects=%q", run, err, raw)
			}
		})
	}
	policyAssertNoHandshake(t, policyCalls(t, dir))
}

func policyJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestConsumerIntervalUsesDefinitionCreation(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	// A server-created definition with no local runs must earn its first fire.
	var state map[string]any
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	rows := state["Resources"].(map[string]any)["job"].(map[string]any)
	row := rows[j.ID].(map[string]any)
	row["created_at"] = time.Now().Add(-1500 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	row["definition"].(map[string]any)["trigger"] = map[string]any{"kind": "interval", "interval_seconds": 1}
	row["definition"].(map[string]any)["catchup"] = "latest"
	policyWrite(t, filepath.Join(dir, "state.json"), state)
	if err := s.SetEnabled(t.Context(), j.ID, true); err != nil {
		t.Fatal(err)
	}
	d := &daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
	d.sweep(t.Context())
	d.wg.Wait()
	effects, _ := os.ReadFile(filepath.Join(j.CWD, "consumer-count"))
	if string(effects) != "x" {
		t.Fatalf("interval never earned a first fire from creation: %q", effects)
	}
}

func TestConsumerResumeUnknownMetadataRetainsCapture(t *testing.T) {
	s, _, dir := policyProduct(t)
	j := policyJob(t, s, store.Job{Name: "Inspect", Kind: "codex", Prompt: "Inspect workspace", CWD: s.Native.Binding.Checkouts["primary"], Persistent: true, KeepContext: true, Timeout: time.Second})
	first, err := Execute(t.Context(), s, j, "manual")
	if err != nil || first.Outcome != runner.OutcomeDone {
		t.Fatalf("capture: %+v %v", first, err)
	}
	captured, err := s.JobSession(t.Context(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	state := policyHerdr(t, dir)
	state.Started = false
	state.Session = ""
	state.NextTab = 41
	policyWrite(t, filepath.Join(dir, "herdr.json"), state)
	resumed, err := Execute(t.Context(), s, j, "manual")
	if err != nil || resumed.Outcome != runner.OutcomeDone || resumed.Context != "resumed" || !strings.Contains(resumed.ContextNote, "has not confirmed the session") {
		t.Fatalf("intentional resume with unknown metadata rejected: %+v %v", resumed, err)
	}
	retained, err := s.JobSession(t.Context(), j.ID)
	if err != nil || retained.Value != captured.Value || retained.RunID != captured.RunID {
		t.Fatalf("unknown metadata laundered capture: %+v %v", retained, err)
	}
	after := policyHerdr(t, dir)
	if len(after.PromptResults) != len(state.PromptResults)+1 {
		t.Fatal("resumed process did not receive one new prompt")
	}
	for _, c := range after.Calls[len(state.Calls):] {
		if len(c) > 1 && c[0] == "tab" && c[1] == "close" {
			t.Fatal("successful persistent resume was closed")
		}
	}
}

func TestConsumerRejectedFreshResumeClosesOnlyOwnedTab(t *testing.T) {
	s, _, dir := policyProduct(t)
	j := policyJob(t, s, store.Job{Name: "Inspect", Kind: "codex", Prompt: "Inspect workspace", CWD: s.Native.Binding.Checkouts["primary"], Persistent: true, KeepContext: true, Timeout: time.Second})
	first, err := Execute(t.Context(), s, j, "manual")
	if err != nil || first.Outcome != runner.OutcomeDone {
		t.Fatalf("capture: %+v %v", first, err)
	}
	// A blocked attempt retains the genuine capture but becomes latest selection.
	state := policyHerdr(t, dir)
	state.AgentStatus = "blocked"
	policyWrite(t, filepath.Join(dir, "herdr.json"), state)
	if r, e := Execute(t.Context(), s, j, "manual"); e != nil || r.ParkReason != runner.ParkBlocked {
		t.Fatalf("blocked: %+v %v", r, e)
	}
	captured, err := s.JobSession(t.Context(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := s.Run(t.Context(), captured.RunID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(record.RunDir, "native-context.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := runner.LoadNativeContext(record.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	proof.Target.Actor = "other-worker"
	policyWrite(t, path, proof)
	state = policyHerdr(t, dir)
	state.AgentStatus = "idle"
	state.Started = false
	state.NextTab = 41
	state.Tabs["w1:t77"] = map[string]string{"tab_id": "w1:t77", "pane_id": "w1:p77", "workspace_id": "w1", "cwd": t.TempDir()}
	policyWrite(t, filepath.Join(dir, "herdr.json"), state)
	for attempt := 0; attempt < 2; attempt++ {
		refused, err := Execute(t.Context(), s, j, "manual")
		if err == nil {
			t.Fatalf("invalid capturing proof accepted: %+v", refused)
		}
		after := policyHerdr(t, dir)
		if _, ok := after.Tabs[after.TabID]; ok || after.Started {
			t.Errorf("owned failed-resume tab leaked: %s", after.TabID)
		}
		if _, ok := after.Tabs["w1:t77"]; !ok {
			t.Error("unrelated tab was closed")
		}
		if len(after.PromptResults) != len(state.PromptResults) {
			t.Error("refused resume received prompt")
		}
		latest, e := s.LastRun(t.Context(), j.ID)
		if e != nil || latest.TabID != "" || latest.AgentName != "" {
			t.Errorf("refused fresh resume became provenance: %+v %v", latest, e)
		}
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := Execute(t.Context(), s, j, "manual")
	if err != nil || restored.Outcome != runner.OutcomeDone || restored.Context != "resumed" {
		t.Fatalf("repair failed: %+v %v", restored, err)
	}
}

func TestConsumerScheduledFireAndActualIssueAreFrozen(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	var state map[string]any
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	row := state["Resources"].(map[string]any)["job"].(map[string]any)[j.ID].(map[string]any)
	row["created_at"] = time.Now().Add(-1500 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	body := row["definition"].(map[string]any)
	body["trigger"] = map[string]any{"kind": "interval", "interval_seconds": 1}
	body["catchup"] = "latest"
	body["issue"] = map[string]any{"kind": "existing", "uid": policyIssueUID}
	body["options"].(map[string]any)["herdr"].(map[string]any)["Ref"] = ""
	policyWrite(t, filepath.Join(dir, "state.json"), state)
	if err := s.SetEnabled(t.Context(), j.ID, true); err != nil {
		t.Fatal(err)
	}
	d := &daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
	d.sweep(t.Context())
	d.wg.Wait()
	record, err := s.LastRun(t.Context(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.LoadNativeContext(record.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Occurrence == "" {
		t.Error("scheduled fire time was discarded before freezing context")
	}
	if record.Ref != policyIssueUID || snapshot.IssueUID != policyIssueUID {
		t.Errorf("actual linked issue missing from selected run: %+v issue=%s", record, snapshot.IssueUID)
	}
}

func TestConsumerCronRespectsConfiguredTimezone(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	var state map[string]any
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	row := state["Resources"].(map[string]any)["job"].(map[string]any)[j.ID].(map[string]any)
	body := row["definition"].(map[string]any)
	for _, tc := range []struct {
		name, cron, zone, anchor, now string
		want                          []string
	}{
		{"civil fold", "30 1 * * *", "America/New_York", "2026-11-01T04:00:00Z", "2026-11-01T07:00:00Z", []string{"2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"}},
		{"civil gap", "30 2 * * *", "America/New_York", "2026-03-08T05:00:00Z", "2026-03-08T08:00:00Z", nil},
		{"UTC", "0 12 * * *", "UTC", "2026-11-01T11:00:00Z", "2026-11-01T13:00:00Z", []string{"2026-11-01T12:00:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body["trigger"] = map[string]any{"kind": "cron", "cron": tc.cron, "timezone": tc.zone}
			body["catchup"] = "all"
			policyWrite(t, filepath.Join(dir, "state.json"), state)
			mapped, err := s.Job(t.Context(), j.ID)
			if err != nil {
				t.Fatal(err)
			}
			mapped.Enabled = true
			anchor, _ := time.Parse(time.RFC3339, tc.anchor)
			now, _ := time.Parse(time.RFC3339, tc.now)
			fires, err := sched.Due(*mapped, anchor, now)
			if err != nil {
				t.Fatal(err)
			}
			if len(fires) != len(tc.want) {
				t.Fatalf("cron civil zone produced %v want %v", fires, tc.want)
			}
			for i, fire := range fires {
				if fire.UTC().Format(time.RFC3339) != tc.want[i] {
					t.Errorf("fire=%s want=%s", fire, tc.want[i])
				}
			}
		})
	}
}
