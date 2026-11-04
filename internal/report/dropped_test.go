package report

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

func TestDropFrozenCheckpointAndReplay(t *testing.T) {
	for _, mode := range []string{"success", "same conflict", "refused", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			o, p, k := outboxFixture(t)
			o.Dir = o.Layout.Dropped()
			key := "2026-11-03T06:00:00Z"
			cp := sched.Checkpoint{Version: 1, JobUID: p.JobUID, EventUID: p.JobEventUID, Through: at, Pending: []sched.Occurrence{{Key: "retained", At: at}}}
			if err := o.Drop(p, "example-project", "worker", "executor", "catchup_missed", "2 occurrences not launched", key, cp, at); err != nil {
				t.Fatal(err)
			}
			rows := records(t, o)
			r := rows[0]
			if len(rows) != 1 || r.State != "pending" || !reflect.DeepEqual(r.Checkpoint, &cp) || len(k.bodies) != 0 {
				t.Fatal(rows, k.bodies)
			}
			want := kata.ObserveCronRunBody{Actor: new("worker"), DefinitionEventUID: new("event"), EndedAt: new(at), ExecutorLabel: new("executor"), JobUID: new("job"), OccurrenceKey: &key, StartedAt: new(at), Status: "cancelled", Summary: kata.Summary{Version: 1, Message: new("reason: catchup_missed\n2 occurrences not launched")}, Teammate: new("herdr-kata"), WorkflowUID: new("wf"), WorkflowDefinitionEventUID: new("wf-event")}
			if !reflect.DeepEqual(body(t, r.Body), want) {
				t.Fatalf("body=%s", r.Body)
			}
			cp.Pending = nil
			if err := o.Drop(p, "example-project", "other", "other", "overlap_skipped", "changed", key, cp, at.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if again := records(t, o)[0]; again.RunUID != r.RunUID || !bytes.Equal(again.Body, r.Body) || len(again.Checkpoint.Pending) != 1 {
				t.Fatal("intent changed", again)
			}
			switch mode {
			case "same conflict":
				k.observeErr = &kata.Error{Code: 5}
				k.observed = map[string][]byte{r.RunUID: bytes.Clone(r.Body)}
			case "refused":
				k.observeErr = &kata.Error{Code: 3}
			case "uncertain":
				k.observeErr = &kata.Error{Code: 7}
			}
			_ = deliver(t, o, k, at)
			state := "done"
			if mode == "refused" {
				state = "refused"
			}
			if mode == "uncertain" {
				state = "pending"
			}
			if got := records(t, o)[0]; got.State != state || got.Checkpoint == nil {
				t.Fatal(got)
			}
			k.observeErr = nil
			if err := deliver(t, o, k, at.Add(20*24*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if len(records(t, o)) != 1 {
				t.Fatal("removed recovery evidence")
			}
			if mode == "refused" && len(k.bodies) != 1 {
				t.Fatal("retried refusal")
			}
			if rows, err := (Outbox{Layout: o.Layout}).All(); err != nil || len(rows) != 0 {
				t.Fatal("mixed outboxes", rows, err)
			}
		})
	}
}
func TestDropWriteFailureAndCorruptSnapshot(t *testing.T) {
	o, p, k := outboxFixture(t)
	o.Dir = o.Layout.Dropped()
	if err := os.WriteFile(o.Dir, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	cp := sched.Checkpoint{Version: 1, JobUID: p.JobUID, EventUID: p.JobEventUID, Through: at}
	if err := o.Drop(p, "example-project", "worker", "executor", "catchup_missed", "detail", "oldest", cp, at); err == nil || len(k.bodies) != 0 {
		t.Fatal(err, k.bodies)
	}
	if err := os.Remove(o.Dir); err != nil {
		t.Fatal(err)
	}
	if err := o.Drop(p, "example-project", "worker", "executor", "catchup_missed", strings.Repeat("界", 2000), "oldest", cp, at); err != nil {
		t.Fatal(err)
	}
	r := records(t, o)[0]
	if len(*body(t, r.Body).Summary.Message) > 4096 {
		t.Fatal("unbounded summary")
	}
	r.Checkpoint.Version = 99
	if err := o.save(r); err != nil {
		t.Fatal(err)
	}
	if _, err := o.All(); err == nil || !strings.Contains(err.Error(), filepath.Join(o.Dir, r.Cause+".json")) {
		t.Fatal(err)
	}
}
