package defs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJobMetadata(t *testing.T) {
	tags, description, warnings := JobMetadata(json.RawMessage(`{"tags":["daily","release:v0.2-rc_1"],"description":"example job"}`))
	if len(tags) != 2 || tags[1] != "release:v0.2-rc_1" || description != "example job" || len(warnings) != 0 {
		t.Fatalf("%v %q %v", tags, description, warnings)
	}
	for _, raw := range []string{`{"tags":null}`, `{"tags":"daily"}`, `{"tags":["ok",2]}`, `{"tags":[""]}`, `{"tags":["a,b"]}`, `{"tags":["a\nb"]}`} {
		tags, _, warnings := JobMetadata(json.RawMessage(raw))
		if len(tags) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "tags") {
			t.Fatalf("%s: %v %v", raw, tags, warnings)
		}
	}
	for _, raw := range []string{`{"description":null}`, `{"description":2}`, `{"description":"` + strings.Repeat("例", 201) + `"}`} {
		_, description, warnings := JobMetadata(json.RawMessage(raw))
		if description != "" || len(warnings) != 1 || !strings.Contains(warnings[0], "description") {
			t.Fatalf("%s: %q %v", raw, description, warnings)
		}
	}
	for _, n := range []int{16, 17} {
		tags := make([]string, n)
		for i := range tags {
			tags[i] = "example"
		}
		if (ValidateJobTags(tags) == nil) != (n == 16) {
			t.Fatalf("tag count %d", n)
		}
	}
	for _, n := range []int{64, 65} {
		if (ValidateJobTags([]string{strings.Repeat("a", n)}) == nil) != (n == 64) {
			t.Fatalf("tag length %d", n)
		}
	}
	for _, n := range []int{200, 201} {
		if (ValidateJobDescription(strings.Repeat("例", n)) == nil) != (n == 200) {
			t.Fatalf("description length %d", n)
		}
	}
	for _, scope := range []string{"job", "workflow", "prompt", "command"} {
		var s Support
		options(json.RawMessage(`{"tags":["daily"],"description":"example"}`), &s, scope)
		if len(s.Reasons) != 0 || (len(s.Warnings) == 0) != (scope == "job") {
			t.Fatalf("scope %s: %+v", scope, s)
		}
	}
	var s Support
	options(json.RawMessage(`{"tags":2,"description":false}`), &s, "job")
	if len(s.Reasons) != 0 || len(s.Warnings) != 2 {
		t.Fatalf("malformed metadata: %+v", s)
	}
}

func TestJobMetadataClassification(t *testing.T) {
	j := fixture(t, "job")
	j.Options = json.RawMessage(`{"tags":["daily"],"description":"example"}`)
	if _, s := Classify(j, nil, cfg()); !s.Supported || len(s.Warnings) != 0 {
		t.Fatalf("metadata: %+v", s)
	}
	j.Options = json.RawMessage(`{"tags":3,"description":false}`)
	if _, s := Classify(j, nil, cfg()); !s.Supported || len(s.Warnings) != 2 {
		t.Fatalf("foreign metadata: %+v", s)
	}
}

// Kata f3a06c1 labels accept only [a-z0-9._:-], length 1..64.
func TestJobTagsUseKataLabelRules(t *testing.T) {
	for _, tag := range []string{"release:v0.2-rc_1", strings.Repeat("a", 64)} {
		if err := ValidateJobTags([]string{tag}); err != nil {
			t.Errorf("valid label %q: %v", tag, err)
		}
	}
	for _, tag := range []string{"", "Daily", "例", "two words", "a/b", "a,b", "a\nb", strings.Repeat("a", 65)} {
		if err := ValidateJobTags([]string{tag}); err == nil {
			t.Errorf("accepted invalid label %q", tag)
		}
	}
}
