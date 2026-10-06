package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUnconfiguredJobCannotBecomeSavedAuthority(t *testing.T) {
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	e := jobAdd([]string{"--id", "01ARZ3NDEKTSV4RRFFQ69G5FAD", "--name", "Inspect", "--prompt", "Inspect workspace", "--cwd", t.TempDir(), "--enabled=false"})
	if !errors.Is(e, store.ErrNativeUnconfigured) {
		t.Fatalf("unconfigured save=%v", e)
	}
	s, e := store.Open(stateDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	jobs, e := s.Jobs(context.Background())
	if e != nil || len(jobs) != 0 {
		t.Fatalf("failed native save created local authority %+v %v", jobs, e)
	}
}
func TestNativeConfigureRequiresExplicitMapping(t *testing.T) {
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	if e := nativeCmd([]string{"configure"}); e == nil {
		t.Fatal("implicit daemon mapping accepted")
	}
	if _, e := os.Stat(stateDir() + "/native.json"); !os.IsNotExist(e) {
		t.Fatalf("invalid binding persisted %v", e)
	}
}
func TestNativeConfigurePersistsExplicitRoutedInstallation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_KATA_HOME", dir)
	bin := filepath.Join(t.TempDir(), "kata")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "../../internal/katacli/testdata/command")
	if raw, e := build.CombinedOutput(); e != nil {
		t.Fatalf("fixture %v %s", e, raw)
	}
	cfg := store.NativeConfig{Client: katacli.Client{Executable: bin, Target: katacli.Target{Server: "http://127.0.0.1:7777", Project: "spoke-project", Workspace: t.TempDir(), Actor: "worker", Teammate: "adapter"}}, Binding: store.NativeBinding{}}
	raw, _ := json.Marshal(cfg)
	file := filepath.Join(t.TempDir(), "mapping.json")
	os.WriteFile(file, raw, 0600)
	if e := nativeCmd([]string{"configure", "--file", file}); e != nil {
		t.Fatal(e)
	}
	got, e := store.LoadNativeConfig(dir)
	if e != nil || got.Binding.ProjectUID != "01ARZ3NDEKTSV4RRFFQ69G5FAD" || got.Client.Target.Actor != "worker" {
		t.Fatalf("mapping %+v %v", got, e)
	}
}
func TestUnconfiguredNativeWorkflowCannotUseLegacyDefinition(t *testing.T) {
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	if err := workflowRun([]string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"}); !errors.Is(err, store.ErrNativeUnconfigured) {
		t.Fatalf("boundary: %v", err)
	}
}
func TestWorkflowNewIsAnUnsavedPortableDraft(t *testing.T) {
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	out, e := captureStdout(t, func() error { return workflowNew([]string{"inspect", "--about", "Inspect workspace"}) })
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out, "unsaved") || !strings.Contains(out, "workflow save") {
		t.Fatalf("draft appears saved: %s", out)
	}
	if _, e := os.Stat(filepath.Join(stateDir(), "drafts", "workflows", "inspect.yml")); e != nil {
		t.Fatal(e)
	}
	if e := workflowSave([]string{"inspect"}); !errors.Is(e, store.ErrNativeUnconfigured) {
		t.Fatalf("offline save=%v", e)
	}
}
func TestWorkflowListDoesNotAdoptPrivateLegacyAuthority(t *testing.T) {
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	os.MkdirAll(workflowDir(), 0700)
	os.WriteFile(filepath.Join(workflowDir(), "legacy.yml"), []byte("about: Legacy\nsteps:\n - id: inspect\n   run: git status\n"), 0600)
	out, e := captureStdout(t, func() error { return workflowList(nil) })
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(out, "legacy") || !strings.Contains(out, "offline") {
		t.Fatalf("legacy fallback: %s", out)
	}
}

