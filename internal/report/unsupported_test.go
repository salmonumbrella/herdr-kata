package report

import (
	"bytes"
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func outboxFixture(t *testing.T) (Outbox, defs.Plan, *fakeKata) {
	t.Helper()
	return Outbox{Layout: state.Layout{Root: t.TempDir()}}, defs.Plan{JobUID: "job", JobEventUID: "event", WorkflowUID: "wf", WorkflowEventUID: "wf-event"}, &fakeKata{}
}
func records(t *testing.T, o Outbox) []UnsupportedRecord {
	t.Helper()
	r, err := o.All()
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func discover(t *testing.T, o Outbox, p defs.Plan, reasons []string) {
	t.Helper()
	if err := o.Discover(p, "example-project", "worker", "executor", reasons, at); err != nil {
		t.Fatal(err)
	}
}
func deliver(t *testing.T, o Outbox, k kata.Kata, now time.Time) error {
	t.Helper()
	return o.Deliver(map[string]kata.Kata{"example-project": k}, now)
}

// Compare every wire field; missing version, teammate, references or frozen times breaks delivery.
func TestUnsupportedFrozenObservationAndDedup(t *testing.T) {
	o, p, k := outboxFixture(t)
	discover(t, o, p, []string{"profile not configured: missing", "commands disabled for project"})
	r := records(t, o)[0]
	if r.State != "pending" || len(k.bodies) != 0 {
		t.Fatal(r)
	}
	want := kata.ObserveCronRunBody{Actor: new("worker"), DefinitionEventUID: new("event"), EndedAt: new(at), ExecutorLabel: new("executor"), JobUID: new("job"), OccurrenceKey: new("unsupported:event"), StartedAt: new(at), Status: "failed", Summary: kata.Summary{Version: 1, Message: new("reason: unsupported\nprofile not configured: missing, commands disabled for project")}, Teammate: new("herdr-kata"), WorkflowDefinitionEventUID: new("wf-event"), WorkflowUID: new("wf")}
	if err := deliver(t, o, k, at); err != nil {
		t.Fatal(err)
	}
	if len(k.bodies) != 1 || !reflect.DeepEqual(body(t, k.bodies[0]), want) {
		t.Fatalf("body %s", k.bodies)
	}
	for range 3 {
		o = Outbox{Layout: o.Layout}
		discover(t, o, p, []string{"profile not configured: missing", "commands disabled for project"})
		if err := deliver(t, o, k, at.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if len(k.bodies) != 1 || records(t, o)[0].State != "done" {
		t.Fatal(k.bodies, records(t, o))
	}
	runs, err := journal.New(o.Layout).All()
	if err != nil || len(runs) != 0 || len(k.keys) != 0 {
		t.Fatalf("outbox leaked into runs/issues: %v %v", runs, err)
	}
	// A changed definition, workflow or reason set is a new cause.
	for _, change := range []string{"definition", "workflow", "reasons"} {
		next := p
		reasons := []string{"profile not configured: missing", "commands disabled for project"}
		switch change {
		case "definition":
			next.JobEventUID = "new-event"
		case "workflow":
			next.WorkflowEventUID = "new-wf-event"
		case "reasons":
			reasons = []string{"another reason"}
		}
		discover(t, o, next, reasons)
	}
	if len(records(t, o)) != 4 {
		t.Fatal(records(t, o))
	}
}

func TestUnsupportedRetriesAndRefusals(t *testing.T) {
	for _, mode := range []string{"unavailable", "uncertain", "lost accepted reply", "crash after accept", "same conflict", "different conflict", "validation"} {
		t.Run(mode, func(t *testing.T) {
			o, p, k := outboxFixture(t)
			discover(t, o, p, []string{"missing"})
			r := records(t, o)[0]
			switch mode {
			case "unavailable":
				k.observeErr = &kata.Error{Code: 7}
			case "uncertain":
				k.observeErr = &kata.Error{Code: 1}
			case "lost accepted reply":
				k.observed = map[string][]byte{r.RunUID: bytes.Clone(r.Body)}
				k.observeErr = &kata.Error{Code: 7, Message: "mutation result may be unknown"}
			case "crash after accept":
				k.observed = map[string][]byte{r.RunUID: bytes.Clone(r.Body)}
			case "same conflict":
				k.observeErr = &kata.Error{Code: 5}
				k.observed = map[string][]byte{r.RunUID: bytes.Clone(r.Body)}
			case "different conflict":
				k.observeErr = &kata.Error{Code: 5}
				k.observed = map[string][]byte{r.RunUID: []byte(`{"status":"running"}`)}
			case "validation":
				k.observeErr = &kata.Error{Code: 3}
			}
			// Accept before saving done simulates a crash at the durable boundary.
			if mode == "crash after accept" {
				if _, _, err := k.Observe(r.RunUID, r.Body, 0); err != nil {
					t.Fatal(err)
				}
			}
			_ = deliver(t, o, k, at)
			refused := mode == "different conflict" || mode == "validation"
			if refused {
				if records(t, o)[0].State != "refused" || records(t, o)[0].Error == "" {
					t.Fatal(records(t, o))
				}
			} else if mode == "same conflict" {
				if records(t, o)[0].State != "done" {
					t.Fatal(records(t, o))
				}
			} else if mode != "crash after accept" && records(t, o)[0].State != "pending" {
				t.Fatal(records(t, o))
			}
			calls := len(k.bodies)
			k.observeErr = nil
			o = Outbox{Layout: o.Layout}
			if err := deliver(t, o, k, at.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			for _, b := range k.bodies {
				if !bytes.Equal(b, r.Body) {
					t.Fatalf("changed body %s", b)
				}
			}
			if refused || mode == "same conflict" {
				if len(k.bodies) != calls {
					t.Fatal("retried terminal record")
				}
			} else if len(k.observed) != 1 || records(t, o)[0].State != "done" {
				t.Fatal(k.observed, records(t, o))
			}
		})
	}
}

func TestUnsupportedFairOneDeliveryAndUnenrolledProject(t *testing.T) {
	o, p, k := outboxFixture(t)
	k.observeErr = &kata.Error{Code: 7}
	for _, uid := range []string{"a", "b", "c"} {
		p.JobUID = uid
		discover(t, o, p, []string{"missing"})
	}
	if err := o.Deliver(nil, at); err != nil {
		t.Fatal(err)
	}
	if len(k.bodies) != 0 {
		t.Fatal("delivered unenrolled project")
	}
	for n := range 3 {
		_ = deliver(t, o, k, at.Add(time.Duration(n)*time.Second))
		if len(k.bodies) != n+1 {
			t.Fatalf("calls %d", len(k.bodies))
		}
	}
	seen := map[string]bool{}
	for _, b := range k.bodies {
		seen[*body(t, b).JobUID] = true
	}
	if len(seen) != 3 {
		t.Fatal(seen)
	}
}

func TestUnsupportedMessageAndMissingWorkflow(t *testing.T) {
	o, p, k := outboxFixture(t)
	p.WorkflowUID = ""
	p.WorkflowEventUID = ""
	discover(t, o, p, []string{strings.Repeat("界", 2000) + "\nmore\rtext"})
	if err := deliver(t, o, k, at); err != nil {
		t.Fatal(err)
	}
	b := body(t, k.bodies[0])
	msg := *b.Summary.Message
	if len(msg) > 4096 || !utf8.ValidString(msg) || !strings.HasPrefix(msg, "reason: unsupported\n") || strings.Count(msg, "\n") != 1 || b.WorkflowUID != nil || b.WorkflowDefinitionEventUID != nil {
		t.Fatalf("body %+v", b)
	}
	// Full reasons, not truncated display, determine the cause.
	discover(t, o, p, []string{strings.Repeat("界", 2000) + "different"})
	if len(records(t, o)) != 2 {
		t.Fatal(records(t, o))
	}
}

func TestUnsupportedDiskFailureRetriedBeforeObserve(t *testing.T) {
	o, p, k := outboxFixture(t)
	if err := state.EnsureDir(o.Layout.Root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.Layout.Unsupported(), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := o.Discover(p, "example-project", "worker", "executor", []string{"missing"}, at); err == nil {
		t.Fatal("accepted failed disk write")
	}
	if len(k.bodies) != 0 {
		t.Fatal("observed without intent")
	}
	if err := os.Remove(o.Layout.Unsupported()); err != nil {
		t.Fatal(err)
	}
	discover(t, o, p, []string{"missing"})
	if err := deliver(t, o, k, at); err != nil {
		t.Fatal(err)
	}
	if len(k.bodies) != 1 {
		t.Fatal(k.bodies)
	}
	// Refuse corrupt records rather than treating them as an empty outbox.
	r := records(t, o)[0]
	if err := os.WriteFile(filepath.Join(o.Layout.Unsupported(), r.Cause+".json"), []byte(`{"Version":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := o.All(); err == nil {
		t.Fatal("accepted corrupt outbox")
	}
}

// This boundary models Kata's identical-evidence replay, including a committed
// write whose reply is lost. The consumer must retain its UID, body and revision.
type replayKata struct {
	kata.Kata
	saved     map[string][]byte
	revisions map[string]int
	loseReply bool
	replayed  int
}

func (k *replayKata) Observe(uid string, b []byte, expected int) (int, bool, error) {
	if expected != 0 {
		return 0, false, &kata.Error{Code: 3, Message: "unexpected initial revision"}
	}
	if previous := k.saved[uid]; previous != nil {
		if !bytes.Equal(previous, b) {
			return 0, false, &kata.Error{Code: 5}
		}
		k.replayed++
		return k.revisions[uid], true, nil
	}
	k.saved[uid] = bytes.Clone(b)
	k.revisions[uid] = 1
	if k.loseReply {
		k.loseReply = false
		return 0, false, &kata.Error{Code: 7, Message: "mutation result may be unknown"}
	}
	return 1, false, nil
}
func TestUnsupportedAcceptedWriteReplaysOneRevision(t *testing.T) {
	for _, crash := range []bool{false, true} {
		o, p, _ := outboxFixture(t)
		discover(t, o, p, []string{"missing"})
		r := records(t, o)[0]
		k := &replayKata{saved: map[string][]byte{}, revisions: map[string]int{}, loseReply: !crash}
		if crash {
			// The process dies after Kata accepts and before local delivery completion.
			if _, _, err := k.Observe(r.RunUID, r.Body, 0); err != nil {
				t.Fatal(err)
			}
		} else if err := deliver(t, o, k, at); err == nil {
			t.Fatal("expected a lost reply")
		}
		if records(t, o)[0].State != "pending" {
			t.Fatal("lost pending intent")
		}
		// A repaired current definition supplies no rediscovery; restart delivers saved evidence.
		o = Outbox{Layout: o.Layout}
		if err := deliver(t, o, k, at.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if len(k.saved) != 1 || k.revisions[r.RunUID] != 1 || k.replayed != 1 || !bytes.Equal(k.saved[r.RunUID], r.Body) || records(t, o)[0].State != "done" {
			t.Fatalf("runs=%d revisions=%v replays=%d records=%v", len(k.saved), k.revisions, k.replayed, records(t, o))
		}
	}
}

func TestUnsupportedCorruptRecordDoesNotBlockDelivery(t *testing.T) {
	o, p, k := outboxFixture(t)
	discover(t, o, p, []string{"missing"})
	bad := filepath.Join(o.Layout.Unsupported(), "invalid.json")
	if err := os.WriteFile(bad, []byte(`{"Version":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	malformed := filepath.Join(o.Layout.Unsupported(), "malformed.json")
	if err := os.WriteFile(malformed, []byte("broken JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(o.Layout.Unsupported(), "unreadable.json")
	if err := os.Mkdir(unreadable, 0700); err != nil {
		t.Fatal(err)
	}
	valid, err := o.All()
	for _, path := range []string{bad, malformed, unreadable} {
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("lost file error %s: %v", path, err)
		}
	}
	if len(valid) != 1 || err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("valid records=%d error=%v", len(valid), err)
	}
	before := bytes.Clone(valid[0].Body)
	err = deliver(t, o, k, at)
	if err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("lost invalid-record error: %v", err)
	}
	if len(k.bodies) != 1 || !bytes.Equal(k.bodies[0], before) {
		t.Fatalf("valid report not delivered: %s", k.bodies)
	}
	valid, err = o.All()
	if err == nil || len(valid) != 1 || valid[0].State != "done" {
		t.Fatalf("valid record %+v error %v", valid, err)
	}
	if _, err := os.Stat(bad); err != nil {
		t.Fatalf("invalid record was removed: %v", err)
	}
}

func TestUnsupportedReadableBodyAndLegacyReplay(t *testing.T) {
	o, p, k := outboxFixture(t)
	discover(t, o, p, []string{"profile not configured: <missing> & \t界\u2028"})
	r := records(t, o)[0]
	path := filepath.Join(o.Layout.Unsupported(), r.Cause+".json")
	assertReadable := func() {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var disk struct{ Body json.RawMessage }
		if err := json.Unmarshal(b, &disk); err != nil {
			t.Fatal(err)
		}
		if len(disk.Body) == 0 || disk.Body[0] != '{' || !bytes.Equal(disk.Body, r.Body) {
			t.Fatalf("body is not the original readable JSON: %s", disk.Body)
		}
	}
	assertReadable()
	// Existing Version 1 files stored Body as a base64-encoded []byte.
	legacy := struct {
		Version                int
		Cause, Project, RunUID string
		Body                   []byte
		State, Error           string
		LastAttempt            time.Time
	}{
		Version: 1, Cause: r.Cause, Project: r.Project, RunUID: r.RunUID, Body: bytes.Clone(r.Body), State: "pending",
	}
	if err := state.WriteJSON(path, legacy); err != nil {
		t.Fatal(err)
	}
	loaded := records(t, o)[0]
	if !bytes.Equal(loaded.Body, r.Body) {
		t.Fatal("legacy body bytes changed during read")
	}
	k.observeErr = &kata.Error{Code: 7}
	if err := deliver(t, o, k, at); err == nil {
		t.Fatal("expected unavailable result")
	}
	assertReadable()
	k.observeErr = nil
	if err := deliver(t, Outbox{Layout: o.Layout}, k, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(k.bodies) != 2 || !bytes.Equal(k.bodies[0], r.Body) || !bytes.Equal(k.bodies[1], r.Body) || records(t, o)[0].State != "done" {
		t.Fatal("retry changed frozen evidence")
	}
}
