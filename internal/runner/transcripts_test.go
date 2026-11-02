package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"hegel.dev/go/hegel"
)

type transcriptWire struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
}

func transcriptStep(agent, id string) StepState {
	return StepState{Key: "main", Kind: "prompt", Attempt: 1, Submission: Accepted, Herdr: &HerdrRef{Session: &herdr.AgentSession{Source: "herdr:" + agent, Agent: agent, Kind: "id", Value: id}}}
}

// These assertions observe the frozen native report: dropping extraction,
// accepting a command locator, or changing identity/order must change the wire.
func settlementTranscripts(t testing.TB, r *Run) []transcriptWire {
	t.Helper()
	for _, op := range SettleOps(r, nil, settleTime) {
		if op.Kind != "observe" {
			continue
		}
		var p struct {
			Observation struct {
				Transcripts []transcriptWire `json:"transcripts"`
			}
		}
		if err := json.Unmarshal(op.Payload, &p); err != nil {
			t.Fatal(err)
		}
		return p.Observation.Transcripts
	}
	t.Fatal("missing native observation")
	return nil
}

func TestSettleTranscriptsPromptStepsAndRetry(t *testing.T) {
	const first = "00000000-0000-4000-8000-000000000001"
	const retry = "00000000-0000-4000-8000-000000000002"
	const second = "00000000-0000-4000-8000-000000000003"
	for _, tc := range []struct {
		name    string
		phase   Phase
		resumed bool
	}{
		{"settled", Settled, false}, {"held", Held, false}, {"handed_off", HandedOff, false}, {"resumed-final", Settled, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := settleFixture()
			r.Phase, r.Outcome = tc.phase, "failed"
			if tc.resumed {
				r.Resume = &ResumeState{UID: "resume-request"}
			}
			r.PastAttempts = []StepState{transcriptStep("codex", first)}
			r.Steps = []StepState{transcriptStep("codex", retry), transcriptStep("claude", second), transcriptStep("codex", first)}
			r.PastAttempts[0].Key = "first"
			r.Steps[0].Key, r.Steps[1].Key, r.Steps[2].Key = "first", "second", "shared"
			r.Steps[0].Attempt = 2
			want := []transcriptWire{{"codex", first}, {"codex", retry}, {"claude", second}}
			if got := settlementTranscripts(t, r); !reflect.DeepEqual(got, want) {
				t.Fatalf("transcripts=%+v want %+v", got, want)
			}
		})
	}
}

func TestSettleTranscriptsRejectUnusableLocators(t *testing.T) {
	const id = "abcdefab-0000-4000-8000-000000000001"
	for _, name := range []string{"missing", "unsupported", "path", "url", "unsafe", "non-uuid", "prefixed", "command", "unrelated-main", "past-without-ref"} {
		t.Run(name, func(t *testing.T) {
			r := settleFixture()
			s := transcriptStep("codex", id)
			switch name {
			case "missing":
				s.Herdr.Session = nil
			case "unsupported":
				s.Herdr.Session.Agent = "other"
			case "path":
				s.Herdr.Session.Kind = "path"
				s.Herdr.Session.Value = "/tmp/session.jsonl"
			case "url":
				s.Herdr.Session.Kind = "url"
				s.Herdr.Session.Value = "https://agent.example/session/" + id
			case "unsafe":
				s.Herdr.Session.Value = "../" + id
			case "non-uuid":
				s.Herdr.Session.Value = "session-1"
			case "prefixed":
				s.Herdr.Session.Value = "codex:" + id
			case "command":
				s.Kind = "command"
			case "unrelated-main":
				r.Herdr = *s.Herdr
				s.Herdr = nil
				s.Submission = None
			case "past-without-ref":
				r.Herdr = *s.Herdr
				s.Herdr = nil
				r.PastAttempts = []StepState{s}
				s.Kind = "command"
			}
			r.Steps = []StepState{s}
			if got := settlementTranscripts(t, r); len(got) != 0 {
				t.Fatalf("unusable locator reported: %+v", got)
			}
			_, p := settlePayload(t, SettleOps(r, nil, settleTime), "observe")
			wire, _ := json.Marshal(p.Observation)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(wire, &fields); err != nil {
				t.Fatal(err)
			}
			if _, ok := fields["transcripts"]; ok {
				t.Fatal("empty transcripts changed legacy wire", string(wire))
			}
		})
	}
}

