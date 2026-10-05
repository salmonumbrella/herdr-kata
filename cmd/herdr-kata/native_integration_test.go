package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/flow"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func realNativeProduct(t *testing.T) (*katacli.Client, *store.Store) {
	t.Helper()
	bin := os.Getenv("KATA_NATIVE_TEST_BINARY")
	if bin == "" {
		t.Skip("requires explicit isolated branch binary")
	}
	// Runtime HOME is isolated too; reuse only explicit shared Go caches for
	// any later fixture build, without inheriting Kata targets/credentials.
	if raw, err := exec.Command("go", "env", "-json", "GOCACHE", "GOMODCACHE", "GOPATH").Output(); err == nil {
		var paths map[string]string
		if json.Unmarshal(raw, &paths) == nil {
			for key, value := range paths {
				t.Setenv(key, value)
			}
		}
	}
	runtimeHome := t.TempDir()
	t.Setenv("HOME", runtimeHome)
	t.Setenv("USERPROFILE", runtimeHome)
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	home, workspace := t.TempDir(), t.TempDir()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := listener.Addr().String()
	listener.Close()
	ctx, stop := context.WithCancel(t.Context())
	log, e := os.Create(filepath.Join(t.TempDir(), "daemon.log"))
	if e != nil {
		t.Fatal(e)
	}
	daemon := exec.CommandContext(ctx, bin, "daemon", "start", "--foreground", "--listen", address, "--no-auto-token")
	for _, key := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "TEMP", "SYSTEMROOT", "SystemRoot", "WINDIR", "COMSPEC"} {
		if value, ok := os.LookupEnv(key); ok {
			daemon.Env = append(daemon.Env, key+"="+value)
		}
	}
	daemon.Env = append(daemon.Env, "KATA_HOME="+home, "KATA_AUTH_TOKEN=fixture-token", "GOMAXPROCS=2")
	daemon.Stdout, daemon.Stderr = log, log
	if e := daemon.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { stop(); daemon.Wait(); log.Close() })
	httpClient := http.Client{Timeout: 200 * time.Millisecond, Transport: &http.Transport{Proxy: nil}}
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, e := httpClient.Get("http://" + address + "/api/v1/ping")
		if e == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			raw, _ := os.ReadFile(log.Name())
			t.Fatalf("daemon unavailable: %s", raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
	c := &katacli.Client{Executable: bin, Target: katacli.Target{Server: "http://" + address, Project: "spoke-project", Workspace: workspace, Actor: "worker", Teammate: "adapter", Home: home, Token: "fixture-token"}, Timeout: 3 * time.Second}
	if e := c.Call(t.Context(), []string{"init"}, nil, new(any)); e != nil {
		realNativeFailure(t, e)
	}
	cap, e := c.Capabilities(t.Context())
	if e != nil {
		realNativeFailure(t, e)
	}
	cfg := store.NativeConfig{Client: *c, Binding: store.NativeBinding{ProjectUID: cap.ProjectUID, Checkouts: map[string]string{"primary": workspace}}}
	if e := store.WriteNativeConfig(stateDir(), cfg); e != nil {
		t.Fatal(e)
	}
	s, e := openStore()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return c, s
}
func realNativeFailure(t *testing.T, e error) {
	t.Helper()
	var ce *katacli.CommandError
	if errors.As(e, &ce) {
		t.Fatalf("%v stdout=%s stderr=%s", e, ce.Stdout, ce.Stderr)
	}
	t.Fatal(e)
}

