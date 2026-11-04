package attempt

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"hegel.dev/go/hegel"
)

// stopHC scripts the external boundary, never the settlement under test.
type stopHC struct {
	*fakeHC
	readErr, killErr error
	keepLive         bool
	onRead           func()
	onRemove         func() error
}

func (f *stopHC) Read(_ context.Context, target string, lines int) (string, bool, error) {
	if target != "t1" || lines != 200 {
		return "", false, errors.New("wrong screen request")
	}
	if f.onRead != nil {
		f.onRead()
	}
	return "Hooks need review", false, f.readErr
}
func (f *stopHC) Remove(ctx context.Context, target string) error {
	if f.onRemove != nil {
		return f.onRemove()
	}
	return f.fakeHC.Remove(ctx, target)
}
func (f *stopHC) Kill(ctx context.Context, target string) error {
	if f.keepLive {
		f.calls = append(f.calls, "kill:"+target)
	} else if err := f.fakeHC.Kill(ctx, target); err != nil {
		return err
	}
	return f.killErr
}
func freshPrompt(t *testing.T) (*journal.Store, *stopHC, journal.Run) {
	t.Helper()
	st, f, r := setup(t, "prompt")
	if err := st.Update(r.Key, func(r *journal.Run) error {
		r.Attempts[0] = journal.Attempt{Step: "main", N: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h := &stopHC{fakeHC: f}
	if err := Launch(clock.WithNow(context.Background(), now), h, st, r.Key, 0, hc.RunSpec{Argv: []string{"/usr/local/bin/agent"}, Labels: map[string]string{"kata.executor": "executor"}}); err != nil {
		t.Fatal(err)
	}
	return st, h, r
}
func stopSettle(t *testing.T, st *journal.Store, h hc.HC, r journal.Run, at time.Time) Outcome {
	t.Helper()
	for range 6 {
		out, err := Settle(clock.WithNow(context.Background(), at), h, st, r.Key, 0, at)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := out.(Pending); !ok {
			return out
		}
		saved, err := st.Load(r.Key)
		if err != nil {
			t.Fatal(err)
		}
		if saved.Attempts[0].Cleanup == "" {
			return out
		}
	}
	t.Fatal("stop cleanup never completed")
	return nil
}
func stopReason(t *testing.T, st *journal.Store, r journal.Run) string {
	t.Helper()
	got, err := st.Load(r.Key)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(got.Attempts[0])
	if err != nil {
		t.Fatal(err)
	}
	var wire struct{ StopReason string }
	if err = json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.StopReason
}
func TestStartupNeverAcceptedAtBoundary(t *testing.T) {
	for _, status := range []string{"", "idle", "blocked"} {
		t.Run(status, func(t *testing.T) {
			st, h, r := freshPrompt(t)
			v := h.terms["t1"]
			v.Status = status
			v.Reported = status != ""
			h.terms["t1"] = v
			_ = stopSettle(t, st, h, r, now.Add(119*time.Second))
			if len(h.calls) != 0 {
				t.Fatal("stopped before startup cutoff", h.calls)
			}
			_ = stopSettle(t, st, h, r, now.Add(120*time.Second))
			if !reflect.DeepEqual(h.calls, []string{"interrupt:t1"}) {
				t.Fatal("startup cutoff did not interrupt", h.calls)
			}
			out := stopSettle(t, st, h, r, now.Add(130*time.Second))
			if d, ok := out.(Done); !ok || d.Status != "failed" || stopReason(t, st, r) != "agent_not_started" {
				t.Fatalf("startup outcome: %#v", out)
			}
			if !reflect.DeepEqual(h.calls, []string{"interrupt:t1", "kill:t1", "rm:t1"}) {
				t.Fatal(h.calls)
			}
			b, err := os.ReadFile(filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "output.log"))
			if err != nil || string(b) != "Hooks need review\n[terminal output incomplete]\n" {
				t.Fatalf("lost screen %q %v", b, err)
			}
		})
	}
}
func TestStopTimeoutConfirmedIsFailed(t *testing.T) {
	for _, kind := range []string{"prompt", "command"} {
		t.Run(kind, func(t *testing.T) {
			st, f, r := setup(t, kind)
			h := &stopHC{fakeHC: f}
			_ = stopSettle(t, st, h, r, r.ExpiresAt)
			out := stopSettle(t, st, h, r, r.ExpiresAt.Add(10*time.Second))
			if d, ok := out.(Done); !ok || d.Status != "failed" || stopReason(t, st, r) != "timeout" {
				t.Fatalf("confirmed timeout: %#v", out)
			}
			if len(h.terms) != 0 {
				t.Fatal("stopped terminal not removed")
			}
		})
	}
}
func TestStopUnstoppableHolds(t *testing.T) {
	for _, killErr := range []error{nil, errors.New("kill unavailable")} {
		st, f, r := setup(t, "prompt")
		h := &stopHC{fakeHC: f, keepLive: true, killErr: killErr}
		_ = stopSettle(t, st, h, r, r.ExpiresAt)
		out, err := Settle(context.Background(), h, st, r.Key, 0, r.ExpiresAt.Add(10*time.Second))
		got, _ := st.Load(r.Key)
		if got.Attempts[0].Hold != "timeout" || h.terms["t1"].State != "live" {
			t.Fatalf("unproven stop was not held: %#v %v %+v", out, err, got.Attempts[0])
		}
	}
}
func TestStopLostKillReplyWithFinishedProof(t *testing.T) {
	st, f, r := setup(t, "command")
	h := &stopHC{fakeHC: f, killErr: errors.New("reply lost")}
	out := stopSettle(t, st, h, r, r.ExpiresAt)
	if d, ok := out.(Done); !ok || d.Status != "failed" {
		t.Fatal(out)
	}
}
func TestStartupObservedActivityPersists(t *testing.T) {
	st, h, r := freshPrompt(t)
	v := h.terms["t1"]
	v.Status = "working"
	v.Reported = true
	h.terms["t1"] = v
	_ = stopSettle(t, st, h, r, now.Add(119*time.Second))
	v.Status = "blocked"
	h.terms["t1"] = v
	restarted := journal.New(st.Layout)
	_ = stopSettle(t, restarted, h, r, now.Add(121*time.Second))
	if len(h.calls) != 0 {
		t.Fatal("started task timed out at startup after restart", h.calls)
	}
}
func TestStartupDecisionOrdering(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		// The whole startup interval and both adjacent nanosecond boundaries.
		elapsed := time.Duration(hegel.Draw(ht, hegel.Integers(int64(0), int64(3*time.Minute))))
		active := hegel.Draw(ht, hegel.Booleans())
		duringCapture := hegel.Draw(ht, hegel.Booleans())
		evidence := hegel.Draw(ht, hegel.Integers(0, 2))
		st, h, r := freshPrompt(t)
		observe := func() {
			v := h.terms["t1"]
			switch evidence {
			case 0:
				v.Status = "working"
				v.Reported = true
			case 1:
				v.Turn = 1
			case 2:
				saveResult(t, st, r, "succeeded")
			}
			h.terms["t1"] = v
		}
		if active {
			if duringCapture {
				h.onRead = observe
			} else {
				observe()
			}
		}
		_ = stopSettle(t, st, h, r, now.Add(elapsed))
		if active || elapsed < 2*time.Minute {
			if len(h.calls) != 0 {
				ht.Fatalf("early/started stop at %s: %v", elapsed, h.calls)
			}
		} else if !reflect.DeepEqual(h.calls, []string{"interrupt:t1"}) {
			ht.Fatalf("no startup stop at %s: %v", elapsed, h.calls)
		}
	}, hegel.WithTestCases(1000))
}

