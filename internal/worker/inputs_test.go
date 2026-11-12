package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type inputKata struct {
	*fakeKata
	version                 string
	response                kata.InputResolution
	inputErr                error
	responses               []kata.InputResolution
	afterFirst              func()
	failObserve             bool
	healthCalls, inputCalls int
}

func (k *inputKata) Observe(uid string, b []byte, rev int) (int, bool, error) {
	n, replay, err := k.fakeKata.Observe(uid, b, rev)
	if k.failObserve {
		return n, replay, errors.New("observation unavailable")
	}
	return n, replay, err
}

func (k *inputKata) HealthVersion() (string, error) { k.healthCalls++; return k.version, nil }
func (k *inputKata) JobInputs(job, request string) (kata.InputResolution, error) {
	k.inputCalls++
	if len(k.responses) > 0 {
		response := k.responses[0]
		k.responses = k.responses[1:]
		if k.inputCalls == 1 && k.afterFirst != nil {
			k.afterFirst()
		}
		return response, k.inputErr
	}
	return k.response, k.inputErr
}

func inputFixture(t *testing.T, body string) (*Worker, *fakeHC, *inputKata) {
	t.Helper()
	w, h, base := fixture(t)
	k := &inputKata{fakeKata: base, version: "0.49.0"}
	if err := json.Unmarshal([]byte(body), &k.response); err != nil {
		t.Fatal(err)
	}
	w.Clients["example-project"] = k
	return w, h, k
}

