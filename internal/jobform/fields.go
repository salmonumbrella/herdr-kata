package jobform

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type FieldKind uint8

const (
	Text FieldKind = iota
	TextArea
	Bool
	Choice
	ReadOnly
)

type Context struct {
	IssueReference func(string) string
	Config         config.Config
	Workflows      []kata.Workflow
	Project        string
	Native         bool
}
type Draft struct {
	Raw    map[string]any
	Values map[string]string
	Dirty  map[string]bool
}
type Field struct {
	Key, Label, Help, ReadOnlyReason string
	Kind                             FieldKind
	Choices                          []string
}

var fieldPaths = map[string]string{"description": "options.description", "tags": "options.tags", "input": "options.input", "enabled": "enabled", "trigger": "trigger.kind", "timezone": "trigger.timezone", "prompt": "action.prompt", "workflow": "action.workflow_uid", "recipient": "action.recipient", "message": "action.message", "issue_kind": "issue.kind", "issue": "issue.uid", "parent": "issue.parent_uid", "title": "issue.title", "body": "issue.body", "deadline": "issue.deadline_offset_seconds", "overlap": "overlap", "catchup": "catchup", "grace": "grace_seconds", "timeout": "timeout_seconds", "checkout": "checkout_key", "profile": "options.agent", "run_parent": "options.run_parent", "secret_refs": "secret_refs"}

func rawValue(raw map[string]any, path string) any {
	var v any = raw
	for key := range strings.SplitSeq(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[key]
	}
	return v
}

// Path returns the definition path for an editable key and trigger kind.
func Path(key, trigger string) string {
	if key == "schedule" {
		switch trigger {
		case "cron":
			return "trigger.cron"
		case "interval":
			return "trigger.interval_seconds"
		case "once":
			return "trigger.at"
		}
		return ""
	}
	return fieldPaths[key]
}

// ModeOf derives the form action from a raw definition.
func ModeOf(raw map[string]any) string {
	if rawValue(raw, "action.kind") == "notify" {
		if rawValue(raw, "issue.kind") == "per-run" {
			return "recurring"
		}
		return "notify"
	}
	if uid := rawValue(raw, "action.workflow_uid"); uid != nil && uid != "" {
		return "workflow"
	}
	return "prompt"
}

// NewDraft hydrates editable text without normalizing the stored definition.
func NewDraft(raw map[string]any, name string) Draft {
	d := Draft{Raw: raw, Values: map[string]string{"name": name, "mode": ModeOf(raw)}, Dirty: map[string]bool{}}
	for key, path := range fieldPaths {
		v := rawValue(raw, path)
		if v == nil {
			continue
		}
		switch key {
		case "tags":
			b, _ := json.Marshal(v)
			var tags []string
			_ = json.Unmarshal(b, &tags)
			d.Values[key] = strings.Join(tags, ", ")
		case "secret_refs":
			b, _ := json.Marshal(v)
			var refs map[string]string
			_ = json.Unmarshal(b, &refs)
			d.Values[key] = secretText(refs)
		case "grace", "timeout", "deadline":
			text := fmt.Sprint(v)
			d.Values[key] = text
			if n, err := ParseSeconds(key, text, key == "deadline"); err == nil {
				d.Values[key] = SecondsText(n)
			}
		default:
			d.Values[key] = fmt.Sprint(v)
		}
	}
	if v := rawValue(raw, Path("schedule", d.Values["trigger"])); v != nil {
		d.Values["schedule"] = fmt.Sprint(v)
		if d.Values["trigger"] == "interval" {
			if n, err := ParseSeconds("interval", d.Values["schedule"], false); err == nil {
				d.Values["schedule"] = SecondsText(n)
			}
		}
	}
	return d
}

func value(d Draft, key string) string { return d.Values[key] }

var knownOptions = map[string]bool{"agent": true, "input": true, "run_parent": true, "tags": true, "description": true}