func TestRealNativeProductMappers(t *testing.T) {
	c, s := realNativeProduct(t)
	t.Run("job add mapper", func(t *testing.T) {
		id, _ := katacli.NewUID()
		output, e := captureStdout(t, func() error {
			return jobAdd([]string{"--id", id, "--name", "Inspect", "--prompt", "Inspect workspace", "--cwd", c.Target.Workspace})
		})
		if e != nil {
			realNativeFailure(t, e)
		}
		if !strings.Contains(output, "job "+id+" saved") || len(output) > 4096 {
			t.Fatalf("unexpected creation result: %q", output)
		}
		j, e := s.Job(t.Context(), id)
		if e != nil {
			realNativeFailure(t, e)
		}
		if j.Enabled || j.Prompt != "Inspect workspace" {
			t.Fatalf("product mapping %+v", j)
		}
		// Independent producer validation must continue rejecting the old discriminator.
		raw := json.RawMessage(`{"version":1,"kind":"recurring","enabled":false,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Inspect"},"overlap":"forbid","catchup":"latest","issue":{"kind":"per-run","title":"Inspect"}}`)
		invalid, _ := katacli.NewDraft("job", "", "Invalid", raw, "")
		_, e = c.Save(t.Context(), invalid)
		var validation *katacli.CommandError
		if !errors.As(e, &validation) || !strings.Contains(validation.Stderr, "unknown job kind") {
			t.Fatalf("producer job discriminator oracle lost: %v", e)
		}
	})
	t.Run("default flow save", func(t *testing.T) {
		created, e := captureStdout(t, func() error { return flowNew([]string{"inspect", "--about", "Inspect workspace"}) })
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(created, filepath.Join(flowDraftDir(), "inspect.yml")) || len(created) > 4096 {
			t.Fatalf("unexpected draft result: %q", created)
		}
		saved, e := captureStdout(t, func() error { return flowSave([]string{"inspect"}) })
		if e != nil {
			realNativeFailure(t, e)
		}
		raw, e := os.ReadFile(filepath.Join(flowDraftDir(), "inspect.yml.native.json"))
		if e != nil {
			t.Fatal(e)
		}
		var draft katacli.Draft
		if e = json.Unmarshal(raw, &draft); e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(saved, draft.UID) || len(saved) > 4096 {
			t.Fatalf("unexpected save result: %q", saved)
		}
		f, e := nativeFlow(t.Context(), draft.UID)
		if e != nil {
			t.Fatal(e)
		}
		if len(f.Steps) != 3 || !f.Steps[0].IsAgent() {
			t.Fatalf("default flow projection %+v", f)
		}
	})
	t.Run("peer prompt roundtrip", func(t *testing.T) {
		raw := json.RawMessage(`{"version":1,"about":"Peer prompt","options":{"future_counter":9007199254740993},"steps":[{"key":"inspect","kind":"prompt","prompt":"Inspect workspace","options":{"herdr_step":{"future_counter":9007199254740993}}}]}`)
		draft, _ := katacli.NewDraft("flow", "", "Peer prompt", raw, "")
		def, e := c.Save(t.Context(), draft)
		if e != nil {
			realNativeFailure(t, e)
		}
		f, e := nativeFlow(t.Context(), def.UID)
		if e != nil {
			t.Fatal(e)
		}
		if f.Steps[0].Agent != "Inspect workspace" {
			t.Fatalf("peer prompt lost %+v", f)
		}
		updated, e := flow.NativeDraft(f, def.UID, def.DefinitionEventUID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e := s.Native.Save(t.Context(), updated); e != nil {
			realNativeFailure(t, e)
		}
		var value map[string]any
		katacli.Decode(updated.Definition, &value)
		if value["options"].(map[string]any)["future_counter"] != json.Number("9007199254740993") {
			t.Fatal("peer future option lost")
		}
	})
}

func TestRealNativeStructuralFlowEdits(t *testing.T) {
	c, s := realNativeProduct(t)
	for _, tc := range []struct {
		name string
		keys []string
	}{{"delete", []string{"b"}}, {"insert", []string{"a", "x", "b"}}, {"reorder", []string{"b", "a"}}} {
		t.Run(tc.name, func(t *testing.T) {
			draft, _ := katacli.NewDraft("flow", "", tc.name, json.RawMessage(`{"version":1,"steps":[{"key":"a","kind":"command","command":"git status"},{"key":"b","kind":"command","command":"git diff","after":["a"],"options":{"future_counter":9007199254740993}}]}`), "")
			def, e := c.Save(t.Context(), draft)
			if e != nil {
				realNativeFailure(t, e)
			}
			f, e := flow.FromNative(def)
			if e != nil {
				t.Fatal(e)
			}
			byKey := map[string]store.Step{}
			for _, st := range f.Steps {
				byKey[st.ID] = st
			}
			byKey["x"] = store.Step{ID: "x", Run: "git log"}
			f.Steps = nil
			for _, key := range tc.keys {
				f.Steps = append(f.Steps, byKey[key])
			}
			update, e := flow.NativeDraft(f, def.UID, def.DefinitionEventUID)
			if e != nil {
				t.Fatal(e)
			}
			accepted, e := s.Native.Save(t.Context(), update)
			if e != nil {
				realNativeFailure(t, e)
			}
			var body struct {
				Steps []struct {
					Key     string
					After   []string
					Options map[string]json.RawMessage
				}
			}
			if e := katacli.Decode(accepted.Definition, &body); e != nil {
				t.Fatal(e)
			}
			for i, step := range body.Steps {
				if step.Key == flow.OverwatchStepID {
					continue
				}
				var expected []string
				if i > 0 {
					expected = []string{tc.keys[i-1]}
				}
				if len(step.After) != len(expected) || (len(expected) > 0 && !reflect.DeepEqual(step.After, expected)) {
					t.Fatalf("edited sequence %s after=%v want=%v", step.Key, step.After, expected)
				}
				if step.Key == "b" && string(step.Options["future_counter"]) != "9007199254740993" {
					t.Fatal("surviving opaque field lost")
				}
			}
		})
	}
}

func TestRealNativeProductCreateRetryAfterLostReply(t *testing.T) {
	c, _ := realNativeProduct(t)
	dir := t.TempDir()
	shim := filepath.Join(dir, "kata")
	if runtime.GOOS == "windows" {
		shim += ".exe"
	}
	if output, e := exec.Command("go", "build", "-o", shim, "../../internal/katacli/testdata/command").CombinedOutput(); e != nil {
		t.Fatalf("shim %v %s", e, output)
	}
	os.WriteFile(filepath.Join(dir, "forward-binary"), []byte(c.Executable), 0600)
	os.WriteFile(filepath.Join(dir, "mode"), []byte("forward"), 0600)
	os.WriteFile(filepath.Join(dir, "lose-reply"), []byte("armed"), 0600)
	cfg, e := store.LoadNativeConfig(stateDir())
	if e != nil {
		t.Fatal(e)
	}
	cfg.Client.Executable = shim
	if e := store.WriteNativeConfig(stateDir(), cfg); e != nil {
		t.Fatal(e)
	}
	id, _ := katacli.NewUID()
	args := []string{"--id", id, "--name", "Lost reply", "--prompt", "Inspect workspace", "--cwd", c.Target.Workspace}
	if e := jobAdd(args); e == nil {
		t.Fatal("accepted but lost response appeared saved")
	}
	accepted, e := c.Definition(t.Context(), "job", id)
	if e != nil {
		realNativeFailure(t, e)
	}
	os.Remove(filepath.Join(dir, "lose-reply"))
	if e := jobAdd(args); e != nil {
		t.Fatalf("retained product create did not recover: %v", e)
	}
	recovered, e := c.Definition(t.Context(), "job", id)
	if e != nil {
		realNativeFailure(t, e)
	}
	if recovered.DefinitionEventUID != accepted.DefinitionEventUID {
		t.Fatal("create retry wrote another winner")
	}
	changed := append([]string(nil), args...)
	changed[5] = "Different document"
	if e := jobAdd(changed); e == nil {
		t.Fatal("different retained create accepted")
	}
	var object map[string]json.RawMessage
	katacli.Decode(accepted.Definition, &object)
	object["timeout_seconds"] = json.RawMessage(`901`)
	raw, _ := json.Marshal(object)
	update, _ := katacli.NewDraft("job", id, accepted.Name, raw, accepted.DefinitionEventUID)
	edited, e := c.Save(t.Context(), update)
	if e != nil {
		realNativeFailure(t, e)
	}
	if e := jobAdd(args); e == nil {
		t.Fatal("later edit recovered as original create")
	}
	if _, e := c.DefinitionAction(t.Context(), "job", "delete", id, edited.DefinitionEventUID); e != nil {
		realNativeFailure(t, e)
	}
	if e := jobAdd(args); e == nil {
		t.Fatal("tombstone recovered as create")
	}
	rows, e := c.Definitions(t.Context(), "job", true)
	if e != nil || len(rows) != 1 {
		t.Fatalf("retry changed resource identity %+v %v", rows, e)
	}
}

func TestRealNativeOpaquePeerProjectionIsolation(t *testing.T) {
	c, s := realNativeProduct(t)
	goodUID, _ := katacli.NewUID()
	if e := jobAdd([]string{"--id", goodUID, "--name", "Supported", "--prompt", "Inspect", "--cwd", c.Target.Workspace}); e != nil {
		realNativeFailure(t, e)
	}
	jobDraft, _ := katacli.NewDraft("job", "", "Opaque peer", json.RawMessage(`{"version":1,"kind":"job","enabled":false,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Inspect"},"overlap":"forbid","catchup":"latest","issue":{"kind":"per-run","title":"Inspect"},"options":{"herdr":{"Tags":"daily"},"future_counter":9007199254740993}}`), "")
	bad, e := c.Save(t.Context(), jobDraft)
	if e != nil {
		realNativeFailure(t, e)
	}
	if good, e := s.Job(t.Context(), goodUID); e != nil || good == nil {
		t.Fatalf("producer-valid opaque peer hides supported job %+v %v", good, e)
	}
	if _, e := s.Job(t.Context(), bad.UID); e == nil || !strings.Contains(e.Error(), bad.UID) {
		t.Fatalf("unprojectable peer lacks named diagnostic: %v", e)
	}
	if taken, e := s.NameTaken(t.Context(), bad.Name, ""); e != nil || !taken {
		t.Fatalf("peer name excluded %v %v", taken, e)
	}
	inspected, e := captureStdout(t, func() error { return nativeCmd([]string{"job", "show", bad.UID}) })
	if e != nil || !strings.Contains(inspected, "9007199254740993") {
		t.Fatalf("opaque raw job inspection %s %v", inspected, e)
	}
	flowDraft, _ := katacli.NewDraft("flow", "", "Opaque prompt", json.RawMessage(`{"version":1,"options":{"herdr":{"SkipPermissions":"unknown"}},"steps":[{"key":"inspect","kind":"prompt","prompt":"Inspect workspace"}]}`), "")
	peer, e := c.Save(t.Context(), flowDraft)
	if e != nil {
		realNativeFailure(t, e)
	}
	inspected, e = captureStdout(t, func() error { return flowShow([]string{peer.UID}) })
	if e != nil || strings.TrimSpace(inspected) != string(peer.Definition) {
		t.Fatalf("opaque raw flow inspection %s %v", inspected, e)
	}
	cached, e := s.Native.Cached(t.Context())
	if e != nil || len(cached.Jobs) != 2 || len(cached.Flows) != 1 {
		t.Fatalf("raw accepted peer cache %+v %v", cached, e)
	}
}
