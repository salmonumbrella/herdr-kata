package runner

import (
	"context"
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

const makerClaim = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// Spec 21.5: assertions require the checker, its saved maker claim, a new
// kind and 40 normalized evidence runes. Status is independent of assertion.
func TestFindingAssertionSelection(t *testing.T) {
	for _, tc := range []struct {
		name, kind, target, summary  string
		index                        int
		repeated, plain              bool
		wantKind, wantTarget, reason string
	}{
		{name: "failed confirm", kind: "confirm", target: makerClaim, summary: strings.Repeat("界", 40), index: 1, wantKind: "confirm", wantTarget: makerClaim},
		{name: "refute", kind: "refute", target: makerClaim, summary: strings.Repeat("界", 40), index: 1, wantKind: "refute", wantTarget: makerClaim},
		{name: "plain assertion", kind: "confirm", target: makerClaim, summary: strings.Repeat("界", 40), index: 1, plain: true, wantKind: "confirm", wantTarget: makerClaim},
		{name: "39 runes", kind: "confirm", target: makerClaim, summary: strings.Repeat("界", 39), index: 1, wantKind: "reply", wantTarget: "head", reason: "short"},
		{name: "40 collapsed", kind: "confirm", target: makerClaim, summary: strings.Repeat("界", 20) + " \t\n " + strings.Repeat("界", 19), index: 1, wantKind: "confirm", wantTarget: makerClaim},
		{name: "39 collapsed", kind: "confirm", target: makerClaim, summary: strings.Repeat("界", 19) + " \t\n " + strings.Repeat("界", 19), index: 1, wantKind: "reply", wantTarget: "head", reason: "short"},
		{name: "wrong target", kind: "refute", target: "other", summary: strings.Repeat("x", 40), index: 1, wantKind: "reply", wantTarget: "head", reason: "target"},
		{name: "repeat", kind: "confirm", target: makerClaim, summary: strings.Repeat("x", 40), index: 1, repeated: true, wantKind: "reply", wantTarget: "head", reason: "already"},
		{name: "maker", kind: "refute", target: makerClaim, summary: strings.Repeat("x", 40), index: 0, wantKind: "reply", wantTarget: "head", reason: "checker"},
		{name: "no inferred assertion", summary: strings.Repeat("x", 40), index: 1, wantKind: "reply", wantTarget: "head"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := loopWorkflow(t)
			r := loaded(t, f.s, f.uid)
			r.Findings = Findings{RootUID: "root", HeadUID: "head", MakerUID: makerClaim, Assertions: map[string][]string{}}
			if tc.repeated {
				r.Findings.Assertions[makerClaim] = []string{"confirm"}
			}
			s := &r.Steps[tc.index]
			s.Status = "failed"
			s.Result = &ResultRef{Summary: tc.summary, Assert: tc.kind, Target: tc.target}
			s.FindingContext = &FindingContext{MakerUID: makerClaim}
			mode := "typed"
			if tc.plain {
				mode = "plain"
			}
			_, e := prepareFinding(r, tc.index, mode)
			if e != nil {
				t.Fatal(e)
			}
			p, e := decodePayload(r.Ops[len(r.Ops)-1])
			if e != nil || p.ReplyKind != tc.wantKind || p.ReplyTo != tc.wantTarget || p.Mode != mode {
				t.Fatal(p, e)
			}
			if tc.reason != "" && !strings.Contains(p.Body, tc.reason) {
				t.Fatal(p.Body)
			}
			if tc.wantKind != "reply" && p.Chain.Assertion != tc.wantKind {
				t.Fatal(p)
			}
			if tc.wantKind == "reply" && p.Chain.Assertion != "" {
				t.Fatal(p)
			}
			if tc.kind != "" && !strings.Contains(p.Body, "c:"+tc.target) {
				t.Fatal("claim not named", p.Body)
			}
		})
	}
}

