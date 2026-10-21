package defs

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
)

func fixture(t *testing.T, name string) JobDef {
	t.Helper()
	b, e := os.ReadFile("testdata/" + name + ".json")
	if e != nil {
		t.Fatal(e)
	}
	j, e := DecodeJob(b)
	if e != nil {
		t.Fatal(e)
	}
	return j
}
func cfg() config.Config {
	return config.Config{Checkouts: map[string]string{"repo": "/repo"}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}, "other": {Kind: "claude"}}}
}
func TestDecodeJobDocExample(t *testing.T) {
	j := fixture(t, "job")
	owner, s := Classify(j, nil, cfg())
	if owner != Adapter || !s.Supported {
		t.Fatalf("%v %+v", owner, s)
	}
	if j.Action.Prompt != "Review the changes" || j.CheckoutKey != "repo" {
		t.Fatalf("%+v", j)
	}
}
func TestDecodeJob540Shape(t *testing.T) {
	j := fixture(t, "native")
	if j.Issue.ParentUID != "01M4TADGDFRMAC38MX3NZCW1K5" || j.Action.Recipient != "actor/runtime" {
		t.Fatalf("%+v", j)
	}
	b, _ := json.Marshal(j)
	round, e := DecodeJob(b)
	if e != nil || !reflect.DeepEqual(round, j) {
		t.Fatal(e, round)
	}
}
func TestClassifyNative(t *testing.T) {
	j := fixture(t, "native")
	owner, s := Classify(j, nil, cfg())
	if owner != Native || !s.Supported {
		t.Fatalf("%v %+v", owner, s)
	}
	j.Enabled = false
	owner, _ = Classify(j, nil, cfg())
	if owner != Ignored {
		t.Fatal(owner)
	}
}
func TestClassifyNativeWithoutMessage(t *testing.T) {
	j := fixture(t, "native")
	j.Action.Message = ""
	owner, support := Classify(j, nil, cfg())
	if owner != Native || !support.Supported || len(support.Warnings) != 0 {
		t.Fatalf("native recurring delivery rejected: %v %+v", owner, support)
	}
	// Omitted messages are still invalid for an explicit adapter recipient.
	j.Issue = &IssuePolicy{Kind: "existing", UID: "issue"}
	owner, support = Classify(j, nil, cfg())
	if owner != Adapter || support.Supported {
		t.Fatal("adapter notify accepted an omitted message", owner, support)
	}
}
func TestClassifyUnsupportedTable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*JobDef)
		reason string
	}{
		{"trigger", func(j *JobDef) { j.Trigger.Kind = "issue-scheduled" }, "trigger"},
		{"secret", func(j *JobDef) { j.SecretRefs = map[string]string{"key": "ref"} }, "unmapped secret"},
		{"checkout", func(j *JobDef) { j.CheckoutKey = "missing" }, "checkout"},
		{"agent", func(j *JobDef) { j.Options = json.RawMessage(`{"agent":"missing"}`) }, "profile"},
		{"scheduled", func(j *JobDef) { n := int64(0); j.Issue.ScheduledOffsetSeconds = &n }, "scheduled_offset_seconds"},
		{"timezone", func(j *JobDef) {
			j.Trigger = Trigger{Kind: "cron", Cron: "CRON_TZ=America/New_York 0 * * * *", Timezone: "UTC"}
		}, "timezone"},
		{"workflow", func(j *JobDef) { j.Action.Prompt = ""; j.Action.WorkflowUID = "missing" }, "workflow"},
		{"version", func(j *JobDef) { j.Version = 2 }, "version"},
		{"interval", func(j *JobDef) { j.Trigger.IntervalSeconds = 0 }, "interval"},
		{"options", func(j *JobDef) { j.Options = json.RawMessage(`{"agent":42}`) }, "agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := fixture(t, "job")
			tc.edit(&j)
			_, s := Classify(j, nil, cfg())
			if s.Supported || !strings.Contains(strings.Join(s.Reasons, " "), tc.reason) {
				t.Fatalf("%+v", s)
			}
		})
	}
	j := fixture(t, "job")
	j.Action.Prompt = ""
	j.Action.WorkflowUID = "wf"
	w := WorkflowDef{Version: 1, Steps: []Step{{Key: "main", Kind: "prompt", Prompt: "work", Options: json.RawMessage(`{"agent":"missing"}`)}}}
	_, s := Classify(j, &w, cfg())
	if s.Supported {
		t.Fatal("unmapped step profile accepted")
	}
	w.Steps[0].Options = nil
	_, s = Classify(j, &w, cfg())
	if !s.Supported {
		t.Fatal(s)
	}
	j.Options = json.RawMessage(`{"future":true}`)
	_, s = Classify(j, &w, cfg())
	if !s.Supported || len(s.Warnings) != 1 {
		t.Fatal(s)
	}
}
func TestTopoOrderTiesCycleUnknownAfter(t *testing.T) {
	steps := []Step{{Key: "c", After: []string{"a"}}, {Key: "b"}, {Key: "a"}}
	order, e := TopoOrder(steps)
	if e != nil || !reflect.DeepEqual(order, []string{"b", "a", "c"}) {
		t.Fatal(order, e)
	}
	for _, bad := range [][]Step{{{Key: "a", After: []string{"b"}}, {Key: "b", After: []string{"a"}}}, {{Key: "a", After: []string{"missing"}}}, {{Key: "a"}, {Key: "a"}}, {{Key: "../bad"}}} {
		if _, e := TopoOrder(bad); e == nil {
			t.Fatal(bad)
		}
	}
}
func FuzzTopoOrder(t *testing.F) {
	t.Add([]byte{0, 1, 2, 3})
	t.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 40 {
			b = b[:40]
		}
		steps := make([]Step, len(b))
		for i := range steps {
			steps[i].Key = fmt.Sprint("step", i)
			if i > 0 {
				steps[i].After = []string{steps[int(b[i])%i].Key}
			}
		}
		order, e := TopoOrder(steps)
		if e != nil {
			t.Fatal(e)
		}
		pos := map[string]int{}
		for i, k := range order {
			pos[k] = i
		}
		if len(pos) != len(steps) {
			t.Fatal(order)
		}
		for _, s := range steps {
			for _, a := range s.After {
				if pos[a] >= pos[s.Key] {
					t.Fatal(order)
				}
			}
		}
	})
}
func TestDecodeRejectsNonObjects(t *testing.T) {
	for _, b := range []string{"null", "[]", "42", "true"} {
		if _, e := DecodeJob([]byte(b)); e == nil {
			t.Fatal("accepted", b)
		}
		if _, e := DecodeWorkflow([]byte(b)); e == nil {
			t.Fatal("accepted", b)
		}
	}
}
func TestClassifySupportedCronVariants(t *testing.T) {
	for _, trigger := range []Trigger{{Kind: "cron", Cron: "0 9 * * MON", Timezone: "UTC"}, {Kind: "cron", Cron: "@hourly"}, {Kind: "cron", Cron: "CRON_TZ=America/New_York 0 9 * * MON", Timezone: "America/New_York"}, {Kind: "cron", Cron: "TZ=UTC @daily", Timezone: "UTC"}} {
		j := fixture(t, "job")
		j.Trigger = trigger
		owner, support := Classify(j, nil, cfg())
		if owner != Adapter || !support.Supported || len(support.Reasons) != 0 {
			t.Fatalf("%+v: %v %+v", trigger, owner, support)
		}
	}
	j := fixture(t, "job")
	j.Trigger = Trigger{Kind: "cron", Cron: "0 0 9 * * MON"}
	_, support := Classify(j, nil, cfg())
	if support.Supported {
		t.Fatal("accepted six-field cron")
	}
}

