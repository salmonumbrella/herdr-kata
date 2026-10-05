package board

import (
	"encoding/json"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNativeFailedFormRetainsUnsavedDraftIdentity(t *testing.T) {
	m := newTestModel(t)
	if e := m.store.AttachNative(t.TempDir()); e != nil {
		t.Fatal(e)
	}
	m.editor = newEditor(store.Job{Name: "Inspect", Prompt: "Inspect workspace", CWD: t.TempDir(), Schedule: store.ScheduleManual, Catchup: store.CatchupAll, Timeout: time.Minute}, true)
	cmd := m.saveEditor()
	if cmd == nil {
		t.Fatal("save not attempted")
	}
	msg := cmd()
	m.Update(msg)
	if m.editor == nil || m.editor.saving || !strings.Contains(m.editor.errMsg, "unsaved") || m.editor.job.Prompt != "Inspect workspace" {
		t.Fatalf("draft discarded %+v", m.editor)
	}
	uid := m.editor.job.ID
	if len(uid) != 26 {
		t.Fatalf("create UID not retained %s", uid)
	}
	m.Update(m.saveEditor()())
	if m.editor == nil || m.editor.job.ID != uid {
		t.Fatal("retry allocated another create identity")
	}
}
func TestNewNativeFormStartsDisabled(t *testing.T) {
	m := newTestModel(t)
	m.openNewJob()
	if m.editor.job.Enabled {
		t.Fatal("new form silently activates")
	}
}
func TestKataKeyUsesPublicNativeLauncher(t *testing.T) {
	m := newTestModel(t)
	called := 0
	m.deps.KataCommand = func(issue string) (*exec.Cmd, error) {
		called++
		if issue != "" {
			t.Fatal("Task7 fabricated selected run issue")
		}
		return exec.Command("unused"), nil
	}
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'K'}})
	if called != 1 || cmd == nil {
		t.Fatal("Kata launcher not reachable from board")
	}
}

func nativeBoardFixture(t *testing.T, m *Model, jobs, flows []katacli.Definition) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "kata")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, e := exec.Command("go", "build", "-o", bin, "../katacli/testdata/command").CombinedOutput(); e != nil {
		t.Fatalf("fixture %v %s", e, out)
	}
	uid := "01ARZ3NDEKTSV4RRFFQ69G5FAD"
	responses := map[string]any{"projects show": map[string]any{"body": map[string]any{"project": map[string]any{"id": 73, "uid": uid}}}, "run list": map[string]any{"body": map[string]any{"runs": []any{}}}, "capabilities show": map[string]any{"body": map[string]any{"project_uid": uid, "event_features": []string{"cron_v1"}}}, "job list": map[string]any{"body": map[string]any{"jobs": jobs}}, "flow list": map[string]any{"body": map[string]any{"flows": flows}}}
	raw, _ := json.Marshal(responses)
	os.WriteFile(filepath.Join(dir, "responses.json"), raw, 0600)
	os.WriteFile(filepath.Join(dir, "mode"), []byte("responses"), 0600)
	m.store.Native = &store.NativeRepository{Store: m.store, Client: &katacli.Client{Executable: bin, Target: katacli.Target{Server: "http://127.0.0.1:7777", Project: "spoke-project", Workspace: t.TempDir(), Actor: "worker", Teammate: "adapter"}, Timeout: 10 * time.Second}, Binding: store.NativeBinding{ProjectUID: uid}}
	return dir
}
func TestNativeBoardDeadlineMarksRetainedSnapshotOffline(t *testing.T) {
	m := newTestModel(t)
	def := katacli.Definition{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAD", Name: "Cached", DefinitionEventUID: "01ARZ3NDEKTSV4RRFFQ69G5FAE", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":false,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Inspect"}}`)}
	dir := nativeBoardFixture(t, m, []katacli.Definition{def}, nil)
	m.Update(m.load()())
	if m.err != nil || len(m.jobs) != 1 || m.nativeLabel != "native Kata" {
		t.Fatalf("initial board %s %v", m.nativeLabel, m.err)
	}
	os.WriteFile(filepath.Join(dir, "mode"), []byte("sleep"), 0600)
	start := time.Now()
	m.Update(m.load()())
	if !strings.Contains(m.nativeLabel, "offline native cache") || len(m.jobs) != 1 || !m.jobs[0].NativeOffline || len(m.runs) != 2 {
		t.Fatalf("timed-out board label=%s jobs=%+v runs=%d error=%v", m.nativeLabel, m.jobs, len(m.runs), m.err)
	}
	if time.Since(start) > 7*time.Second {
		t.Fatal("board deadline exceeded bounded local fallback")
	}
}
func TestNativeBoardIsolatesProjectionFailures(t *testing.T) {
	m := newTestModel(t)
	good := katacli.Definition{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAD", Name: "Supported", DefinitionEventUID: "01ARZ3NDEKTSV4RRFFQ69G5FAE", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":false,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Inspect"}}`)}
	bad := good
	bad.UID = "01ARZ3NDEKTSV4RRFFQ69G5FAF"
	bad.Definition = json.RawMessage(`{"version":1,"kind":"job","enabled":false,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Inspect"},"options":{"herdr":{"Tags":"daily"}}}`)
	prompt := katacli.Definition{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAG", Name: "Prompt", DefinitionEventUID: "01ARZ3NDEKTSV4RRFFQ69G5FAE", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"inspect","kind":"prompt","prompt":"Inspect workspace"}]}`)}
	badFlow := prompt
	badFlow.UID = "01ARZ3NDEKTSV4RRFFQ69G5FAH"
	badFlow.Definition = json.RawMessage(`{"version":1,"options":{"herdr":{"SkipPermissions":"unknown"}},"steps":[{"key":"inspect","kind":"prompt","prompt":"Inspect workspace"}]}`)
	nativeBoardFixture(t, m, []katacli.Definition{bad, good}, []katacli.Definition{badFlow, prompt})
	m.Update(m.load()())
	if len(m.jobs) != 1 || m.jobs[0].ID != good.UID || len(m.flows) != 1 || len(m.runs) != 2 || len(m.flowErrs) != 1 {
		t.Fatalf("bad peer hides board jobs=%+v flows=%+v runs=%d flowErrors=%v err=%v", m.jobs, m.flows, len(m.runs), m.flowErrs, m.err)
	}
	if m.err == nil || !strings.Contains(m.err.Error(), bad.UID) || !strings.Contains(m.flowErrs[0].Error(), badFlow.UID) {
		t.Fatalf("projection diagnostic missing uid: %v %v", m.err, m.flowErrs)
	}
	cached, e := m.store.Native.Cached(t.Context())
	if e != nil || len(cached.Jobs) != 2 || len(cached.Flows) != 2 {
		t.Fatalf("raw cache lost %+v %v", cached, e)
	}
}
