package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

const workflowUID = "01M4TADGDFRMAC38MX3NZCW1K6"
const workflowEventUID = "01M4TADGDFRMAC38MX3NZCW1K7"

type workflowReader struct {
	rows []kata.Workflow
	err  error
}

func (r *workflowReader) ListWorkflows(context.Context) ([]kata.Workflow, error) {
	return r.rows, r.err
}
func (r *workflowReader) ShowWorkflow(_ context.Context, uid string) (kata.Workflow, error) {
	if r.err != nil {
		return kata.Workflow{}, r.err
	}
	for _, w := range r.rows {
		if w.UID == uid {
			return w, nil
		}
	}
	return kata.Workflow{}, &kata.Error{Code: "not_found"}
}
func bindingConfig() config.Config {
	return config.Config{Profiles: map[string]config.Profile{"default": {Kind: "codex"}, "fast": {Kind: "codex"}}, Checkouts: map[string]string{"repo": "/tmp/example-checkout"}}
}
func workflowFixture() kata.Workflow {
	return kata.Workflow{UID: workflowUID, Name: "Check", DefinitionEventUID: workflowEventUID, Definition: json.RawMessage(`{"version":1,"steps":[{"key":"check","kind":"command","command":"true"}]}`)}
}
func bindingParent(key, raw string) kata.IssueView {
	return kata.IssueView{UID: parentUID, Metadata: map[string]json.RawMessage{key: json.RawMessage(raw)}}
}
func TestBindingDefaultPrompt(t *testing.T) {
	// With no workflow key the later prompt builder receives a nil WFDef.
	b, err := ResolveBinding(t.Context(), nil, kata.IssueView{}, bindingConfig())
	if err != nil || b.Profile != "default" || b.WorkflowUID != "" || b.WFDef != nil || b.Cwd != "" {
		t.Fatal(b, err)
	}
}
func TestBindingAmbiguousUnsupported(t *testing.T) {
	w := workflowFixture()
	duplicate := w
	duplicate.UID = eventUID
	_, err := ResolveBinding(t.Context(), &workflowReader{rows: []kata.Workflow{w, duplicate}}, bindingParent("herdr_kata.workflow", `"Check"`), bindingConfig())
	if !errors.Is(err, ErrUnsupported) {
		t.Fatal("ambiguous name accepted", err)
	}
}
func TestBindingWorkflowUIDAndName(t *testing.T) {
	for _, value := range []string{`"Check"`, `"01M4TADGDFRMAC38MX3NZCW1K6"`} {
		b, err := ResolveBinding(t.Context(), &workflowReader{rows: []kata.Workflow{workflowFixture()}}, bindingParent("herdr_kata.workflow", value), bindingConfig())
		if err != nil || b.WorkflowName != "Check" || b.WorkflowUID != workflowUID || b.WorkflowEventUID != workflowEventUID || b.WFDef == nil || b.WFDef.Steps[0].Command != "true" {
			t.Fatal(b, err)
		}
	}
}
func TestBindingCheckout(t *testing.T) {
	p := bindingParent("herdr_kata.checkout", `"repo"`)
	p.Metadata["herdr_kata.profile"] = json.RawMessage(`"fast"`)
	b, err := ResolveBinding(t.Context(), nil, p, bindingConfig())
	if err != nil || b.Profile != "fast" || b.Cwd != "/tmp/example-checkout" {
		t.Fatal(b, err)
	}
}
func TestBindingUnknownOrInvalidUnsupported(t *testing.T) {
	for _, key := range []string{"herdr_kata.workflow", "herdr_kata.profile", "herdr_kata.checkout"} {
		for _, value := range []string{`"unknown"`, `null`, `""`, `"  "`, `1`, `{}`, `broken`} {
			_, err := ResolveBinding(t.Context(), &workflowReader{}, bindingParent(key, value), bindingConfig())
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("%s=%s accepted: %v", key, value, err)
			}
		}
	}
	_, err := ResolveBinding(t.Context(), nil, kata.IssueView{}, config.Config{})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown default profile: %v", err)
	}
}
func TestBindingInvalidWorkflowUnsupported(t *testing.T) {
	for _, raw := range []string{`null`, `{"version":2,"steps":[{"key":"s","kind":"command","command":"true"}]}`, `{"version":1,"steps":[]}`, `{"version":1,"steps":[{"key":"s","kind":"prompt","prompt":"work","options":{"agent":"missing"}}]}`, `{"version":1,"steps":[{"key":"s","kind":"command","command":"true","after":["s"]}]}`} {
		w := workflowFixture()
		w.Definition = json.RawMessage(raw)
		_, err := ResolveBinding(t.Context(), &workflowReader{rows: []kata.Workflow{w}}, bindingParent("herdr_kata.workflow", `"Check"`), bindingConfig())
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("invalid workflow %s: %v", raw, err)
		}
	}
	w := workflowFixture()
	now := time.Now()
	w.DeletedAt = &now
	_, err := ResolveBinding(t.Context(), &workflowReader{rows: []kata.Workflow{w}}, bindingParent("herdr_kata.workflow", `"01M4TADGDFRMAC38MX3NZCW1K6"`), bindingConfig())
	if !errors.Is(err, ErrUnsupported) {
		t.Fatal("deleted workflow accepted", err)
	}
}
func TestBindingEditedAfterAdmissionIgnored(t *testing.T) {
	r := &workflowReader{rows: []kata.Workflow{workflowFixture()}}
	p := bindingParent("herdr_kata.workflow", `"Check"`)
	b, err := ResolveBinding(t.Context(), r, p, bindingConfig())
	if err != nil {
		t.Fatal(err)
	}
	r.rows[0].Definition = json.RawMessage(`{"version":1,"steps":[{"key":"check","kind":"command","command":"false"}]}`)
	r.rows[0].DefinitionEventUID = eventUID
	p.Metadata["herdr_kata.workflow"] = json.RawMessage(`"Other"`)
	if b.WorkflowEventUID != workflowEventUID || b.WFDef.Steps[0].Command != "true" {
		t.Fatal("binding was not frozen", b)
	}
}
func TestBindingReaderFailure(t *testing.T) {
	want := errors.New("reader unavailable")
	_, err := ResolveBinding(t.Context(), &workflowReader{err: want}, bindingParent("herdr_kata.workflow", `"Check"`), bindingConfig())
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
}

type hiddenWorkflow struct {
	*workflowReader
	proofErr error
}

func (r *hiddenWorkflow) ShowWorkflow(context.Context, string) (kata.Workflow, error) {
	return kata.Workflow{}, &kata.Error{Code: "not_found"}
}
func (r *hiddenWorkflow) ListWorkflows(context.Context) ([]kata.Workflow, error) {
	return r.rows, r.proofErr
}
func TestBinding404RequiresAffirmativeAbsence(t *testing.T) {
	for _, proof := range []string{"present", "absent", "failed"} {
		t.Run(proof, func(t *testing.T) {
			r := &hiddenWorkflow{workflowReader: &workflowReader{rows: []kata.Workflow{workflowFixture()}}}
			if proof == "absent" {
				r.rows = nil
			}
			if proof == "failed" {
				r.proofErr = errors.New("offline")
			}
			_, err := ResolveBinding(t.Context(), r, bindingParent("herdr_kata.workflow", `"01M4TADGDFRMAC38MX3NZCW1K6"`), bindingConfig())
			if err == nil || errors.Is(err, ErrUnsupported) != (proof == "absent") {
				t.Fatal("wrong 404 decision", err)
			}
		})
	}
}
