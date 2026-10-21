//go:build kata

package kata

import (
	"testing"
	"time"
)

func TestIntegRunActivityTimestamps(t *testing.T) {
	c := newKata(t)
	job := fixtureJob(t, c)
	if _, _, err := c.Observe(t.Context(), "01K00000000000000000000000", observation(job)); err != nil {
		t.Fatal(err)
	}
	runs, _, err := c.ListRuns(t.Context(), "", "", 100)
	if err != nil || len(runs) != 1 {
		t.Fatal(runs, err)
	}
	if runs[0].ExecutorLabel != "installation" || runs[0].UpdatedAt.IsZero() || time.Since(runs[0].UpdatedAt) > time.Minute {
		t.Fatal("recent admission activity unavailable", runs[0])
	}
}