// Exercises public subprocess responses through the actual command construction.
func nativeCommandFixture(t *testing.T, responses map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HERDR_KATA_HOME", dir)
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "kata")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if raw, e := exec.Command("go", "build", "-o", bin, "../../internal/katacli/testdata/command").CombinedOutput(); e != nil {
		t.Fatalf("fixture: %v %s", e, raw)
	}
	responses["authority show"] = map[string]any{"body": map[string]any{"event_features": []string{"cron_v1"}, "authority": map[string]string{"project_uid": "01ARZ3NDEKTSV4RRFFQ69G5FAD", "instance_uid": "01ARZ3NDEKTSV4RRFFQ69G5FAD", "epoch": "01ARZ3NDEKTSV4RRFFQ69G5FAD"}}}
	responses["capabilities show"] = map[string]any{"body": map[string]any{"project_uid": "01ARZ3NDEKTSV4RRFFQ69G5FAD", "event_features": []string{"cron_v1"}}}
	if _, ok := responses["job list"]; !ok {
		responses["job list"] = map[string]any{"body": map[string]any{"jobs": []any{}}}
	}
	if _, ok := responses["workflow list"]; !ok {
		responses["workflow list"] = map[string]any{"body": map[string]any{"workflows": []any{}}}
	}
	raw, e := json.Marshal(responses)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(binDir, "responses.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(binDir, "mode"), []byte("responses"), 0600); e != nil {
		t.Fatal(e)
	}
	cfg := store.NativeConfig{Client: katacli.Client{Executable: bin, Target: katacli.Target{Server: "http://127.0.0.1:7777", Project: "spoke-project", Workspace: t.TempDir(), Actor: "worker", Teammate: "adapter"}}, Binding: store.NativeBinding{ProjectUID: "01ARZ3NDEKTSV4RRFFQ69G5FAD"}}
	if e := store.WriteNativeConfig(dir, cfg); e != nil {
		t.Fatal(e)
	}
	return binDir
}
func nativeWorkflowFixture(t *testing.T) katacli.Definition {
	t.Helper()
	return katacli.Definition{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAD", Name: "Inspect workspace", DefinitionEventUID: "01ARZ3NDEKTSV4RRFFQ69G5FAE", Definition: json.RawMessage(`{"version":1,"options":{"counter":9007199254740993,"herdr":{"input":"which inbox","about":"Inspect workspace"}},"steps":[{"key":"inspect","kind":"command","command":"git status","options":{"limit":9007199254740993}}]}`)}
}
func TestWorkflowShowDisplaysSelectedNativeDefinitionWithoutPrivateFile(t *testing.T) {
	def := nativeWorkflowFixture(t)
	nativeCommandFixture(t, map[string]any{"workflow list": map[string]any{"body": map[string]any{"workflows": []katacli.Definition{def}}}})
	out, e := captureStdout(t, func() error { return workflowShow([]string{def.UID}) })
	if e != nil || !strings.Contains(out, "9007199254740993") || !strings.Contains(out, "git status") {
		t.Fatalf("native workflow display %s %v", out, e)
	}
}

func TestBrokenUnsavedWorkflowDraftRemainsEditableWithoutNativeAuthority(t *testing.T) {
	dir := workflowFilesIn(t)
	os.MkdirAll(workflowDraftDir(), 0700)
	path := filepath.Join(dir, "drafts", "workflows", "broken.yml")
	if e := os.WriteFile(path, []byte("steps:\n - id: inspect\n   agnet: typo\n"), 0600); e != nil {
		t.Fatal(e)
	}
	out, e := captureStdout(t, func() error { return workflowEdit([]string{"broken"}) })
	if e != nil || strings.TrimSpace(out) != path {
		t.Fatalf("broken unsaved draft %s %v", out, e)
	}
}
func TestImportedWorkflowSaveRetainsSelectedNativeNameAndWinner(t *testing.T) {
	def := nativeWorkflowFixture(t)
	def.Name = "Canonical native name"
	binDir := nativeCommandFixture(t, map[string]any{"workflow list": map[string]any{"body": map[string]any{"workflows": []katacli.Definition{def}}}, "workflow update": map[string]any{"body": map[string]any{"workflow": def}}})
	draft, e := prepareWorkflowDraft(def.UID)
	if e != nil {
		t.Fatal(e)
	}
	if e = workflowSave([]string{strings.TrimSuffix(filepath.Base(draft.Path), ".yml")}); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(draft.Path + ".native.json")
	if e != nil {
		t.Fatal(e)
	}
	var saved katacli.Draft
	if e = json.Unmarshal(raw, &saved); e != nil {
		t.Fatal(e)
	}
	if saved.Name != def.Name || saved.ExpectedEventUID != def.DefinitionEventUID || !strings.Contains(string(saved.Definition), "9007199254740993") {
		t.Fatalf("lost imported identity %+v fixture %s", saved, binDir)
	}
}

func nativeCommandStore(t *testing.T) string {
	t.Helper()
	binDir := nativeCommandFixture(t, map[string]any{})
	if e := os.WriteFile(filepath.Join(binDir, "mode"), []byte("store"), 0600); e != nil {
		t.Fatal(e)
	}
	return binDir
}
func mapTestCheckout(t *testing.T, path string) {
	t.Helper()
	cfg, e := store.LoadNativeConfig(stateDir())
	if e != nil {
		t.Fatal(e)
	}
	if cfg.Binding.Checkouts == nil {
		cfg.Binding.Checkouts = map[string]string{}
	}
	cfg.Binding.Checkouts[filepath.Base(path)] = path
	if e = store.WriteNativeConfig(stateDir(), cfg); e != nil {
		t.Fatal(e)
	}
}
func TestConfiguredJobCommandsUseNativeCASAndKeepLocalRunJournal(t *testing.T) {
	binDir := nativeCommandStore(t)
	ctx := context.Background()
	id := "01ARZ3NDEKTSV4RRFFQ69G5FAD"
	checkout := t.TempDir()
	mapTestCheckout(t, checkout)
	if e := jobAdd([]string{"--id", id, "--name", "Inspect", "--prompt", "Inspect workspace", "--cwd", checkout, "--cron", "0 7 * * *", "--tags", "daily", "--ref", "Ticket: Mixed / #42"}); e != nil {
		t.Fatal(e)
	}
	s := storeForEnv(t)
	original, e := s.Job(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	if original.Enabled || original.CronExpr != "0 7 * * *" || original.CWD != checkout || original.Ref != "Ticket: Mixed / #42" {
		t.Fatalf("native defaults/mapping %+v", original)
	}
	if e := jobAdd([]string{"--id", id, "--name", "Other", "--prompt", "Overwrite", "--cwd", checkout}); e == nil {
		t.Fatal("existing native UID overwritten")
	}
	if e := jobAdd([]string{"--id", "01ARZ3NDEKTSV4RRFFQ69G5FAE", "--name", "Inspect", "--prompt", "Duplicate name", "--cwd", checkout}); e == nil {
		t.Fatal("duplicate displayed name accepted")
	}
	if e := jobEdit([]string{id, "--prompt", "Edited workspace", "--ref", ""}); e != nil {
		t.Fatal(e)
	}
	edited, e := s.Job(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	if edited.Ref != "" || edited.Prompt != "Edited workspace" || edited.CronExpr != original.CronExpr || edited.CWD != checkout || len(edited.Tags) != 1 {
		t.Fatalf("edit reset fields %+v", edited)
	}
	request, e := os.ReadFile(filepath.Join(binDir, "last-request.json"))
	if e != nil {
		t.Fatal(e)
	}
	var sent struct {
		Expected string `json:"expected_event_uid"`
	}
	json.Unmarshal(request, &sent)
	if sent.Expected != original.NativeEventUID {
		t.Fatalf("wrong edit winner %s want %s", sent.Expected, original.NativeEventUID)
	}
	original.Prompt = "Stale edit"
	if e := s.PutJob(ctx, *original); e == nil {
		t.Fatal("stale displayed winner overwrote native edit")
	}
	if e := s.DeleteJobAt(ctx, *original); e == nil {
		t.Fatal("stale displayed delete adopted a newer winner")
	}
	if e := jobEnable([]string{id}, true); e != nil {
		t.Fatal(e)
	}
	if e := jobEnable([]string{id}, false); e != nil {
		t.Fatal(e)
	}
	if e := s.PutRun(ctx, store.Run{ID: "local-journal", JobID: id, Outcome: "done"}); e != nil {
		t.Fatal(e)
	}
	if e := jobRemove([]string{id}); e != nil {
		t.Fatal(e)
	}
	if j, e := s.Job(ctx, id); !errors.Is(e, store.ErrNotFound) || j != nil {
		t.Fatalf("tombstoned native job remains %+v %v", j, e)
	}
	if runs, e := s.JobRuns(ctx, id, 10); e != nil || len(runs) != 1 {
		t.Fatalf("journal removed %+v %v", runs, e)
	}
	legacy, e := store.Open(stateDir())
	if e != nil {
		t.Fatal(e)
	}
	defer legacy.Close()
	if rows, e := legacy.Jobs(ctx); e != nil || len(rows) != 0 {
		t.Fatalf("native writes copied to private authority %+v %v", rows, e)
	}
}
func TestWorkflowDraftSaveRetainsIdentityAcrossFailureThenNativeSave(t *testing.T) {
	binDir := nativeCommandStore(t)
	if e := workflowNew([]string{"inspect", "--about", "Inspect workspace"}); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(binDir, "mode"), []byte("nonzero"), 0600)
	if e := workflowSave([]string{"inspect"}); e == nil || !strings.Contains(e.Error(), "unsaved workflow draft retained") {
		t.Fatalf("failed save %v", e)
	}
	path := filepath.Join(workflowDraftDir(), "inspect.yml.native.json")
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var retained katacli.Draft
	if e = json.Unmarshal(raw, &retained); e != nil {
		t.Fatal(e)
	}
	if retained.UID == "" || retained.ExpectedEventUID != "" {
		t.Fatalf("bad unsaved identity %+v", retained)
	}
	os.WriteFile(filepath.Join(binDir, "mode"), []byte("store"), 0600)
	if e := workflowSave([]string{"inspect"}); e != nil {
		t.Fatal(e)
	}
	raw, e = os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var saved katacli.Draft
	json.Unmarshal(raw, &saved)
	if saved.UID != retained.UID || saved.ExpectedEventUID == "" {
		t.Fatalf("retry changed UID %+v %+v", retained, saved)
	}
	f, e := nativeWorkflow(context.Background(), saved.UID)
	if e != nil || len(f.Steps) != 3 {
		t.Fatalf("saved native peer workflow %+v %v", f, e)
	}
	if e := workflowRemove([]string{saved.UID}); e != nil {
		t.Fatal(e)
	}
	if _, e := nativeWorkflow(context.Background(), saved.UID); e == nil {
		t.Fatal("workflow tombstone remains live")
	}
	if _, e := os.Stat(filepath.Join(workflowDraftDir(), "inspect.yml")); e != nil {
		t.Fatal("native tombstone deleted local draft")
	}
}

func TestJobShowReadsSelectedNativeWorkflowAndKeepsOfflineLabel(t *testing.T) {
	workflowDef := nativeWorkflowFixture(t)
	jobDef := katacli.Definition{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAF", Name: "Inspect job", DefinitionEventUID: "01ARZ3NDEKTSV4RRFFQ69G5FAG", Definition: json.RawMessage(`{"version":1,"enabled":false,"trigger":{"kind":"manual"},"action":{"kind":"execute","workflow_uid":"` + workflowDef.UID + `"},"options":{"herdr":{"Model":"sonnet","Tags":["daily"]}}}`)}
	binDir := nativeCommandFixture(t, map[string]any{"job show": map[string]any{"body": map[string]any{"job": jobDef}}, "job list": map[string]any{"body": map[string]any{"jobs": []katacli.Definition{jobDef}}}, "workflow list": map[string]any{"body": map[string]any{"workflows": []katacli.Definition{workflowDef}}}})
	out, e := captureStdout(t, func() error { return jobShow([]string{jobDef.UID}) })
	if e != nil || !strings.Contains(out, "git status") {
		t.Fatalf("job native workflow %s %v", out, e)
	}
	if _, e := captureStdout(t, func() error { return jobList(nil) }); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(binDir, "mode"), []byte("nonzero"), 0600)
	out, e = captureStdout(t, func() error { return jobList(nil) })
	if e != nil || !strings.Contains(out, "offline native cache") {
		t.Fatalf("offline list %s %v", out, e)
	}
	out, e = captureStdout(t, func() error { return jobShow([]string{jobDef.UID}) })
	if e != nil || strings.Count(out, "offline native cache") < 2 || !strings.Contains(out, "git status") {
		t.Fatalf("offline job show and workflow provenance %s %v", out, e)
	}
}

func TestCachedWorkflowShowLabelsOfflineAndPreservesRawDocument(t *testing.T) {
	def := nativeWorkflowFixture(t)
	binDir := nativeCommandFixture(t, map[string]any{"workflow list": map[string]any{"body": map[string]any{"workflows": []katacli.Definition{def}}}})
	if _, e := captureStdout(t, func() error { return workflowShow([]string{def.UID}) }); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(binDir, "mode"), []byte("nonzero"), 0600)
	var out string
	diagnostic := captureStderr(t, func() {
		var e error
		out, e = captureStdout(t, func() error { return workflowShow([]string{def.UID}) })
		if e != nil {
			t.Fatal(e)
		}
	})
	if strings.TrimSpace(out) != string(def.Definition) || !strings.Contains(diagnostic, "offline native cache") {
		t.Fatalf("unlabeled cached native workflow: %s diagnostic %s", out, diagnostic)
	}
}

func TestNativeMixedProjectionRowsRemainInspectable(t *testing.T) {
	good := nativeWorkflowFixture(t)
	bad := good
	bad.UID = "01ARZ3NDEKTSV4RRFFQ69G5FAH"
	bad.Definition = json.RawMessage(`{"version":1,"options":{"herdr":{"SkipPermissions":"unknown"}},"steps":[{"key":"inspect","kind":"prompt","prompt":"Inspect"}]}`)
	goodJob := katacli.Definition{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAF", Name: "Supported", DefinitionEventUID: "01ARZ3NDEKTSV4RRFFQ69G5FAG", Definition: json.RawMessage(`{"version":1,"kind":"job","trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Inspect"}}`)}
	badJob := goodJob
	badJob.UID = "01ARZ3NDEKTSV4RRFFQ69G5FAJ"
	badJob.Name = "Opaque peer"
	badJob.Definition = json.RawMessage(`{"version":1,"kind":"job","trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Inspect"},"options":{"herdr":{"Tags":"daily"}}}`)
	nativeCommandFixture(t, map[string]any{"job list": map[string]any{"body": map[string]any{"jobs": []katacli.Definition{badJob, goodJob}}}, "workflow list": map[string]any{"body": map[string]any{"workflows": []katacli.Definition{bad, good}}}})
	s := storeForEnv(t)
	if job, e := s.Job(t.Context(), goodJob.UID); e != nil || job == nil {
		t.Fatalf("unrelated good job hidden %+v %v", job, e)
	}
	if _, e := s.Job(t.Context(), badJob.UID); e == nil || !strings.Contains(e.Error(), badJob.UID) {
		t.Fatalf("selected projection problem missing UID %v", e)
	}
	if taken, e := s.NameTaken(t.Context(), badJob.Name, ""); e != nil || !taken {
		t.Fatalf("unprojectable peer name was not checked %v %v", taken, e)
	}
	var listed string
	diag := captureStderr(t, func() {
		var e error
		listed, e = captureStdout(t, func() error { return jobList(nil) })
		if e != nil {
			t.Fatal(e)
		}
	})
	if !strings.Contains(listed, goodJob.UID) || !strings.Contains(diag, badJob.UID) {
		t.Fatalf("job list isolation %s diagnostic %s", listed, diag)
	}
	diag = captureStderr(t, func() {
		var e error
		listed, e = captureStdout(t, func() error { return workflowList(nil) })
		if e != nil {
			t.Fatal(e)
		}
	})
	if !strings.Contains(listed, good.UID) || !strings.Contains(diag, bad.UID) {
		t.Fatalf("workflow list isolation %s diagnostic %s", listed, diag)
	}
	if f, e := nativeWorkflow(t.Context(), good.UID); e != nil || f.ID != good.UID {
		t.Fatalf("unrelated good workflow hidden %+v %v", f, e)
	}
	raw, e := captureStdout(t, func() error { return workflowShow([]string{bad.UID}) })
	if e != nil || strings.TrimSpace(raw) != string(bad.Definition) {
		t.Fatalf("bad peer raw inspection unavailable %s %v", raw, e)
	}
}
