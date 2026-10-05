package board

import (
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestNativeBoardCreateRetryAfterLostReply(t *testing.T) {
	m := newTestModel(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "kata")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, e := exec.Command("go", "build", "-o", bin, "../katacli/testdata/command").CombinedOutput(); e != nil {
		t.Fatalf("fixture %v %s", e, out)
	}
	os.WriteFile(filepath.Join(dir, "mode"), []byte("store"), 0600)
	os.WriteFile(filepath.Join(dir, "lose-reply"), []byte("armed"), 0600)
	uid := "01ARZ3NDEKTSV4RRFFQ69G5FAD"
	checkout := t.TempDir()
	c := &katacli.Client{Executable: bin, Target: katacli.Target{Server: "http://127.0.0.1:7777", Project: "spoke-project", Workspace: checkout, Actor: "worker", Teammate: "adapter"}}
	m.store.Native = &store.NativeRepository{Store: m.store, Client: c, Binding: store.NativeBinding{ProjectUID: uid, Checkouts: map[string]string{"primary": checkout}}}
	m.editor = newEditor(store.Job{Name: "Inspect", Prompt: "Inspect workspace", CWD: checkout, Model: store.DefaultModel, Schedule: store.ScheduleManual, Catchup: store.CatchupLatest, Timeout: time.Minute}, true)
	first := m.saveEditor()
	if first == nil {
		t.Fatalf("save unavailable %s", m.editor.errMsg)
	}
	msg := first()
	if _, ok := msg.(editFailedMsg); !ok {
		t.Fatalf("lost reply %T", msg)
	}
	m.Update(msg)
	retained := m.editor.job
	os.Remove(filepath.Join(dir, "lose-reply"))
	accepted, e := c.Definition(t.Context(), "job", retained.ID)
	if e != nil {
		t.Fatal(e)
	}
	retry := m.saveEditor()
	if retry == nil {
		t.Fatal("retry unavailable")
	}
	msg = retry()
	if _, ok := msg.(editSavedMsg); !ok {
		t.Fatalf("retained board create did not recover: %+v", msg)
	}
	recovered, e := c.Definition(t.Context(), "job", retained.ID)
	if e != nil || accepted.DefinitionEventUID != recovered.DefinitionEventUID {
		t.Fatalf("retry changed winner %+v %v", recovered, e)
	}
	retained.Prompt = "Different document"
	m.editor = newEditor(retained, true)
	if _, ok := m.saveEditor()().(editFailedMsg); !ok {
		t.Fatal("changed create accepted")
	}
}
