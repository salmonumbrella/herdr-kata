package attempt

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/prompt"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"hegel.dev/go/hegel"
)

type fakeHC struct {
	hc.HC
	core   string
	terms  map[string]hc.Terminal
	calls  []string
	runs   int
	runErr error
	onRun  func(hc.RunSpec)
	onGet  func(string) (hc.Terminal, error)
}

func (f *fakeHC) CoreID(context.Context) (string, error) { return f.core, nil }
func (f *fakeHC) Run(_ context.Context, s hc.RunSpec) (hc.Launched, error) {
	f.runs++
	if f.onRun != nil {
		f.onRun(s)
	}
	if f.runErr != nil {
		return hc.Launched{}, f.runErr
	}
	f.terms["t1"] = hc.Terminal{Address: "t1", Name: s.Name, State: "live", CreatedMs: 100, PID: 101, Labels: s.Labels, Argv: append(slices.Clone(s.Prefix), s.Argv...)}
	return hc.Launched{Address: "t1", Name: s.Name, State: "live"}, nil
}
func (f *fakeHC) Get(_ context.Context, target string) (hc.Terminal, error) {
	if f.onGet != nil {
		return f.onGet(target)
	}
	t, ok := f.terms[target]
	if !ok {
		return t, hc.ErrNotFound
	}
	return t, nil
}
func (f *fakeHC) List(_ context.Context, all bool, labels map[string]string) ([]string, error) {
	if !all {
		return nil, errors.New("recovery omitted --all")
	}
	var out []string
	for id, t := range f.terms {
		if t.Labels["kata.nonce"] == labels["kata.nonce"] {
			out = append(out, id)
		}
	}
	return out, nil
}
func (f *fakeHC) Interrupt(_ context.Context, target string) error {
	f.calls = append(f.calls, "interrupt:"+target)
	return nil
}
func (f *fakeHC) Kill(_ context.Context, target string) error {
	f.calls = append(f.calls, "kill:"+target)
	t := f.terms[target]
	t.State = "finished"
	t.Reason = "killed"
	f.terms[target] = t
	return nil
}
func (f *fakeHC) Remove(_ context.Context, target string) error {
	f.calls = append(f.calls, "rm:"+target)
	if f.terms[target].State == "live" {
		return hc.ErrLive
	}
	delete(f.terms, target)
	return nil
}

var now = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

