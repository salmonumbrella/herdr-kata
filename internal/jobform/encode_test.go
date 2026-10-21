package jobform

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"go.kenn.io/kit/secretref"
)

func TestEncodeFieldRoundTrip(t *testing.T) {
	c := Context{Config: config.Config{Secrets: map[string]secretref.Ref{"example": {Env: "EXAMPLE_SECRET"}}}}
	for _, tc := range []struct {
		name, key, trigger, input, path, edit string
		want                                  any
	}{
		{"tags", "tags", "manual", "daily, daily, nightly", "options.tags", "daily, nightly", []string{"daily", "nightly"}},
		{"secrets", "secret_refs", "manual", "TOKEN=example", "secret_refs", "TOKEN=example", map[string]string{"TOKEN": "example"}},
		{"timeout", "timeout", "manual", "3600", "timeout_seconds", "1h0m0s", int64(3600)},
		{"grace", "grace", "manual", "60", "grace_seconds", "1m0s", int64(60)},
		{"deadline", "deadline", "manual", "-60", "issue.deadline_offset_seconds", "-1m0s", int64(-60)},
		{"interval", "schedule", "interval", "3600", "trigger.interval_seconds", "1h0m0s", int64(3600)},
		{"cron", "schedule", "cron", "0 * * * *", "trigger.cron", "0 * * * *", "0 * * * *"},
		{"once unchanged", "schedule", "once", "2030-01-01T12:00:00-07:00", "trigger.at", "2030-01-01T12:00:00-07:00", "2030-01-01T12:00:00-07:00"},
		{"once edited", "schedule", "once", "2030-01-02T12:00:00-07:00", "trigger.at", "2030-01-02T19:00:00Z", "2030-01-02T19:00:00Z"},
		{"enabled", "enabled", "manual", "true", "enabled", "true", true},
		{"disabled", "enabled", "manual", "false", "enabled", "false", false},
		{"manual", "trigger", "manual", "manual", "trigger.kind", "manual", "manual"},
		{"blank text", "description", "manual", "", "options.description", "", nil},
		{"blank tags", "tags", "manual", "", "options.tags", "", nil},
		{"blank secrets", "secret_refs", "manual", "", "secret_refs", "", nil},
		{"blank timeout", "timeout", "manual", "", "timeout_seconds", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]any{"trigger": map[string]any{"kind": tc.trigger, "at": "2030-01-01T12:00:00-07:00", "timezone": "UTC"}}
			d := NewDraft(raw, "Example")
			if err := Set(Field{Key: tc.key}, &d, c, tc.input); err != nil {
				t.Fatal(err)
			}
			path, v, err := Encode(tc.key, d, c)
			if err != nil || path != tc.path || !reflect.DeepEqual(v, tc.want) {
				t.Fatalf("encode=(%q,%#v,%v) want=(%q,%#v)", path, v, err, tc.path, tc.want)
			}
			// Hydrate the encoded value through JSON, as reopening a saved form does.
			stored := map[string]any{"trigger": map[string]any{"kind": tc.trigger}}
			at := stored
			keys := strings.Split(tc.path, ".")
			for _, key := range keys[:len(keys)-1] {
				next, ok := at[key].(map[string]any)
				if !ok {
					next = map[string]any{}
					at[key] = next
				}
				at = next
			}
			if v != nil {
				at[keys[len(keys)-1]] = v
			}
			data, err := json.Marshal(stored)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			decoder := json.NewDecoder(strings.NewReader(string(data)))
			decoder.UseNumber()
			if err := decoder.Decode(&decoded); err != nil {
				t.Fatal(err)
			}
			if got := NewDraft(decoded, "Example").Values[tc.key]; got != tc.edit {
				t.Fatalf("reopened %s=%q want=%q", tc.key, got, tc.edit)
			}
		})
	}
}

func TestEncodeRejectsInvalidFields(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"tags", "INVALID TAG"}, {"secret_refs", "TOKEN=unmapped"}, {"timeout", "1.5s"}, {"deadline", "invalid"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			_, _, err := Encode(tc.key, Draft{Values: map[string]string{tc.key: tc.value}}, Context{})
			if err == nil {
				t.Fatalf("encoded invalid %s %q", tc.key, tc.value)
			}
		})
	}
}
