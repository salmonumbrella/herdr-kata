package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"path/filepath"
)

const runParentUID = "01K00000000000000000000001"

func TestAdmitRunParent(t *testing.T) {
	for _, tc := range []struct {
		name, status, project, uid string
		err                        error
		result                     sched.AdmitResult
		reason                     string
	}{
		{name: "closed", status: "closed", project: "project", uid: runParentUID, result: sched.Dropped, reason: "parent_closed"},
		{name: "outside", status: "open", project: "elsewhere", uid: runParentUID, result: sched.Dropped, reason: "parent_outside_project"},
		{name: "missing", err: &kata.Error{Code: "not_found"}, result: sched.NotAdmitted, reason: "parent_unavailable"},
		{name: "transport", err: lostReply, result: sched.NotAdmitted},
		{name: "wrong identity", status: "open", project: "project", uid: "other", result: sched.NotAdmitted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdmission(t)
			f.d.Def.Options = json.RawMessage(`{"run_parent":"` + runParentUID + `"}`)
			reads := 0
			f.k.show = func(_ context.Context, ref string) (kata.IssueView, error) {
				reads++
				if ref != runParentUID {
					t.Fatal(ref)
				}
				return kata.IssueView{UID: tc.uid, Status: tc.status, ProjectUID: tc.project}, tc.err
			}
			f.k.create = func(context.Context, kata.CreateReq) (kata.IssueView, error) {
				t.Error("created after failed parent check")
				return kata.IssueView{}, nil
			}
			// An absent parent must return before queue delivery, without a journal.
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.start(t)
			f.m.executions.Store(f.uid, struct{}{})
			result, err := f.m.Admit(ctx, f.d)
			if result != tc.result || reads != 1 || tc.reason != "" && (err == nil || !strings.Contains(err.Error(), tc.reason)) {
				t.Fatal(result, err, reads)
			}
			if _, err := f.s.Load(f.uid); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal("journal created", err)
			}
			if f.m.FreeSlots() != 2 {
				t.Fatal("reservation leaked")
			}
		})
	}
}
func TestAdmitFreezesRunParentAndLabel(t *testing.T) {
	for _, parent := range []string{"", runParentUID} {
		t.Run("parent="+parent, func(t *testing.T) {
			f := newAdmission(t)
			if parent != "" {
				f.d.Def.Options = json.RawMessage(`{"run_parent":"` + parent + `"}`)
			}
			f.k.show = func(_ context.Context, ref string) (kata.IssueView, error) {
				if ref != runParentUID {
					t.Fatal(ref)
				}
				return kata.IssueView{UID: ref, Status: "open", ProjectUID: "project"}, nil
			}
			f.k.create = func(_ context.Context, req kata.CreateReq) (kata.IssueView, error) {
				if req.Parent != parent || !reflect.DeepEqual(req.Labels, []string{"herdr-kata:run"}) {
					t.Error(req)
				}
				return kata.IssueView{UID: "child"}, nil
			}
			f.start(t)
			r := f.admit(t)
			var p Payload
			_ = json.Unmarshal(r.Ops[0].Payload, &p)
			b, _ := json.Marshal(p.Create)
			if !strings.Contains(string(b), "herdr-kata:run") {
				t.Fatal("label not durable", string(b))
			}
		})
	}
}

func TestPreparedRunParentRecoveryDoesNotReadParent(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "legacy"}[legacy], func(t *testing.T) {
			f := newAdmission(t)
			f.d.Def.Options = json.RawMessage(`{"run_parent":"` + runParentUID + `"}`)
			target, err := f.m.pin()
			if err != nil {
				t.Fatal(err)
			}
			r, err := f.m.prepare(f.d, f.uid, "2026-01-02T03:04:05.000000006Z", target)
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				var p Payload
				_ = json.Unmarshal(r.Ops[0].Payload, &p)
				p.Create.Parent = ""
				p.Create.Labels = nil
				r.Ops[0] = newOp("create", "create", p, r.Ops[0].Key)
			}
			before := append([]byte(nil), r.Ops[0].Payload...)
			if err := f.s.Create(r); err != nil {
				t.Fatal(err)
			}
			f.k.run = func(context.Context, string) (kata.Run, error) {
				t.Error("authority re-read on recovery")
				return kata.Run{}, nil
			}
			f.k.show = func(context.Context, string) (kata.IssueView, error) {
				t.Error("parent re-read on recovery")
				return kata.IssueView{}, nil
			}
			f.k.create = func(_ context.Context, req kata.CreateReq) (kata.IssueView, error) {
				var p Payload
				_ = json.Unmarshal(before, &p)
				if !reflect.DeepEqual(req, *p.Create) {
					t.Error("create changed", req, p.Create)
				}
				return kata.IssueView{UID: "child"}, nil
			}
			f.start(t)
			f.m.executions.Store(f.uid, struct{}{})
			if err := f.m.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			saved := loaded(t, f.s, f.uid)
			if saved.Phase != Admitted || !reflect.DeepEqual(saved.Ops[0].Payload, json.RawMessage(before)) {
				t.Fatal(saved)
			}
		})
	}
}

