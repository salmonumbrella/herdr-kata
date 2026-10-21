package defs

import (
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/netresearch/go-cron"
	"github.com/salmonumbrella/herdr-kata/internal/config"
)

type Owner int

const (
	Ignored Owner = iota
	Adapter
	Native
)

type Support struct {
	Supported         bool
	Reasons, Warnings []string
}

// Dispatchable reports whether an undeleted job is supported for adapter dispatch.
func Dispatchable(owner Owner, support Support, deleted *time.Time) bool {
	return owner == Adapter && support.Supported && deleted == nil
}

const MaxSeconds = int64(math.MaxInt64) / int64(time.Second)

// CronExpression enforces one timezone and Kata's exact five-field/descriptor grammar.
func CronExpression(t Trigger) (cron.Schedule, error) {
	zone := t.Timezone
	if zone == "" {
		zone = "UTC"
	}
	if _, e := time.LoadLocation(zone); e != nil {
		return nil, fmt.Errorf("timezone: %w", e)
	}
	expr := strings.TrimSpace(t.Cron)
	fields := strings.Fields(expr)
	if len(fields) > 0 && (strings.HasPrefix(fields[0], "TZ=") || strings.HasPrefix(fields[0], "CRON_TZ=")) {
		embedded := strings.SplitN(fields[0], "=", 2)[1]
		if embedded != zone {
			return nil, fmt.Errorf("embedded timezone conflicts with trigger timezone")
		}
		expr = strings.TrimSpace(strings.TrimPrefix(expr, fields[0]))
	}
	// DowOrDom keeps crontab's either-day rule when both day fields are restricted.
	return cron.MustNewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor | cron.DowOrDom).Parse("CRON_TZ=" + zone + " " + expr)
}
func ValidateTrigger(t Trigger) error {
	if t.Timezone != "" {
		if _, e := time.LoadLocation(t.Timezone); e != nil {
			return fmt.Errorf("timezone: %w", e)
		}
	}
	switch t.Kind {
	case "cron":
		if _, e := CronExpression(t); e != nil {
			return e
		}
	case "interval":
		if t.IntervalSeconds <= 0 || t.IntervalSeconds > MaxSeconds {
			return fmt.Errorf("invalid interval")
		}
	case "once":
		if _, e := time.Parse(time.RFC3339Nano, t.At); e != nil {
			return fmt.Errorf("invalid once instant")
		}
	case "manual":
	default:
		return fmt.Errorf("unsupported trigger %q", t.Kind)
	}
	if (t.Kind != "cron" && t.Cron != "") || (t.Kind != "interval" && t.IntervalSeconds != 0) || (t.Kind != "once" && t.At != "") || t.IssueUID != "" || t.LeadSeconds != 0 {
		return fmt.Errorf("trigger has fields for another kind")
	}
	return nil
}
func options(raw json.RawMessage, s *Support, scope string) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if e := json.Unmarshal(raw, &m); e != nil || m == nil {
		s.Reasons = append(s.Reasons, "options must be an object")
		return ""
	}
	var agent string
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "agent" {
			if e := json.Unmarshal(m[k], &agent); e != nil || agent == "" {
				s.Reasons = append(s.Reasons, "options.agent must name a profile")
			}
		} else if scope == "job" && (k == "tags" || k == "description") {
			_, _, warnings := JobMetadata(json.RawMessage(`{"` + k + `":` + string(m[k]) + `}`))
			s.Warnings = append(s.Warnings, warnings...)
		} else if k == "run_parent" {
			var uid string
			if scope != "job" || json.Unmarshal(m[k], &uid) != nil || len(uid) != 26 || uid[0] > '7' || strings.Trim(uid, "0123456789ABCDEFGHJKMNPQRSTVWXYZ") != "" {
				s.Reasons = append(s.Reasons, "options.run_parent must be a full issue UID on a job")
			}
		} else if k == "input" {
			var value string
			if scope != "job" || string(m[k]) == "null" || json.Unmarshal(m[k], &value) != nil {
				s.Reasons = append(s.Reasons, "options.input must be a job string")
			} else if e := ValidateInput(value); e != nil {
				s.Reasons = append(s.Reasons, e.Error())
			}
		} else if k == "agent_mode" {
			var mode string
			if scope != "prompt" || json.Unmarshal(m[k], &mode) != nil || mode != "fresh" {
				s.Reasons = append(s.Reasons, "options.agent_mode supports only fresh prompt steps")
			}
		} else if k == "on_fail" && (scope == "prompt" || scope == "command") {
			// The complete edge is validated against the workflow below.
		} else if k == "on_failure" && scope == "workflow" {
			var policy string
			if json.Unmarshal(m[k], &policy) != nil || policy != "park" {
				s.Reasons = append(s.Reasons, "options.on_failure supports only park workflows")
			}
		} else if k == "on_fail" || k == "on_failure" {
			s.Reasons = append(s.Reasons, "options."+k+" unsupported")
		} else {
			s.Warnings = append(s.Warnings, "ignored options key: "+k)
		}
	}
	return agent
}