func TestSettleTranscriptsLegacySharedAttempt(t *testing.T) {
	r := settleFixture()
	s := transcriptStep("codex", "ABCDEFAB-0000-4000-8000-000000000001")
	r.Herdr = *s.Herdr
	s.Herdr = nil
	r.Steps = []StepState{s}
	want := []transcriptWire{{"codex", "abcdefab-0000-4000-8000-000000000001"}}
	if got := settlementTranscripts(t, r); !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy transcripts=%+v", got)
	}
}

func TestSettleTranscriptsBoundDoesNotBlockOutcome(t *testing.T) {
	r := settleFixture()
	r.Steps = nil
	for i := range 70 {
		r.PastAttempts = append(r.PastAttempts, transcriptStep("codex", fmt.Sprintf("00000000-0000-4000-8000-%012x", i)))
	}
	got := settlementTranscripts(t, r)
	if len(got) != 64 || got[0].SessionID != "00000000-0000-4000-8000-000000000000" || got[63].SessionID != "00000000-0000-4000-8000-00000000003f" {
		t.Fatalf("bounded transcripts=%+v", got)
	}
	_, p := settlePayload(t, SettleOps(r, resultFixture(), settleTime), "observe")
	if p.Observation.Status != "succeeded" {
		t.Fatalf("outcome blocked: %+v", p.Observation)
	}
}

// Lists use Hegel's full default size domain. They materialize directly as
// attempt records; UUIDs cover native identity and injected repetition checks
// duplicate handling without requiring random collisions.
func generatedTranscriptRun(ht *hegel.T) *Run {
	r := settleFixture()
	r.Steps = nil
	for _, id := range hegel.Draw(ht, hegel.Lists(hegel.UUIDs())) {
		agent := hegel.Draw(ht, hegel.SampledFrom([]string{"codex", "claude"}))
		value := id.String()
		if hegel.Draw(ht, hegel.Booleans()) {
			value = strings.ToUpper(value)
		}
		r.Steps = append(r.Steps, transcriptStep(agent, value))
	}
	split := hegel.Draw(ht, hegel.Integers(0, len(r.Steps)))
	r.PastAttempts = slices.Clone(r.Steps[:split])
	r.Steps = r.Steps[split:]
	return r
}

func TestTranscriptPropertyDeterministicDeduplication(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		r := generatedTranscriptRun(ht)
		got := runTranscripts(r)
		r.Steps = append(r.Steps, slices.Clone(r.Steps)...)
		r.PastAttempts = append(r.PastAttempts, slices.Clone(r.PastAttempts)...)
		if again := runTranscripts(r); !reflect.DeepEqual(got, again) {
			ht.Fatalf("duplicate attempts changed ordered identities: %+v != %+v", got, again)
		}
	})
}

func TestTranscriptPropertyBounded(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		r := generatedTranscriptRun(ht)
		var identities []string
		for _, s := range append(slices.Clone(r.PastAttempts), r.Steps...) {
			identities = append(identities, s.Herdr.Session.Agent+":"+strings.ToLower(s.Herdr.Session.Value))
		}
		slices.Sort(identities)
		want := min(64, len(slices.Compact(identities)))
		if got := runTranscripts(r); len(got) != want {
			ht.Fatalf("reported %d sessions, want bounded set cardinality %d", len(got), want)
		}
	})
}

func TestTranscriptPropertyPreservesValidMembership(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		r := generatedTranscriptRun(ht)
		inputs := append(slices.Clone(r.PastAttempts), r.Steps...)
		got := runTranscripts(r)
		// Every input in the first 64 positions must fit; generated duplicate
		// UUIDs remain valid and must not cause an identity to disappear.
		for i, s := range inputs {
			want := kata.Transcript{Agent: s.Herdr.Session.Agent, SessionID: strings.ToLower(s.Herdr.Session.Value)}
			if i < 64 && !slices.Contains(got, want) {
				ht.Fatalf("valid input missing: %+v in %+v", want, got)
			}
		}
		for _, item := range got {
			found := slices.ContainsFunc(inputs, func(s StepState) bool {
				return item.Agent == s.Herdr.Session.Agent && item.SessionID == strings.ToLower(s.Herdr.Session.Value)
			})
			if !found {
				ht.Fatalf("invented session %+v", item)
			}
		}
	})
}