func workflow(d Draft, c Context) *defs.WorkflowDef {
	for _, w := range c.Workflows {
		if w.DeletedAt == nil && w.UID == value(d, "workflow") {
			parsed, err := defs.DecodeWorkflow(w.Definition)
			if err == nil {
				return &parsed
			}
		}
	}
	return nil
}
func Fields(d Draft, c Context) []Field {
	var out []Field
	add := func(key, label string, kind FieldKind, help string, choices ...string) {
		out = append(out, Field{Key: key, Label: label, Kind: kind, Help: help, Choices: choices})
	}
	mode := value(d, "mode")
	execute := mode == "prompt" || mode == "workflow"
	add("name", "Name", Text, "required; must be unique in the project")
	if mode != "recurring" {
		add("description", "Description", Text, "up to 200 runes")
		add("tags", "Tags", Text, "comma-delimited · [a-z0-9._:-], 1–64 characters · at most 16 tags")
	}
	add("mode", "Action", Choice, "choose the job action", "prompt", "workflow", "notify", "recurring")
	if c.Native {
		out[len(out)-1].Kind = ReadOnly
		out[len(out)-1].ReadOnlyReason = "native jobs must remain recurring"
	}
	switch mode {
	case "prompt":
		add("prompt", "Prompt", TextArea, "required; supports job placeholders")
	case "workflow":
		var choices []string
		for _, w := range c.Workflows {
			if w.DeletedAt == nil {
				choices = append(choices, w.UID)
			}
		}
		sort.Slice(choices, func(i, j int) bool {
			return workflowName(c, choices[i])+choices[i] < workflowName(c, choices[j])+choices[j]
		})
		help := "choose an available workflow"
		if w := workflow(d, c); w != nil {
			help = fmt.Sprintf("%s · %d steps · %d prompt steps", workflowName(c, value(d, "workflow")), len(w.Steps), defs.PromptBudget(defs.JobDef{Action: defs.Action{Kind: "execute", WorkflowUID: value(d, "workflow")}}, w))
		}
		add("workflow", "Workflow", Choice, help, choices...)
		add("input", "Input", Text, "workflow input; up to 4 KiB")
	case "notify":
		add("recipient", "Recipient", Text, "notification recipient")
		add("message", "Message", TextArea, "notification text")
	case "recurring":
		add("parent", "Parent issue", Text, "parent issue reference")
		if c.Native {
			out[len(out)-1].Kind = ReadOnly
			out[len(out)-1].ReadOnlyReason = "fixed; b edits the binding"
		}
		add("title", "Child title", Text, "required child title")
		add("body", "Child body", TextArea, "the child's instruction")
		add("message", "Message", TextArea, "optional notification text")
	}
	triggers := []string{"manual", "cron", "interval", "once"}
	if mode == "recurring" {
		triggers = []string{"cron", "interval"}
	}
	add("trigger", "Schedule", Choice, "choose the schedule", triggers...)
	switch value(d, "trigger") {
	case "cron":
		add("schedule", "Cron", Text, "5-field cron, e.g. 0 7 * * *")
	case "interval":
		add("schedule", "Interval", Text, "e.g. 1h, 30m · whole seconds also accepted")
	case "once":
		add("schedule", "Run at", Text, "RFC3339 with offset or 2006-01-02 15:04[:05] · "+value(d, "schedule"))
	}
	add("timezone", "Timezone", Text, "the instant is kept; future wall-time input uses this timezone")
	add("catchup", "Catchup", Choice, "missed occurrences", "latest", "all", "skip")
	add("grace", "Grace", Text, "duration; blank removes the override")
	add("overlap", "Overlap", Choice, "allow or forbid overlapping runs", "forbid", "allow")
	if mode == "recurring" {
		out[len(out)-1].Kind = ReadOnly
		out[len(out)-1].ReadOnlyReason = "recurring jobs require allow"
	}
	if execute {
		timeout, maxTimeout := c.Config.Limits.DefaultTimeout, c.Config.Limits.MaxTimeout
		if timeout == 0 {
			timeout = 2 * time.Hour
		}
		if maxTimeout == 0 {
			maxTimeout = 8 * time.Hour
		}
		add("timeout", "Timeout", Text, fmt.Sprintf("blank uses limits.default_timeout (%s) · max %s", timeout, maxTimeout))
		checkouts := []string{""}
		for key := range c.Config.Checkouts {
			checkouts = append(checkouts, key)
		}
		sort.Strings(checkouts)
		add("checkout", "Checkout", Choice, c.Config.Checkouts[value(d, "checkout")], checkouts...)
		profiles := make([]string, 0, len(c.Config.Profiles))
		for key := range c.Config.Profiles {
			profiles = append(profiles, key)
		}
		sort.Strings(profiles)
		facts := ProfileFacts(c.Config.Profiles[value(d, "profile")])
		model := facts.Model
		if model == "" {
			model = "—"
		} else if facts.Effort != "" {
			model += " " + facts.Effort
		}
		planned := 1
		var steps *defs.WorkflowDef
		if mode == "workflow" {
			steps = workflow(d, c)
			planned = defs.PromptBudget(defs.JobDef{Action: defs.Action{Kind: "execute", WorkflowUID: value(d, "workflow")}}, steps)
		}
		help := fmt.Sprintf("%s · model %s · sandbox %s · approvals %s · %d planned / %d cap", facts.Kind, model, facts.Sandbox, facts.Approvals, planned, defs.RunCap(c.Config, value(d, "profile"), steps))
		add("profile", "Profile", Choice, help, profiles...)
		if mode == "workflow" {
			if w := workflow(d, c); w != nil && defs.PromptBudget(defs.JobDef{Action: defs.Action{Kind: "execute", WorkflowUID: value(d, "workflow")}}, w) == 0 {
				out[len(out)-1].Kind = ReadOnly
				out[len(out)-1].ReadOnlyReason = "not needed: command-only workflow"
			}
		}
		add("issue_kind", "Issue", Choice, "issue policy", "per-run", "existing")
		if value(d, "issue_kind") == "per-run" {
			add("title", "Issue title", Text, "blank uses the job name")
			add("body", "Issue body", TextArea, "run issue body")
			add("run_parent", "Run parent", Text, "open issue in this project; resolved at save")
			add("deadline", "Deadline offset", Text, "signed duration; blank removes the offset")
		} else {
			add("issue", "Existing issue", Text, "issue reference; resolved at save")
		}
		names := make([]string, 0, len(c.Config.Secrets))
		for name := range c.Config.Secrets {
			names = append(names, name)
		}
		sort.Strings(names)
		add("secret_refs", "Secret refs", Text, "VAR=name, … · allowed names: "+strings.Join(names, ", "))
	} else if mode == "notify" {
		add("issue", "Existing issue", Text, "issue reference; resolved at save")
	}
	if mode != "recurring" {
		add("other_options", "Other options", ReadOnly, "preserved opaque fields")
		out[len(out)-1].ReadOnlyReason = "preserved; edit the definition in Kata"
	}
	add("enabled", "Enabled", Bool, "enable scheduling")
	if mode == "recurring" {
		out[len(out)-1].Help = "new recurring jobs are saved disabled"
	}
	add("favorite", "Favorite", Bool, "local board preference")
	return out
}
func workflowName(c Context, uid string) string {
	if uid == "" {
		return "select a workflow"
	}
	for _, w := range c.Workflows {
		if w.UID == uid {
			return w.Name
		}
	}
	return "workflow unavailable"
}
func Get(f Field, d Draft, c Context) (display, edit string) {
	edit = value(d, f.Key)
	display = edit
	switch f.Key {
	case "overlap":
		if value(d, "mode") == "recurring" {
			display = "allow"
			edit = "allow"
		}
	case "schedule":
		if value(d, "trigger") == "once" && edit != "" {
			if s, _, err := RunAtText(edit, value(d, "timezone")); err == nil {
				display = s
			}
		}
	case "workflow":
		display = workflowName(c, edit)
	case "issue", "parent", "run_parent":
		display = ""
		if c.IssueReference != nil {
			display = c.IssueReference(edit)
		}
		if display == "" {
			if i := strings.Index(edit, "#"); i > 0 {
				display = edit
			} else if short := strings.TrimPrefix(edit, "#"); short != "" && len(short) < 26 && c.Project != "" {
				display = c.Project + "#" + short
			}
		}

	case "other_options":
		if opts, ok := d.Raw["options"].(map[string]any); ok {
			var keys []string
			for key := range opts {
				if !knownOptions[key] {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			display = strings.Join(keys, ", ")
			if display != "" {
				display += " (preserved)"
			}
		}
	case "profile":
		if f.Kind == ReadOnly {
			display = "— (" + f.ReadOnlyReason + ")"
		} else if p, ok := c.Config.Profiles[edit]; ok {
			facts := ProfileFacts(p)
			model := facts.Model
			if model == "" {
				model = "agent default"
			}
			display = edit + " · " + model
			if facts.Effort != "" {
				display += " " + facts.Effort
			}
		}
	}
	if f.Kind == Bool {
		display = "no"
		if edit == "true" {
			display = "yes"
		}
	}
	if display == "" {
		display = "—"
	}
	return
}
func Set(f Field, d *Draft, c Context, text string) error {
	if f.Kind == ReadOnly {
		return fmt.Errorf("%s is read-only: %s", f.Label, f.ReadOnlyReason)
	}
	canonical := text
	switch f.Key {
	case "name":
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("name: required")
		}
		canonical = strings.TrimSpace(text)
	case "description":
		if err := defs.ValidateJobDescription(text); err != nil {
			return err
		}
	case "tags":
		tags, err := canonicalTags(text)
		if err != nil {
			return err
		}
		canonical = strings.Join(tags, ", ")
	case "secret_refs":
		refs, err := ParseSecretRefs(text, c.Config)
		if err != nil {
			return err
		}
		canonical = secretText(refs)
	case "timezone":
		if _, err := jobLocation(text); err != nil {
			return err
		}
	case "schedule":
		switch value(*d, "trigger") {
		case "cron":
			if _, err := defs.CronExpression(defs.Trigger{Kind: "cron", Cron: text, Timezone: value(*d, "timezone")}); err != nil {
				return fmt.Errorf("cron: %w", err)
			}
		case "once":
			var err error
			canonical, err = ParseRunAt(text, value(*d, "schedule"), value(*d, "timezone"))
			if err != nil {
				return err
			}
		case "interval":
			n, err := ParseSeconds("interval", text, false)
			if err != nil {
				return err
			}
			if n <= 0 || value(*d, "mode") == "recurring" && n < 60 {
				return fmt.Errorf("interval: must be positive; recurring requires at least 1m")
			}
			canonical = SecondsText(n)
		}
	case "timeout", "grace", "deadline":
		if strings.TrimSpace(text) != "" {
			n, err := ParseSeconds(f.Key, text, f.Key == "deadline")
			if err != nil {
				return err
			}
			if f.Key == "timeout" {
				maximum := c.Config.Limits.MaxTimeout
				if maximum == 0 {
					maximum = 8 * time.Hour
				}
				if time.Duration(n)*time.Second > maximum {
					return fmt.Errorf("timeout: exceeds max %s", maximum)
				}
			}
			canonical = SecondsText(n)
		} else {
			canonical = ""
		}
	case "input":
		if err := defs.ValidateInput(text); err != nil {
			return err
		}
	case "prompt":
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("prompt: required")
		}
		if err := defs.ValidatePlaceholders(text, true); err != nil {
			return err
		}
	}
	if f.Kind == Choice {
		valid := false
		for _, choice := range f.Choices {
			valid = valid || canonical == choice
		}
		if !valid {
			return fmt.Errorf("%s: choose an available value", f.Label)
		}
	}
	if canonical == value(*d, f.Key) {
		return nil
	}
	if d.Values == nil {
		d.Values = map[string]string{}
	}
	if d.Dirty == nil {
		d.Dirty = map[string]bool{}
	}
	if f.Key == "trigger" {
		d.Values["schedule:"+value(*d, "trigger")] = value(*d, "schedule")
		d.Values["schedule"] = d.Values["schedule:"+canonical]
	}
	d.Values[f.Key] = canonical
	d.Dirty[f.Key] = true
	return nil
}