func TestRunParentCreateRetryAndExpiry(t *testing.T) {
	for _, code := range []string{"not_found", "root_signing_key_unavailable"} {
		for _, repair := range []bool{true, false} {
			t.Run(code+map[bool]string{true: " repair", false: " unresolved"}[repair], func(t *testing.T) {
				req := kata.CreateReq{Title: "Run", Body: "Frozen", Parent: runParentUID, Labels: []string{"herdr-kata:run"}}
				op := newOp("create", "create", Payload{Create: &req}, "run-key")
				q, s, uid, k, c, g := queueFixture(t, op)
				calls := 0
				k.create = func(_ context.Context, got kata.CreateReq) (kata.IssueView, error) {
					calls++
					want := req
					want.IdempotencyKey = "run-key"
					if !reflect.DeepEqual(got, want) {
						t.Error("request changed", got)
					}
					if repair && calls > 1 {
						return kata.IssueView{UID: "child"}, nil
					}
					return kata.IssueView{}, &kata.Error{Code: code}
				}
				deliver(t, q, uid)
				r := loaded(t, s, uid)
				if r.Ops[0].Inspect != "" || r.Ops[0].Done || r.Ops[0].SigningRefused != (code == "root_signing_key_unavailable") {
					t.Fatal(r)
				}
				if code == "not_found" && r.Reason != "parent_unavailable" {
					t.Fatal("parent wait invisible", r.Reason)
				}
				if blocked, err := s.SigningBlocked(); err != nil || blocked != (code == "root_signing_key_unavailable") {
					t.Fatal(blocked, err)
				}
				// Reconstruct the delivery side to prove the saved intent survives restart.
				s = NewStore(s.fsys, s.layout)
				q = NewQueue(k, s, g, c)
				c.at = r.Ops[0].NextAt
				deliver(t, q, uid)
				r = loaded(t, s, uid)
				if repair {
					if !r.Ops[0].Done || r.IssueUID != "child" || r.Reason != "" || r.Ops[0].SigningRefused {
						t.Fatal(r)
					}
					return
				}
				if code == "not_found" {
					c.at = r.Ops[0].FirstSent.Add(7*24*time.Hour - time.Second)
					deliver(t, q, uid)
					if loaded(t, s, uid).Ops[0].Inspect != "" {
						t.Fatal("early inspection")
					}
					c.at = loaded(t, s, uid).Ops[0].NextAt
					deliver(t, q, uid)
					if loaded(t, s, uid).Ops[0].Inspect == "" {
						t.Fatal("missing expiry inspection")
					}
				}
			})
		}
	}
}
func TestCloseThrottleRetriesFrozenMessage(t *testing.T) {
	for _, code := range []string{"sibling_throttle", "duplicate_message"} {
		t.Run(code, func(t *testing.T) {
			req := kata.CloseReq{Ref: "child", IfMatch: "1", Reason: "done", Message: "Verified all command steps completed successfully.", Evidence: []string{"test:true"}}
			q, s, uid, k, c, _ := queueFixture(t, newOp("close", "close", Payload{Close: &req}, "close-key"))
			calls := 0
			k.close = func(_ context.Context, got kata.CloseReq) error {
				calls++
				want := req
				want.IdempotencyKey = "close-key"
				if !reflect.DeepEqual(got, want) {
					t.Fatal(got)
				}
				if calls == 1 {
					return &kata.Error{Code: code, Exit: 1}
				}
				return nil
			}
			deliver(t, q, uid)
			r := loaded(t, s, uid)
			if r.Ops[0].Inspect != "" || r.Ops[0].Done {
				t.Fatal(r)
			}
			c.at = r.Ops[0].NextAt
			deliver(t, q, uid)
			if calls != 2 || !loaded(t, s, uid).Ops[0].Done {
				t.Fatal("close did not recover", calls)
			}
		})
	}
}

type parentProbeGuard struct {
	RouteChecker
	probes int
	probe  *kata.RouteGuard
}

