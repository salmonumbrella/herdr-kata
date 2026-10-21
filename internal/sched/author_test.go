package sched

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

// Exercise real list/show decoding before the scheduler consumes provenance.
func TestAutomaticJobsRequireOriginalAuthor(t *testing.T) {
	for _, actor := range []string{"worker", ""} {
		t.Run("actor="+actor, func(t *testing.T) {
			s, k, a, clock := setup(t)
			s.cfg.Kata.Actor = actor
			edit(k, func(j *defs.JobDef) { j.Overlap = "allow" })
			ownID := "01K00000000000000000000001"
			rows := []string{}
			byID := map[string]string{}
			for n, author := range []string{`"worker"`, `"colleague"`, `""`, `null`, `"Worker"`} {
				uid := fmt.Sprintf("01K0000000000000000000000%d", n+1)
				row := fmt.Sprintf(`{"uid":%q,"author":%s,"definition_event_uid":"event1","created_at":"2026-01-01T00:00:00Z","definition":%s}`, uid, author, k.job.Definition)
				rows = append(rows, row)
				byID[uid] = row
			}
			// A missing field must also fail closed.
			missingID := "01K00000000000000000000006"
			byID[missingID] = fmt.Sprintf(`{"uid":%q,"definition_event_uid":"event1","created_at":"2026-01-01T00:00:00Z","definition":%s}`, missingID, k.job.Definition)
			rows = append(rows, byID[missingID])
			cli := kata.New(config.Kata{Bin: "kata", Project: "example-project", Actor: actor}, t.TempDir(), func(_ context.Context, argv, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
				pos := slices.Index(argv, "cron")
				if pos < 0 {
					t.Fatalf("unexpected command %v", argv)
				}
				args := argv[pos:]
				switch strings.Join(args[:3], " ") {
				case "cron job list":
					return []byte(`{"jobs":[` + strings.Join(rows, ",") + `]}`), nil, 0, nil
				case "cron workflow list":
					return []byte(`{"workflows":[]}`), nil, 0, nil
				case "cron job show":
					row, ok := byID[args[3]]
					if !ok {
						t.Fatalf("unexpected job %s", args[3])
					}
					return []byte(`{"job":` + row + `}`), nil, 0, nil
				default:
					t.Fatalf("unexpected command %v", args)
				}
				return nil, nil, 1, nil
			}, nil)
			// Shared reads are deliberately unfiltered.
			visible, err := cli.ListJobs(t.Context())
			if err != nil || len(visible) != 6 {
				t.Fatalf("shared list: %v %v", visible, err)
			}
			shared, err := cli.ShowJob(t.Context(), "01K00000000000000000000002")
			if err != nil || shared.UID != "01K00000000000000000000002" {
				t.Fatalf("shared show: %v %v", shared, err)
			}
			s.k = cli
			if err := s.load(t.Context()); err != nil {
				t.Fatal(err)
			}
			clock.advance(time.Minute)
			tick(t, s)
			var got []string
			for _, due := range a.got {
				got = append(got, due.Job.UID)
			}
			var want []string
			if actor == "worker" {
				want = []string{ownID}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("admitted jobs = %v, want %v", got, want)
			}
		})
	}
}

func TestAutomaticDispatchRechecksAuthor(t *testing.T) {
	for _, author := range []string{"colleague", "", "Worker"} {
		for _, recovered := range []bool{false, true} {
			t.Run(fmt.Sprintf("author=%s/recovered=%t", author, recovered), func(t *testing.T) {
				s, k, a, clock := setup(t)
				a.slots = 0
				clock.advance(time.Minute)
				tick(t, s)
				if len(pending(t, s)) != 1 {
					t.Fatal("expected queued occurrence before revalidation")
				}
				if recovered {
					// Own list response reloads an existing checkpoint; show changes afterward.
					s = New(s.cfg, k, a, s.fsys, s.layout, clock)
					if err := s.load(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				k.job.Author = author
				a.slots = 10
				tick(t, s)
				if len(a.got) != 0 || len(pending(t, s)) != 0 {
					t.Fatalf("ineligible refreshed job admitted or retained: %v", a.got)
				}
				// Even a later eligible read must not replay the dropped occurrence.
				k.job.Author = "worker"
				s = New(s.cfg, k, a, s.fsys, s.layout, clock)
				if err := s.load(t.Context()); err != nil {
					t.Fatal(err)
				}
				tick(t, s)
				if len(a.got) != 0 {
					t.Fatalf("dropped occurrence retried after restart: %v", a.got)
				}
			})
		}
	}
}

func TestAutomaticLoadDropsRecoveredForeignPending(t *testing.T) {
	for _, author := range []string{"colleague", ""} {
		for _, superseded := range []bool{false, true} {
			t.Run(fmt.Sprintf("author=%s/superseded=%t", author, superseded), func(t *testing.T) {
				s, k, a, clock := setup(t)
				a.slots = 0
				clock.advance(3 * time.Minute)
				tick(t, s)
				if len(pending(t, s)) != 3 {
					t.Fatal("expected catch-up backlog")
				}
				k.job.Author = author
				if superseded {
					k.job.DefinitionEventUID = "event2"
				}
				s = New(s.cfg, k, a, s.fsys, s.layout, clock)
				if err := s.load(t.Context()); err != nil {
					t.Fatal(err)
				}
				if len(pending(t, s)) != 0 {
					t.Fatal("ineligible recovered backlog retained")
				}
				a.slots = 10
				tick(t, s)
				if len(a.got) != 0 {
					t.Fatalf("ineligible backlog admitted: %v", a.got)
				}
			})
		}
	}
}

func TestManualSharedJobStillRuns(t *testing.T) {
	s, k, a, clock := setup(t)
	k.job.Author = "colleague"
	if err := s.load(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.RunNow(jobID, "01K00000000000000000000000"); err != nil {
		t.Fatal(err)
	}
	s.drainManual()
	clock.advance(time.Minute)
	tick(t, s)
	if len(a.got) != 1 || a.got[0].Job.UID != jobID || !a.got[0].Manual || a.got[0].Job.Author != "colleague" {
		t.Fatalf("expected exactly one explicit shared admission: %v", a.got)
	}
}

func TestAutomaticOwnJobUsesSharedWorkflow(t *testing.T) {
	s, k, a, clock := setup(t)
	edit(k, func(j *defs.JobDef) { j.Action.Prompt = ""; j.Action.WorkflowUID = jobID })
	k.workflow = kata.Workflow{Author: "colleague", UID: jobID, DefinitionEventUID: "shared-workflow", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"main","kind":"prompt","prompt":"Shared work"}]}`)}
	if err := s.load(t.Context()); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	tick(t, s)
	if len(a.got) != 1 || a.got[0].Workflow == nil || a.got[0].Workflow.DefinitionEventUID != "shared-workflow" {
		t.Fatalf("own job using shared workflow refused: %v", a.got)
	}
}
