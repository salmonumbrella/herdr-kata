package worker

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

// These are literal wire expectations, independent of the report builder.
func assertUnsupportedObservation(t *testing.T, k *fakeKata, detail string) {
	t.Helper()
	want := kata.ObserveCronRunBody{
		Actor: new("worker"), DefinitionEventUID: new("01J00000000000000000000011"),
		EndedAt: new(at), ExecutorLabel: new("executor"), ExpectedRevision: 0,
		JobUID: new("01J00000000000000000000010"), OccurrenceKey: new("unsupported:01J00000000000000000000011"),
		StartedAt: new(at), Status: "failed", Summary: kata.Summary{Version: 1, Message: new("reason: unsupported\n" + detail)}, Teammate: new("herdr-kata"),
	}
	if len(k.bodies) != 1 {
		t.Fatalf("observations=%d, want 1", len(k.bodies))
	}
	var got kata.ObserveCronRunBody
	if err := json.Unmarshal(k.bodies[0], &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("observation %s; want %+v", k.bodies[0], want)
	}
}

func TestUnsupportedReasonsPreserveWhitespace(t *testing.T) {
	for _, tt := range []struct {
		name, agent, detail string
		emptyCheckout       bool
	}{
		{name: "profile spaces", agent: "missing  profile", detail: "profile not configured: missing  profile"},
		{name: "profile tabs", agent: "missing\tprofile ", detail: "profile not configured: missing\tprofile "},
		{name: "empty checkout", emptyCheckout: true, detail: "checkout not configured: "},
		{name: "line breaks", agent: "missing\r\nprofile\nnext\rlast", detail: "profile not configured: missing profile next last"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, h, k := fixture(t)
			if tt.emptyCheckout {
				k.jobs[0].Definition.CheckoutKey = new("")
			} else {
				k.jobs[0].Definition.Options = map[string]any{"agent": tt.agent}
			}
			tick(t, w, at)
			assertUnsupportedObservation(t, k, tt.detail)
			if h.runs != 0 || k.creates != 0 {
				t.Fatal("unsupported definition launched or created an issue")
			}
		})
	}
}