func TestClassifyExecutePerRunParentUnsupported(t *testing.T) {
	j := fixture(t, "job")
	j.Issue = &IssuePolicy{Kind: "per-run", Title: "t", ParentUID: "01M4TADGDFRMAC38MX3NZCW1K5"}
	owner, s := Classify(j, nil, cfg())
	if owner == Adapter && s.Supported {
		t.Fatal("execute per-run with parent_uid is a supported adapter job")
	}
}

func TestTopoOrderStepKeyLength(t *testing.T) {
	for _, n := range []int{255, 256} {
		_, err := TopoOrder([]Step{{Key: strings.Repeat("a", n), Kind: "command", Command: "true"}})
		if (err == nil) != (n == 255) {
			t.Fatalf("%d-byte key: %v", n, err)
		}
	}
}

func TestClassifyReportsTopoFailureOnce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		steps  []Step
		reason string
	}{
		{name: "cycle", steps: []Step{
			{Key: "first", Kind: "command", Command: "true", After: []string{"second"}},
			{Key: "second", Kind: "command", Command: "true", After: []string{"first"}},
		}, reason: "workflow cycle"},
		{name: "duplicate key", steps: []Step{
			{Key: "first", Kind: "command", Command: "true"},
			{Key: "first", Kind: "command", Command: "true"},
		}, reason: `invalid or duplicate step key "first"`},
		{name: "unknown after", steps: []Step{
			{Key: "first", Kind: "command", Command: "true", After: []string{"missing"}},
		}, reason: `unknown after key "missing"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := fixture(t, "job")
			j.Action.Prompt = ""
			j.Action.WorkflowUID = "workflow"
			w := WorkflowDef{Version: 1, Steps: tc.steps}
			_, support := Classify(j, &w, cfg())
			if support.Supported || !reflect.DeepEqual(support.Reasons, []string{tc.reason}) {
				t.Fatalf("want unsupported with one reason %q, got %+v", tc.reason, support)
			}
		})
	}
}

// Adapter dispatch requires support and a live definition, regardless of cache use.
func TestDispatchable(t *testing.T) {
	deleted := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		owner   Owner
		support Support
		deleted *time.Time
		want    bool
	}{
		{"supported adapter", Adapter, Support{Supported: true}, nil, true},
		{"unsupported adapter", Adapter, Support{}, nil, false},
		{"ignored", Ignored, Support{Supported: true}, nil, false},
		{"native", Native, Support{Supported: true}, nil, false},
		{"deleted adapter", Adapter, Support{Supported: true}, &deleted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Dispatchable(tc.owner, tc.support, tc.deleted); got != tc.want {
				t.Fatalf("dispatchable = %v, want %v", got, tc.want)
			}
		})
	}
}