// ParseSecretRefs validates names only; it never resolves secret sources.
func ParseSecretRefs(text string, c config.Config) (map[string]string, error) {
	refs := map[string]string{}
	if strings.TrimSpace(text) == "" {
		return refs, nil
	}
	for part := range strings.SplitSeq(text, ",") {
		variable, name, ok := strings.Cut(strings.TrimSpace(part), "=")
		variable = strings.TrimSpace(variable)
		name = strings.TrimSpace(name)
		if !ok || !config.ChildVariable(variable) {
			return nil, fmt.Errorf("secret refs: invalid variable %s", variable)
		}
		if c.ReservedVariable(variable) {
			return nil, fmt.Errorf("secret refs: reserved variable %s", variable)
		}
		if _, exists := refs[variable]; exists {
			return nil, fmt.Errorf("secret refs: duplicate variable %s", variable)
		}
		if _, mapped := c.Secrets[name]; !mapped {
			return nil, fmt.Errorf("secret refs: unmapped name %s", name)
		}
		refs[variable] = name
	}
	return refs, nil
}
func secretText(refs map[string]string) string {
	keys := make([]string, 0, len(refs))
	for key := range refs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+refs[key])
	}
	return strings.Join(parts, ", ")
}

func canonicalTags(text string) ([]string, error) {
	var tags []string
	seen := map[string]bool{}
	if strings.TrimSpace(text) != "" {
		for tag := range strings.SplitSeq(text, ",") {
			tag = strings.TrimSpace(tag)
			if !seen[tag] {
				tags = append(tags, tag)
				seen[tag] = true
			}
		}
	}
	if err := defs.ValidateJobTags(tags); err != nil {
		return nil, err
	}
	return tags, nil
}