func TestTranscriptPropertyRejectsNonUUIDs(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		r := settleFixture()
		// The unsafe prefix makes every arbitrary UTF-8 suffix an invalid locator.
		s := transcriptStep("codex", "../"+hegel.Draw(ht, hegel.Text()))
		s.Attempt = hegel.Draw(ht, hegel.Integers(1, math.MaxInt))
		r.Steps = []StepState{s}
		if got := settlementTranscripts(t, r); len(got) != 0 {
			ht.Fatalf("unsafe locator accepted: %+v", got)
		}
	})
}

func TestSettleTranscriptsMarksIncomplete(t *testing.T) {
	for _, reason := range []string{"missing", "unsupported", "bound"} {
		t.Run(reason, func(t *testing.T) {
			r := settleFixture()
			s := transcriptStep("codex", "00000000-0000-4000-8000-000000000001")
			switch reason {
			case "missing":
				s.Herdr.Session = nil
			case "unsupported":
				s.Herdr.Session.Agent = "other"
			}
			r.Steps = []StepState{s}
			if reason == "bound" {
				for i := range 65 {
					r.Steps = append(r.Steps, transcriptStep("claude", fmt.Sprintf("00000000-0000-4000-8000-%012x", i)))
				}
			}
			_, p := settlePayload(t, SettleOps(r, nil, settleTime), "observe")
			raw, _ := json.Marshal(p.Observation)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["transcripts_incomplete"]) != "true" {
				t.Fatalf("missing incomplete marker: %s", raw)
			}
		})
	}
}

type transcriptPreparingKata struct {
	*fakeKata
	prepare func(kata.Observation) (kata.Observation, error)
}

func (k *transcriptPreparingKata) PrepareObservation(_ context.Context, o kata.Observation) (kata.Observation, error) {
	return k.prepare(o)
}

func TestObservationTranscriptCompatibilityFrozenBeforeSend(t *testing.T) {
	for _, supported := range []bool{true, false} {
		t.Run(fmt.Sprint(supported), func(t *testing.T) {
			o := kata.Observation{Status: "failed", Transcripts: []kata.Transcript{{Agent: "codex", SessionID: "00000000-0000-4000-8000-000000000001"}}, TranscriptsIncomplete: true}
			q, s, uid, k, c, _ := queueFixture(t, operation("final", "observe", Payload{Observation: &o}))
			preparations, sends := 0, 0
			q.k = &transcriptPreparingKata{fakeKata: k, prepare: func(got kata.Observation) (kata.Observation, error) {
				preparations++
				if !supported {
					got.Transcripts = nil
					got.TranscriptsIncomplete = false
				}
				return got, nil
			}}
			k.observe = func(_ context.Context, _ string, got kata.Observation) (kata.Run, bool, error) {
				sends++
				saved := loaded(t, s, uid).Ops[0]
				p, err := decodePayload(saved)
				if err != nil {
					t.Fatal(err)
				}
				if saved.FirstSent == nil || !reflect.DeepEqual(*p.Observation, got) || (len(got.Transcripts) > 0) != supported {
					t.Fatalf("wire not durably frozen: %+v vs %+v", p.Observation, got)
				}
				if sends == 1 {
					return kata.Run{}, false, &kata.Error{Code: "unavailable", Exit: 7}
				}
				return kata.Run{}, false, nil
			}
			if err := q.send(t.Context(), uid); err != nil {
				t.Fatal(err)
			}
			r := loaded(t, s, uid)
			c.at = r.Ops[0].NextAt
			if err := q.send(t.Context(), uid); err != nil {
				t.Fatal(err)
			}
			if preparations != 1 || sends != 2 || !loaded(t, s, uid).Ops[0].Landed() {
				t.Fatalf("preparations=%d sends=%d", preparations, sends)
			}
		})
	}
}