func TestTypedQueueFrozenRetryAndResponse(t *testing.T) {
	for _, branch := range []string{"lost", "duplicate_reply", "issue_closed", "daemon_api_too_old", "comment_not_found", "idempotency_mismatch", "wrong target", "wrong kind", "omitted edges", "omitted target", "omitted kind"} {
		t.Run(branch, func(t *testing.T) {
			p := Payload{Ref: "issue", Body: strings.Repeat("e", 40), Mode: "typed", ReplyTo: makerClaim, ReplyKind: "confirm", Chain: &ChainComment{Purpose: "checker", Target: makerClaim, Assertion: "confirm"}}
			q, s, uid, k, clock, _ := queueFixture(t, operation("typed", "comment", p))
			var requests []kata.CommentReq
			k.typedComment = func(_ context.Context, req kata.CommentReq) (kata.Comment, error) {
				requests = append(requests, req)
				c := kata.Comment{UID: "saved", ReplyToUID: makerClaim, ReplyKind: "confirm"}
				switch branch {
				case "lost":
					if len(requests) == 1 {
						return kata.Comment{}, lostReply
					}
				case "wrong target":
					c.ReplyToUID = "wrong"
				case "wrong kind":
					c.ReplyKind = "reply"
				case "omitted edges":
					c.ReplyToUID = ""
					c.ReplyKind = ""
				case "omitted target":
					c.ReplyToUID = ""
				case "omitted kind":
					c.ReplyKind = ""
				default:
					return kata.Comment{}, &kata.Error{Code: branch, Exit: 5, ExistingUID: "existing", ExistingHandle: "c:abcdef"}
				}
				return c, nil
			}
			if e := q.send(t.Context(), uid); e != nil {
				t.Fatal(e)
			}
			if branch == "lost" {
				q = NewQueue(k, s, &testGuard{}, clock)
				clock.at = clock.at.Add(time.Minute)
				if e := q.send(t.Context(), uid); e != nil {
					t.Fatal(e)
				}
				if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
					t.Fatal(requests)
				}
			}
			r := loaded(t, s, uid)
			if len(r.Ops) != 1 {
				t.Fatal("fallback appended", r.Ops)
			}
			if branch == "lost" || strings.HasPrefix(branch, "omitted ") {
				if !r.Ops[0].Done || r.Findings.HeadUID != "saved" || !reflect.DeepEqual(r.Findings.Assertions[makerClaim], []string{"confirm"}) {
					t.Fatal(r)
				}
			} else {
				if r.Inspect == "" || r.Ops[0].Done || r.Ops[0].CommentUID != "" {
					t.Fatal(r)
				}
				if branch == "duplicate_reply" && !strings.Contains(r.Inspect, "existing") {
					t.Fatal(r.Inspect)
				}
			}
			frozen, _ := decodePayload(r.Ops[0])
			if frozen.Mode != "typed" || frozen.ReplyKind != "confirm" {
				t.Fatal(frozen)
			}
		})
	}
}

func TestFindingModeFrozenAcrossSupportFlip(t *testing.T) {
	for _, first := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "typed"}[first], func(t *testing.T) {
			f := loopWorkflow(t)
			k := findingQueue(t, f)
			saveVerdict(t, f, "")
			support := first
			probes := 0
			k.typedReplies = func(context.Context) (bool, string) { probes++; return support, "unsupported" }
			if e := f.m.publishFinding(t.Context(), f.uid, 1); e != nil {
				t.Fatal(e)
			}
			before := loaded(t, f.s, f.uid).Ops[0]
			support = !first
			if e := f.m.publishFinding(t.Context(), f.uid, 1); e != nil {
				t.Fatal(e)
			}
			if _, e := f.s.Transition(f.uid, func(r *Run) error { r.Steps[0].Status = "succeeded"; return nil }); e != nil {
				t.Fatal(e)
			}
			if e := f.m.publishFinding(t.Context(), f.uid, 0); e != nil {
				t.Fatal(e)
			}
			r := loaded(t, f.s, f.uid)
			p, _ := decodePayload(r.Ops[1])
			want := "typed"
			if first {
				want = "plain"
			}
			if probes != 2 {
				t.Fatal("probes per new operation", probes)
			}
			if !reflect.DeepEqual(before, r.Ops[0]) || p.Mode != want || p.ReplyTo != before.CommentUID || p.ReplyKind != "reply" {
				t.Fatal(r.Ops, p)
			}
		})
	}
}

func TestCheckerPromptAssertionContract(t *testing.T) {
	f := loopWorkflow(t)
	r := loaded(t, f.s, f.uid)
	s := r.Steps[1]
	s.FindingContext = &FindingContext{RootUID: "root", HeadUID: "head", MakerUID: makerClaim}
	got := BuildPrompt(r, s, "/state/result.json", "/state/report", true)
	for _, want := range []string{"--assert <confirm|refute>", "--target c:" + makerClaim, "specific claim", "40 Unicode", "never pad"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
	s.FindingContext = nil
	if strings.Contains(BuildPrompt(r, s, "/state/result.json", "/state/report", true), "--assert") {
		t.Fatal("assert without saved maker claim")
	}
	s = r.Steps[0]
	s.FindingContext = &FindingContext{MakerUID: makerClaim}
	if strings.Contains(BuildPrompt(r, s, "/state/result.json", "/state/report", true), "--assert") {
		t.Fatal("maker invited to assert")
	}
}

func TestUntargetedCommentRejectsUnexpectedEdge(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t, operation("root", "comment", Payload{Ref: "issue", Body: "root", Mode: "typed"}))
	k.typedComment = func(context.Context, kata.CommentReq) (kata.Comment, error) {
		return kata.Comment{UID: "unexpected", ReplyToUID: makerClaim, ReplyKind: "reply"}, nil
	}
	if e := q.send(t.Context(), uid); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, s, uid)
	if r.Inspect != "typed reply response mismatch" || r.Ops[0].Done {
		t.Fatal(r)
	}
}