// Encode returns a field's stored definition path and value. Nil deletes the field.
// It leaves form policy and preservation of untouched fields to the caller.
func Encode(key string, d Draft, c Context) (path string, v any, err error) {
	path = Path(key, value(d, "trigger"))
	if path == "" {
		return "", nil, nil
	}
	text := value(d, key)
	switch key {
	case "schedule":
		switch value(d, "trigger") {
		case "interval":
			n, err := ParseSeconds("interval", text, false)
			return path, n, err
		case "once":
			original, _ := rawValue(d.Raw, "trigger.at").(string)
			stored, err := ParseRunAt(text, original, value(d, "timezone"))
			return path, stored, err
		default:
			return path, text, nil
		}
	case "enabled":
		return path, text == "true", nil
	case "deadline", "grace", "timeout":
		if strings.TrimSpace(text) == "" {
			return path, nil, nil
		}
		n, err := ParseSeconds(key, text, key == "deadline")
		return path, n, err
	case "tags":
		tags, err := canonicalTags(text)
		if err != nil {
			return path, nil, err
		}
		if len(tags) == 0 {
			return path, nil, nil
		}
		return path, tags, nil
	case "secret_refs":
		refs, err := ParseSecretRefs(text, c.Config)
		if err != nil {
			return path, nil, err
		}
		if len(refs) == 0 {
			return path, nil, nil
		}
		return path, refs, nil
	default:
		if text == "" {
			return path, nil, nil
		}
		return path, text, nil
	}
}
