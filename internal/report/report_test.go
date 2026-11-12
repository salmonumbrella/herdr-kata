package report

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"hegel.dev/go/hegel"
)

type fakeKata struct {
	kata.Kata
	notifications                    []notification
	createRequests                   []createRequest
	created                          map[string]createRequest
	events                           []string
	onNotify                         func()
	keys, parents                    []string
	bodies                           [][]byte
	createErr, observeErr, notifyErr error
	observed                         map[string][]byte
	issues                           map[string]string
	deadlines, notifies, shows       int
	loseCreateReply                  bool
	omitCreateRef                    bool
}

type notification struct{ ref, to, message string }
type createRequest struct{ title, body, key, parent string }

func (f *fakeKata) CreateAlertCarrier(title, body, key string) (string, error) {
	ref, _, err := f.CreateIssue(title, body, key, "")
	return ref, err
}

func (f *fakeKata) CreateIssue(title, body, key, parent string) (string, string, error) {
	request := createRequest{title, body, key, parent}
	f.createRequests = append(f.createRequests, request)
	f.keys = append(f.keys, key)
	f.parents = append(f.parents, parent)
	if f.createErr != nil {
		return "", "", f.createErr
	}
	if f.issues == nil {
		f.issues = make(map[string]string)
		f.created = make(map[string]createRequest)
	}
	if previous, ok := f.created[key]; ok && previous != request {
		return "", "", &kata.Error{Code: 5, Message: "idempotency_mismatch"}
	}
	f.created[key] = request
	uid := f.issues[key]
	if uid == "" {
		uid = "01J00000000000000000000020"
		f.issues[key] = uid
	}
	if f.loseCreateReply {
		f.loseCreateReply = false
		return "", "", &kata.Error{Code: 1}
	}
	if f.omitCreateRef {
		return "", uid, nil
	}
	return "abcd", uid, nil
}
func (f *fakeKata) SetDeadline(string, time.Time) error { f.deadlines++; return nil }
func (f *fakeKata) Notify(ref, to, message string) error {
	f.notifies++
	f.notifications = append(f.notifications, notification{ref, to, message})
	f.events = append(f.events, "notify")
	if f.onNotify != nil {
		f.onNotify()
	}
	return f.notifyErr
}
func (f *fakeKata) Observe(uid string, b []byte, rev int) (int, bool, error) {
	f.events = append(f.events, "observe")
	f.bodies = append(f.bodies, bytes.Clone(b))
	if f.observed == nil {
		f.observed = make(map[string][]byte)
	}
	if f.observeErr != nil {
		return 0, false, f.observeErr
	}
	f.observed[uid] = bytes.Clone(b)
	return rev + 1, false, nil
}
func (f *fakeKata) ShowRun(uid string) (int, []byte, error) {
	f.shows++
	if len(f.observed[uid]) == 0 {
		return 0, nil, &kata.Error{Code: 7}
	}
	return 1, f.observed[uid], nil
}

var at = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

