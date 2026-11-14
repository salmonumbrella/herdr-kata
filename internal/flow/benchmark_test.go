package flow

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
)

func BenchmarkNextBlockedChain(b *testing.B) {
	for _, n := range []int{32, 128, 512} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			p := defs.Plan{}
			for i := n - 1; i >= 0; i-- {
				step := defs.Step{Key: fmt.Sprint(i)}
				if i > 0 {
					step.After = []string{fmt.Sprint(i - 1)}
				}
				p.Steps = append(p.Steps, step)
			}
			states := map[string]StepState{"0": {Status: "failed", Attempts: 1}}
			b.ReportAllocs()
			for b.Loop() {
				if got := Next(p, states); !reflect.DeepEqual(got, Finish{"failed"}) {
					b.Fatal(got)
				}
			}
		})
	}
}

func BenchmarkNextWaiting(b *testing.B) {
	p := defs.Plan{Steps: []defs.Step{{Key: "main"}}}
	states := map[string]StepState{"main": {Status: "running", Attempts: 1}}
	b.ReportAllocs()
	for b.Loop() {
		if _, ok := Next(p, states).(Wait); !ok {
			b.Fatal("running workflow did not wait")
		}
	}
}