// Classify separates hub-owned notify children from adapter scheduling.
func Classify(j JobDef, w *WorkflowDef, c config.Config) (Owner, Support) {
	s := Support{}
	owner := Adapter
	if !j.Enabled {
		owner = Ignored
	}
	if j.Enabled && j.Action.Kind == "notify" && j.Issue != nil && j.Issue.Kind == "per-run" {
		owner = Native
	}
	bad := func(reason string) { s.Reasons = append(s.Reasons, reason) }
	if j.Version != 1 || j.Kind != "job" {
		bad("unsupported job version or kind")
	}
	if e := ValidateTrigger(j.Trigger); e != nil {
		bad(e.Error())
	}
	if j.Overlap != "allow" && j.Overlap != "forbid" {
		bad("invalid overlap")
	}
	if j.Catchup != "skip" && j.Catchup != "latest" && j.Catchup != "all" {
		bad("invalid catchup")
	}
	if j.GraceSeconds < 0 || j.TimeoutSeconds < 0 || j.GraceSeconds > MaxSeconds || j.TimeoutSeconds > MaxSeconds {
		bad("invalid duration")
	}
	maxTimeout := c.Limits.MaxTimeout
	if maxTimeout == 0 {
		maxTimeout = 8 * time.Hour
	}
	if j.TimeoutSeconds > 0 && j.TimeoutSeconds <= MaxSeconds && time.Duration(j.TimeoutSeconds)*time.Second > maxTimeout {
		bad(fmt.Sprintf("timeout_seconds %d exceeds limits.max_timeout %s", j.TimeoutSeconds, maxTimeout))
	}
	secretKeys := make([]string, 0, len(j.SecretRefs))
	for variable := range j.SecretRefs {
		secretKeys = append(secretKeys, variable)
	}
	sort.Strings(secretKeys)
	for _, variable := range secretKeys {
		name := j.SecretRefs[variable]
		if !config.ChildVariable(variable) {
			bad("secret_refs invalid variable: " + variable)
		}
		if c.ReservedVariable(variable) {
			bad("secret_refs reserved variable: " + variable)
		}
		if _, ok := c.Secrets[name]; !ok {
			bad("unmapped secret: " + name)
		}
	}
	if j.CheckoutKey != "" {
		if _, ok := c.Checkouts[j.CheckoutKey]; !ok {
			bad("unmapped checkout: checkout_key " + j.CheckoutKey + "; configure checkouts." + j.CheckoutKey)
		}
	}
	profile := options(j.Options, &s, "job")
	var opts map[string]json.RawMessage
	_ = json.Unmarshal(j.Options, &opts)
	if _, present := opts["run_parent"]; present && (j.Action.Kind != "execute" || j.Issue == nil || j.Issue.Kind != "per-run") {
		bad("options.run_parent requires execute with a per-run issue")
	}
	if profile == "" {
		profile = "default"
	}
	if _, ok := c.Profiles[profile]; !ok && owner != Native && PromptBudget(j, w) > 0 {
		bad("unmapped profile: options.agent " + profile + "; configure profiles." + profile + " and include it in the project profiles allowlist")
	}
	if j.Issue == nil {
		bad("missing issue policy")
	} else {
		for _, text := range []struct{ name, value string }{{"title", j.Issue.Title}, {"body", j.Issue.Body}} {
			if !utf8.ValidString(text.value) || strings.ContainsRune(text.value, 0) {
				bad("issue." + text.name + " must be UTF-8 without NUL")
			}
		}
		switch j.Issue.Kind {
		case "existing":
			if j.Issue.UID == "" || j.Issue.ParentUID != "" || j.Issue.Title != "" || j.Issue.Body != "" || j.Issue.DeadlineOffsetSeconds != nil {
				bad("invalid existing issue policy")
			}
		case "per-run":
			if strings.TrimSpace(j.Issue.Title) == "" || j.Issue.UID != "" || j.Action.Kind == "execute" && j.Issue.ParentUID != "" {
				bad("invalid per-run issue policy")
			}
		default:
			bad("unsupported issue policy")
		}
		if j.Issue.ScheduledOffsetSeconds != nil {
			bad("scheduled_offset_seconds unsupported")
		}
		if n := j.Issue.DeadlineOffsetSeconds; n != nil && (*n > MaxSeconds || *n < -MaxSeconds) {
			bad("invalid deadline offset")
		}
	}
	switch j.Action.Kind {
	case "execute":
		if e := ValidatePlaceholders(j.Action.Prompt, true); e != nil {
			bad(e.Error())
		}
		if (j.Action.WorkflowUID == "") == (strings.TrimSpace(j.Action.Prompt) == "") || j.Action.Recipient != "" || j.Action.Message != "" {
			bad("execute requires one prompt or workflow")
		}
	case "notify":
		if j.Action.Prompt != "" || j.Action.WorkflowUID != "" || j.CheckoutKey != "" {
			bad("notify carries execution fields")
		}
		native := j.Issue != nil && j.Issue.Kind == "per-run"
		if strings.TrimSpace(j.Action.Recipient) == "" || (!native && j.Action.Recipient != "current-owner-or-author" && strings.TrimSpace(j.Action.Message) == "") {
			bad("invalid notify recipient or message")
		}
	default:
		bad("unsupported action")
	}
	if j.Action.WorkflowUID != "" {
		if w == nil {
			bad("missing or deleted workflow")
		} else {
			if w.Version != 1 || len(w.Steps) == 0 {
				bad("invalid workflow version or empty steps")
			}
			order, e := TopoOrder(w.Steps)
			if e != nil {
				bad(e.Error())
			} else if _, e := WorkflowLoop(w); e != nil {
				bad(e.Error())
			}
			if e := ValidateInput(w.Input); e != nil {
				bad("workflow " + e.Error())
			}
			for _, step := range w.Steps {
				first := len(order) > 0 && step.Key == order[0]
				if e := ValidatePlaceholders(step.Prompt, first); e != nil {
					bad(e.Error())
				}
			}
			if e := ValidatePlaceholders(w.About, true); e != nil {
				bad(e.Error())
			}
			if p := options(w.Options, &s, "workflow"); p != "" && p != profile {
				bad("workflow profile differs from run profile")
			}
			for _, step := range w.Steps {
				if step.Kind == "command" && (!utf8.ValidString(step.Command) || strings.ContainsRune(step.Command, 0)) {
					bad("command must be UTF-8 without NUL")
				}
				if step.Kind == "command" && !c.CommandsAllowed() {
					bad(fmt.Sprintf("commands are disabled for project %q (commands=false)", c.Kata.Project))
				}
				if step.Retries < 0 || step.Retries > 100 {
					bad("invalid retries")
				}
				if (step.Kind == "prompt" && (strings.TrimSpace(step.Prompt) == "" || step.Command != "")) || (step.Kind == "command" && (strings.TrimSpace(step.Command) == "" || step.Prompt != "")) || (step.Kind != "prompt" && step.Kind != "command") {
					bad("invalid workflow step")
				}
				if p := options(step.Options, &s, step.Kind); p != "" && p != profile {
					if step.Kind != "prompt" {
						bad("step profile differs from run profile")
					} else if _, ok := c.Profiles[p]; !ok {
						bad("unmapped profile: steps." + step.Key + ".options.agent " + p + "; configure profiles." + p + " and include it in the project profiles allowlist")
					}
				}
			}
		}
	}
	if PromptBudget(j, w) > 0 && promptContentBytes(j, w) > MaxPromptBytes-(4<<10) {
		bad("prompt exceeds 256 KiB including context and runtime envelope (4 KiB reserved)")
	}
	if budget, cap := PromptBudget(j, w), RunCap(c, profile, w); budget > cap {
		bad(fmt.Sprintf("prompt_budget %d > %d", budget, cap))
	}
	if owner == Adapter && j.Issue != nil && j.Issue.Kind == "per-run" {
		if n := dailyOccurrences(j.Trigger); n > maxDailyIssues {
			advice := "an existing issue, a run parent, or a native recurring job"
			if _, grouped := opts["run_parent"]; grouped {
				advice = "an existing issue or a native recurring job"
			}
			s.Warnings = append(s.Warnings, fmt.Sprintf("creates about %d%s%s", n, volumeMarker, advice))
		}
	}
	s.Supported = len(s.Reasons) == 0
	return owner, s
}

