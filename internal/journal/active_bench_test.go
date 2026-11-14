package journal

import (
	"fmt"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func BenchmarkActiveHistory(b *testing.B) {
	st := New(state.Layout{Root: b.TempDir()})
	for n := range 128 {
		r := sample()
		r.Key = ManualKey("executor", "example-project", fmt.Sprint(n))
		r.Phase = "reported"
		if n == 0 {
			r.Phase = "running"
		}
		if err := st.Create(r); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		runs, err := st.Active()
		if err != nil || len(runs) != 1 || runs[0].Phase != "running" {
			b.Fatalf("active history: %v %v", runs, err)
		}
	}
}
