package flow

import (
	"reflect"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"hegel.dev/go/hegel"
)

func TestWorkflowOrder(t *testing.T) {
	p := defs.Plan{Steps: []defs.Step{{Key: "a", Retries: 1}, {Key: "b", After: []string{"a"}}, {Key: "c"}, {Key: "d", After: []string{"b"}}}}
	for _, tt := range []struct {
		name   string
		states map[string]StepState
		want   Decision
	}{
		{"definition ties", nil, Start{"a", 1}},
		{"linear success", map[string]StepState{"a": {"succeeded", 1}}, Start{"b", 1}},
		{"failure independent", map[string]StepState{"a": {"failed", 2}}, Start{"c", 1}},
		{"retry certain failure", map[string]StepState{"a": {"failed", 1}}, Start{"a", 2}},
		{"running waits", map[string]StepState{"a": {"running", 1}}, Wait{}},
		{"held holds", map[string]StepState{"a": {"held", 1}}, Hold{}},
		{"handoff ends run", map[string]StepState{"a": {"needs-human", 1}}, Finish{"unknown"}},
		{"closed gate skips chain", map[string]StepState{"a": {"skipped", 1}, "c": {"succeeded", 1}}, Finish{"succeeded"}},
		{"failed chain ends failed", map[string]StepState{"a": {"failed", 2}, "c": {"succeeded", 1}}, Finish{"failed"}},
		{"all succeeded", map[string]StepState{"a": {"succeeded", 1}, "b": {"succeeded", 1}, "c": {"succeeded", 1}, "d": {"succeeded", 1}}, Finish{"succeeded"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Next(p, tt.states)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("decision %+v != %+v", got, tt.want)
			}
		})
	}
}
func TestRetryOnlyFailed(t *testing.T) {
	for _, status := range []string{"held", "unknown", "needs-human", "succeeded", "skipped"} {
		got := Next(defs.Plan{Steps: []defs.Step{{Key: "a", Retries: 10}}}, map[string]StepState{"a": {status, 1}})
		if _, ok := got.(Start); ok {
			t.Fatalf("retried %s", status)
		}
	}
}
func TestRetryBudget(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		retries := hegel.Draw(ht, hegel.Integers(0, 1<<31-1))
		attempts := hegel.Draw(ht, hegel.Integers(1, 1<<31-1))
		got := Next(defs.Plan{Steps: []defs.Step{{Key: "a", Retries: retries}}}, map[string]StepState{"a": {"failed", attempts}})
		if attempts <= retries {
			if !reflect.DeepEqual(got, Start{"a", attempts + 1}) {
				ht.Fatal(got)
			}
		} else if !reflect.DeepEqual(got, Finish{"failed"}) {
			ht.Fatal(got)
		}
	})
}
