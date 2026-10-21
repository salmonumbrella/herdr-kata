package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"go.kenn.io/kit/secretref"
)

func TestDraftUntouchedNumbersAndTime(t *testing.T) {
	j := kata.Job{UID: "job", DefinitionEventUID: "event", Name: "example-job", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"once","at":"2026-10-20T09:00:37.123+02:00","timezone":"Europe/Berlin"},"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"per-run","title":"example"},"overlap":"forbid","catchup":"latest","future":9007199254740993,"options":{"opaque":9007199254740993}}`)}
	m := newJobForm(t.Context(), cfg(), &fakeWriter{}, &j, nil, editorContext{})
	m.Set("description", "new description")
	raw, err := m.definition()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"description":"new description"`) || !strings.Contains(string(raw), `"at":"2026-10-20T09:00:37.123+02:00"`) || strings.Count(string(raw), "9007199254740993") != 2 {
		t.Fatal(string(raw))
	}
}
func TestDraftShapeRoundTrip(t *testing.T) {
	c := cfg()
	c.Secrets = map[string]secretref.Ref{"example": {Env: "EXAMPLE_SECRET"}}
	m := newJobForm(t.Context(), c, &fakeWriter{}, nil, nil, editorContext{})
	for k, v := range map[string]string{"name": "example-job", "description": "example", "tags": "daily", "secret_refs": "TOKEN=example", "timeout": "2h", "checkout": "repo", "prompt": "work", "title": "example"} {
		m.Set(k, v)
	}
	m.Set("mode", "recurring")
	m.Set("trigger", "interval")
	m.Set("schedule", "1m")
	m.Set("parent", "example-parent")
	raw, err := m.definition()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	for _, key := range []string{"options", "secret_refs", "checkout_key", "timeout_seconds"} {
		if _, ok := got[key]; ok {
			t.Fatalf("recurring retained %s: %s", key, raw)
		}
	}
	if got["enabled"] != false {
		t.Fatal("new recurring enabled")
	}
	m.Set("mode", "prompt")
	raw, err = m.definition()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"tags":["daily"]`) || !strings.Contains(string(raw), `"secret_refs":{"TOKEN":"example"}`) || !strings.Contains(string(raw), `"timeout_seconds":7200`) {
		t.Fatal(string(raw))
	}
}
func TestDraftUnchangedNativeEmptyKeys(t *testing.T) {
	for _, extra := range []string{`,"options":{}`, `,"secret_refs":{}`, `,"timeout_seconds":0`, `,"checkout_key":""`, `,"options":{},"secret_refs":{},"timeout_seconds":0,"checkout_key":""`} {
		j := kata.Job{UID: "job", DefinitionEventUID: "event", Name: "example-job", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":false,"trigger":{"kind":"interval","interval_seconds":60},"action":{"kind":"notify","recipient":"worker/inbox"},"issue":{"kind":"per-run","parent_uid":"parent","title":"example"},"overlap":"allow","catchup":"latest"` + extra + `}`)}
		m := newJobForm(t.Context(), cfg(), &fakeWriter{}, &j, nil, editorContext{})
		raw, err := m.definition()
		if err != nil {
			t.Fatal(err)
		}
		if !kata.SameDefinition(j, kata.Job{UID: j.UID, Name: j.Name, Definition: raw}) {
			t.Fatalf("native normalized: %s", raw)
		}
	}
}

func TestDraftNativeRejectsIncompatibleFields(t *testing.T) {
	for _, extra := range []string{`,"options":{"tags":["daily"]}`, `,"secret_refs":{"TOKEN":"example"}`, `,"timeout_seconds":60`, `,"checkout_key":"repo"`} {
		j := kata.Job{UID: "job", DefinitionEventUID: "event", Name: "example-job", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":false,"trigger":{"kind":"interval","interval_seconds":60},"action":{"kind":"notify","recipient":"worker/inbox"},"issue":{"kind":"per-run","parent_uid":"parent","title":"example"},"overlap":"allow","catchup":"latest"` + extra + `}`)}
		m := newJobForm(t.Context(), cfg(), &fakeWriter{}, &j, nil, editorContext{})
		raw, err := m.definition()
		if err != nil {
			t.Fatal(err)
		}
		if err = m.validate(raw); err == nil {
			t.Fatalf("invalid native accepted: %s", raw)
		}
	}
}
func TestDraftProfileApplicability(t *testing.T) {
	c := cfg()
	c.Profiles = nil
	for _, mode := range []string{"prompt", "workflow", "notify", "recurring"} {
		t.Run(mode, func(t *testing.T) {
			flows := []kata.Workflow{{UID: "command", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"work","kind":"command","command":"true"}]}`)}}
			m := newJobForm(t.Context(), c, &fakeWriter{}, nil, flows, editorContext{})
			for _, kv := range [][2]string{{"name", "example-job"}, {"mode", mode}, {"prompt", "work"}, {"title", "example"}, {"workflow", "command"}, {"issue", "example-issue"}, {"recipient", "worker"}, {"message", "due"}, {"parent", "example-parent"}} {
				m.Set(kv[0], kv[1])
			}
			if mode == "recurring" {
				m.Set("trigger", "interval")
				m.Set("schedule", "1m")
			}
			raw, err := m.definition()
			if err != nil {
				t.Fatal(err)
			}
			err = m.validate(raw)
			if mode == "prompt" {
				if err == nil || !strings.Contains(err.Error(), "no profile allowed") {
					t.Fatalf("prompt: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
