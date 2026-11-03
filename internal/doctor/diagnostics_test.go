package doctor

import (
	"bytes"
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/report"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

func ownedJob(uid string) kata.CronJob {
	return kata.CronJob{UID: uid, Definition: kata.JobDefinition{Enabled: true, Action: kata.Action{Kind: "execute", Prompt: new("work")}, Trigger: kata.Trigger{Kind: "manual"}, CheckoutKey: new("example-project")}}
}

func TestDoctorOtherExecutorsPerOwnedJob(t *testing.T) {
	for _, tt := range []struct {
		name string
		runs []kata.CronRun
		err  error
		want string
	}{
		{name: "own", runs: []kata.CronRun{{CreatedAt: now, ExecutorLabel: new("executor")}}},
		{name: "foreign", runs: []kata.CronRun{{CreatedAt: now, ExecutorLabel: new("other")}}, want: "warning: another executor reported job job: other"},
		{name: "absent label", runs: []kata.CronRun{{CreatedAt: now}}, want: "warning: another executor reported job job: unknown"},
		{name: "empty label", runs: []kata.CronRun{{CreatedAt: now, ExecutorLabel: new("")}}, want: "warning: another executor reported job job: unknown"},
		{name: "unsupported", runs: []kata.CronRun{{CreatedAt: now, ExecutorLabel: new("other"), Summary: kata.Summary{Message: new("reason: unsupported\nmissing")}}}},
		{name: "old", runs: []kata.CronRun{{CreatedAt: now.Add(-7*24*time.Hour - time.Nanosecond), ExecutorLabel: new("other")}}},
		{name: "boundary", runs: []kata.CronRun{{CreatedAt: now.Add(-7 * 24 * time.Hour), ExecutorLabel: new("other")}}, want: "warning: another executor reported job job: other"},
		{name: "list failure", err: errors.New("offline"), want: "job job: executor check incomplete: offline"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, l, h := fixture(t)
			var out bytes.Buffer
			requested := []string{}
			j := ownedJob("job")
			foreign := ownedJob("foreign")
			foreign.Definition.Options = map[string]any{"executor": "other"}
			k := fakeKata{jobs: []kata.CronJob{j, foreign}, runs: map[string][]kata.CronRun{"job": tt.runs}, runErr: tt.err, requested: &requested}
			_ = Check(clock.WithNow(t.Context(), now), c, l, h, map[string]kata.Kata{"example-project": k}, &out)
			if len(requested) != 1 || requested[0] != "job" {
				t.Fatal(requested)
			}
			if tt.want != "" {
				if !strings.Contains(out.String(), tt.want) {
					t.Fatal(out.String())
				}
			} else if strings.Contains(out.String(), "another executor") || strings.Contains(out.String(), "incomplete") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestDoctorExecutorSampleTruncation(t *testing.T) {
	for _, recent := range []bool{true, false} {
		c, l, h := fixture(t)
		runs := make([]kata.CronRun, 100)
		for i := range runs {
			runs[i] = kata.CronRun{CreatedAt: now.Add(-time.Duration(i) * time.Minute), ExecutorLabel: new("executor")}
		}
		if !recent {
			runs[99].CreatedAt = now.Add(-8 * 24 * time.Hour)
		}
		var out bytes.Buffer
		_ = Check(clock.WithNow(t.Context(), now), c, l, h, map[string]kata.Kata{"example-project": fakeKata{jobs: []kata.CronJob{ownedJob("job")}, runs: map[string][]kata.CronRun{"job": runs}}}, &out)
		if strings.Contains(out.String(), "job job: executor sample truncated") != recent {
			t.Fatal(out.String())
		}
	}
}

func TestDoctorReadinessAndHeldCapacity(t *testing.T) {
	for _, gate := range []bool{true, false} {
		c, l, h := fixture(t)
		j := ownedJob("job")
		calls := 0
		if gate {
			j.Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: new("issue")}
		}
		st := journal.New(l)
		for _, id := range []string{"01J00000000000000000000001", "01J00000000000000000000002"} {
			if err := st.Create(journal.Run{Version: 1, Key: journal.ManualKey("executor", "example-project", id), RunUID: id, Project: "example-project", Phase: "held"}); err != nil {
				t.Fatal(err)
			}
		}
		var out bytes.Buffer
		_ = Check(t.Context(), c, l, h, map[string]kata.Kata{"example-project": fakeKata{jobs: []kata.CronJob{j}, readyCalls: &calls}}, &out)
		if strings.Contains(out.String(), "gated job: existing issue issue not ready") != gate || calls != map[bool]int{true: 1, false: 0}[gate] || !strings.Contains(out.String(), "project example-project: held runs hold capacity:") || !strings.Contains(out.String(), "01J00000000000000000000001") || !strings.Contains(out.String(), "01J00000000000000000000002") {
			t.Fatal(out.String(), calls)
		}
	}
}

func TestDoctorOutboxIncludesUnenrolledAndRefused(t *testing.T) {
	c, l, h := fixture(t)
	box := report.Outbox{Layout: l}
	for _, project := range []string{"example-project", "unenrolled"} {
		if err := box.Discover(defs.Plan{JobUID: project, JobEventUID: "event"}, project, "worker", "executor", []string{"missing"}, now); err != nil {
			t.Fatal(err)
		}
	}
	records, err := box.All()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		r.Error = "offline"
		if r.Project == "example-project" {
			r.State = "refused"
			r.Error = "invalid reference"
		}
		if err := state.WriteJSON(filepath.Join(l.Unsupported(), r.Cause+".json"), r); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	_ = Check(t.Context(), c, l, h, map[string]kata.Kata{"example-project": fakeKata{}}, &out)
	for _, want := range []string{"unsupported reports: pending=1 refused=1", "unsupported unenrolled: report pending: offline", "unsupported example-project: report refused: invalid reference", "fix the cause, then delete", "new run UID"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
}