func TestParkedMakerContinuesItsOwnHead(t *testing.T) {
	f := loopWorkflow(t)
	r := loaded(t, f.s, f.uid)
	r.Findings = Findings{RootUID: "root", HeadUID: "previous-maker", MakerUID: "previous-maker"}
	r.Steps[0].Attempt = 3
	r.Steps[0].Status = "succeeded"
	if _, e := prepareFinding(r, 0, "typed"); e != nil {
		t.Fatal(e)
	}
	p, e := decodePayload(r.Ops[0])
	if e != nil || p.ReplyTo != "previous-maker" || p.ReplyKind != "reply" {
		t.Fatal(p, e)
	}
}

func TestPlainFindingRetryAfterUpgrade(t *testing.T) {
	p := Payload{Ref: "issue", Body: "confirms c:" + makerClaim, Mode: "plain", ReplyTo: makerClaim, ReplyKind: "confirm", Chain: &ChainComment{Purpose: "checker", Target: makerClaim, Assertion: "confirm"}}
	q, s, uid, k, clock, _ := queueFixture(t, operation("plain", "comment", p))
	calls := 0
	k.typedReplies = func(context.Context) (bool, string) { t.Fatal("reprobed a frozen operation"); return true, "" }
	k.typedComment = func(_ context.Context, req kata.CommentReq) (kata.Comment, error) {
		calls++
		if req.ReplyTo != "" || req.Body != "confirms c:"+makerClaim || req.IdempotencyKey != "key-plain" {
			t.Fatal(req)
		}
		if calls == 1 {
			return kata.Comment{}, lostReply
		}
		return kata.Comment{UID: "plain-comment"}, nil
	}
	if e := q.send(t.Context(), uid); e != nil {
		t.Fatal(e)
	}
	clock.at = clock.at.Add(time.Minute)
	q = NewQueue(k, NewStore(s.fsys, s.layout), &testGuard{}, clock)
	if e := q.send(t.Context(), uid); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, s, uid)
	if calls != 2 || !r.Ops[0].Done || r.Findings.HeadUID != "plain-comment" || len(r.Findings.Assertions[makerClaim]) != 1 {
		t.Fatal(calls, r)
	}
}

func TestAssertionRetainsPublishedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, summary string
		wantKind      string
	}{
		{"leading whitespace", strings.Repeat(" ", 17<<10) + strings.Repeat("界", 40), "confirm"},
		{"evidence beyond cap", strings.Repeat("界", 20) + strings.Repeat(" ", 17<<10) + strings.Repeat("界", 20), "reply"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := loopWorkflow(t)
			r := loaded(t, f.s, f.uid)
			r.Findings = Findings{RootUID: "root", HeadUID: "head", MakerUID: makerClaim}
			s := &r.Steps[1]
			s.Status = "failed"
			s.Note = strings.Repeat("n", 280)
			s.FindingContext = &FindingContext{MakerUID: makerClaim}
			s.Result = &ResultRef{Summary: tc.summary, Assert: "confirm", Target: makerClaim}
			if _, e := prepareFinding(r, 1, "typed"); e != nil {
				t.Fatal(e)
			}
			p, e := decodePayload(r.Ops[0])
			if e != nil || p.ReplyKind != tc.wantKind {
				t.Fatal(p, e)
			}
			if tc.wantKind == "confirm" && !strings.Contains(p.Body, strings.Repeat("界", 40)) {
				t.Fatal("published assertion lost evidence", p.Body)
			}
			if tc.wantKind == "reply" && p.Chain.Assertion != "" {
				t.Fatal("truncated evidence consumed assertion", p)
			}
		})
	}
}

