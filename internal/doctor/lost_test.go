package doctor

import (
	"context"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"os"
	"path/filepath"
	"testing"
)

type pagedRuns struct{ before []string }

func (p *pagedRuns) ListRuns(_ context.Context, job, before string, limit int) ([]kata.Run, string, error) {
	p.before = append(p.before, before)
	var n int
	if before != "" {
		fmt.Sscan(before, &n)
	}
	return []kata.Run{{UID: fmt.Sprint(n), Status: "running", ExecutorLabel: "test", Actor: "actor"}}, fmt.Sprint(n + 1), nil
}
func TestLostRunsPaginatedCursor(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	k := &pagedRuns{}
	lost, e := LostRuns(t.Context(), k, state.OSFS{}, l, "test", "actor")
	if e != nil || len(lost) != 10 {
		t.Fatal(lost, e)
	}
	lost, e = LostRuns(t.Context(), k, state.OSFS{}, l, "test", "actor")
	if e != nil || len(lost) != 10 || len(k.before) != 20 || k.before[10] != "10" {
		t.Fatal(k.before, lost, e)
	}
	if _, e = os.Stat(filepath.Join(l.Root, "doctor-lost.cursor")); e != nil {
		t.Fatal(e)
	}
}