func setup(t *testing.T, kind string) (*journal.Store, *fakeHC, journal.Run) {
	t.Helper()
	st := journal.New(state.Layout{Root: t.TempDir()})
	r := journal.Run{Version: 1, Key: journal.ManualKey("executor", "example-project", "request"), RunUID: "01J00000000000000000000001", Project: "example-project", Phase: "running", ExpiresAt: now.Add(time.Hour), StartedAt: new(now), Plan: defs.Plan{Steps: []defs.Step{{Key: "main", Kind: kind, SkipExit: 7}}}, Attempts: []journal.Attempt{{Step: "main", N: 1, Status: "running", CoreID: "core-one", Nonce: "fixture-nonce", Address: "t1", Name: "fixture", PID: 101, CreatedMs: 100, LaunchedAt: now}}}
	if err := st.Create(r); err != nil {
		t.Fatal(err)
	}
	f := &fakeHC{core: "core-one", terms: map[string]hc.Terminal{"t1": {Address: "t1", Name: "fixture", State: "live", CreatedMs: 100, PID: 101, Labels: map[string]string{"kata.nonce": "fixture-nonce"}}}}
	return st, f, r
}
func saveResult(t *testing.T, st *journal.Store, r journal.Run, status string) {
	t.Helper()
	path := filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "result.json")
	if err := prompt.WriteResult(path, prompt.Result{RunUID: r.RunUID, Step: "main", Attempt: 1, Status: status, Summary: "Completed", Note: "Result note"}); err != nil {
		t.Fatal(err)
	}
}
func advance(t *testing.T, st *journal.Store, f *fakeHC, r journal.Run, at time.Time) Outcome {
	t.Helper()
	for range 5 {
		out, err := Settle(clock.WithNow(context.Background(), now), f, st, r.Key, 0, at)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := out.(Pending); !ok {
			return out
		}
		saved, _ := st.Load(r.Key)
		if saved.Attempts[0].Cleanup == "" {
			return out
		}
	}
	t.Fatal("cleanup did not finish")
	return nil
}
func TestIntentSavedBeforeRun(t *testing.T) {
	st, f, r := setup(t, "prompt")
	_ = st.Update(r.Key, func(r *journal.Run) error { r.Attempts[0] = journal.Attempt{Step: "main", N: 1}; return nil })
	f.onRun = func(spec hc.RunSpec) {
		got, err := st.Load(r.Key)
		if err != nil {
			t.Fatal(err)
		}
		a := got.Attempts[0]
		if a.Nonce == "" || a.CoreID != "core-one" || a.Name != spec.Name || a.Address != "" || a.Spec.Name != spec.Name {
			t.Fatalf("missing intent: %+v", a)
		}
	}
	spec := hc.RunSpec{Cwd: "/srv/projects/example", Labels: map[string]string{"kata.executor": "executor"}, Argv: []string{"/usr/local/bin/agent"}}
	if err := Launch(clock.WithNow(context.Background(), now), f, st, r.Key, 0, spec); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Load(r.Key)
	if got.Attempts[0].Address != "t1" || got.Attempts[0].PID != 101 {
		t.Fatal(got.Attempts)
	}
}
func intent(t *testing.T) (*journal.Store, *fakeHC, journal.Run) {
	st, f, r := setup(t, "prompt")
	f.runErr = errors.New("reply lost")
	_ = st.Update(r.Key, func(r *journal.Run) error { r.Attempts[0] = journal.Attempt{Step: "main", N: 1}; return nil })
	_ = Launch(clock.WithNow(context.Background(), now), f, st, r.Key, 0, hc.RunSpec{Cwd: "/srv/projects/example", Labels: map[string]string{"kata.executor": "executor"}, Argv: []string{"/usr/local/bin/agent"}})
	f.runErr = nil
	got, _ := st.Load(r.Key)
	return st, f, got
}
func TestRecoverAdoptsExactlyOne(t *testing.T) {
	st, f, r := intent(t)
	a := r.Attempts[0]
	f.terms = map[string]hc.Terminal{"t2": {Address: "t2", Name: a.Name, State: "finished", CreatedMs: 200, PID: 202, Labels: a.Spec.Labels, Argv: a.Spec.Argv}}
	if err := Recover(clock.WithNow(context.Background(), now), f, st, r.Key, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Load(r.Key)
	if got.Attempts[0].Address != "t2" || f.runs != 1 {
		t.Fatal(got.Attempts, f.runs)
	}
}
func TestRecoverZeroSameCoreRelaunchesWithNewNonce(t *testing.T) {
	st, f, r := intent(t)
	old := r.Attempts[0].Nonce
	if err := Recover(clock.WithNow(context.Background(), now), f, st, r.Key, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Load(r.Key)
	if f.runs != 2 || got.Attempts[0].Nonce == old || got.Attempts[0].Address != "t1" {
		t.Fatal(got.Attempts, f.runs)
	}
}
func TestRecoverZeroNewCoreHoldsUnproven(t *testing.T) {
	st, f, r := intent(t)
	f.core = "core-two"
	if err := Recover(clock.WithNow(context.Background(), now), f, st, r.Key, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Load(r.Key)
	if got.Attempts[0].Hold != "launch_unproven" || f.runs != 1 {
		t.Fatal(got.Attempts, f.runs)
	}
}
func TestRecoverManyOrMismatchHoldsAmbiguous(t *testing.T) {
	for _, many := range []bool{false, true} {
		t.Run(map[bool]string{false: "mismatch", true: "many"}[many], func(t *testing.T) {
			st, f, r := intent(t)
			a := r.Attempts[0]
			term := hc.Terminal{Address: "t2", Name: a.Name, Labels: a.Spec.Labels, Argv: []string{"/wrong/binary"}}
			f.terms = map[string]hc.Terminal{"t2": term}
			if many {
				f.terms["t3"] = term
			}
			if err := Recover(clock.WithNow(context.Background(), now), f, st, r.Key, 0); err != nil {
				t.Fatal(err)
			}
			got, _ := st.Load(r.Key)
			if got.Attempts[0].Hold != "launch_ambiguous" || f.runs != 1 {
				t.Fatal(got.Attempts, f.runs)
			}
		})
	}
}
func TestSameInstantNamesDiffer(t *testing.T) {
	a := Name("executor", "01J00000000000000000000001", "main", 1, "nonce-one")
	b := Name("executor", "01J00000000000000000000002", "main", 1, "nonce-one")
	if a == b || len(a) != 32 || !strings.HasPrefix(a, "hk-") {
		t.Fatalf("names %s %s", a, b)
	}
}
func TestGuardBlocksKill(t *testing.T) {
	st, f, r := setup(t, "prompt")
	saveResult(t, st, r, "succeeded")
	term := f.terms["t1"]
	term.Reported = true
	term.Turn = 1
	term.Status = "done"
	f.terms["t1"] = term
	calls := 0
	f.onGet = func(string) (hc.Terminal, error) {
		calls++
		v := term
		if calls > 1 {
			v.CreatedMs++
		}
		return v, nil
	}
	out, err := Settle(clock.WithNow(context.Background(), now), f, st, r.Key, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	held, ok := out.(Held)
	if !ok || held.Reason != "occupant_changed" || len(f.calls) != 0 {
		t.Fatalf("guard: %+v %v", out, f.calls)
	}
}
func TestPromptPendingAndErrors(t *testing.T) {
	for _, tt := range []struct {
		status   string
		reported bool
		turn     uint64
		want     string
	}{{"working", true, 1, "pending"}, {"", false, 0, "pending"}, {"idle", true, 0, "pending"}, {"error", true, 1, "agent_error"}} {
		t.Run(tt.status, func(t *testing.T) {
			st, f, r := setup(t, "prompt")
			v := f.terms["t1"]
			v.Status = tt.status
			v.Reported = tt.reported
			v.Turn = tt.turn
			f.terms["t1"] = v
			out, err := Settle(clock.WithNow(context.Background(), now), f, st, r.Key, 0, now)
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == "pending" {
				if _, ok := out.(Pending); !ok {
					t.Fatal(out)
				}
			} else if held, ok := out.(Held); !ok || held.Reason != tt.want {
				t.Fatal(out)
			}
		})
	}
}
func TestNoActivityAfterFiveMinutes(t *testing.T) {
	st, f, r := setup(t, "prompt")
	v := f.terms["t1"]
	v.Reported = true
	v.Status = "idle"
	f.terms["t1"] = v
	_, _ = Settle(clock.WithNow(context.Background(), now), f, st, r.Key, 0, now)
	out := advance(t, st, f, r, now.Add(5*time.Minute))
	if h, ok := out.(Held); !ok || h.Reason != "no_activity" {
		t.Fatal(out)
	}
}
func TestNeverReportingAgentNotNoActivity(t *testing.T) {
	st, f, r := setup(t, "prompt")
	_, _ = Settle(clock.WithNow(context.Background(), now), f, st, r.Key, 0, now)
	if out := advance(t, st, f, r, now.Add(6*time.Minute)); reflect.TypeOf(out) != reflect.TypeFor[Pending]() {
		t.Fatal(out)
	}
}
func TestNeverReportingAgentSettlesOnExit(t *testing.T) {
	st, f, r := setup(t, "prompt")
	saveResult(t, st, r, "succeeded")
	v := f.terms["t1"]
	v.State = "finished"
	v.Reason = "exited"
	f.terms["t1"] = v
	out := advance(t, st, f, r, now)
	if d, ok := out.(Done); !ok || d.Status != "succeeded" || !reflect.DeepEqual(f.calls, []string{"rm:t1"}) {
		t.Fatal(out, f.calls)
	}
}
func TestNeverReportingAgentSettlesOnStableResult(t *testing.T) {
	st, f, r := setup(t, "prompt")
	saveResult(t, st, r, "succeeded")
	_ = advance(t, st, f, r, now)
	if out := advance(t, st, f, r, now.Add(29*time.Second)); reflect.TypeOf(out) != reflect.TypeFor[Pending]() {
		t.Fatal(out)
	}
	out := advance(t, st, f, r, now.Add(30*time.Second))
	if d, ok := out.(Done); !ok || d.Status != "succeeded" || !reflect.DeepEqual(f.calls, []string{"interrupt:t1", "kill:t1", "rm:t1"}) {
		t.Fatal(out, f.calls)
	}
}
func TestNeedsHumanKeepsTerminal(t *testing.T) {
	st, f, r := setup(t, "prompt")
	saveResult(t, st, r, "needs-human")
	v := f.terms["t1"]
	v.State = "finished"
	v.Reason = "exited"
	f.terms["t1"] = v
	if out := advance(t, st, f, r, now); reflect.TypeOf(out) != reflect.TypeFor[Done]() {
		t.Fatal(out)
	}
	if len(f.calls) != 0 || len(f.terms) != 1 {
		t.Fatal(f.calls)
	}
}
func TestNoResultGrace10s(t *testing.T) {
	st, f, r := setup(t, "prompt")
	v := f.terms["t1"]
	v.Reported = true
	v.Turn = 1
	v.Status = "idle"
	f.terms["t1"] = v
	_ = advance(t, st, f, r, now)
	if out := advance(t, st, f, r, now.Add(9*time.Second)); reflect.TypeOf(out) != reflect.TypeFor[Pending]() {
		t.Fatal(out)
	}
	if out := advance(t, st, f, r, now.Add(10*time.Second)); reflect.TypeOf(out) != reflect.TypeFor[Held]() || out.(Held).Reason != "no_result" {
		t.Fatal(out)
	}
}
func TestBlockedNotifiesOncePerSpell(t *testing.T) {
	st, f, r := setup(t, "prompt")
	v := f.terms["t1"]
	v.Reported = true
	v.Status = "blocked"
	f.terms["t1"] = v
	out := advance(t, st, f, r, now)
	notice, ok := out.(Notify)
	if !ok {
		t.Fatal(out)
	}
	_ = st.Update(r.Key, func(r *journal.Run) error { r.Notified = append(r.Notified, notice.ID); return nil })
	if out := advance(t, st, f, r, now); reflect.TypeOf(out) != reflect.TypeFor[Pending]() {
		t.Fatal(out)
	}
	v.Status = "working"
	f.terms["t1"] = v
	_ = advance(t, st, f, r, now)
	v.Status = "blocked"
	f.terms["t1"] = v
	out = advance(t, st, f, r, now)
	if next, ok := out.(Notify); !ok || next.ID == notice.ID {
		t.Fatal(out)
	}
}
func TestExpiryInterruptThenKill(t *testing.T) {
	st, f, r := setup(t, "prompt")
	_ = advance(t, st, f, r, r.ExpiresAt)
	if !reflect.DeepEqual(f.calls, []string{"interrupt:t1"}) {
		t.Fatal(f.calls)
	}
	_ = advance(t, st, f, r, r.ExpiresAt.Add(9*time.Second))
	if len(f.calls) != 1 {
		t.Fatal(f.calls)
	}
	out := advance(t, st, f, r, r.ExpiresAt.Add(10*time.Second))
	if h, ok := out.(Held); !ok || h.Reason != "timeout" || !reflect.DeepEqual(f.calls, []string{"interrupt:t1", "kill:t1"}) {
		t.Fatal(out, f.calls)
	}
}

func TestMissingTerminalDuringExpiryIsAgentLost(t *testing.T) {
	st, f, r := setup(t, "prompt")
	term := f.terms["t1"]
	calls := 0
	f.onGet = func(string) (hc.Terminal, error) {
		calls++
		if calls == 1 {
			return term, nil
		}
		return hc.Terminal{}, hc.ErrNotFound
	}
	out, err := Settle(context.Background(), f, st, r.Key, 0, r.ExpiresAt)
	if h, ok := out.(Held); err != nil || !ok || h.Reason != "agent_lost" {
		t.Fatalf("lost at expiry: %v %v", out, err)
	}
}

func TestAgentLost(t *testing.T) {
	for _, missing := range []bool{false, true} {
		st, f, r := setup(t, "prompt")
		if missing {
			delete(f.terms, "t1")
		} else {
			v := f.terms["t1"]
			v.State = "finished"
			v.Reason = "killed"
			f.terms["t1"] = v
		}
		out := advance(t, st, f, r, now)
		if h, ok := out.(Held); !ok || h.Reason != "agent_lost" {
			t.Fatal(out)
		}
	}
}
func command(t *testing.T, code int) (*journal.Store, *fakeHC, journal.Run) {
	st, f, r := setup(t, "command")
	v := f.terms["t1"]
	v.State = "finished"
	v.Reason = "exited"
	f.terms["t1"] = v
	receipt := journal.Receipt{ExitCode: code, StartedAt: now, EndedAt: now.Add(time.Second)}
	if err := state.WriteJSON(filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "receipt.json"), receipt); err != nil {
		t.Fatal(err)
	}
	return st, f, r
}
func TestCommandOutcomes(t *testing.T) {
	for _, tt := range []struct {
		code   int
		status string
	}{{0, "succeeded"}, {8, "failed"}, {7, "skipped"}} {
		st, f, r := command(t, tt.code)
		out := advance(t, st, f, r, now.Add(time.Second))
		if d, ok := out.(Done); !ok || d.Status != tt.status {
			t.Fatal(out)
		}
	}
}
func TestSkipExitGate(t *testing.T) {
	st, f, r := command(t, 7)
	out := advance(t, st, f, r, now)
	if d, ok := out.(Done); !ok || d.Status != "skipped" || d.Note != "gate_closed" {
		t.Fatal(out)
	}
}
func TestCommandNoReceiptUnknown(t *testing.T) {
	st, f, r := setup(t, "command")
	v := f.terms["t1"]
	v.State = "finished"
	v.Reason = "exited"
	v.ExitCode = new(0)
	f.terms["t1"] = v
	out := advance(t, st, f, r, now)
	if h, ok := out.(Held); !ok || h.Reason != "command_outcome_unknown" {
		t.Fatal(out)
	}
}

// A receipt is evidence only when its ordinary child exit code is present.
func TestReceiptRequiresExitCode(t *testing.T) {
	for _, raw := range []string{"", "null"} {
		st, f, r := command(t, 0)
		body := map[string]any{"StartedAt": now, "EndedAt": now.Add(time.Second)}
		if raw != "" {
			body["ExitCode"] = nil
		}
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "receipt.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
		out := advance(t, st, f, r, now.Add(time.Second))
		if h, ok := out.(Held); !ok || h.Reason != "command_outcome_unknown" {
			t.Fatalf("missing/null exit code established certainty: %v", out)
		}
	}
}

func TestReceiptExitCodeDomain(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		code := hegel.Draw(ht, hegel.Integers(math.MinInt, math.MaxInt))
		st, f, r := command(t, code)
		out := advance(t, st, f, r, now.Add(time.Second))
		if code < 0 || code > 255 {
			if h, ok := out.(Held); !ok || h.Reason != "command_outcome_unknown" {
				ht.Fatalf("invalid exit %d established certainty: %v", code, out)
			}
			return
		}
		want := "failed"
		if code == 0 {
			want = "succeeded"
		} else if code == 7 {
			want = "skipped"
		}
		if d, ok := out.(Done); !ok || d.Status != want {
			ht.Fatalf("ordinary exit %d got %v want %s", code, out, want)
		}
	})
}
func TestCommandNotExitedReasonUnknown(t *testing.T) {
	st, f, r := command(t, 0)
	v := f.terms["t1"]
	v.Reason = "killed"
	f.terms["t1"] = v
	out := advance(t, st, f, r, now)
	if h, ok := out.(Held); !ok || h.Reason != "command_outcome_unknown" {
		t.Fatal(out)
	}
}
func TestInvalidCommandNote(t *testing.T) {
	st, f, r := command(t, 0)
	if err := os.WriteFile(filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "note.txt"), []byte("two\nlines"), 0600); err != nil {
		t.Fatal(err)
	}
	out := advance(t, st, f, r, now)
	if h, ok := out.(Held); !ok || h.Reason != "invalid_note" {
		t.Fatal(out)
	}
}
func TestLaunchPrefixHasPathHomeAndAbsoluteArgv(t *testing.T) {
	t.Setenv("PATH", "/usr/local/bin:/usr/bin:/bin")
	t.Setenv("HOME", "/srv/home")
	got := LaunchEnv(nil, map[string]string{"KATA_CRON_RUN": "run"})
	for _, want := range []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/srv/home", "KATA_CRON_RUN=run"} {
		if !slices.Contains(got, want) {
			t.Fatal(got)
		}
	}
	st, f, r := setup(t, "prompt")
	_ = st.Update(r.Key, func(r *journal.Run) error { r.Attempts[0] = journal.Attempt{Step: "main", N: 1}; return nil })
	f.onRun = func(s hc.RunSpec) {
		if s.Argv[0] != "/usr/local/bin/agent" || !reflect.DeepEqual(s.Prefix, got) {
			t.Fatal(s)
		}
	}
	if err := Launch(clock.WithNow(context.Background(), now), f, st, r.Key, 0, hc.RunSpec{Prefix: got, Argv: []string{"/usr/local/bin/agent"}, Labels: map[string]string{"kata.executor": "executor"}}); err != nil {
		t.Fatal(err)
	}
}
func TestNamesDistinguishNonces(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		nonce := hegel.Draw(ht, hegel.Text())
		a := Name("executor", "run", "main", 1, nonce)
		b := Name("executor", "run", "main", 1, nonce+"x")
		if a == b || len(a) != 32 {
			ht.Fatalf("colliding names %s %s", a, b)
		}
	})
}

