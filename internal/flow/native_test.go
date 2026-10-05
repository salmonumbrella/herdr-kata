package flow

import (
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"strings"
	"testing"
)

func TestNativeFlowDoesNotSynthesizePermissionStep(t *testing.T) {
	d, e := NativeDraft(Flow{NativeName: "Inspect", Steps: []store.Step{{ID: "inspect", Agent: "Inspect workspace"}}}, "", "")
	if e != nil {
		t.Fatal(e)
	}
	var body struct{ Steps []struct{ Key string } }
	if e = katacli.Decode(d.Definition, &body); e != nil {
		t.Fatal(e)
	}
	if len(body.Steps) != 1 || body.Steps[0].Key != "inspect" {
		t.Fatalf("synthetic permission step retained: %s", d.Definition)
	}
	d, e = NativeDraft(Flow{NativeName: "Supervise", Steps: []store.Step{{ID: "overwatch", Run: "git status"}}}, "", "")
	if e != nil {
		t.Fatalf("ordinary step borrowed as permission key: %v", e)
	}
}

func TestNativeFlowDraftPreservesWholeWinnerAndNumbers(t *testing.T) {
	def := katacli.Definition{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAD", Name: "Inspect", DefinitionEventUID: "01ARZ3NDEKTSV4RRFFQ69G5FAD", Definition: json.RawMessage(`{"version":1,"about":"Inspect","input":"workspace","options":{"counter":9007199254740993},"steps":[{"key":"inspect","kind":"command","command":"git status","options":{"limit":9007199254740993}}]}`)}
	f, e := FromNative(def)
	if e != nil {
		t.Fatal(e)
	}
	if f.ID != def.UID || len(f.Steps) != 1 || f.Steps[0].Run != "git status" {
		t.Fatalf("projection %+v", f)
	}
	draft, e := NativeDraft(f, def.UID, def.DefinitionEventUID)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Count(string(draft.Definition), "9007199254740993") != 2 || draft.ExpectedEventUID != def.DefinitionEventUID {
		t.Fatalf("winner/number lost %+v", draft)
	}
}

func TestNativeFlowEditorPreservesFutureOwnedOptionsExactly(t *testing.T) {
	def := katacli.Definition{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAD", Name: "Inspect", DefinitionEventUID: "01ARZ3NDEKTSV4RRFFQ69G5FAE", Definition: json.RawMessage(`{"version":1,"about":"Inspect","options":{"herdr":{"future_counter":9007199254740993}},"steps":[{"key":"inspect","kind":"command","command":"git status","options":{"herdr_step":{"future_counter":9007199254740993}}}]}`)}
	f, e := FromNative(def)
	if e != nil {
		t.Fatal(e)
	}
	draft, e := NativeDraft(f, def.UID, def.DefinitionEventUID)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Count(string(draft.Definition), `"future_counter":9007199254740993`) != 2 {
		t.Fatalf("future owned options lost %s", draft.Definition)
	}
}

func TestNativeFlowStepEditClearsOwnedFieldsAndKeepsFutureOptions(t *testing.T) {
	def := katacli.Definition{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAD", Name: "Inspect", DefinitionEventUID: "01ARZ3NDEKTSV4RRFFQ69G5FAE", Definition: json.RawMessage(`{"version":1,"about":"Inspect","steps":[{"key":"inspect","kind":"prompt","prompt":"Inspect workspace","options":{"herdr_step":{"agent":"Inspect workspace","model":"sonnet","future_counter":9007199254740993}}}]}`)}
	f, e := FromNative(def)
	if e != nil {
		t.Fatal(e)
	}
	f.Steps[0].Agent = ""
	f.Steps[0].Model = ""
	f.Steps[0].Run = "git status"
	draft, e := NativeDraft(f, def.UID, def.DefinitionEventUID)
	if e != nil {
		t.Fatal(e)
	}
	def.Definition = draft.Definition
	next, e := FromNative(def)
	if e != nil {
		t.Fatal(e)
	}
	if next.Steps[0].Agent != "" || next.Steps[0].Model != "" || next.Steps[0].Run != "git status" || !strings.Contains(string(draft.Definition), `"future_counter":9007199254740993`) {
		t.Fatalf("step field clearing lost %+v %s", next.Steps[0], draft.Definition)
	}
}

func TestNativeNonStructuralEditPreservesDependencyGraph(t *testing.T) {
	def := katacli.Definition{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAD", Name: "Graph", DefinitionEventUID: "01ARZ3NDEKTSV4RRFFQ69G5FAE", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"a","kind":"command","command":"git status"},{"key":"b","kind":"command","command":"git diff"},{"key":"c","kind":"command","command":"git log","after":["a"],"options":{"future_counter":9007199254740993}}]}`)}
	f, e := FromNative(def)
	if e != nil {
		t.Fatal(e)
	}
	f.About = "Updated description"
	draft, e := NativeDraft(f, def.UID, def.DefinitionEventUID)
	if e != nil {
		t.Fatal(e)
	}
	var body struct{ Steps []struct{ After []string } }
	if e := katacli.Decode(draft.Definition, &body); e != nil {
		t.Fatal(e)
	}
	if len(body.Steps[1].After) != 0 || len(body.Steps[2].After) != 1 || body.Steps[2].After[0] != "a" {
		t.Fatalf("non-structural edit changed graph %s", draft.Definition)
	}
}
