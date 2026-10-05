package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func FromNative(def katacli.Definition) (Flow, error) {
	if err := store.ValidateNativeFlowPolicy(def.Definition); err != nil {
		return Flow{}, err
	}
	var body struct {
		About, Input string
		Options      map[string]json.RawMessage
		Steps        []struct {
			Key, Kind, Command, Prompt string
			Options                    map[string]json.RawMessage
		}
	}
	if e := katacli.Decode(def.Definition, &body); e != nil {
		return Flow{}, e
	}
	f := Flow{ID: def.UID, NativeName: def.Name, About: body.About, Input: body.Input, NativeEventUID: def.DefinitionEventUID, NativeDefinition: append(json.RawMessage(nil), def.Definition...)}
	if raw := body.Options["herdr"]; len(raw) > 0 {
		var portable Flow
		if e := katacli.Decode(raw, &portable); e != nil {
			return f, e
		}
		f.SkipPermissions = portable.SkipPermissions
		f.Overwatch = portable.Overwatch
	}
	for _, native := range body.Steps {
		if native.Key == OverwatchStepID && auxiliaryOverwatch(native.Options["herdr_auxiliary"]) {
			if native.Kind != "prompt" || !auxiliaryOverwatch(native.Options["herdr_auxiliary"]) {
				return f, errors.New("reserved overwatch key requires a marked auxiliary prompt")
			}
			continue
		}
		step := store.Step{}
		if raw := native.Options["herdr_step"]; len(raw) > 0 {
			if e := katacli.Decode(raw, &step); e != nil {
				return f, e
			}
		}
		step.ID = native.Key
		switch native.Kind {
		case "prompt":
			step.Agent = native.Prompt
		case "command":
			step.Run = native.Command
		default:
			return f, errors.New("unsupported native flow step kind")
		}
		f.Steps = append(f.Steps, step)
	}
	return f, nil
}
func NativeDraft(f Flow, uid, expected string) (katacli.Draft, error) {
	if e := store.ValidateSteps(f.Steps, ""); e != nil {
		return katacli.Draft{}, e
	}
	def := map[string]json.RawMessage{}
	if len(f.NativeDefinition) > 0 {
		if e := katacli.Decode(f.NativeDefinition, &def); e != nil {
			return katacli.Draft{}, e
		}
	}
	put := func(m map[string]json.RawMessage, key string, value any) { raw, _ := json.Marshal(value); m[key] = raw }
	put(def, "version", 1)
	put(def, "about", f.About)
	put(def, "input", f.Input)
	options := map[string]json.RawMessage{}
	if raw := def["options"]; len(raw) > 0 {
		if e := katacli.Decode(raw, &options); e != nil {
			return katacli.Draft{}, e
		}
	}
	mergedOptions, e := katacli.MergeObject(options["herdr"], struct {
		SkipPermissions *bool
		Overwatch       *Overwatch
	}{f.SkipPermissions, f.Overwatch})
	if e != nil {
		return katacli.Draft{}, e
	}
	options["herdr"] = mergedOptions
	put(def, "options", options)
	var old []map[string]json.RawMessage
	if raw := def["steps"]; len(raw) > 0 {
		if e := katacli.Decode(raw, &old); e != nil {
			return katacli.Draft{}, e
		}
	}
	ordinary := old[:0]
	for _, row := range old {
		var key string
		json.Unmarshal(row["key"], &key)
		var opts map[string]json.RawMessage
		katacli.Decode(row["options"], &opts)
		if key == OverwatchStepID && auxiliaryOverwatch(opts["herdr_auxiliary"]) {
			continue
		}
		ordinary = append(ordinary, row)
	}
	old = ordinary
	structural := len(old) != len(f.Steps)
	previous := map[string]map[string]json.RawMessage{}
	for i, st := range old {
		var key string
		json.Unmarshal(st["key"], &key)
		previous[key] = st
		if i >= len(f.Steps) || f.Steps[i].ID != key {
			structural = true
		}
	}
	var steps []map[string]json.RawMessage
	for i, st := range f.Steps {
		row := previous[st.ID]
		if row == nil {
			row = map[string]json.RawMessage{}
		}
		if structural {
			delete(row, "after")
			if i > 0 {
				put(row, "after", []string{f.Steps[i-1].ID})
			}
		}
		put(row, "key", st.ID)
		delete(row, "command")
		delete(row, "prompt")
		if st.IsAgent() {
			put(row, "kind", "prompt")
			put(row, "prompt", st.Agent)
		} else {
			put(row, "kind", "command")
			put(row, "command", st.Run)
		}
		opts := map[string]json.RawMessage{}
		if raw := row["options"]; len(raw) > 0 {
			if e := katacli.Decode(raw, &opts); e != nil {
				return katacli.Draft{}, e
			}
		}
		// Explicit empty values clear editor-owned options; unknown raw fields survive.
		mergedStep, e := katacli.MergeObject(opts["herdr_step"], map[string]any{"id": st.ID, "agent": st.Agent, "run": st.Run, "model": st.Model, "effort": st.Effort, "kind": st.Kind, "subagent": st.Subagent, "skip_permissions": st.SkipPermissions, "on_fail": st.OnFail})
		if e != nil {
			return katacli.Draft{}, e
		}
		opts["herdr_step"] = mergedStep
		put(row, "options", opts)
		steps = append(steps, row)
	}
	put(def, "steps", steps)
	raw, e := json.Marshal(def)
	if e != nil {
		return katacli.Draft{}, e
	}
	name := f.NativeName
	if name == "" {
		name = f.About
	}
	return katacli.NewDraft("flow", uid, name, raw, expected)
}

func auxiliaryOverwatch(raw json.RawMessage) bool {
	var marker struct {
		Version int
		Role    string
	}
	return katacli.Decode(raw, &marker) == nil && marker.Version == 1 && marker.Role == "overwatch"
}

// ProjectDefinitions retains supported rows while naming executor-option failures.
func ProjectDefinitions(defs []katacli.Definition) ([]Flow, []error) {
	var flows []Flow
	var problems []error
	for _, def := range defs {
		f, e := FromNative(def)
		if e != nil {
			problems = append(problems, fmt.Errorf("native flow %s cannot be projected: %w", def.UID, e))
			continue
		}
		flows = append(flows, f)
	}
	return flows, problems
}
