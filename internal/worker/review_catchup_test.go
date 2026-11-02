package worker

import (
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestSkipRunsBetweenLiveTicks(t *testing.T) {
	w, h, k := fixture(t)
	k.jobs[0].CreatedAt = at.Add(-24 * time.Hour)
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "cron", Cron: new("0 9 * * *")}
	k.jobs[0].Definition.Catchup = "skip"
	k.jobs[0].Definition.GraceSeconds = new(int64(0))
	tick(t, w, at.Add(-10*time.Second))
	tick(t, w, at.Add(17*time.Second))
	if h.runs != 1 {
		t.Fatalf("on-time occurrence lost between live ticks: launches=%d", h.runs)
	}
}

func TestSkipDropsExpiredCatchupAfterRestart(t *testing.T) {
	w, h, k := fixture(t)
	k.jobs[0].CreatedAt = at.Add(-24 * time.Hour)
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "cron", Cron: new("0 9 * * *")}
	k.jobs[0].Definition.Catchup = "skip"
	k.jobs[0].Definition.GraceSeconds = new(int64(60))
	tick(t, w, at.Add(-time.Hour))
	restarted := New(w.Config, w.Store, h, w.Clients, w.Bin)
	tick(t, restarted, at.Add(5*time.Minute))
	if h.runs != 0 {
		t.Fatalf("expired downtime occurrence ran: launches=%d", h.runs)
	}
}