func TestStopCaptureFailureDoesNotDestroy(t *testing.T) {
	for _, writeFailure := range []bool{false, true} {
		st, h, r := freshPrompt(t)
		if writeFailure {
			if err := os.MkdirAll(filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "output.log"), 0700); err != nil {
				t.Fatal(err)
			}
		} else {
			h.readErr = errors.New("screen unavailable")
		}
		_, err := Settle(context.Background(), h, st, r.Key, 0, now.Add(2*time.Minute))
		if err == nil || len(h.calls) != 0 || stopReason(t, st, r) != "" {
			t.Fatalf("capture failure destroyed terminal: %v %v", err, h.calls)
		}
	}
}
func TestStopChangedOccupantAfterCaptureIsUntouched(t *testing.T) {
	st, h, r := freshPrompt(t)
	h.onRead = func() { v := h.terms["t1"]; v.CreatedMs++; h.terms["t1"] = v }
	out, err := Settle(context.Background(), h, st, r.Key, 0, now.Add(2*time.Minute))
	if held, ok := out.(Held); err != nil || !ok || held.Reason != "occupant_changed" || len(h.calls) != 0 {
		t.Fatalf("changed occupant: %#v %v %v", out, err, h.calls)
	}
}
func TestStartupLegacyMissingMarkerIsGrandfathered(t *testing.T) {
	st, h, r := freshPrompt(t)
	path := filepath.Join(st.Layout.Run(r.Key), "run.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	delete(wire["Attempts"].([]any)[0].(map[string]any), "TaskStarted")
	b, err = json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	_ = stopSettle(t, journal.New(st.Layout), h, r, now.Add(3*time.Minute))
	if len(h.calls) != 0 {
		t.Fatal("stopped legacy attempt", h.calls)
	}
}
func TestStartupRecoveryAdoptionLatchesWorking(t *testing.T) {
	st, f, r := intent(t)
	a := r.Attempts[0]
	f.terms = map[string]hc.Terminal{"t1": {Address: "t1", Name: a.Name, State: "live", CreatedMs: 100, PID: 101, Labels: a.Spec.Labels, Argv: a.Spec.Argv, Reported: true, Status: "working"}}
	if err := Recover(context.Background(), f, st, r.Key, 0); err != nil {
		t.Fatal(err)
	}
	v := f.terms["t1"]
	v.Status = "blocked"
	f.terms["t1"] = v
	h := &stopHC{fakeHC: f}
	_ = stopSettle(t, journal.New(st.Layout), h, r, now.Add(3*time.Minute))
	if len(h.calls) != 0 {
		t.Fatal("lost adoption evidence", h.calls)
	}
}
func TestStartupValidResultAtCutoffWins(t *testing.T) {
	st, h, r := freshPrompt(t)
	saveResult(t, st, r, "succeeded")
	_ = stopSettle(t, st, h, r, now.Add(2*time.Minute))
	if len(h.calls) != 0 || stopReason(t, st, r) != "" {
		t.Fatal("valid result ignored at cutoff", h.calls)
	}
	out := stopSettle(t, st, h, r, now.Add(150*time.Second))
	if d, ok := out.(Done); !ok || d.Status != "succeeded" {
		t.Fatal(out)
	}
}
func TestStopArtifactsAreAmbiguous(t *testing.T) {
	for _, shape := range []string{"valid", "malformed", "directory", "dangling"} {
		t.Run(shape, func(t *testing.T) {
			st, h, r := freshPrompt(t)
			_ = stopSettle(t, st, h, r, now.Add(2*time.Minute))
			path := filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "result.json")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			switch shape {
			case "valid":
				saveResult(t, st, r, "succeeded")
			case "malformed":
				err = os.WriteFile(path, []byte("{"), 0600)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "dangling":
				err = os.Symlink("absent", path)
			}
			if err != nil {
				t.Fatal(err)
			}
			out := stopSettle(t, st, h, r, now.Add(130*time.Second))
			if held, ok := out.(Held); !ok || held.Reason != "agent_not_started" {
				t.Fatal(out)
			}
			if len(h.terms) != 1 {
				t.Fatal("ambiguous terminal removed")
			}
		})
	}
}
func TestStopRemovalIntentSurvivesRestart(t *testing.T) {
	st, h, r := freshPrompt(t)
	_ = stopSettle(t, st, h, r, now.Add(2*time.Minute))
	h.onRemove = func() error { delete(h.terms, "t1"); return errors.New("lost remove reply") }
	_, err := Settle(context.Background(), h, st, r.Key, 0, now.Add(130*time.Second))
	if err == nil {
		t.Fatal("expected lost remove reply")
	}
	h.onRemove = nil
	out := stopSettle(t, journal.New(st.Layout), h, r, now.Add(131*time.Second))
	if d, ok := out.(Done); !ok || d.Status != "failed" || stopReason(t, st, r) != "agent_not_started" || h.runs != 1 {
		t.Fatalf("lost failed settlement: %#v", out)
	}
}
func TestStopLateActivityCannotCancelSavedDecision(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		elapsed := time.Duration(hegel.Draw(ht, hegel.Integers(int64(120*time.Second), int64(180*time.Second))))
		artifact := hegel.Draw(ht, hegel.Booleans())
		st, h, r := freshPrompt(t)
		_ = stopSettle(t, st, h, r, now.Add(120*time.Second))
		v := h.terms["t1"]
		v.Status = "working"
		v.Turn = 1
		v.Reported = true
		h.terms["t1"] = v
		if artifact {
			saveResult(t, st, r, "succeeded")
		}
		out := stopSettle(t, journal.New(st.Layout), h, r, now.Add(elapsed))
		if elapsed < 130*time.Second {
			if _, ok := out.(Pending); !ok {
				ht.Fatalf("ended during grace: %#v", out)
			}
		} else if artifact {
			if v, ok := out.(Held); !ok || v.Reason != "agent_not_started" {
				ht.Fatalf("late result accepted: %#v", out)
			}
		} else if v, ok := out.(Done); !ok || v.Status != "failed" {
			ht.Fatalf("saved stop cancelled: %#v", out)
		}
	}, hegel.WithTestCases(1000))
}

