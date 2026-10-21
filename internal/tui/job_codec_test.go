package tui

import (
	"bytes"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestUntouchedDurationJobDoesNotSave(t *testing.T) {
	for _, trigger := range []string{
		`{"kind":"interval","interval_seconds":3600,"timezone":"UTC"}`,
		`{"kind":"once","at":"2030-01-01T12:00:00-07:00","timezone":"America/Denver"}`,
	} {
		t.Run(trigger, func(t *testing.T) {
			original := kata.Job{UID: "job", Name: "Example", DefinitionEventUID: "event", Definition: []byte(`{"version":1,"kind":"job","enabled":true,"trigger":` + trigger + `,"action":{"kind":"execute","prompt":"Work"},"issue":{"kind":"per-run","title":"Example","deadline_offset_seconds":-60},"timeout_seconds":3600,"grace_seconds":90,"overlap":"forbid","catchup":"latest","options":{"opaque":9007199254740993}}`)}
			w := &classificationWriter{}
			m := newJobForm(t.Context(), cfg(), w, &original, nil, editorContext{ProjectUID: "project"})
			closed := completeSave(t, m)
			if closed.Message != "no changes" || closed.Saved != nil || len(w.calls) != 0 || !bytes.Equal(m.job.Definition, original.Definition) {
				t.Fatalf("untouched job saved or normalized: completion=%+v calls=%v definition=%s", closed, w.calls, m.job.Definition)
			}
		})
	}
}
