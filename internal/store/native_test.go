package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const nativeUID = "01ARZ3NDEKTSV4RRFFQ69G5FAD"

func nativeFixture(t *testing.T) (*NativeRepository, func(map[string]json.RawMessage)) {
	t.Helper()
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	dir := t.TempDir()
	bin := filepath.Join(dir, "kata")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "../katacli/testdata/command")
	if raw, e := build.CombinedOutput(); e != nil {
		t.Fatalf("fixture %v %s", e, raw)
	}
	os.WriteFile(filepath.Join(dir, "mode"), []byte("responses"), 0600)
	c := &katacli.Client{Executable: bin, Target: katacli.Target{Server: "http://127.0.0.1:7777", Project: "spoke-project", Workspace: t.TempDir(), Actor: "worker", Teammate: "adapter"}}
	r := &NativeRepository{Store: s, Client: c, Binding: NativeBinding{ProjectUID: nativeUID, Checkouts: map[string]string{"primary": t.TempDir()}, Secrets: map[string]string{"service": "private-value"}}}
	respond := func(entries map[string]json.RawMessage) {
		entries["authority show"] = json.RawMessage(`{"body":{"event_features":["automations_v1"],"authority":{"project_uid":"` + nativeUID + `","instance_uid":"` + nativeUID + `","epoch":"` + nativeUID + `"}}}`)
		entries["capabilities show"] = json.RawMessage(`{"body":{"project_uid":"` + nativeUID + `","event_features":["automations_v1"]}}`)
		raw, _ := json.Marshal(entries)
		os.WriteFile(filepath.Join(dir, "responses.json"), raw, 0600)
	}
	return r, respond
}
func nativeDef(name string) json.RawMessage {
	return json.RawMessage(`{"uid":"` + nativeUID + `","name":"` + name + `","definition_event_uid":"` + nativeUID + `","revision":1,"definition":{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Inspect"},"executor":{"uid":"` + nativeUID + `","kind":"herdr"},"checkout_key":"primary","options":{"counter":9007199254740993}}}`)
}
func TestNativeRefreshImportsPeerDefinitionsAndLabelsOffline(t *testing.T) {
	r, respond := nativeFixture(t)
	respond(map[string]json.RawMessage{"job list": json.RawMessage(`{"body":{"jobs":[` + string(nativeDef("Peer")) + `]}}`), "flow list": json.RawMessage(`{"body":{"flows":[` + string(nativeDef("Peer flow")) + `]}}`)})
	snapshot, e := r.Refresh(t.Context())
	if e != nil || snapshot.Offline || len(snapshot.Jobs) != 1 || len(snapshot.Flows) != 1 {
		t.Fatalf("peer refresh %+v %v", snapshot, e)
	}
	r.Client.Executable = filepath.Join(t.TempDir(), "missing")
	snapshot, e = r.Refresh(t.Context())
	if e != nil || !snapshot.Offline || len(snapshot.Jobs) != 1 || !strings.Contains(snapshot.Label(), "offline") {
		t.Fatalf("offline %+v %v", snapshot, e)
	}
	if e := r.ActivationAllowed(context.Background()); e == nil {
		t.Fatal("offline cache granted activation")
	}
}
func TestNativeSaveFailureRetainsCacheAndDraft(t *testing.T) {
	r, respond := nativeFixture(t)
	def := nativeDef("Saved")
	respond(map[string]json.RawMessage{"job list": json.RawMessage(`{"body":{"jobs":[` + string(def) + `]}}`), "flow list": json.RawMessage(`{"body":{"flows":[]}}`)})
	if _, e := r.Refresh(t.Context()); e != nil {
		t.Fatal(e)
	}
	draft, e := katacli.NewDraft("job", nativeUID, "Changed", json.RawMessage(`{"version":1,"options":{"counter":9007199254740993}}`), nativeUID)
	if e != nil {
		t.Fatal(e)
	}
	respond(map[string]json.RawMessage{"job update": json.RawMessage(`{"exit":5,"body":{"error":"stale revision"}}`)})
	if _, e := r.Save(t.Context(), draft); e == nil {
		t.Fatal("stale save accepted")
	}
	cached, e := r.Cached(t.Context())
	if e != nil || cached.Jobs[0].Name != "Saved" || draft.Name != "Changed" || draft.ExpectedEventUID != nativeUID {
		t.Fatalf("failed save changed cache or draft %+v %v", cached, e)
	}
	respond(map[string]json.RawMessage{"job update": json.RawMessage(`{"body":{"job":` + string(nativeDef("Changed")) + `}}`)})
	if _, e := r.Save(t.Context(), draft); e != nil {
		t.Fatal(e)
	}
	cached, e = r.Cached(t.Context())
	if e != nil || cached.Jobs[0].Name != "Changed" {
		t.Fatalf("native save not cached %+v %v", cached, e)
	}
}
func TestNativeCacheScopeDoesNotCrossTargets(t *testing.T) {
	r, respond := nativeFixture(t)
	respond(map[string]json.RawMessage{"job list": json.RawMessage(`{"body":{"jobs":[` + string(nativeDef("Peer")) + `]}}`), "flow list": json.RawMessage(`{"body":{"flows":[]}}`)})
	if _, e := r.Refresh(t.Context()); e != nil {
		t.Fatal(e)
	}
	r.Client.Target.Server = "https://daemon.example"
	snapshot, e := r.Cached(t.Context())
	if e != nil || len(snapshot.Jobs) != 0 {
		t.Fatalf("foreign cache %+v %v", snapshot, e)
	}
}
func TestNativePortableJobOmitsLocalMappings(t *testing.T) {
	r, _ := nativeFixture(t)
	j := Job{ID: nativeUID, Name: "Inspect", CWD: r.Binding.Checkouts["primary"], Prompt: "Inspect", Schedule: ScheduleManual, Enabled: false, Catchup: CatchupAll, Model: DefaultModel}
	draft, e := r.JobDraft(j)
	if e != nil {
		t.Fatal(e)
	}
	for _, local := range []string{j.CWD, "private-value", "installation_uid"} {
		if strings.Contains(string(draft.Definition), local) {
			t.Fatalf("local mapping exported %q", draft.Definition)
		}
	}
	if !strings.Contains(string(draft.Definition), `"checkout_key":"primary"`) {
		t.Fatal("checkout reference lost")
	}
	j.CWD = t.TempDir()
	if _, e := r.JobDraft(j); e == nil {
		t.Fatal("unmapped absolute path accepted")
	}
}
func TestNativeMappingNeedsProjectAndLocalSecretsOnly(t *testing.T) {
	r, _ := nativeFixture(t)
	dir := t.TempDir()
	cfg := NativeConfig{Client: *r.Client, Binding: NativeBinding{ProjectUID: r.Binding.ProjectUID, Checkouts: r.Binding.Checkouts, Secrets: r.Binding.Secrets}}
	if e := WriteNativeConfig(dir, cfg); e != nil {
		t.Fatalf("ordinary local mapping demands retired registration: %v", e)
	}
	got, e := LoadNativeConfig(dir)
	if e != nil || got.Binding.Secrets["service"] != "private-value" || got.Binding.Checkouts["primary"] != cfg.Binding.Checkouts["primary"] {
		t.Fatalf("local mapping lost %+v %v", got, e)
	}
	cfg.Binding.ProjectUID = ""
	if e := WriteNativeConfig(dir, cfg); e == nil {
		t.Fatal("missing ordinary project identity accepted")
	}
}
func TestNativeJobMappingPreservesOpaqueNumbers(t *testing.T) {
	r, _ := nativeFixture(t)
	var def katacli.Definition
	if e := json.Unmarshal(nativeDef("Peer"), &def); e != nil {
		t.Fatal(e)
	}
	j, e := r.JobFrom(def, false)
	if e != nil {
		t.Fatal(e)
	}
	draft, e := r.JobDraft(j)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(draft.Definition), `"counter":9007199254740993`) {
		t.Fatalf("mapping rounded native number %s", draft.Definition)
	}
}

