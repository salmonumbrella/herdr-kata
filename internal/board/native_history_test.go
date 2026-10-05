package board

import (
	"encoding/json"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func historyRow(uid string) map[string]any {
	return map[string]any{"uid": uid, "project_id": 73, "job_uid": "01ARZ3NDEKTSV4RRFFQ69G5FAE", "definition_event_uid": "01ARZ3NDEKTSV4RRFFQ69G5FAF", "issue_uid": "01ARZ3NDEKTSV4RRFFQ69G5FAG", "actor": "peer-worker", "teammate": "peer/task", "executor_label": "example-executor", "status": "succeeded", "summary": map[string]any{"version": 1, "message": "Peer inspection complete", "input_tokens": 13, "output_tokens": 7}, "revision": 2, "created_at": "2026-10-05T00:00:00Z", "updated_at": "2026-10-05T00:00:01Z", "started_at": "2026-10-05T00:00:00Z", "ended_at": "2026-10-05T00:00:01Z"}
}
func historyResponse(t *testing.T, dir string, rows []map[string]any) {
	t.Helper()
	path := filepath.Join(dir, "responses.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var responses map[string]any
	if err := json.Unmarshal(raw, &responses); err != nil {
		t.Fatal(err)
	}
	responses["run list"] = map[string]any{"body": map[string]any{"runs": rows, "next_before_uid": "01ARZ3NDEKTSV4RRFFQ69G5FAH"}}
	raw, err = json.Marshal(responses)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestNativeHistoryMergesReportedRowsWithoutImportingHandles(t *testing.T) {
	m := newTestModel(t)
	dir := nativeBoardFixture(t, m, nil, nil)
	local := store.Run{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", JobID: "01ARZ3NDEKTSV4RRFFQ69G5FAE", Outcome: "done", Note: "Local inspection", AgentName: "owned-agent", TabID: "w1:t9", RunDir: t.TempDir(), StartedAt: time.Now()}
	if err := m.store.PutRun(t.Context(), local); err != nil {
		t.Fatal(err)
	}
	peer := historyRow("01ARZ3NDEKTSV4RRFFQ69G5FAW")
	peer["agent_name"] = "unrelated-agent"
	peer["tab_id"] = "w9:t9"
	peer["run_dir"] = "/unrelated"
	duplicate := historyRow(local.ID)
	duplicate["status"] = "running"
	historyResponse(t, dir, []map[string]any{peer, duplicate})
	m.Update(m.load()())
	var found *store.Run
	count := 0
	for i := range m.runs {
		r := &m.runs[i]
		if r.ID == peer["uid"] {
			found = r
		}
		if r.ID == local.ID {
			count++
			if r.AgentName != local.AgentName || r.Note != local.Note || r.Outcome != "done" {
				t.Errorf("shared row replaced local execution: %+v", r)
			}
		}
	}
	if found == nil {
		t.Fatal("ordinary shared run missing from board")
	}
	if count != 1 {
		t.Fatalf("local/shared duplicate count=%d", count)
	}
	if found.RunDir != "" || found.AgentName != "" || found.TabID != "" || found.Ref != peer["issue_uid"] || found.InputTokens != 13 || found.Note != "Peer inspection complete" {
		t.Fatalf("unsafe or incorrect peer projection: %+v", found)
	}
	m.runDetail = found
	view := m.renderRunDetail()
	for _, text := range []string{"reported", "peer-worker", "peer/task", "example-executor"} {
		if !strings.Contains(view, text) {
			t.Errorf("peer detail omits %q", text)
		}
	}
	if cmd := m.focusRun(); cmd != nil {
		msg := cmd().(actionMsg)
		if !strings.Contains(msg.status, "no agent") {
			t.Fatalf("peer row attempted agent focus: %+v", msg)
		}
	} else {
		t.Fatal("peer detail did not report that it has no local agent")
	}
	// A failed refresh retains already-read evidence, labelled offline, and local rows.
	os.WriteFile(filepath.Join(dir, "mode"), []byte("nonzero"), 0600)
	m.Update(m.load()())
	found = nil
	for i := range m.runs {
		if m.runs[i].ID == peer["uid"] {
			found = &m.runs[i]
		}
	}
	if found == nil || !strings.Contains(m.View(), "offline") {
		t.Fatal("offline refresh discarded evidence or hid its stale label")
	}
}
func TestNativeHistoryRejectsWrongProjectAndOversizedPage(t *testing.T) {
	m := newTestModel(t)
	dir := nativeBoardFixture(t, m, nil, nil)
	for _, fault := range []string{"project", "bound", "status", "identity", "summary", "pair"} {
		t.Run(fault, func(t *testing.T) {
			row := historyRow("01ARZ3NDEKTSV4RRFFQ69G5FAW")
			rows := []map[string]any{row}
			switch fault {
			case "project":
				row["project_id"] = 74
			case "bound":
				for len(rows) <= 100 {
					rows = append(rows, row)
				}
			case "status":
				row["status"] = "authorized"
			case "identity":
				row["uid"] = "not-a-uid"
			case "summary":
				row["summary"] = map[string]any{"version": 1, "message": strings.Repeat("x", 65536)}
			case "pair":
				delete(row, "definition_event_uid")
			}
			historyResponse(t, dir, rows)
			m.Update(m.load()())
			for _, r := range m.runs {
				if r.ID == row["uid"] {
					t.Errorf("invalid %s peer row imported", fault)
				}
			}
			if m.err == nil {
				t.Errorf("invalid %s history lacks diagnostic", fault)
			}
			if len(m.runs) != 2 {
				t.Errorf("invalid shared history hid local rows: %d", len(m.runs))
			}
		})
	}
}
func TestSelectedIssueNavigationUsesInstalledKata(t *testing.T) {
	m := newTestModel(t)
	m.runDetail = &store.Run{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Ref: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
	selected := ""
	m.deps.KataCommand = func(issue string) (*exec.Cmd, error) { selected = issue; return exec.Command("unused"), nil }
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if cmd == nil || selected != m.runDetail.Ref {
		t.Fatalf("selected issue not routed to installed Kata: %q", selected)
	}
}

func TestNativeHistoryIncludedInSelectedJobDetail(t *testing.T) {
	m := newTestModel(t)
	def := katacli.Definition{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAE", Name: "Inspect", DefinitionEventUID: "01ARZ3NDEKTSV4RRFFQ69G5FAF", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":false,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Inspect"}}`)}
	dir := nativeBoardFixture(t, m, []katacli.Definition{def}, nil)
	m.store.Native.StateDir = t.TempDir()
	peer := historyRow("01ARZ3NDEKTSV4RRFFQ69G5FAW")
	historyResponse(t, dir, []map[string]any{peer})
	m.Update(m.load()())
	if m.err != nil || len(m.sharedRuns) != 1 || len(m.jobs) != 1 || m.jobs[0].ID != def.UID {
		t.Fatalf("history/detail setup: jobs=%+v shared=%+v err=%v", m.jobs, m.sharedRuns, m.err)
	}
	cmd := m.openDetail()
	if cmd == nil {
		t.Fatal("job detail unavailable")
	}
	m.Update(cmd())
	if m.err != nil || m.detail == nil {
		t.Fatalf("job detail setup failed: %v", m.err)
	}
	if len(m.detailRuns) != 1 || m.detailRuns[0].ID != peer["uid"] {
		t.Fatalf("selected job omits ordinary reported history: %+v", m.detailRuns)
	}
}
