package sched

import (
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"testing"
	"time"
)

const jobID = "01M4TADGDFRMAC38MX3NZCW1K5"

func TestFirstSightAnchor(t *testing.T) {
	at := instant("2026-01-01T00:00:00Z")
	c, e := LoadCheckpoint(state.OSFS{}, state.Layout{Root: t.TempDir()}, jobID, at)
	if e != nil || !c.HandledThrough.Equal(at) {
		t.Fatal(c, e)
	}
}
func catchup(t *testing.T, policy string, age time.Duration, grace int64) ([]string, time.Time, DropInfo) {
	t.Helper()
	now := instant("2026-01-10T00:00:00Z")
	s, _ := Compile(defs.Trigger{Kind: "interval", IntervalSeconds: 60}, now.Add(-age))
	c := Checkpoint{HandledThrough: now.Add(-age)}
	return Plan(c, s, defs.JobDef{Catchup: policy, GraceSeconds: grace}, now)
}
func TestSkipGrace(t *testing.T) {
	q, _, _ := catchup(t, "skip", 2*time.Minute, 0)
	if len(q) != 1 {
		t.Fatal(q)
	}
	now := instant("2026-01-01T00:00:31Z")
	s, _ := Compile(defs.Trigger{Kind: "interval", IntervalSeconds: 60}, now.Add(-91*time.Second))
	q, _, _ = Plan(Checkpoint{HandledThrough: now.Add(-91 * time.Second)}, s, defs.JobDef{Catchup: "skip", GraceSeconds: 30}, now)
	if len(q) != 0 {
		t.Fatal(q)
	}
}
func TestLatest(t *testing.T) {
	q, through, _ := catchup(t, "latest", 10*time.Minute, 0)
	if len(q) != 1 || q[0] != "2026-01-10T00:00:00Z" || !through.Equal(instant(q[0])) {
		t.Fatal(q, through)
	}
}
func TestAllCapped100(t *testing.T) {
	q, _, d := catchup(t, "all", 200*time.Minute, 0)
	if len(q) != 100 || q[0] != "2026-01-09T22:21:00Z" || !d.Dropped {
		t.Fatal(q, d)
	}
}
func TestSevenDayDrop(t *testing.T) {
	q, _, d := catchup(t, "all", 10*24*time.Hour, 0)
	if len(q) != 100 || !d.Dropped || d.From.IsZero() {
		t.Fatal(len(q), d)
	}
}
func TestPendingCapIncludesNormalFires(t *testing.T) {
	now := instant("2026-01-01T01:40:00Z")
	s, _ := Compile(defs.Trigger{Kind: "interval", IntervalSeconds: 60}, now.Add(-100*time.Minute))
	c := Checkpoint{HandledThrough: now.Add(-time.Minute)}
	for i := 100; i > 0; i-- {
		c.Pending = append(c.Pending, now.Add(-time.Duration(i)*time.Minute).Format(time.RFC3339Nano))
	}
	q, _, d := Plan(c, s, defs.JobDef{Catchup: "all"}, now)
	if len(q) != 100 || q[0] != "2026-01-01T00:01:00Z" || !d.Dropped {
		t.Fatal(len(q), q[0], d)
	}
}
func TestSupersedeMonotonic(t *testing.T) {
	at := instant("2026-01-01T00:00:00Z")
	c := Checkpoint{DefinitionEventUID: "old", HandledThrough: at, Pending: []string{at.Format(time.RFC3339Nano)}}
	if dropped := c.Supersede("new", at.Add(-time.Hour)); len(dropped) != 1 || c.DefinitionEventUID != "new" || !c.HandledThrough.Equal(at) || len(c.Pending) != 0 {
		t.Fatal(c, dropped)
	}
}

type failSync struct{ state.OSFS }

func (failSync) SyncDir(string) error { return errors.New("sync fault") }
func TestCheckpointSaveDurable(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	at := instant("2026-01-01T00:00:00Z")
	c, _ := LoadCheckpoint(state.OSFS{}, l, jobID, at)
	c.Pending = []string{at.Format(time.RFC3339Nano)}
	if e := c.Save(state.OSFS{}, l); e != nil {
		t.Fatal(e)
	}
	loaded, e := LoadCheckpoint(state.OSFS{}, l, jobID, at.Add(time.Hour))
	if e != nil || len(loaded.Pending) != 1 || !loaded.HandledThrough.Equal(at) {
		t.Fatal(loaded, e)
	}
	if e := c.Save(failSync{}, l); e == nil {
		t.Fatal("ignored sync fault")
	}
}
func TestPolicyDropExcludesRetainedOccurrence(t *testing.T) {
	q, _, d := catchup(t, "latest", 2*time.Minute, 0)
	retained := instant(q[0])
	if !d.Dropped || !d.To.Before(retained) {
		t.Fatal(q, d)
	}
}
func TestDropInfoBoundsAllRanges(t *testing.T) {
	now := instant("2026-01-01T00:03:31Z")
	s, _ := Compile(defs.Trigger{Kind: "interval", IntervalSeconds: 60}, instant("2026-01-01T00:00:00Z"))
	q, _, d := Plan(Checkpoint{HandledThrough: instant("2026-01-01T00:00:00Z")}, s, defs.JobDef{Catchup: "skip", GraceSeconds: 30}, now)
	if len(q) != 0 || len(d.Ranges) != 2 || !d.From.Equal(instant("2026-01-01T00:01:00Z")) || !d.To.Equal(instant("2026-01-01T00:03:00Z")) {
		t.Fatal(q, d)
	}
}
func TestSupersedeUsesWallClock(t *testing.T) {
	base := time.Now()
	c := Checkpoint{HandledThrough: base}
	c.Supersede("new", wallJump(base, 3*time.Minute))
	if !c.HandledThrough.Equal(base.Add(3 * time.Minute).UTC()) {
		t.Fatal("wall-clock cutover was ignored", c.HandledThrough)
	}
}