func TestDeclaredInputsAppearAsPromptData(t *testing.T) {
	for _, tc := range []struct{ name, response, values string }{
		{"empty", `{"job_uid":"01J00000000000000000000010","definition_event_uid":"01J00000000000000000000011"}`, `{}`},
		{"literal", `{"job_uid":"01J00000000000000000000010","definition_event_uid":"01J00000000000000000000011","inputs":[{"name":"text","type":"string","source":"default","value":"{{previous}} $(touch x)"}]}`, `{"text":"{{previous}} $(touch x)"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, h, k := inputFixture(t, tc.response)
			declarations := map[string]any{}
			if tc.name == "literal" {
				declarations["text"] = map[string]any{"type": "string", "default": "{{previous}} $(touch x)"}
			}
			k.jobs[0].Definition.Options = map[string]any{"inputs": declarations}
			request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
			tick(t, w, at)
			r := active(t, w)
			body, err := os.ReadFile(filepath.Join(w.Store.Layout.Attempt(r.Key, "main", 1), "prompt.md"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), tc.values) {
				t.Fatalf("prompt: %s", body)
			}
			for _, value := range h.specs[0].Prefix {
				if strings.HasPrefix(value, "KATA_INPUT_") {
					t.Fatalf("prompt input environment: %q", value)
				}
			}
		})
	}
}

func TestDeclaredCommandInputsFreezeAndDeliver(t *testing.T) {
	w, h, k := inputFixture(t, `{"job_uid":"01J00000000000000000000010","definition_event_uid":"01J00000000000000000000011","workflow_uid":"01J00000000000000000000012","workflow_definition_event_uid":"01J00000000000000000000013","inputs":[{"name":"text","type":"string","source":"default","value":"$(touch x) {{input}}"},{"name":"zero","type":"int","source":"job","value":-0},{"name":"flag","type":"bool","source":"default","value":false},{"name":"optional","type":"string","source":"unset"}]}`)
	workflow(k.fakeKata)
	k.workflows[0].Definition.Options = map[string]any{"inputs": map[string]any{
		"text":     map[string]any{"type": "string", "default": "$(touch x) {{input}}"},
		"zero":     map[string]any{"type": "int"},
		"flag":     map[string]any{"type": "bool", "default": false},
		"optional": map[string]any{"type": "string"},
	}}
	k.jobs[0].Definition.Options = map[string]any{"input_values": map[string]any{"zero": json.Number("-0")}}
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	r := active(t, w)
	if string(r.Plan.InputValues) != `{"flag":false,"text":"$(touch x) {{input}}","zero":0}` {
		t.Fatalf("frozen values: %s", r.Plan.InputValues)
	}
	if h.runs != 1 || k.healthCalls != 1 || k.inputCalls != 1 {
		t.Fatalf("runs=%d health=%d inputs=%d", h.runs, k.healthCalls, k.inputCalls)
	}
	for _, want := range []string{"KATA_INPUT_FLAG=false", "KATA_INPUT_TEXT=$(touch x) {{input}}", "KATA_INPUT_ZERO=0"} {
		if !slices.Contains(h.specs[0].Prefix, want) {
			t.Fatalf("missing %q in %q", want, h.specs[0].Prefix)
		}
	}
	for _, value := range h.specs[0].Prefix {
		if strings.HasPrefix(value, "KATA_INPUT_OPTIONAL=") {
			t.Fatal(value)
		}
	}
}

func TestOldDaemonCancelsBeforeLaunch(t *testing.T) {
	w, h, k := inputFixture(t, `{}`)
	k.version = "0.47.0"
	k.jobs[0].Definition.Options = map[string]any{"inputs": map[string]any{}}
	k.failObserve = true
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	if err := w.Tick(clock.WithNow(context.Background(), at), at); err == nil || !strings.Contains(err.Error(), "observation unavailable") {
		t.Fatalf("expected deferred observation: %v", err)
	}
	if h.runs != 0 || k.creates != 0 || k.inputCalls != 0 || len(k.bodies) != 1 {
		t.Fatalf("runs=%d creates=%d calls=%d bodies=%d", h.runs, k.creates, k.inputCalls, len(k.bodies))
	}
	runs, err := w.Store.All()
	if err != nil || len(runs) != 1 {
		t.Fatalf("journals: %v %v", runs, err)
	}
	r := runs[0]
	if r.Phase != "settled" || r.Outcome != "cancelled" || r.Hold != "inputs_unavailable" || len(r.Attempts) != 0 || r.IssueUID != "" {
		t.Fatalf("refusal journal: %+v", r)
	}
}

func TestSuffixedDaemonVersionAllowsResolution(t *testing.T) {
	for _, version := range []string{" 0.48.0-rc.1 ", "0.48.0+build"} {
		t.Run(version, func(t *testing.T) {
			w, h, k := inputFixture(t, `{"job_uid":"01J00000000000000000000010","definition_event_uid":"01J00000000000000000000011"}`)
			k.version = version
			k.jobs[0].Definition.Options = map[string]any{"inputs": map[string]any{}}
			request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
			tick(t, w, at)
			if h.runs != 1 || k.inputCalls != 1 {
				t.Fatalf("runs=%d calls=%d", h.runs, k.inputCalls)
			}
		})
	}
}

func TestNoInputKeysMakeNoResolutionCalls(t *testing.T) {
	w, h, k := inputFixture(t, `{}`)
	k.jobs[0].Definition.Options = map[string]any{"input": "context"}
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	if h.runs != 1 || k.healthCalls != 0 || k.inputCalls != 0 {
		t.Fatalf("runs=%d health=%d inputs=%d", h.runs, k.healthCalls, k.inputCalls)
	}
}

func TestInvalidInputsAreRecordedWithoutLaunching(t *testing.T) {
	w, h, k := inputFixture(t, `{"job_uid":"01J00000000000000000000010","definition_event_uid":"01J00000000000000000000011","errors":[{"name":"needed","code":"missing_required","message":"required input is unset"}]}`)
	k.jobs[0].Definition.Options = map[string]any{"inputs": map[string]any{}}
	k.failObserve = true
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	if err := w.Tick(clock.WithNow(context.Background(), at), at); err == nil || !strings.Contains(err.Error(), "observation unavailable") {
		t.Fatalf("expected deferred observation: %v", err)
	}
	if h.runs != 0 || k.creates != 0 || len(k.bodies) != 1 {
		t.Fatalf("runs=%d creates=%d bodies=%d", h.runs, k.creates, len(k.bodies))
	}
	runs, err := w.Store.All()
	if err != nil || len(runs) != 1 {
		t.Fatalf("journals: %v %v", runs, err)
	}
	r := runs[0]
	if r.Phase != "settled" || r.Outcome != "cancelled" || r.Hold != "invalid_inputs" || len(r.Attempts) != 0 || r.IssueUID != "" {
		t.Fatalf("refusal journal: %+v", r)
	}
	if r.Detail != `[{"name":"needed","code":"missing_required","message":"required input is unset"}]` {
		t.Fatalf("diagnostics: %s", r.Detail)
	}
}

func TestTransientInputReadKeepsRequestPending(t *testing.T) {
	w, h, k := inputFixture(t, `{}`)
	k.inputErr = errors.New("temporary outage")
	k.jobs[0].Definition.Options = map[string]any{"inputs": map[string]any{}}
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	if err := w.Tick(clock.WithNow(context.Background(), at), at); err == nil {
		t.Fatal("expected read failure")
	}
	if h.runs != 0 || len(k.bodies) != 0 {
		t.Fatalf("runs=%d bodies=%d", h.runs, len(k.bodies))
	}
	k.inputErr = nil
	k.response = kata.InputResolution{JobUID: k.jobs[0].UID, DefinitionEventUID: k.jobs[0].DefinitionEventUID}
	tick(t, w, at.Add(time.Second))
	if h.runs != 1 {
		t.Fatalf("pending request did not launch: %d", h.runs)
	}
}

func TestWorkflowRefreshRequiresMatchingPinnedJob(t *testing.T) {
	for _, changedJob := range []bool{false, true} {
		t.Run(map[bool]string{false: "workflow-only", true: "job-changed"}[changedJob], func(t *testing.T) {
			w, h, k := inputFixture(t, `{}`)
			workflow(k.fakeKata)
			k.workflows[0].Definition.Options = map[string]any{"inputs": map[string]any{}}
			first := kata.InputResolution{JobUID: k.jobs[0].UID, DefinitionEventUID: k.jobs[0].DefinitionEventUID, WorkflowUID: k.workflows[0].UID, WorkflowDefinitionEventUID: "new-workflow-event"}
			k.responses = []kata.InputResolution{first, first}
			k.afterFirst = func() {
				k.workflows[0].DefinitionEventUID = "new-workflow-event"
				if changedJob {
					k.jobs[0].DefinitionEventUID = "new-job-event"
				}
			}
			request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
			err := w.Tick(clock.WithNow(context.Background(), at), at)
			if changedJob {
				if err == nil || h.runs != 0 || len(k.bodies) != 0 {
					t.Fatalf("changed job: runs=%d bodies=%d err=%v", h.runs, len(k.bodies), err)
				}
			} else if err != nil || h.runs != 1 || k.inputCalls != 2 || active(t, w).Plan.WorkflowEventUID != "new-workflow-event" {
				t.Fatalf("workflow refresh: runs=%d calls=%d err=%v", h.runs, k.inputCalls, err)
			}
		})
	}
}

type fleetInputKata struct {
	*fleetKata
	version     string
	requestUID  string
	afterHealth func()
}

func (k *fleetInputKata) HealthVersion() (string, error) {
	if k.afterHealth != nil {
		k.afterHealth()
	}
	return k.version, nil
}
func (k *fleetInputKata) JobInputs(job, request string) (kata.InputResolution, error) {
	k.requestUID = request
	return kata.InputResolution{JobUID: job, DefinitionEventUID: k.jobs[0].DefinitionEventUID}, nil
}

func TestRemoteInputValuesRequireRequestAwareDaemon(t *testing.T) {
	for _, version := range []string{"0.48.0", "0.49.0"} {
		t.Run(version, func(t *testing.T) {
			w, h, base := fleetFixture(t)
			k := &fleetInputKata{fleetKata: base, version: version}
			w.Clients["example-project"] = k
			k.caps.Controls = []string{"run_request"}
			req := remoteRun(k.fleetKata, at.Add(time.Minute))
			req.Inputs = map[string]json.RawMessage{"name": json.RawMessage(`"value"`)}
			k.remote = []kata.CronRequest{req}
			tick(t, w, at)
			if version == "0.48.0" {
				if h.runs != 0 || k.requestUID != "" || len(k.bodies) != 1 {
					t.Fatalf("old daemon: runs=%d request=%q bodies=%d", h.runs, k.requestUID, len(k.bodies))
				}
				var observation kata.ObserveCronRunBody
				if err := json.Unmarshal(k.bodies[0], &observation); err != nil {
					t.Fatal(err)
				}
				if observation.Summary.Message == nil || !strings.HasPrefix(*observation.Summary.Message, "reason: inputs_unavailable") {
					t.Fatalf("observation: %+v", observation)
				}
			} else if h.runs != 1 || k.requestUID != req.UID {
				t.Fatalf("request-aware call: runs=%d request=%q", h.runs, k.requestUID)
			}
		})
	}
}

func TestInputRequestExpiresDuringHealthRead(t *testing.T) {
	for _, version := range []string{"0.48.0", "0.49.0"} {
		t.Run(version, func(t *testing.T) {
			w, h, base := fleetFixture(t)
			current := at
			k := &fleetInputKata{fleetKata: base, version: version, afterHealth: func() { current = at.Add(time.Second) }}
			w.Clients["example-project"] = k
			k.caps.Controls = []string{"run_request"}
			req := remoteRun(base, at.Add(time.Second))
			req.Inputs = map[string]json.RawMessage{"name": json.RawMessage(`"value"`)}
			k.remote = []kata.CronRequest{req}
			if err := w.Tick(clock.WithSource(context.Background(), func() time.Time { return current }), at); err != nil {
				t.Fatal(err)
			}
			runs, err := w.Store.All()
			if err != nil || len(runs) != 0 || h.runs != 0 || len(k.bodies) != 0 {
				t.Fatalf("expired: runs=%v launches=%d bodies=%d err=%v", runs, h.runs, len(k.bodies), err)
			}
		})
	}
}

func TestPreparedInputValuesSurviveRestart(t *testing.T) {
	w, h, k := inputFixture(t, `{"job_uid":"01J00000000000000000000010","definition_event_uid":"01J00000000000000000000011","workflow_uid":"01J00000000000000000000012","workflow_definition_event_uid":"01J00000000000000000000013","inputs":[{"name":"text","type":"string","source":"default","value":"original"}]}`)
	workflow(k.fakeKata)
	k.workflows[0].Definition.Options = map[string]any{"inputs": map[string]any{"text": map[string]any{"type": "string", "default": "original"}}}
	k.workflows[0].Definition.Steps = []kata.WorkflowStep{
		{Key: "first", Kind: "prompt", Prompt: new("First")},
		{Key: "second", Kind: "prompt", Prompt: new("Second"), After: []string{"first"}},
	}
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	r := active(t, w)
	finishAgent(t, w, h, r)
	k.inputErr = errors.New("resolution should not be repeated")
	w = New(w.Config, w.Store, h, w.Clients, w.Bin)
	tick(t, w, at.Add(time.Second))
	saved, err := w.Store.Load(r.Key)
	if err != nil || string(saved.Plan.InputValues) != `{"text":"original"}` || h.runs != 2 || k.inputCalls != 1 {
		t.Fatalf("recovery: values=%s runs=%d calls=%d err=%v", saved.Plan.InputValues, h.runs, k.inputCalls, err)
	}
	body, err := os.ReadFile(filepath.Join(w.Store.Layout.Attempt(r.Key, "second", 1), "prompt.md"))
	if err != nil || !strings.Contains(string(body), `{"text":"original"}`) {
		t.Fatalf("recovered prompt: %s %v", body, err)
	}
}

func TestRepeatedInputDriftRemainsPending(t *testing.T) {
	w, h, k := inputFixture(t, `{}`)
	k.jobs[0].Definition.Options = map[string]any{"inputs": map[string]any{}}
	k.responses = []kata.InputResolution{{JobUID: k.jobs[0].UID, DefinitionEventUID: "other-one"}, {JobUID: k.jobs[0].UID, DefinitionEventUID: "other-two"}}
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	if err := w.Tick(clock.WithNow(context.Background(), at), at); err == nil || !strings.Contains(err.Error(), "other-one") || !strings.Contains(err.Error(), "other-two") {
		t.Fatalf("expected both definition mismatches: %v", err)
	}
	runs, err := w.Store.All()
	if err != nil || len(runs) != 0 || h.runs != 0 || len(k.bodies) != 0 || k.inputCalls != 2 {
		t.Fatalf("runs=%v launches=%d bodies=%d calls=%d err=%v", runs, h.runs, len(k.bodies), k.inputCalls, err)
	}
}
