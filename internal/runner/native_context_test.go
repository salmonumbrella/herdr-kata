package runner

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func TestNativeContextImmutableRawSnapshotAndRouting(t *testing.T) {
	dir := t.TempDir()
	target := katacli.Target{Server: "http://127.0.0.1:7777", Project: "spoke-project", Workspace: t.TempDir(), Actor: "worker", Teammate: "launch"}
	c := NativeExecutionContext{Version: 1, RunUID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Target: target, ProjectUID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Job: &katacli.Definition{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", Definition: json.RawMessage(`{"version":1,"future":9007199254740993}`)}, Runtime: store.Job{CWD: t.TempDir(), Input: "original"}}
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	c.Runtime.Input = "changed"
	if err := c.Save(dir); err == nil {
		t.Fatal("snapshot overwritten")
	}
	got, err := LoadNativeContext(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runtime.Input != "original" || string(got.Job.Definition) != `{"version":1,"future":9007199254740993}` {
		t.Fatalf("snapshot changed: %+v", got)
	}
	repo := &store.NativeRepository{Client: &katacli.Client{Target: target}, Binding: store.NativeBinding{ProjectUID: c.ProjectUID}}
	repo.Client.Target.Token = "current-credential"
	if err := got.Check(repo); err != nil {
		t.Fatal(err)
	}
	repo.Client.Target.Actor = "different-worker"
	if err := got.Check(repo); err == nil {
		t.Fatal("actor retarget accepted")
	}
	info, err := os.Stat(filepath.Join(dir, "native-context.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("snapshot permissions: %v", info.Mode())
	}
}

func FuzzNativeContextLocalInputRoundTrip(f *testing.F) {
	f.Add("", uint8(0))
	f.Add("Inspect workspace", uint8(0))
	f.Add("Inspect workspace", uint8(1))
	f.Add("line one\nline two λ", uint8(2))
	f.Add("inspect with workflow", uint8(3))
	f.Fuzz(func(t *testing.T, prompt string, variant uint8) {
		// Snapshot strings are JSON Unicode text. Materialize arbitrary fuzz bytes
		// as Unicode; empty, NUL and all valid text remain in the domain.
		prompt = strings.ToValidUTF8(prompt, "\ufffd")
		dir := t.TempDir()
		c := NativeExecutionContext{Version: 1, RunUID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Target: katacli.Target{Actor: "actor:" + prompt}, Runtime: store.Job{Prompt: prompt, Input: "input:" + prompt, CWD: dir}}
		definition := katacli.Definition{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Definition: json.RawMessage(`{"future":9007199254740993}`)}
		switch variant % 4 {
		case 1:
			c.Job = &definition
		case 2:
			c.Workflow = &definition
		case 3:
			c.Job = &definition
			c.Workflow = &definition
		}

		raw, _ := json.Marshal(c)
		if len(raw) > 8<<20 {
			if err := c.Save(dir); err == nil {
				t.Fatal("oversized snapshot accepted")
			}
			return
		}
		if err := c.Save(dir); err != nil {
			t.Fatal(err)
		}
		got, err := LoadNativeContext(dir)
		if err != nil {
			t.Fatalf("local-only input snapshot did not round trip: %v", err)
		}
		// JSON null is the serialized absence of RawMessage. Compare JSON values
		// with exact Number text, rather than Go nil-vs-null implementation detail.
		restored, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		decode := func(body []byte) any {
			t.Helper()
			var v any
			d := json.NewDecoder(bytes.NewReader(body))
			d.UseNumber()
			if err := d.Decode(&v); err != nil {
				t.Fatal(err)
			}
			return v
		}
		if !reflect.DeepEqual(decode(raw), decode(restored)) {
			t.Fatalf("serialized snapshot changed: before=%s after=%s", raw, restored)
		}
	})
}