const (
	maxDailyIssues = 24
	volumeMarker   = " issues a day; consider "
)

// IsVolumeWarning reports whether a Classify warning is the issue-volume warning.
func IsVolumeWarning(w string) bool {
	return strings.HasPrefix(w, "creates about ") && strings.Contains(w, volumeMarker)
}

// dailyOccurrences estimates a trigger's occurrences on its busiest day. A
// cron that fires at all fires at each of its minutes and hours on any day
// it matches, whatever its weekday or day of the month.
func dailyOccurrences(t Trigger) int64 {
	switch t.Kind {
	case "interval":
		if t.IntervalSeconds > 0 {
			return 86400 / t.IntervalSeconds
		}
	case "cron":
		schedule, err := CronExpression(t)
		if err != nil {
			return 0
		}
		start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
		if spec, ok := schedule.(*cron.SpecSchedule); ok {
			if schedule.Next(start).IsZero() {
				return 0
			}
			return int64(bits.OnesCount64(spec.Minute&(1<<60-1)) * bits.OnesCount64(spec.Hour&(1<<24-1)))
		}
		// Five-field cron fires at most once a minute.
		var n int64
		for next := schedule.Next(start.Add(-time.Second)); !next.IsZero() && next.Before(start.Add(24*time.Hour)) && n < 1440; next = schedule.Next(next) {
			n++
		}
		return n
	}
	return 0
}