func TestReportedResultCleanup(t *testing.T) {
	for _, status := range []string{"idle", "done"} {
		st, f, r := setup(t, "prompt")
		saveResult(t, st, r, "failed")
		v := f.terms["t1"]
		v.Reported = true
		v.Status = status
		v.Turn = 1
		f.terms["t1"] = v
		out := advance(t, st, f, r, now)
		if d, ok := out.(Done); !ok || d.Status != "failed" || !reflect.DeepEqual(f.calls, []string{"kill:t1", "rm:t1"}) {
			t.Fatal(out, f.calls)
		}
	}
}
func TestChangedResultRestartsStableClock(t *testing.T) {
	st, f, r := setup(t, "prompt")
	saveResult(t, st, r, "succeeded")
	_ = advance(t, st, f, r, now)
	saveResult(t, st, r, "failed")
	_ = advance(t, st, f, r, now.Add(29*time.Second))
	if out := advance(t, st, f, r, now.Add(30*time.Second)); reflect.TypeOf(out) != reflect.TypeFor[Pending]() {
		t.Fatal(out)
	}
	out := advance(t, st, f, r, now.Add(59*time.Second))
	if d, ok := out.(Done); !ok || d.Status != "failed" {
		t.Fatal(out)
	}
}
func TestCleanupSurvivesMissingTerminalAfterRemove(t *testing.T) {
	st, f, r := setup(t, "prompt")
	saveResult(t, st, r, "succeeded")
	v := f.terms["t1"]
	v.Reported = true
	v.Status = "done"
	v.Turn = 1
	f.terms["t1"] = v
	_, err := Settle(clock.WithNow(context.Background(), now), f, st, r.Key, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	// Saved result + remove intent survives a crash after rm but before its save.
	_ = st.Update(r.Key, func(r *journal.Run) error { r.Attempts[0].Cleanup = "remove"; return nil })
	delete(f.terms, "t1")
	out := advance(t, st, f, r, now)
	if d, ok := out.(Done); !ok || d.Status != "succeeded" {
		t.Fatal(out)
	}
}
func TestCommandLiveAndExpiry(t *testing.T) {
	st, f, r := setup(t, "command")
	if out := advance(t, st, f, r, now); reflect.TypeOf(out) != reflect.TypeFor[Pending]() {
		t.Fatal(out)
	}
	out := advance(t, st, f, r, r.ExpiresAt)
	if h, ok := out.(Held); !ok || h.Reason != "timeout" || !reflect.DeepEqual(f.calls, []string{"kill:t1"}) {
		t.Fatal(out, f.calls)
	}
}

func TestGuardReadFailureDefers(t *testing.T) {
	st, f, r := setup(t, "prompt")
	saveResult(t, st, r, "succeeded")
	term := f.terms["t1"]
	term.Reported = true
	term.Status = "done"
	term.Turn = 1
	unavailable := errors.New("core temporarily unavailable")
	calls := 0
	f.onGet = func(string) (hc.Terminal, error) {
		calls++
		if calls > 1 {
			return hc.Terminal{}, unavailable
		}
		return term, nil
	}
	out, err := Settle(clock.WithNow(context.Background(), now), f, st, r.Key, 0, now)
	if !errors.Is(err, unavailable) {
		t.Fatalf("outcome %+v error %v", out, err)
	}
	got, _ := st.Load(r.Key)
	if got.Attempts[0].Hold != "" || len(f.calls) != 0 {
		t.Fatalf("unavailability became a hold: %+v", got.Attempts[0])
	}
}

func TestCommandCompletedAfterExpiryHolds(t *testing.T) {
	st, f, r := command(t, 0)
	if err := state.WriteJSON(filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "receipt.json"), journal.Receipt{ExitCode: 0, StartedAt: now, EndedAt: r.ExpiresAt.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	out := advance(t, st, f, r, r.ExpiresAt.Add(2*time.Second))
	if h, ok := out.(Held); !ok || h.Reason != "timeout" {
		t.Fatal(out)
	}
}
func TestRecoverMissingIntentHolds(t *testing.T) {
	st, f, r := setup(t, "prompt")
	_ = st.Update(r.Key, func(r *journal.Run) error { a := &r.Attempts[0]; a.Address = ""; a.Nonce = ""; return nil })
	_ = Recover(clock.WithNow(context.Background(), now), f, st, r.Key, 0)
	got, _ := st.Load(r.Key)
	if got.Attempts[0].Hold != "launch_ambiguous" || f.runs != 0 {
		t.Fatal(got.Attempts, f.runs)
	}
}
