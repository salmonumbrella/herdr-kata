package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"math"
	"path/filepath"
	"strings"
	"time"
)

// The editable operational fields live under executor options. Paths and secret
// values are resolved from NativeBinding and never copied into this document.
type nativeJobOptions struct {
	Name, Description, Ref, Kind, Model, PermissionMode, AllowedTools, DisallowedTools, ExtraArgs, MaxBudgetUSD, AutoCompact, OnContextLoss string
	Tags                                                                                                                                    []string
	SkipPermissions, Favorite, Persistent, KeepContext                                                                                      bool
	Input                                                                                                                                   string
}

func putRaw(m map[string]json.RawMessage, key string, value any) {
	raw, _ := json.Marshal(value)
	m[key] = raw
}
func (r *NativeRepository) JobDraft(j Job) (katacli.Draft, error) {
	if r == nil || r.Client == nil {
		return katacli.Draft{}, ErrNativeUnconfigured
	}
	if j.Timeout < 0 {
		return katacli.Draft{}, errors.New("timeout must be nonnegative; zero uses the local default")
	}
	if j.Schedule == ScheduleInterval && (j.IntervalSeconds <= 0 || int64(j.IntervalSeconds) > math.MaxInt64/int64(time.Second)) {
		return katacli.Draft{}, errors.New("interval must be positive and fit a local duration")
	}
	key := j.CheckoutKey
	if j.CWD != "" {
		key = ""
		for candidate, path := range r.Binding.Checkouts {
			if filepath.Clean(path) == filepath.Clean(j.CWD) {
				key = candidate
				break
			}
		}
		if key == "" {
			return katacli.Draft{}, errors.New("map this directory with `native checkout` before saving; absolute paths stay local")
		}
	}
	if len(j.AddDirs) > 0 {
		return katacli.Draft{}, errors.New("additional directories need local checkout mappings; portable definitions cannot export paths")
	}
	var def map[string]json.RawMessage
	if len(j.NativeDefinition) > 0 {
		if e := katacli.Decode(j.NativeDefinition, &def); e != nil {
			return katacli.Draft{}, e
		}
	} else {
		def = map[string]json.RawMessage{}
		putRaw(def, "version", 1)
		putRaw(def, "kind", "job")
		putRaw(def, "overlap", "forbid")
		putRaw(def, "issue", map[string]string{"kind": "per-run", "title": j.Name})
	}
	if j.Catchup == "" {
		j.Catchup = CatchupLatest
	}
	putRaw(def, "catchup", j.Catchup)
	if len(j.NativeDefinition) == 0 {
		putRaw(def, "enabled", false)
	}
	putRaw(def, "timeout_seconds", int64(j.Timeout/time.Second))
	putRaw(def, "checkout_key", key)
	delete(def, "executor")
	trigger := map[string]any{"kind": string(j.Schedule)}
	switch j.Schedule {
	case ScheduleCron:
		trigger["cron"] = j.CronExpr
	case ScheduleInterval:
		trigger["interval_seconds"] = j.IntervalSeconds
	case ScheduleOnce:
		if j.RunAt != nil {
			trigger["at"] = j.RunAt.Format(time.RFC3339Nano)
		}
	case "":
		trigger["kind"] = "manual"
	}
	// Preserve native date triggers and their timezone/source fields unless this
	// form actually selects a different fixed trigger.
	var original map[string]json.RawMessage
	if katacli.Decode(def["trigger"], &original) == nil {
		var kind string
		json.Unmarshal(original["kind"], &kind)
		if strings.HasPrefix(kind, "issue-") && string(j.Schedule) == kind {
			trigger = nil
		} else if timezone, ok := original["timezone"]; ok {
			var zone string
			json.Unmarshal(timezone, &zone)
			trigger["timezone"] = zone
		}
	}
	if trigger != nil {
		putRaw(def, "trigger", trigger)
	}
	var originalAction map[string]json.RawMessage
	if katacli.Decode(def["action"], &originalAction) == nil {
		var kind string
		json.Unmarshal(originalAction["kind"], &kind)
		if kind == "notify" {
			return katacli.Draft{}, errors.New("notification jobs use native definition JSON editing")
		}
	}
	action := map[string]string{"kind": "execute"}
	if j.Flow != "" {
		flowUID, e := katacli.NormalizeUID(j.Flow)
		if e != nil {
			return katacli.Draft{}, errors.New("select a native flow UID")
		}
		action["flow_uid"] = flowUID
	} else {
		action["prompt"] = j.Prompt
	}
	putRaw(def, "action", action)
	options := map[string]json.RawMessage{}
	if raw := def["options"]; len(raw) > 0 {
		if e := katacli.Decode(raw, &options); e != nil {
			return katacli.Draft{}, e
		}
	}
	mergedOptions, e := katacli.MergeObject(options["herdr"], nativeJobOptions{j.Name, j.Description, j.Ref, j.Kind, j.Model, j.PermissionMode, j.AllowedTools, j.DisallowedTools, j.ExtraArgs, j.MaxBudgetUSD, j.AutoCompact, j.OnContextLoss, j.Tags, j.SkipPermissions, j.Favorite, j.Persistent, j.KeepContext, j.Input})
	if e != nil {
		return katacli.Draft{}, e
	}
	options["herdr"] = mergedOptions
	putRaw(def, "options", options)
	raw, e := json.Marshal(def)
	if e != nil {
		return katacli.Draft{}, e
	}
	return katacli.NewDraft("job", j.ID, j.Name, raw, j.NativeEventUID)
}
func (r *NativeRepository) JobFrom(def katacli.Definition, offline bool) (Job, error) {
	var body struct {
		Enabled bool `json:"enabled"`
		Trigger struct {
			Kind, Cron, At string
			Interval       int `json:"interval_seconds"`
		} `json:"trigger"`
		Action struct {
			Prompt string `json:"prompt"`
			Flow   string `json:"flow_uid"`
		} `json:"action"`
		Checkout string                     `json:"checkout_key"`
		Catchup  string                     `json:"catchup"`
		Timeout  int64                      `json:"timeout_seconds"`
		Overlap  string                     `json:"overlap"`
		Grace    int64                      `json:"grace_seconds"`
		Options  map[string]json.RawMessage `json:"options"`
	}
	if e := katacli.Decode(def.Definition, &body); e != nil {
		return Job{}, e
	}
	if body.Overlap != "" && body.Overlap != "forbid" {
		return Job{}, errors.New("unsupported overlap policy: this installation supports forbid")
	}
	if body.Grace != 0 {
		return Job{}, errors.New("unsupported grace_seconds: this installation supports the default first-fire grace")
	}
	if body.Timeout < 0 || body.Timeout > math.MaxInt64/int64(time.Second) {
		return Job{}, fmt.Errorf("unsupported timeout_seconds: must fit a local duration")
	}
	if body.Trigger.Kind == "interval" && (body.Trigger.Interval <= 0 || int64(body.Trigger.Interval) > math.MaxInt64/int64(time.Second)) {
		return Job{}, fmt.Errorf("unsupported interval_seconds: must be positive and fit a local duration")
	}
	var opts nativeJobOptions
	if raw := body.Options["herdr"]; len(raw) > 0 {
		if e := katacli.Decode(raw, &opts); e != nil {
			return Job{}, e
		}
	}
	j := Job{CreatedAt: def.CreatedAt, ID: def.UID, Name: def.Name, Description: opts.Description, Ref: opts.Ref, Kind: opts.Kind, Model: opts.Model, Prompt: body.Action.Prompt, Flow: body.Action.Flow, Input: opts.Input, CWD: r.Binding.Checkouts[body.Checkout], CheckoutKey: body.Checkout, Enabled: body.Enabled, Schedule: ScheduleType(body.Trigger.Kind), CronExpr: body.Trigger.Cron, IntervalSeconds: body.Trigger.Interval, Catchup: body.Catchup, Timeout: time.Duration(body.Timeout) * time.Second, Tags: opts.Tags, PermissionMode: opts.PermissionMode, AllowedTools: opts.AllowedTools, DisallowedTools: opts.DisallowedTools, ExtraArgs: opts.ExtraArgs, SkipPermissions: opts.SkipPermissions, MaxBudgetUSD: opts.MaxBudgetUSD, AutoCompact: opts.AutoCompact, OnContextLoss: opts.OnContextLoss, Favorite: opts.Favorite, Persistent: opts.Persistent, KeepContext: opts.KeepContext, NativeEventUID: def.DefinitionEventUID, NativeDefinition: append(json.RawMessage(nil), def.Definition...), NativeOffline: offline}
	if body.Trigger.At != "" {
		at, e := time.Parse(time.RFC3339Nano, body.Trigger.At)
		if e != nil {
			return j, e
		}
		j.RunAt = &at
	}
	return j, nil
}