func TestObservationTranscriptReadbackRequiresReportedMembership(t *testing.T) {
	for _, remote := range []string{"matching", "additional", "missing", "missing-incomplete", "legacy"} {
		t.Run(remote, func(t *testing.T) {
			o := kata.Observation{Status: "failed", Summary: kata.Summary{Version: 1, Message: "saved failure"}, Teammate: new("herdr-kata"), Transcripts: []kata.Transcript{{Agent: "codex", SessionID: "abcdefab-0000-4000-8000-000000000001"}}, TranscriptsIncomplete: true}
			if remote == "legacy" {
				o.Transcripts = nil
				o.TranscriptsIncomplete = false
			}
			p := Payload{Observation: &o}
			op := operation("final", "observe", p)
			q, _, uid, k, _, _ := queueFixture(t, op)
			var got kata.Run
			raw, _ := json.Marshal(o)
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			got.UID = uid
			switch remote {
			case "additional":
				got.Transcripts = append(got.Transcripts, kata.Transcript{Agent: "claude", SessionID: "00000000-0000-4000-8000-000000000002"})
			case "missing":
				got.Transcripts = nil
			case "missing-incomplete":
				got.TranscriptsIncomplete = false
			}
			q.k = &admissionKata{fakeKata: k, run: func(context.Context, string) (kata.Run, error) { return got, nil }}
			matches, _, err := q.readBack(t.Context(), uid, op, p, lostReply)
			if err != nil {
				t.Fatal(err)
			}
			want := remote == "matching" || remote == "additional" || remote == "legacy"
			if matches != want {
				t.Fatalf("readback %s: matched=%t want %t", remote, matches, want)
			}
		})
	}
}

func TestSettleTranscriptsLeaveRoomForOutcomeEnvelope(t *testing.T) {
	r := settleFixture()
	r.Steps = nil
	for i := range 64 {
		r.Steps = append(r.Steps, transcriptStep("claude", fmt.Sprintf("00000000-0000-4000-8000-%012x", i)))
	}
	result := resultFixture()
	result.Summary = strings.Repeat("x", maxSummaryBytes)
	_, p := settlePayload(t, SettleOps(r, result, settleTime), "observe")
	raw, err := json.Marshal(p.Observation)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 64<<10 {
		t.Fatalf("sessions push ordinary outcome beyond envelope: %d bytes", len(raw))
	}
	if len(p.Observation.Transcripts) != 64 || p.Observation.Status != "succeeded" || p.Observation.Summary.Message == "" {
		t.Fatalf("outcome or sessions lost: %+v", p.Observation)
	}
}

func TestSettleTranscriptsIdentityIncludesAgentAndNormalizedUUID(t *testing.T) {
	r := settleFixture()
	r.Steps = []StepState{
		transcriptStep("codex", "ABCDEFAB-0000-4000-8000-000000000001"),
		transcriptStep("codex", "abcdefab-0000-4000-8000-000000000001"),
		transcriptStep("claude", "abcdefab-0000-4000-8000-000000000001"),
	}
	want := []transcriptWire{{"codex", "abcdefab-0000-4000-8000-000000000001"}, {"claude", "abcdefab-0000-4000-8000-000000000001"}}
	if got := settlementTranscripts(t, r); !reflect.DeepEqual(got, want) {
		t.Fatalf("identity set=%+v want %+v", got, want)
	}
}

func TestAdmissionObservationDoesNotReportKnownTranscripts(t *testing.T) {
	r := settleFixture()
	r.Steps = []StepState{transcriptStep("codex", "00000000-0000-4000-8000-000000000001")}
	got := AdmissionBody(r)
	if len(got.Transcripts) != 0 || got.TranscriptsIncomplete {
		t.Fatalf("nonterminal observation reported sessions: %+v", got)
	}
}

