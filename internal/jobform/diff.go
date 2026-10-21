package jobform

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
)

type Change struct{ Field, Yours, Kata string }

func diffDraft(def defs.JobDef, name string) Draft {
	body, _ := json.Marshal(def)
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var raw map[string]any
	_ = decoder.Decode(&raw)
	return NewDraft(raw, name)
}
func scheduleText(d defs.JobDef) string {
	switch d.Trigger.Kind {
	case "cron":
		return d.Trigger.Cron
	case "interval":
		return "every " + SecondsText(d.Trigger.IntervalSeconds)
	case "once":
		if display, _, err := RunAtText(d.Trigger.At, d.Trigger.Timezone); err == nil {
			return "once " + display
		}
		return "once " + d.Trigger.At
	default:
		return d.Trigger.Kind
	}
}
func opaqueValue(raw json.RawMessage) any {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return string(raw)
	}
	return value
}

// Diff lists human fields in form order and names changed opaque options only.
func Diff(a, b defs.JobDef, nameA, nameB string, contexts ...Context) []Change {
	da, db := diffDraft(a, nameA), diffDraft(b, nameB)
	c := Context{}
	if len(contexts) > 0 {
		c = contexts[0]
	}
	fields := Fields(da, c)
	seen := map[string]bool{}
	for _, f := range fields {
		seen[f.Key] = true
	}
	for _, f := range Fields(db, c) {
		if !seen[f.Key] {
			fields = append(fields, f)
			seen[f.Key] = true
		}
	}
	var changes []Change
	for _, f := range fields {
		if f.Key == "favorite" || f.Key == "other_options" || f.Key == "schedule" {
			continue
		}
		label := strings.ToLower(f.Label)
		av, ae := Get(f, da, c)
		bv, be := Get(f, db, c)
		if av == bv && ae != be && (f.Key == "workflow" || f.Key == "issue" || f.Key == "parent" || f.Key == "run_parent") {
			av = "previous reference"
			bv = "different reference"
		}
		if f.Key == "trigger" {
			label = "schedule"
			av, bv = scheduleText(a), scheduleText(b)
		}
		if av != bv {
			changes = append(changes, Change{label, av, bv})
		}
	}
	var oa, ob map[string]json.RawMessage
	_ = json.Unmarshal(a.Options, &oa)
	_ = json.Unmarshal(b.Options, &ob)
	keys := map[string]bool{}
	for k := range oa {
		keys[k] = true
	}
	for k := range ob {
		keys[k] = true
	}
	var ordered []string
	for k := range keys {
		if !knownOptions[k] {
			ordered = append(ordered, k)
		}
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		ar, ap := oa[key]
		br, bp := ob[key]
		if ap == bp && reflect.DeepEqual(opaqueValue(ar), opaqueValue(br)) {
			continue
		}
		av, bv := "—", "—"
		if ap {
			av = "present (preserved)"
		}
		if bp {
			bv = "present (preserved)"
		}
		changes = append(changes, Change{"options." + key, av, bv})
	}
	return changes
}
