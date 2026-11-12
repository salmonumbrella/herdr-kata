package attempt

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/prompt"
	"hegel.dev/go/hegel"
)

type wakeHC struct {
	*fakeHC
	sends      int
	sendStatus string
	sendErr    error
}

func (f *wakeHC) List(_ context.Context, all bool, labels map[string]string) ([]string, error) {
	if all || labels["herdr-kata.agent"] != "worker" {
		return nil, errors.New("wrong claim lookup")
	}
	var ids []string
	for id, term := range f.terms {
		if term.Labels["herdr-kata.agent"] == "worker" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (f *wakeHC) Send(_ context.Context, id, text, status string) (uint64, error) {
	f.sends++
	f.sendStatus = status
	if id != "t1" || text == "" {
		return 0, errors.New("wrong send")
	}
	return 4, f.sendErr
}
func wakeSetup(t *testing.T) (*journal.Store, *wakeHC, journal.Run) {
	st, base, r := setup(t, "prompt")
	if err := st.Update(r.Key, func(r *journal.Run) error {
		r.Plan.Agent = "worker"
		r.Attempts[0] = journal.Attempt{Step: "main", N: 1, Status: "queued", Nonce: "wake:" + r.RunUID}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	base.terms["t1"] = hc.Terminal{Address: "t1", State: "live", Status: "idle", Reported: true, CreatedMs: 100, PID: 101, ForegroundPID: 101, Turn: 4, Labels: map[string]string{"herdr-kata.agent": "worker"}}
	return st, &wakeHC{fakeHC: base}, r
}
func TestWakeDeliveryAndResult(t *testing.T) {
	st, f, r := wakeSetup(t)
	if out, err := Wake(context.Background(), f, st, r.Key, 0, now); err != nil || out != (Pending{}) {
		t.Fatalf("delivery: %v %v", out, err)
	}
	saveResult(t, st, r, "succeeded")
	term := f.terms["t1"]
	term.Turn = 5
	f.terms["t1"] = term
	if out, err := Wake(context.Background(), f, st, r.Key, 0, now.Add(time.Second)); err != nil || out != (Done{"succeeded", "Result note"}) {
		t.Fatalf("result: %v %v", out, err)
	}
	if f.sends != 1 || len(f.calls) != 0 {
		t.Fatalf("side effects: %d %v", f.sends, f.calls)
	}
}
func TestWakeDoneDeliveryAndReread(t *testing.T) {
	for _, changing := range []bool{false, true} {
		t.Run(map[bool]string{false: "done", true: "became working"}[changing], func(t *testing.T) {
			st, f, r := wakeSetup(t)
			term := f.terms["t1"]
			term.Status = "done"
			reads := 0
			f.onGet = func(string) (hc.Terminal, error) {
				reads++
				if changing && reads == 2 {
					term.Status = "working"
				}
				return term, nil
			}
			out, err := Wake(context.Background(), f, st, r.Key, 0, now)
			if err != nil || out != (Pending{}) || reads != 2 {
				t.Fatalf("wake: %v %v reads %d", out, err, reads)
			}
			if changing {
				if f.sends != 0 {
					t.Fatalf("sent to working target: %d", f.sends)
				}
				saved, err := st.Load(r.Key)
				if err != nil || saved.Attempts[0].Address != "" {
					t.Fatalf("queued: %+v %v", saved.Attempts[0], err)
				}
			} else {
				if f.sends != 1 || f.sendStatus != "done" {
					t.Fatalf("sends %d status %q", f.sends, f.sendStatus)
				}
				_, err = Wake(context.Background(), f, st, r.Key, 0, now)
				if err != nil || f.sends != 1 {
					t.Fatalf("resent: %d %v", f.sends, err)
				}
			}
		})
	}
}
func TestWakeUncertainNeverResends(t *testing.T) {
	st, f, r := wakeSetup(t)
	f.sendErr = errors.New("lost reply")
	for range 2 {
		out, err := Wake(context.Background(), f, st, r.Key, 0, now)
		if err != nil || out != (Held{"wake_uncertain"}) {
			t.Fatalf("%v %v", out, err)
		}
	}
	if f.sends != 1 || len(f.calls) != 0 {
		t.Fatalf("side effects: %d %v", f.sends, f.calls)
	}
}

func TestWakeUnavailableAndRefused(t *testing.T) {
	for _, mode := range []string{"busy", "blocked", "unreported", "missing", "duplicate", "refused"} {
		t.Run(mode, func(t *testing.T) {
			st, f, r := wakeSetup(t)
			term := f.terms["t1"]
			switch mode {
			case "busy":
				term.Status = "working"
			case "blocked":
				term.Status = "blocked"
			case "unreported":
				term.Reported = false
			case "missing":
				delete(f.terms, "t1")
			case "duplicate":
				f.terms["t2"] = term
			case "refused":
				f.sendErr = hc.ErrNotSent
			}
			if mode != "missing" {
				f.terms["t1"] = term
			}
			out, err := Wake(context.Background(), f, st, r.Key, 0, now)
			if err != nil || out != (Pending{}) {
				t.Fatalf("first: %v %v", out, err)
			}
			if mode == "refused" && f.sends != 1 || mode != "refused" && f.sends != 0 {
				t.Fatalf("sends: %d", f.sends)
			}
			out, err = Wake(context.Background(), f, st, r.Key, 0, r.ExpiresAt)
			if err != nil || out != (Done{"failed", ""}) {
				t.Fatalf("deadline: %v %v", out, err)
			}
			saved, _ := st.Load(r.Key)
			if saved.Attempts[0].StopReason != "not_delivered" || len(f.calls) != 0 {
				t.Fatalf("attempt: %+v calls: %v", saved.Attempts[0], f.calls)
			}
		})
	}
}
func TestWakeIdentityAndDeadline(t *testing.T) {
	for _, mode := range []string{"created", "pid", "foreground", "lost", "core", "timeout", "no_result", "stale_turn", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			st, f, r := wakeSetup(t)
			_, err := Wake(context.Background(), f, st, r.Key, 0, now)
			if err != nil {
				t.Fatal(err)
			}
			term := f.terms["t1"]
			at := now.Add(time.Second)
			want := "occupant_changed"
			switch mode {
			case "created":
				term.CreatedMs++
			case "pid":
				term.PID++
			case "foreground":
				term.ForegroundPID++
			case "lost":
				delete(f.terms, "t1")
				want = "agent_lost"
			case "core":
				f.core = "other"
				want = "agent_lost"
			case "timeout":
				at = r.ExpiresAt
				want = "timeout"
			case "no_result":
				term.Turn = 5
				want = "no_result"
			case "stale_turn":
				saveResult(t, st, r, "succeeded")
				want = ""
			case "cancel":
				want = "cancelled"
			}
			if mode != "lost" {
				f.terms["t1"] = term
			}
			var out Outcome
			if mode == "cancel" {
				out, err = Cancel(context.Background(), f, st, r.Key, 0, at)
			} else {
				out, err = Wake(context.Background(), f, st, r.Key, 0, at)
			}
			if mode == "no_result" && err == nil {
				out, err = Wake(context.Background(), f, st, r.Key, 0, at.Add(10*time.Second))
			}
			if err != nil {
				t.Fatal(err)
			}
			if want == "" {
				if out != (Pending{}) {
					t.Fatal(out)
				}
			} else if mode == "cancel" && out != (Done{}) {
				t.Fatalf("cancel: %v", out)
			} else if mode != "cancel" && out != (Held{want}) {
				t.Fatalf("got %v want %s", out, want)
			}
			if f.sends != 1 || len(f.calls) != 0 {
				t.Fatalf("side effects: %d %v", f.sends, f.calls)
			}
		})
	}
}

func TestWakeMarkerProtectsOlderWorkerPaths(t *testing.T) {
	for _, phase := range []string{"queued", "launching", "running"} {
		for _, path := range []string{"recover", "settle", "cleanup"} {
			t.Run(phase+"/"+path, func(t *testing.T) {
				st, f, r := wakeSetup(t)
				if err := st.Update(r.Key, func(r *journal.Run) error {
					a := &r.Attempts[0]
					a.Status = phase
					if path != "recover" {
						a.Address, a.CoreID, a.CreatedMs, a.PID = "t1", "core-one", 100, 101
					}
					if path == "cleanup" {
						a.Cleanup = "interrupt"
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if path == "recover" {
					if err := Recover(context.Background(), f.fakeHC, st, r.Key, 0); err != nil {
						t.Fatal(err)
					}
				} else if _, err := Settle(context.Background(), f.fakeHC, st, r.Key, 0, now); err != nil {
					t.Fatal(err)
				}
				saved, err := st.Load(r.Key)
				want := "occupant_changed"
				if path == "recover" {
					want = "launch_ambiguous"
				}
				if err != nil || saved.Attempts[0].Hold != want || f.runs != 0 || len(f.calls) != 0 {
					t.Fatalf("hold %q, runs %d, calls %v, err %v", saved.Attempts[0].Hold, f.runs, f.calls, err)
				}
			})
		}
	}
}
func TestWakeCancelBeforeDelivery(t *testing.T) {
	for _, launching := range []bool{false, true} {
		st, f, r := wakeSetup(t)
		if launching {
			_ = st.Update(r.Key, func(r *journal.Run) error {
				r.Attempts[0].Address = "t1"
				r.Attempts[0].Status = "launching"
				return nil
			})
		}
		out, err := Cancel(context.Background(), f, st, r.Key, 0, now)
		if err != nil {
			t.Fatal(err)
		}
		if launching && out != (Held{"wake_uncertain"}) || !launching && out != (Done{}) {
			t.Fatal(out)
		}
		if f.sends != 0 || len(f.calls) != 0 {
			t.Fatal(f.sends, f.calls)
		}
	}
}

func TestWakeAtMostOneSend(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		st, f, r := wakeSetup(t)
		for i := range 8 {
			term := f.terms["t1"]
			term.Status = []string{"idle", "working", "blocked", "done", "error"}[hegel.Draw(ht, hegel.Integers(0, 4))]
			term.Turn = uint64(hegel.Draw(ht, hegel.Integers(4, 6)))
			f.terms["t1"] = term
			if hegel.Draw(ht, hegel.Integers(0, 1)) == 0 {
				f.sendErr = errors.New("lost reply")
			} else {
				f.sendErr = nil
			}
			_, err := Wake(context.Background(), f, st, r.Key, 0, now.Add(time.Duration(i)*time.Second))
			if err != nil {
				t.Fatal(err)
			}
		}
		if f.sends > 1 || len(f.calls) != 0 {
			t.Fatalf("sends %d calls %v", f.sends, f.calls)
		}
	}, hegel.WithTestCases(48))
}

func TestWakeStaleResultDoesNotSettle(t *testing.T) {
	st, f, r := wakeSetup(t)
	if _, err := Wake(context.Background(), f, st, r.Key, 0, now); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "result.json")
	if err := prompt.WriteResult(path, prompt.Result{RunUID: r.RunUID, Step: "other", Attempt: 1, Status: "succeeded", Summary: "Old result"}); err != nil {
		t.Fatal(err)
	}
	term := f.terms["t1"]
	term.Turn = 5
	f.terms["t1"] = term
	out, err := Wake(context.Background(), f, st, r.Key, 0, now.Add(time.Second))
	if err != nil || out != (Pending{}) {
		t.Fatalf("stale result: %v %v", out, err)
	}
	out, err = Wake(context.Background(), f, st, r.Key, 0, now.Add(11*time.Second))
	if err != nil || out != (Held{"no_result"}) {
		t.Fatalf("grace: %v %v", out, err)
	}
}
func TestWakeReloadedIntentAndBlocked(t *testing.T) {
	t.Run("intent", func(t *testing.T) {
		st, f, r := wakeSetup(t)
		_ = st.Update(r.Key, func(r *journal.Run) error {
			r.Attempts[0].Address = "t1"
			r.Attempts[0].Status = "launching"
			return nil
		})
		out, err := Wake(context.Background(), f, st, r.Key, 0, now)
		if err != nil || out != (Held{"wake_uncertain"}) || f.sends != 0 {
			t.Fatalf("%v %v sends %d", out, err, f.sends)
		}
	})
	t.Run("blocked", func(t *testing.T) {
		st, f, r := wakeSetup(t)
		_, err := Wake(context.Background(), f, st, r.Key, 0, now)
		if err != nil {
			t.Fatal(err)
		}
		term := f.terms["t1"]
		term.Status = "blocked"
		f.terms["t1"] = term
		out, err := Wake(context.Background(), f, st, r.Key, 0, now)
		if err != nil || out != (Notify{"blocked:main:1:1", "blocked"}) {
			t.Fatalf("%v %v", out, err)
		}
		_ = st.Update(r.Key, func(r *journal.Run) error { r.Notified = append(r.Notified, "blocked:main:1:1"); return nil })
		out, err = Wake(context.Background(), f, st, r.Key, 0, now)
		if err != nil || out != (Pending{}) || f.sends != 1 {
			t.Fatalf("%v %v sends %d", out, err, f.sends)
		}
	})
}