// Supply native identity through the same Herdr observation and durable
// occupant fields used by execution; prompt/retry/recovery transitions stay real.
func setNativeTranscriptSession(t *testing.T, f *executionFixture, id string) {
	t.Helper()
	r := loaded(t, f.s, f.uid)
	a, err := f.m.h.AgentGet(t.Context(), AgentReference(r).AgentName)
	if err != nil {
		t.Fatal(err)
	}
	a.Session = &herdr.AgentSession{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: id}
	f.h.SetAgent(a)
	if _, err = f.s.Transition(f.uid, func(r *Run) error { AgentReference(r).Session = a.Session; return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptSharedRetryHeldBeforeResubmission(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first", Retries: 1})
	f.launch(t)
	setNativeTranscriptSession(t, f, id)
	f.submit(t)
	f.result(t, "failed")
	if f.await(t) == nil {
		t.Fatal("missing accepted failed result")
	}
	if _, err := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = f.m.clock.Now(); return nil }); err != nil {
		t.Fatal(err)
	}
	r := executeWorkflow(t, f)
	if r.Phase != Held || r.Reason != "timeout" || r.Steps[0].Attempt != 2 || r.Steps[0].Submission != None || f.count("agent.prompt") != 1 {
		t.Fatalf("retry did not stop before another submission: phase=%s reason=%s step=%+v prompts=%d", r.Phase, r.Reason, r.Steps[0], f.count("agent.prompt"))
	}
	_, p := settlePayload(t, r.Ops, "observe")
	want := []kata.Transcript{{Agent: "codex", SessionID: id}}
	if !reflect.DeepEqual(p.Observation.Transcripts, want) || p.Observation.TranscriptsIncomplete {
		t.Fatalf("submitted shared attempt lost before retry: %+v", p.Observation)
	}
}

func TestTranscriptRecoveredCompletedSharedAttemptIsComplete(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first"})
	f.launch(t)
	setNativeTranscriptSession(t, f, id)
	f.submit(t)
	f.result(t, "succeeded")
	old := loaded(t, f.s, f.uid).Herdr
	if err := f.m.h.TabClose(t.Context(), old.TabID); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	if r.Phase != Settled || r.Outcome != "succeeded" || len(r.PastAttempts) != 1 || f.count("agent.prompt") != 1 {
		t.Fatalf("completed shared recovery changed execution: phase=%s outcome=%s history=%d prompts=%d", r.Phase, r.Outcome, len(r.PastAttempts), f.count("agent.prompt"))
	}
	_, p := settlePayload(t, r.Ops, "observe")
	want := []kata.Transcript{{Agent: "codex", SessionID: id}}
	if !reflect.DeepEqual(p.Observation.Transcripts, want) || p.Observation.TranscriptsIncomplete {
		t.Fatalf("retained complete shared attempt marked partial: %+v", p.Observation)
	}
}

func TestTranscriptSharedReplacementHeldBeforeFirstSubmission(t *testing.T) {
	const first = "00000000-0000-4000-8000-000000000001"
	const unused = "00000000-0000-4000-8000-000000000002"
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first"}, defs.Step{Key: "next", Kind: "prompt", Prompt: "next", After: []string{"first"}})
	f.launch(t)
	setNativeTranscriptSession(t, f, first)
	f.submit(t)
	f.result(t, "succeeded")
	if f.await(t) == nil {
		t.Fatal("missing first result")
	}
	old := loaded(t, f.s, f.uid).Herdr
	if err := f.m.h.TabClose(t.Context(), old.TabID); err != nil {
		t.Fatal(err)
	}
	if err := f.m.launch(t.Context(), f.uid, 1); err != nil {
		t.Fatal(err)
	}
	setNativeTranscriptSession(t, f, unused)
	if err := f.m.holdStopped(f.uid, "timeout", false, "next"); err != nil {
		t.Fatal(err)
	}
	if err := f.m.settle(t.Context(), f.uid, nil); err != nil {
		t.Fatal(err)
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Steps[1].Submission != None || f.count("agent.start") != 2 || f.count("agent.prompt") != 1 {
		t.Fatalf("replacement lifecycle changed: phase=%s step=%+v starts=%d prompts=%d", r.Phase, r.Steps[1], f.count("agent.start"), f.count("agent.prompt"))
	}
	_, p := settlePayload(t, r.Ops, "observe")
	want := []kata.Transcript{{Agent: "codex", SessionID: first}}
	if !reflect.DeepEqual(p.Observation.Transcripts, want) || p.Observation.TranscriptsIncomplete {
		t.Fatalf("unused replacement session attributed to earlier attempt: %+v", p.Observation)
	}
}

func TestTranscriptSharedReplacementRetainsAllCompletedSteps(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, submittedReplacement := range []bool{false, true} {
			t.Run(fmt.Sprintf("legacy-%t/submitted-%t", legacy, submittedReplacement), func(t *testing.T) {
				const first = "00000000-0000-4000-8000-000000000001"
				const replacementID = "00000000-0000-4000-8000-000000000002"
				f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first"}, defs.Step{Key: "second", Kind: "prompt", Prompt: "second", After: []string{"first"}}, defs.Step{Key: "next", Kind: "prompt", Prompt: "next", After: []string{"second"}})
				f.launch(t)
				setNativeTranscriptSession(t, f, first)
				f.submit(t)
				f.result(t, "succeeded")
				if f.await(t) == nil {
					t.Fatal("missing first result")
				}
				if err := f.m.launch(t.Context(), f.uid, 1); err != nil {
					t.Fatal(err)
				}
				if err := f.m.submitStep(t.Context(), f.uid, 1); err != nil {
					t.Fatal(err)
				}
				if err := WriteResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, "second", 1), "result.json"), &Result{Version: 1, Run: f.uid, Step: "second", Attempt: 1, Status: "succeeded"}); err != nil {
					t.Fatal(err)
				}
				if res, err := f.m.awaitStepResult(t.Context(), f.uid, 1); err != nil || res == nil {
					t.Fatal(res, err)
				}
				old := loaded(t, f.s, f.uid).Herdr
				if err := f.m.h.TabClose(t.Context(), old.TabID); err != nil {
					t.Fatal(err)
				}
				if err := f.m.launch(t.Context(), f.uid, 2); err != nil {
					t.Fatal(err)
				}
				setNativeTranscriptSession(t, f, replacementID)
				if legacy {
					// Older journals retained only the most recent shared step. Reproduce
					// that saved representation after exercising the actual retirement.
					if _, err := f.s.Transition(f.uid, func(r *Run) error {
						r.PastAttempts = slices.DeleteFunc(r.PastAttempts, func(s StepState) bool { return s.Key == "first" })
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}

				var r *Run
				wantPhase, wantSubmission, wantPrompts := Held, None, 2
				if submittedReplacement {
					if err := f.m.submitStep(t.Context(), f.uid, 2); err != nil {
						t.Fatal(err)
					}
					if err := WriteResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, "next", 1), "result.json"), &Result{Version: 1, Run: f.uid, Step: "next", Attempt: 1, Status: "succeeded"}); err != nil {
						t.Fatal(err)
					}
					if res, err := f.m.awaitStepResult(t.Context(), f.uid, 2); err != nil || res == nil {
						t.Fatal(res, err)
					}
					r = executeWorkflow(t, f)
					wantPhase, wantSubmission, wantPrompts = Settled, Accepted, 3
					if r.Outcome != "succeeded" || r.Herdr.HasSubmitted == nil || !*r.Herdr.HasSubmitted {
						t.Fatalf("replacement did not complete an accepted prompt: phase=%s outcome=%s submitted=%v", r.Phase, r.Outcome, r.Herdr.HasSubmitted)
					}
					if r.Steps[0].SubmittedAt == nil || r.Herdr.LaunchedAt.IsZero() || !r.Steps[0].SubmittedAt.Before(r.Herdr.LaunchedAt) || r.Steps[2].SubmittedAt == nil || r.Steps[2].SubmittedAt.Before(r.Herdr.LaunchedAt) {
						t.Fatalf("journal did not preserve the occupant boundary: first=%v launch=%v next=%v", r.Steps[0].SubmittedAt, r.Herdr.LaunchedAt, r.Steps[2].SubmittedAt)
					}
				} else {
					if err := f.m.holdStopped(f.uid, "timeout", false, "next"); err != nil {
						t.Fatal(err)
					}
					if err := f.m.settle(t.Context(), f.uid, nil); err != nil {
						t.Fatal(err)
					}
					r = loaded(t, f.s, f.uid)
				}
				if r.Phase != wantPhase || r.Steps[2].Submission != wantSubmission || f.count("agent.start") != 2 || f.count("agent.prompt") != wantPrompts {
					t.Fatalf("shared lifecycle changed: phase=%s starts=%d prompts=%d", r.Phase, f.count("agent.start"), f.count("agent.prompt"))
				}
				_, p := settlePayload(t, r.Ops, "observe")
				want := []kata.Transcript{{Agent: "codex", SessionID: first}}
				if submittedReplacement {
					want = append(want, kata.Transcript{Agent: "codex", SessionID: replacementID})
				}
				if !reflect.DeepEqual(p.Observation.Transcripts, want) || p.Observation.TranscriptsIncomplete != legacy {
					t.Fatalf("earlier shared step used replacement identity: %+v", p.Observation)
				}
			})
		}
	}
}