func setup(t *testing.T) (context.Context, *journal.Store, *fakeKata, journal.Run) {
	t.Helper()
	st := journal.New(state.Layout{Root: t.TempDir()})
	r := journal.Run{Version: 1, Key: journal.ManualKey("executor", "example-project", "request"), RunUID: "01J00000000000000000000001", Project: "example-project", Actor: "worker", Executor: "executor", Phase: "prepared", JobUID: "01J00000000000000000000010", StartedAt: new(at), Plan: defs.Plan{JobUID: "01J00000000000000000000010", JobEventUID: "01J00000000000000000000011", Issue: defs.IssuePolicy{Kind: "per-run", Title: "Example run", Body: "Example body"}}}
	if err := st.Create(r); err != nil {
		t.Fatal(err)
	}
	return WithNow(context.Background(), at), st, &fakeKata{}, r
}
func body(t *testing.T, b []byte) kata.ObserveCronRunBody {
	t.Helper()
	var got kata.ObserveCronRunBody
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	return got
}
func TestAdmissionBodyFrozenAfterIssueUID(t *testing.T) {
	ctx, st, k, r := setup(t)
	// Simulate the accepted create followed by a lost response, before UID save.
	k.loseCreateReply = true
	if err := Admit(ctx, k, st, r.Key); err == nil {
		t.Fatal("lost reply should defer")
	}
	saved, _ := st.Load(r.Key)
	if len(saved.AdmissionBody) != 0 || len(k.bodies) != 0 {
		t.Fatal("body frozen before UID")
	}
	if err := Admit(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	saved, _ = st.Load(r.Key)
	obs := body(t, k.bodies[0])
	if saved.IssueUID != "01J00000000000000000000020" || obs.IssueUID == nil || *obs.IssueUID != saved.IssueUID || !bytes.Equal(saved.AdmissionBody, k.bodies[0]) || saved.Phase != "running" {
		t.Fatalf("admission %+v %s", saved, k.bodies[0])
	}
	if len(k.keys) != 2 || k.keys[0] != k.keys[1] {
		t.Fatal(k.keys)
	}
}
func TestAdmitCreatesUnderRunParent(t *testing.T) {
	ctx, st, k, r := setup(t)
	if err := st.Update(r.Key, func(r *journal.Run) error { r.Plan.Issue.Parent = "01J00000000000000000000030"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := Admit(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	if len(k.parents) != 1 || k.parents[0] != "01J00000000000000000000030" {
		t.Fatal(k.parents)
	}
}
func TestAdmitUncertainRetriesSameKey(t *testing.T) {
	ctx, st, k, r := setup(t)
	k.createErr = &kata.Error{Code: 1}
	_ = Admit(ctx, k, st, r.Key)
	k.createErr = nil
	if err := Admit(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	if len(k.keys) != 2 || k.keys[0] != k.keys[1] {
		t.Fatal(k.keys)
	}
}
func TestAdmissionExpiredAfter6Days(t *testing.T) {
	for _, tt := range []struct {
		age     time.Duration
		known   bool
		expired bool
	}{{6*24*time.Hour - time.Second, false, false}, {6 * 24 * time.Hour, false, true}, {7 * 24 * time.Hour, true, false}} {
		ctx, st, k, r := setup(t)
		ctx = WithNow(ctx, at.Add(tt.age))
		if tt.known {
			_ = st.Update(r.Key, func(r *journal.Run) error { r.IssueUID = "01J00000000000000000000020"; r.IssueRef = "abcd"; return nil })
		}
		if err := Admit(ctx, k, st, r.Key); err != nil {
			t.Fatal(err)
		}
		got, _ := st.Load(r.Key)
		if tt.expired {
			if got.Hold != "admission_expired" || got.Phase != "held" || len(k.keys) != 0 {
				t.Fatal(got, k.keys)
			}
		} else if got.Phase != "running" {
			t.Fatal(got)
		}
	}
}
func TestAdmitDefinitiveHoldsReportRefused(t *testing.T) {
	ctx, st, k, r := setup(t)
	k.createErr = &kata.Error{Code: 3, Message: "refused"}
	_ = Admit(ctx, k, st, r.Key)
	got, _ := st.Load(r.Key)
	if got.Phase != "held" || got.Hold != "report_refused" || !strings.Contains(got.Detail, "refused") {
		t.Fatal(got)
	}
}
func TestObserveBytesStableAcrossRetries(t *testing.T) {
	ctx, st, k, r := setup(t)
	k.observeErr = &kata.Error{Code: 1}
	_ = Admit(ctx, k, st, r.Key)
	k.observeErr = nil
	if err := Admit(WithNow(ctx, at.Add(time.Hour)), k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	if len(k.bodies) != 2 || !bytes.Equal(k.bodies[0], k.bodies[1]) || len(k.keys) != 1 {
		t.Fatal(k.bodies, k.keys)
	}
}
func TestObserveConflictReadsBack(t *testing.T) {
	ctx, st, k, r := setup(t)
	k.observeErr = &kata.Error{Code: 5}
	k.observed = make(map[string][]byte)
	// First attempt freezes the canonical bytes; a confirmed-but-lost response is read back.
	_ = Admit(ctx, k, st, r.Key)
	saved, _ := st.Load(r.Key)
	k.observed[r.RunUID] = saved.AdmissionBody
	if err := Admit(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	saved, _ = st.Load(r.Key)
	if k.shows < 1 || saved.Phase != "running" {
		t.Fatal(saved, k.shows)
	}
}
func TestNotifyUncertainNotRetried(t *testing.T) {
	ctx, st, k, r := setup(t)
	_ = Admit(ctx, k, st, r.Key)
	k.notifyErr = &kata.Error{Code: 1}
	_ = NotifyOnce(ctx, k, st, r.Key, "blocked:main:1:1", "blocked")
	_ = NotifyOnce(ctx, k, st, r.Key, "blocked:main:1:1", "blocked")
	if k.notifies != 1 {
		t.Fatal(k.notifies)
	}
}

func TestNotifyUnknownMutationExitSevenNotRetried(t *testing.T) {
	ctx, st, k, r := setup(t)
	if err := Admit(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	k.notifyErr = &kata.Error{Code: 7, Message: "daemon response lost; mutation result may be unknown"}
	_ = NotifyOnce(ctx, k, st, r.Key, "blocked:main:1:1", "blocked")
	_ = NotifyOnce(ctx, k, journal.New(st.Layout), r.Key, "blocked:main:1:1", "blocked")
	if k.notifies != 1 {
		t.Fatalf("uncertain exit 7 notification sent %d times", k.notifies)
	}
}
func TestExistingIssueGetsNoWrites(t *testing.T) {
	ctx, st, k, r := setup(t)
	_ = st.Update(r.Key, func(r *journal.Run) error {
		r.Plan.Issue = defs.IssuePolicy{Kind: "existing", UID: "01J00000000000000000000020"}
		return nil
	})
	if err := Admit(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	_ = st.Update(r.Key, func(r *journal.Run) error { r.Phase = "held"; r.Hold = "no_result"; return nil })
	if err := Hold(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	if len(k.keys) != 1 || k.deadlines != 0 || !slices.Equal(k.notifications, []notification{{"abcd", "worker", "held: no_result; hc "}}) || body(t, k.bodies[0]).IssueUID == nil || *body(t, k.bodies[1]).IssueUID != "01J00000000000000000000020" {
		t.Fatal(k)
	}
}
func TestTimesSent(t *testing.T) {
	ctx, st, k, r := setup(t)
	r.Plan.Issue.DeadlineOffsetSeconds = new(int64(60))
	_ = st.Update(r.Key, func(saved *journal.Run) error { saved.Plan = r.Plan; return nil })
	if err := Admit(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	ended := at.Add(time.Minute)
	_ = st.Update(r.Key, func(r *journal.Run) error {
		r.Phase = "settled"
		r.Outcome = "succeeded"
		r.EndedAt = &ended
		return nil
	})
	if err := Final(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	if err := Final(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	first, last := body(t, k.bodies[0]), body(t, k.bodies[1])
	if first.StartedAt == nil || !first.StartedAt.Equal(at) || last.EndedAt == nil || !last.EndedAt.Equal(ended) || k.deadlines != 1 || len(k.bodies) != 2 {
		t.Fatal(k.bodies, k.deadlines)
	}
}
func TestHoldReporting(t *testing.T) {
	ctx, st, k, r := setup(t)
	_ = Admit(ctx, k, st, r.Key)
	_ = st.Update(r.Key, func(r *journal.Run) error { r.Phase = "held"; r.Hold = "agent_lost"; return nil })
	if err := Hold(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	if err := Hold(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	last := body(t, k.bodies[1])
	if last.Status != "unknown" || last.Summary.Message == nil || !strings.Contains(*last.Summary.Message, "reason: agent_lost") || k.notifies != 1 || len(k.bodies) != 2 {
		t.Fatal(k.bodies, k.notifies)
	}
}

func TestNotifyRefusalKeepsObservedHoldReason(t *testing.T) {
	ctx, st, k, r := setup(t)
	if err := Admit(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(r.Key, func(r *journal.Run) error { r.Phase = "held"; r.Hold = "agent_lost"; return nil }); err != nil {
		t.Fatal(err)
	}
	k.notifyErr = &kata.Error{Code: 3, Message: "notification refused"}
	_ = Hold(ctx, k, st, r.Key)
	got, err := st.Load(r.Key)
	if err != nil || got.Hold != "agent_lost" || !got.ReportDone {
		t.Fatalf("observed hold overwritten: %s %v", got.Hold, err)
	}
}

func TestReasonAloneOnFirstLine(t *testing.T) {
	for _, phase := range []string{"held", "settled"} {
		t.Run(phase, func(t *testing.T) {
			ctx, st, k, r := setup(t)
			if err := Admit(ctx, k, st, r.Key); err != nil {
				t.Fatal(err)
			}
			if err := st.Update(r.Key, func(r *journal.Run) error {
				r.Phase = phase
				r.Outcome = "failed"
				r.Hold = "timeout"
				r.Detail = "Fixture detail"
				r.Attempts = []journal.Attempt{{Step: "main", Summary: "Fixture summary", Note: "Fixture note"}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var err error
			want := "reason: failed"
			if phase == "held" {
				err = Hold(ctx, k, st, r.Key)
				want = "reason: timeout"
			} else {
				err = Final(ctx, k, st, r.Key)
			}
			if err != nil {
				t.Fatal(err)
			}
			msg := *body(t, k.bodies[len(k.bodies)-1]).Summary.Message
			lines := strings.SplitN(msg, "\n", 2)
			if len(lines) != 2 || lines[0] != want || !strings.Contains(lines[1], "Fixture detail\nmain: Fixture summary\nFixture note") {
				t.Fatalf("reason/details format: %q", msg)
			}
		})
	}
}
func TestUnlaunchedCancellationReason(t *testing.T) {
	for _, tc := range []struct{ hold, want string }{
		{"invalid_inputs", "reason: invalid_inputs"},
		{"inputs_unavailable", "reason: inputs_unavailable"},
		{"timeout", "reason: cancelled"},
	} {
		t.Run(tc.hold, func(t *testing.T) {
			ctx, st, k, r := setup(t)
			if err := st.Update(r.Key, func(r *journal.Run) error {
				r.Phase, r.Outcome, r.Hold, r.Detail = "settled", "cancelled", tc.hold, "Fixture detail"
				r.Attempts = nil
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := Final(ctx, k, st, r.Key); err != nil {
				t.Fatal(err)
			}
			msg := *body(t, k.bodies[len(k.bodies)-1]).Summary.Message
			if msg != tc.want+"\nFixture detail" {
				t.Fatalf("message: %q", msg)
			}
		})
	}
}
func TestAdmitSecondOccurrenceCreates(t *testing.T) {
	ctx, st, _, r := setup(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "kata")
	log := filepath.Join(dir, "calls")
	t.Setenv("HK_REPORT_LOG", log)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$HK_REPORT_LOG"
create=false; force=false
for arg in "$@"; do [ "$arg" = create ] && create=true; [ "$arg" = --force-new ] && force=true; done
if $create; then
 if ! $force; then printf '%s\n' '{"error":{"code":"duplicate_candidates"}}' >&2; exit 5; fi
 printf '%s\n' '{"issue":{"short_id":"abcd","uid":"01J00000000000000000000020"}}'
else cat >/dev/null; printf '%s\n' '{"run":{"revision":1},"replayed":false}'; fi
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	k := kata.New(bin, "fixture", nil, "example-project", "worker")
	if err := Admit(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	r.Key = journal.ManualKey("executor", r.Project, "second")
	r.RunUID = "01J00000000000000000000002"
	if err := st.Create(r); err != nil {
		t.Fatal(err)
	}
	if err := Admit(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), "--force-new") != 2 || !strings.Contains(string(b), "cron-worker:01J00000000000000000000002:issue") {
		t.Fatal(string(b))
	}
}
func TestNotifyUnavailableCanRetry(t *testing.T) {
	ctx, st, k, r := setup(t)
	_ = Admit(ctx, k, st, r.Key)
	k.notifyErr = &kata.Error{Code: 7}
	_ = NotifyOnce(ctx, k, st, r.Key, "notice", "blocked")
	k.notifyErr = nil
	if err := NotifyOnce(ctx, k, st, r.Key, "notice", "blocked"); err != nil {
		t.Fatal(err)
	}
	if k.notifies != 2 {
		t.Fatal(k.notifies)
	}
}
func TestFinalObservationStableSummary(t *testing.T) {
	ctx, st, k, r := setup(t)
	_ = Admit(ctx, k, st, r.Key)
	hegel.Test(t, func(ht *hegel.T) {
		note := hegel.Draw(ht, hegel.Text())
		if err := st.Update(r.Key, func(r *journal.Run) error {
			r.ReportBody = nil
			r.ReportDone = false
			r.Phase = "settled"
			r.Outcome = "failed"
			r.Detail = note
			r.Attempts = []journal.Attempt{{Step: "main", N: 1, Status: "failed", Summary: note}}
			return nil
		}); err != nil {
			ht.Fatal(err)
		}
		k.observeErr = &kata.Error{Code: 1}
		_ = Final(ctx, k, st, r.Key)
		first := bytes.Clone(k.bodies[len(k.bodies)-1])
		// Later file or journal changes cannot rewrite accepted report evidence.
		if err := st.Update(r.Key, func(r *journal.Run) error { r.Attempts[0].Summary = "Changed after publication"; return nil }); err != nil {
			ht.Fatal(err)
		}
		_ = Final(ctx, k, st, r.Key)
		second := k.bodies[len(k.bodies)-1]
		if !bytes.Equal(first, second) || body(t, second).Status != "failed" {
			ht.Fatal("final bytes changed")
		}
	})
}

func TestFinalHandoffReason(t *testing.T) {
	ctx, st, k, r := setup(t)
	if err := Admit(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	_ = st.Update(r.Key, func(r *journal.Run) error {
		r.Phase = "settled"
		r.Outcome = "unknown"
		r.Attempts = []journal.Attempt{{Step: "main", N: 1, Status: "needs-human"}}
		return nil
	})
	if err := Final(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	last := body(t, k.bodies[len(k.bodies)-1])
	if last.Status != "unknown" || last.Summary.Message == nil || !strings.Contains(*last.Summary.Message, "reason: needs-human") {
		t.Fatalf("handoff summary %+v", last)
	}
}