func (k *parentProbeGuard) ProbeAccess(ctx context.Context) error {
	k.probes++
	return k.probe.ProbeAccess(ctx)
}
func TestUnavailableRunParentProbesAccess(t *testing.T) {
	f := newAdmission(t)
	f.d.Def.Options = json.RawMessage(`{"run_parent":"` + runParentUID + `"}`)
	probe := kata.New(config.Kata{Project: "example-project"}, f.s.layout.Root, func(context.Context, []string, []string, string, []byte) ([]byte, []byte, int, error) {
		return []byte(`{"error":{"code":"not_found","message":"unavailable"}}`), nil, 6, nil
	}, nil)
	k := &parentProbeGuard{RouteChecker: f.g, probe: kata.NewRouteGuard(config.Config{Kata: config.Kata{Project: "example-project"}}, probe, state.OSFS{}, f.s.layout)}
	f.m.guard = k
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{}, &kata.Error{Code: "not_found"}
	}
	result, err := f.m.Admit(t.Context(), f.d)
	if result != sched.NotAdmitted || err == nil || k.probes != 1 {
		t.Fatal(result, err, k.probes)
	}
	var route struct {
		AccessLost bool   `json:"access_lost"`
		Allowed    bool   `json:"allowed"`
		Error      string `json:"error"`
		Project    string `json:"project"`
	}
	if err := state.ReadJSON(state.OSFS{}, filepath.Join(f.s.layout.Root, "route.json"), 4096, &route); err != nil || !route.AccessLost || route.Allowed {
		t.Fatal("access loss not visible", route, err)
	}
	if _, err := f.s.Load(f.uid); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}
func TestRunParentForceNewKeepsParentAndLabel(t *testing.T) {
	f := newAdmission(t)
	f.d.Def.Options = json.RawMessage(`{"run_parent":"` + runParentUID + `"}`)
	f.k.show = func(_ context.Context, ref string) (kata.IssueView, error) {
		if ref == runParentUID {
			return kata.IssueView{UID: ref, ProjectUID: "project", Status: "open"}, nil
		}
		if ref != "candidate" {
			t.Fatal(ref)
		}
		return kata.IssueView{UID: "candidate"}, nil
	}
	var calls atomic.Int32
	f.k.create = func(_ context.Context, req kata.CreateReq) (kata.IssueView, error) {
		n := calls.Add(1)
		if req.Parent != runParentUID || !reflect.DeepEqual(req.Labels, []string{"herdr-kata:run"}) || req.IdempotencyKey != "herdr-kata:"+f.uid || req.ForceNew != (n == 2) {
			t.Error(req)
		}
		if n == 1 {
			return kata.IssueView{}, &kata.Error{Code: "duplicate_candidates", CandidateUIDs: []string{"candidate"}}
		}
		return kata.IssueView{UID: "child"}, nil
	}
	f.start(t)
	r := f.admit(t)
	if calls.Load() != 2 || r.IssueUID != "child" {
		t.Fatal(calls.Load(), r)
	}
}

func TestRunParentAccessLossFencesRoute(t *testing.T) {
	f := newAdmission(t)
	f.d.Def.Options = json.RawMessage(`{"run_parent":"` + runParentUID + `"}`)
	f.k.show = func(_ context.Context, ref string) (kata.IssueView, error) {
		if ref != runParentUID {
			t.Fatalf("unexpected parent %q", ref)
		}
		return kata.IssueView{}, &kata.Error{Code: "not_found"}
	}
	cfg := config.Config{Executor: "worker", Kata: config.Kata{Bin: "/fake", Project: "example-project", Actor: "worker"}}
	l := state.Layout{Root: t.TempDir()}
	refused := false
	cli := kata.New(cfg.Kata, l.Root, func(_ context.Context, args, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
		var body string
		switch command := strings.Join(args[8:], " "); command {
		case "health":
			body = `{"schema_version":33}`
		case "cron capabilities":
			if refused {
				return []byte(`{"error":{"code":"not_found","message":"access withheld"}}`), nil, 1, nil
			}
			body = `{"project_uid":"project","event_features":["cron_v1"]}`
		case "federation identity":
			body = `{"instance_uid":"instance","auth":{"actor":"worker"}}`
		case "daemon diagnose --expect-project-uid project":
			body = `{"state":"ready","endpoint":"local","project_uid":"project"}`
		default:
			t.Fatalf("unexpected route command %q", command)
		}
		return []byte(body), nil, 0, nil
	}, nil)
	guard := kata.NewRouteGuard(cfg, cli, state.OSFS{}, l)
	if err := guard.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.m.guard = guard
	refused = true
	result, err := f.m.runParentCheck(t.Context(), f.d, "project", map[string]string{})
	if result != sched.NotAdmitted || err == nil || !strings.Contains(err.Error(), "parent_unavailable") {
		t.Fatal(result, err)
	}
	var status kata.RouteStatus
	if err := state.ReadJSON(state.OSFS{}, l.RouteFile(), 4096, &status); err != nil {
		t.Fatal(err)
	}
	if guard.Allowed() || status.Allowed || !status.AccessLost {
		t.Fatalf("parent access loss left route enabled: %+v", status)
	}
}