func TestCheckerRetainedReporterUsesCompatiblePrompt(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(map[bool]string{false: "old reporter", true: "new reporter"}[supported], func(t *testing.T) {
			f := workflowExecution(t, defs.Step{Key: "make", Kind: "command", Command: "true"}, defs.Step{Key: "check", Kind: "prompt", Prompt: "Check", After: []string{"make"}, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":1}}`)})
			reporter := filepath.Join(t.TempDir(), "retained-report")
			help := "-result -status -summary"
			if supported {
				help += " -assert -target"
			}
			script := "#!/bin/sh\n[ \"$#\" = 2 ] && [ \"$1\" = report ] && [ \"$2\" = --help ] || exit 2\nprintf '%s\\n' '" + help + "' >&2\n"
			if e := os.WriteFile(reporter, []byte(script), 0700); e != nil {
				t.Fatal(e)
			}
			r := loaded(t, f.s, f.uid)
			r.Herdr.ReportBin = reporter
			r.ActiveStep = "check"
			r.Steps[1].Attempt = 3
			r.Steps[1].FindingContext = &FindingContext{RootUID: "root", HeadUID: "head", MakerUID: makerClaim}
			if e := f.m.writePrompt(r, 1); e != nil {
				t.Fatal(e)
			}
			body, e := os.ReadFile(filepath.Join(f.s.layout.AttemptDir(f.uid, "check", 3), "prompt.md"))
			if e != nil {
				t.Fatal(e)
			}
			if strings.Contains(string(body), "--assert") != supported || !strings.Contains(string(body), reporter) {
				t.Fatal("prompt incompatible with retained reporter", string(body))
			}
		})
	}
}

func TestCheckerSubmissionKeepsFrozenReporterCapabilities(t *testing.T) {
	for _, initiallySupported := range []bool{false, true} {
		t.Run(map[bool]string{false: "probe recovers", true: "probe fails later"}[initiallySupported], func(t *testing.T) {
			f := workflowExecution(t, defs.Step{Key: "make", Kind: "command", Command: "true"}, defs.Step{Key: "check", Kind: "prompt", Prompt: "Check", After: []string{"make"}, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":1}}`)})
			reporter := filepath.Join(t.TempDir(), "retained-report")
			success := "printf '%s\\n' '-assert -target'\n"
			first, later := "exit 1\n", success
			if initiallySupported {
				first, later = success, "exit 1\n"
			}
			script := "#!/bin/sh\nif [ ! -f \"$0.seen\" ]; then\n: > \"$0.seen\"\n" + first + "else\n" + later + "fi\n"
			if err := os.WriteFile(reporter, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			r, err := f.s.Transition(f.uid, func(r *Run) error {
				r.Herdr.ReportBin, r.ActiveStep = reporter, "check"
				r.ExpiresAt = time.Now().Add(time.Minute)
				r.Steps[1].FindingContext = &FindingContext{RootUID: "root", HeadUID: "head", MakerUID: makerClaim}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = f.m.writePrompt(r, 1); err != nil {
				t.Fatal(err)
			}
			prompt := filepath.Join(f.s.layout.AttemptDir(f.uid, "check", 1), "prompt.md")
			before, err := os.ReadFile(prompt)
			if err != nil || strings.Contains(string(before), "--assert") != initiallySupported {
				t.Fatal(string(before), err)
			}
			if err = f.m.launch(t.Context(), f.uid, 1); err != nil {
				t.Fatal(err)
			}
			if err = f.m.submitStep(t.Context(), f.uid, 1); err != nil {
				t.Fatal("reporter probe change prevented submission", err)
			}
			after, err := os.ReadFile(prompt)
			saved := loaded(t, f.s, f.uid)
			if err != nil || string(after) != string(before) || saved.Steps[1].Submission != Accepted || f.count("agent.prompt") != 1 {
				t.Fatal("frozen prompt was not submitted once", saved, string(after), err)
			}
		})
	}
}

func TestDuplicateInspectionAllowsMissingDiagnostics(t *testing.T) {
	for _, tc := range []struct{ name, uid, handle, want string }{
		{"absent", "", "", "duplicate_reply: inspection required"},
		{"cross issue", "existing", "abcd:abcdef", "duplicate_reply: inspection required; existing abcd:abcdef existing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, store, uid, k, _, _ := queueFixture(t, operation("reply", "comment", Payload{Mode: "typed", ReplyTo: makerClaim, ReplyKind: "reply"}))
			k.typedComment = func(context.Context, kata.CommentReq) (kata.Comment, error) {
				return kata.Comment{}, &kata.Error{Code: "duplicate_reply", Exit: 5, ExistingUID: tc.uid, ExistingHandle: tc.handle}
			}
			if err := q.send(t.Context(), uid); err != nil {
				t.Fatal(err)
			}
			r := loaded(t, store, uid)
			if r.Inspect != tc.want || r.Ops[0].Done || r.Ops[0].CommentUID != "" || len(r.Ops) != 1 {
				t.Fatal(r)
			}
		})
	}
}