func TestStartupActivityDuringCaptureWinsBeforeIntent(t *testing.T) {
	for _, evidence := range []string{"working", "turn", "result"} {
		t.Run(evidence, func(t *testing.T) {
			st, h, r := freshPrompt(t)
			h.onRead = func() {
				v := h.terms["t1"]
				switch evidence {
				case "working":
					v.Status = "working"
					v.Reported = true
				case "turn":
					v.Turn = 1
				case "result":
					saveResult(t, st, r, "succeeded")
				}
				h.terms["t1"] = v
			}
			_ = stopSettle(t, st, h, r, now.Add(2*time.Minute))
			if len(h.calls) != 0 || stopReason(t, st, r) != "" {
				t.Fatalf("pre-intent activity ignored: %v", h.calls)
			}
			v := h.terms["t1"]
			v.Status = "blocked"
			v.Turn = 0
			h.terms["t1"] = v
			h.onRead = nil
			_ = stopSettle(t, journal.New(st.Layout), h, r, now.Add(3*time.Minute))
			if len(h.calls) != 0 {
				t.Fatal("pre-intent evidence not durable", h.calls)
			}
		})
	}
}

func TestStopRetainsScreenAndReasonAcrossRestart(t *testing.T) {
	st, h, r := freshPrompt(t)
	_ = stopSettle(t, st, h, r, now.Add(2*time.Minute))
	h.readErr = errors.New("screen no longer available")
	out := stopSettle(t, journal.New(st.Layout), h, r, r.ExpiresAt.Add(time.Minute))
	if d, ok := out.(Done); !ok || d.Status != "failed" || stopReason(t, st, r) != "agent_not_started" {
		t.Fatalf("saved startup decision lost at deadline: %#v", out)
	}
	b, err := os.ReadFile(filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "output.log"))
	if err != nil || string(b) != "Hooks need review\n[terminal output incomplete]\n" {
		t.Fatalf("saved screen changed: %q %v", b, err)
	}
}

func TestStopLegacyExpiryCapturesWithoutRestartingGrace(t *testing.T) {
	for _, kind := range []string{"prompt", "command"} {
		t.Run(kind, func(t *testing.T) {
			st, f, r := setup(t, kind)
			h := &stopHC{fakeHC: f}
			path := filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "output.log")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("command output\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := st.Update(r.Key, func(r *journal.Run) error { r.Attempts[0].ExpiringAt = new(now.Add(-time.Minute)); return nil }); err != nil {
				t.Fatal(err)
			}
			out := stopSettle(t, journal.New(st.Layout), h, r, now)
			if d, ok := out.(Done); !ok || d.Status != "failed" || stopReason(t, st, r) != "timeout" {
				t.Fatalf("legacy expiry restarted grace: %#v", out)
			}
			want := "command output\n"
			if kind == "prompt" {
				want = "Hooks need review\n[terminal output incomplete]\n"
			}
			b, err := os.ReadFile(path)
			if err != nil || string(b) != want {
				t.Fatalf("legacy output %q %v", b, err)
			}
		})
	}
}
