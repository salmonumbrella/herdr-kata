package board

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func activationModel(t *testing.T) (*Model, store.Job) {
	t.Helper()
	m := newTestModel(t)
	dir := nativeBoardFixture(t, m, nil, nil)
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("execution-policy"), 0600); err != nil {
		t.Fatal(err)
	}
	r := m.store.Native
	r.StateDir = t.TempDir()
	r.Binding.ProjectUID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	r.Binding.Checkouts = map[string]string{"primary": t.TempDir()}
	draft, err := r.JobDraft(store.Job{Name: "Inspect", Prompt: "Inspect workspace", CWD: r.Binding.Checkouts["primary"], Model: store.DefaultModel, Kind: store.DefaultKind, Schedule: store.ScheduleManual, Catchup: store.CatchupLatest, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := r.Save(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	j, err := m.store.Job(t.Context(), saved.UID)
	if err != nil {
		t.Fatal(err)
	}
	m.jobs = []store.Job{*j}
	m.cursor = 0
	return m, *j
}

func TestNativeBoardToggleAndEditorChangeOnlyLocalActivation(t *testing.T) {
	for _, route := range []string{"toggle", "editor"} {
		t.Run(route, func(t *testing.T) {
			m, j := activationModel(t)
			before, err := m.store.Native.Client.Definition(t.Context(), "job", j.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []bool{true, false} {
				current, err := m.store.Job(t.Context(), j.ID)
				if err != nil {
					t.Fatal(err)
				}
				m.jobs = []store.Job{*current}
				m.cursor = 0
				if route == "toggle" {
					msg := m.togglePause()()
					if action, ok := msg.(actionMsg); !ok || action.err != nil {
						t.Fatalf("toggle failed: %+v", msg)
					}
				} else {
					m.editor = newEditor(*current, false)
					m.editor.job.Enabled = want
					if msg := m.saveEditor()(); func() bool { _, ok := msg.(editSavedMsg); return ok }() == false {
						t.Fatalf("editor failed: %+v", msg)
					}
				}
				active, err := m.store.Native.IsActivated(j.ID)
				if err != nil || active != want {
					t.Errorf("%s activation=%v want %v err=%v", route, active, want, err)
				}
				after, err := m.store.Native.Client.Definition(t.Context(), "job", j.ID)
				if err != nil || after.DefinitionEventUID != before.DefinitionEventUID || string(after.Definition) != string(before.Definition) {
					t.Errorf("activation-only %s edited shared winner", route)
				}
				jobs, err := m.store.Jobs(t.Context())
				if err != nil || len(jobs) != 1 || jobs[0].Enabled != want {
					t.Errorf("scheduler projection did not see local activation: %+v %v", jobs, err)
				}
			}
			current, err := m.store.Job(t.Context(), j.ID)
			if err != nil {
				t.Fatal(err)
			}
			m.store.Native.Binding.Checkouts = map[string]string{}
			if route == "toggle" {
				m.jobs = []store.Job{*current}
				msg := m.togglePause()()
				if a, ok := msg.(actionMsg); !ok || a.err == nil {
					t.Fatalf("failed activation reported success: %+v", msg)
				}
			} else {
				m.editor = newEditor(*current, false)
				m.editor.job.Enabled = true
				msg := m.saveEditor()()
				if _, ok := msg.(editFailedMsg); !ok {
					t.Fatalf("failed activation reported editor success: %+v", msg)
				}
			}
			if active, err := m.store.Native.IsActivated(j.ID); err != nil || active {
				t.Fatal("failed activation left local job active")
			}
		})
	}
}

func TestNativeEditorDefinitionAndActivationAreSeparateWrites(t *testing.T) {
	m, j := activationModel(t)
	m.editor = newEditor(j, false)
	m.editor.job.Prompt = "Inspect changed workspace"
	m.editor.job.Enabled = true
	if msg := m.saveEditor()(); func() bool { _, ok := msg.(editSavedMsg); return ok }() == false {
		t.Fatalf("definition editor failed: %+v", msg)
	}
	active, err := m.store.Native.IsActivated(j.ID)
	if err != nil || !active {
		t.Fatalf("definition edit dropped local enabled intent: %v %v", active, err)
	}
	def, err := m.store.Native.Client.Definition(t.Context(), "job", j.ID)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Enabled bool
		Action  struct{ Prompt string }
	}
	if err := json.Unmarshal(def.Definition, &body); err != nil || body.Enabled || body.Action.Prompt != "Inspect changed workspace" {
		t.Fatalf("shared definition conflated local activation: %+v %v", body, err)
	}
}

func TestNativeEditorRetainsAcceptedDefinitionAfterActivationFailure(t *testing.T) {
	for _, isNew := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "new"}[isNew], func(t *testing.T) {
			m, j := activationModel(t)
			oldEvent := j.NativeEventUID
			if isNew {
				j.ID = ""
				j.NativeEventUID = ""
				j.NativeDefinition = nil
				j.Name = "Second inspection"
				j.Enabled = true
			}
			m.editor = newEditor(j, isNew)
			m.editor.job.Prompt = "Inspect changed workspace"
			m.editor.job.Enabled = true
			path := filepath.Join(m.store.Native.StateDir, "native-activation.json")
			if err := os.WriteFile(path, []byte("invalid-json"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := m.saveEditor()
			if cmd == nil {
				t.Fatal("no save command")
			}
			msg := cmd()
			m.Update(msg)
			accepted, err := m.store.Native.Client.Definition(t.Context(), "job", m.editor.job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if accepted.DefinitionEventUID == oldEvent {
				t.Fatal("definition was not accepted before activation failure")
			}
			if !strings.Contains(m.editor.errMsg, "definition saved; local activation failed") {
				t.Errorf("misleading partial save: %q", m.editor.errMsg)
			}
			if m.editor.isNew || m.editor.job.NativeEventUID != accepted.DefinitionEventUID || m.editor.original.NativeEventUID != accepted.DefinitionEventUID || !m.editor.job.Enabled || m.editor.original.Enabled {
				t.Errorf("lost accepted baseline: new=%v draft=%s original=%s want=%s desired=%v actual=%v", m.editor.isNew, m.editor.job.NativeEventUID, m.editor.original.NativeEventUID, accepted.DefinitionEventUID, m.editor.job.Enabled, m.editor.original.Enabled)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			retry := m.saveEditor()
			if retry == nil {
				t.Fatal("no retry")
			}
			result := retry()
			if _, ok := result.(editSavedMsg); !ok {
				t.Errorf("activation-only retry failed: %+v", result)
			}
			after, err := m.store.Native.Client.Definition(t.Context(), "job", accepted.UID)
			if err != nil || after.DefinitionEventUID != accepted.DefinitionEventUID || string(after.Definition) != string(accepted.Definition) {
				t.Errorf("retry wrote another shared revision: %+v %v", after, err)
			}
			active, err := m.store.Native.IsActivated(accepted.UID)
			if err != nil || !active {
				t.Errorf("retry did not activate: %v %v", active, err)
			}
		})
	}
}
