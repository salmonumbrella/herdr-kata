package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/workflow"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func workflowDraftDir() string { return filepath.Join(stateDir(), "drafts", "workflows") }
func nativeWorkflows(ctx context.Context) ([]workflow.Workflow, string, error) {
	s, e := openStore()
	if e != nil {
		return nil, "", e
	}
	defer s.Close()
	snapshot, e := s.Native.Refresh(ctx)
	if e != nil {
		return nil, "", e
	}
	workflows, problems := workflow.ProjectDefinitions(snapshot.Workflows)
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, "herdr-kata:", problem)
	}
	return workflows, snapshot.Label(), nil
}
func nativeWorkflow(ctx context.Context, id string) (workflow.Workflow, error) {
	f, _, e := nativeWorkflowSnapshot(ctx, id)
	return f, e
}
func nativeWorkflowSnapshot(ctx context.Context, id string) (workflow.Workflow, string, error) {
	def, label, e := nativeDefinitionSnapshot(ctx, "workflow", id)
	if e != nil {
		return workflow.Workflow{}, label, e
	}
	f, e := workflow.FromNative(def)
	if e != nil {
		return f, label, fmt.Errorf("native workflow %s cannot be projected: %w", def.UID, e)
	}
	return f, label, nil
}
func nativeDefinitionSnapshot(ctx context.Context, resource, id string) (katacli.Definition, string, error) {
	s, e := openStore()
	if e != nil {
		return katacli.Definition{}, "", e
	}
	defer s.Close()
	snapshot, e := s.Native.Refresh(ctx)
	if e != nil {
		return katacli.Definition{}, snapshot.Label(), e
	}
	normalized, e := katacli.NormalizeUID(id)
	if e != nil {
		return katacli.Definition{}, snapshot.Label(), e
	}
	defs := snapshot.Workflows
	if resource == "job" {
		defs = snapshot.Jobs
	}
	for _, def := range defs {
		if def.UID == normalized {
			return def, snapshot.Label(), nil
		}
	}
	return katacli.Definition{}, snapshot.Label(), errors.New("native " + resource + " not found in the selected target/project")
}

func workflowSave(argv []string) error {
	if len(argv) != 1 {
		return errors.New("usage: workflow save <draft-id>")
	}
	id, e := workflow.ParseID(strings.ToLower(argv[0]))
	if e != nil {
		return e
	}
	s, e := openStore()
	if e != nil {
		return e
	}
	defer s.Close()
	if s.Native.Client == nil {
		return s.Native.ActivationAllowed(context.Background())
	}
	f, e := workflow.Load(workflowDraftDir(), id)
	if e != nil {
		return e
	}
	sidecar := f.Path + ".native.json"
	var retained katacli.Draft
	raw, e := os.ReadFile(sidecar)
	if e == nil {
		if e := json.Unmarshal(raw, &retained); e != nil {
			return e
		}
		f.NativeDefinition = retained.Definition
		f.NativeEventUID = retained.ExpectedEventUID
		f.NativeName = retained.Name
	} else if !os.IsNotExist(e) {
		return e
	} else {
		uid, e := katacli.NewUID()
		if e != nil {
			return e
		}
		retained, e = workflow.NativeDraft(f, uid, "")
		if e != nil {
			return e
		}
		raw, e = json.Marshal(retained)
		if e != nil {
			return e
		}
		fd, e := os.OpenFile(sidecar, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(e, os.ErrExist) {
			return workflowSave(argv)
		}
		if e != nil {
			return e
		}
		_, e = fd.Write(raw)
		closeErr := fd.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	draft, e := workflow.NativeDraft(f, retained.UID, retained.ExpectedEventUID)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	def, e := s.Native.Save(ctx, draft)
	if e != nil {
		return fmt.Errorf("unsaved workflow draft retained at %s (UID %s): %w", f.Path, draft.UID, e)
	}
	draft.ExpectedEventUID = def.DefinitionEventUID
	draft.Definition = def.Definition
	raw, e = json.Marshal(draft)
	if e != nil {
		return e
	}
	if e := os.WriteFile(sidecar, raw, 0600); e != nil {
		return fmt.Errorf("native workflow saved; refresh its winner before retry: %w", e)
	}
	fmt.Printf("native workflow %s saved; winner %s\n", def.UID, def.DefinitionEventUID)
	return nil
}
func prepareWorkflowDraft(id string) (workflow.Workflow, error) {
	if parsed, e := workflow.ParseID(id); e == nil {
		path := filepath.Join(workflowDraftDir(), parsed+workflow.Ext)
		info, err := os.Stat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return workflow.Workflow{}, errors.New("workflow draft must be a regular file")
			}
			f, err := workflow.Load(workflowDraftDir(), parsed)
			if err != nil {
				return workflow.Workflow{ID: parsed, Path: path}, nil
			}
			return f, nil
		}
		if !os.IsNotExist(err) {
			return workflow.Workflow{}, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f, e := nativeWorkflow(ctx, id)
	if e != nil {
		return f, e
	}
	path := filepath.Join(workflowDraftDir(), strings.ToLower(f.ID)+workflow.Ext)
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return f, e
	}
	raw, e := yaml.Marshal(f)
	if e != nil {
		return f, e
	}
	if e := os.WriteFile(path, raw, 0600); e != nil {
		return f, e
	}
	draft, e := workflow.NativeDraft(f, f.ID, f.NativeEventUID)
	if e != nil {
		return f, e
	}
	metadata, e := json.Marshal(draft)
	if e != nil {
		return f, e
	}
	if e := os.WriteFile(path+".native.json", metadata, 0600); e != nil {
		return f, e
	}
	f.Path = path
	return f, nil
}
