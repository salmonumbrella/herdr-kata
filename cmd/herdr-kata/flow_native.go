package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/flow"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func flowDraftDir() string { return filepath.Join(stateDir(), "drafts", "flows") }
func nativeFlows(ctx context.Context) ([]flow.Flow, string, error) {
	s, e := openStore()
	if e != nil {
		return nil, "", e
	}
	defer s.Close()
	snapshot, e := s.Native.Refresh(ctx)
	if e != nil {
		return nil, "", e
	}
	flows, problems := flow.ProjectDefinitions(snapshot.Flows)
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, "herdr-kata:", problem)
	}
	return flows, snapshot.Label(), nil
}
func nativeFlow(ctx context.Context, id string) (flow.Flow, error) {
	f, _, e := nativeFlowSnapshot(ctx, id)
	return f, e
}
func nativeFlowSnapshot(ctx context.Context, id string) (flow.Flow, string, error) {
	def, label, e := nativeDefinitionSnapshot(ctx, "flow", id)
	if e != nil {
		return flow.Flow{}, label, e
	}
	f, e := flow.FromNative(def)
	if e != nil {
		return f, label, fmt.Errorf("native flow %s cannot be projected: %w", def.UID, e)
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
	defs := snapshot.Flows
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

func flowSave(argv []string) error {
	if len(argv) != 1 {
		return errors.New("usage: flow save <draft-id>")
	}
	id, e := flow.ParseID(strings.ToLower(argv[0]))
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
	f, e := flow.Load(flowDraftDir(), id)
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
		retained, e = flow.NativeDraft(f, uid, "")
		if e != nil {
			return e
		}
		raw, e = json.Marshal(retained)
		if e != nil {
			return e
		}
		fd, e := os.OpenFile(sidecar, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(e, os.ErrExist) {
			return flowSave(argv)
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
	draft, e := flow.NativeDraft(f, retained.UID, retained.ExpectedEventUID)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	def, e := s.Native.Save(ctx, draft)
	if e != nil {
		return fmt.Errorf("unsaved flow draft retained at %s (UID %s): %w", f.Path, draft.UID, e)
	}
	draft.ExpectedEventUID = def.DefinitionEventUID
	draft.Definition = def.Definition
	raw, e = json.Marshal(draft)
	if e != nil {
		return e
	}
	if e := os.WriteFile(sidecar, raw, 0600); e != nil {
		return fmt.Errorf("native flow saved; refresh its winner before retry: %w", e)
	}
	fmt.Printf("native flow %s saved; winner %s\n", def.UID, def.DefinitionEventUID)
	return nil
}
func prepareFlowDraft(id string) (flow.Flow, error) {
	if parsed, e := flow.ParseID(id); e == nil {
		path := filepath.Join(flowDraftDir(), parsed+flow.Ext)
		info, err := os.Stat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return flow.Flow{}, errors.New("flow draft must be a regular file")
			}
			f, err := flow.Load(flowDraftDir(), parsed)
			if err != nil {
				return flow.Flow{ID: parsed, Path: path}, nil
			}
			return f, nil
		}
		if !os.IsNotExist(err) {
			return flow.Flow{}, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f, e := nativeFlow(ctx, id)
	if e != nil {
		return f, e
	}
	path := filepath.Join(flowDraftDir(), strings.ToLower(f.ID)+flow.Ext)
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
	draft, e := flow.NativeDraft(f, f.ID, f.NativeEventUID)
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