// RunParent returns the run parent of a validated job.
func RunParent(j JobDef) string {
	return OptionString(j.Options, "run_parent")
}

// OptionString reads a string from validated options using an exact key.
func OptionString(raw json.RawMessage, key string) string {
	var opts map[string]json.RawMessage
	if json.Unmarshal(raw, &opts) != nil {
		return ""
	}
	var value string
	if json.Unmarshal(opts[key], &value) != nil {
		return ""
	}
	return value
}

// Static validation reserves the report instructions and run identity envelope.
// Runtime expansion still checks the complete prompt, including findings and paths.
func promptContentBytes(j JobDef, w *WorkflowDef) int {
	base := 0
	if j.Issue != nil {
		base = len(j.Issue.Title) + len(j.Issue.Body)
	}
	input, _ := JobInput(j.Options)
	inputBytes := 0
	if input != nil {
		inputBytes = len(*input)
	}
	expand := func(s string) int {
		return len(s) + strings.Count(s, "{{input}}")*max(0, inputBytes-9) + strings.Count(s, "{{previous}}")*(MaxNoteBytes-12)
	}
	if w == nil {
		return base + inputBytes + expand(j.Action.Prompt)
	}
	base += inputBytes + len(w.Input) + expand(w.About)
	largest := 0
	for _, step := range w.Steps {
		if step.Kind == "prompt" {
			largest = max(largest, expand(step.Prompt))
		}
	}
	return base + largest
}