func TestNativeJobEditorPreservesFutureOwnedOptionsExactly(t *testing.T) {
	r, _ := nativeFixture(t)
	var def katacli.Definition
	if e := katacli.Decode(nativeDef("Inspect"), &def); e != nil {
		t.Fatal(e)
	}
	var body map[string]json.RawMessage
	katacli.Decode(def.Definition, &body)
	body["options"] = json.RawMessage(`{"herdr":{"future_counter":9007199254740993}}`)
	def.Definition, _ = json.Marshal(body)
	job, e := r.JobFrom(def, false)
	if e != nil {
		t.Fatal(e)
	}
	draft, e := r.JobDraft(job)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(draft.Definition), `"future_counter":9007199254740993`) {
		t.Fatalf("future owned options lost %s", draft.Definition)
	}
}

func TestNativeRefreshDeadlineKeepsOfflineCacheAndCancellation(t *testing.T) {
	r, respond := nativeFixture(t)
	respond(map[string]json.RawMessage{"job list": json.RawMessage(`{"body":{"jobs":[` + string(nativeDef("Cached")) + `]}}`), "flow list": json.RawMessage(`{"body":{"flows":[]}}`)})
	if _, e := r.Refresh(t.Context()); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(filepath.Dir(r.Client.Executable), "mode"), []byte("sleep"), 0600)
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	snapshot, e := r.Refresh(ctx)
	if e != nil || !snapshot.Offline || len(snapshot.Jobs) != 1 || !strings.Contains(snapshot.Label(), "deadline") {
		t.Fatalf("deadline erased cached presentation %+v %v", snapshot, e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("offline fallback exceeded work bound")
	}
	expired, done := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer done()
	snapshot, e = r.Refresh(expired)
	if e != nil || !snapshot.Offline || len(snapshot.Jobs) != 1 {
		t.Fatalf("already expired context erased cache %+v %v", snapshot, e)
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if _, e := r.Refresh(canceled); !errors.Is(e, context.Canceled) {
		t.Fatalf("caller cancellation lost: %v", e)
	}
}
