package defs

import (
	"strings"
	"testing"
)

func TestDecodeRejectsInvalidUTF8(t *testing.T) {
	for _, invalid := range []string{"\xff", "\xc3", "\x80", "\xc0\x80", "\xed\xa0\x80"} {
		for _, field := range []string{"known", "unknown"} {
			jobRaw := []byte(`{"action":{"prompt":"text ` + invalid + `"}}`)
			workflowRaw := []byte(`{"steps":[{"key":"check","kind":"command","command":"true` + invalid + `"}]}`)
			if field == "unknown" {
				jobRaw = []byte(`{"unknown":"text ` + invalid + `"}`)
				workflowRaw = jobRaw
			}
			t.Run(field, func(t *testing.T) {
				if _, err := DecodeJob(jobRaw); err == nil || err.Error() != "invalid UTF-8 in definition" {
					t.Errorf("job accepted or normalized invalid bytes: %v", err)
				}
				if _, err := DecodeWorkflow(workflowRaw); err == nil || err.Error() != "invalid UTF-8 in definition" {
					t.Errorf("workflow accepted or normalized invalid bytes: %v", err)
				}
			})
		}
	}
}

func TestDecodePreservesValidUnicode(t *testing.T) {
	job, err := DecodeJob([]byte(`{"action":{"kind":"execute","prompt":"Review café, 你好 and �"}}`))
	if err != nil || job.Action.Prompt != "Review café, 你好 and �" {
		t.Fatal("valid job Unicode changed", job.Action.Prompt, err)
	}
	wf, err := DecodeWorkflow([]byte(`{"version":1,"steps":[{"key":"check","kind":"command","command":"printf 'café 你好 �'"}]}`))
	if err != nil || len(wf.Steps) != 1 || wf.Steps[0].Command != "printf 'café 你好 �'" {
		t.Fatal("valid command Unicode changed", wf, err)
	}
}

func TestCommandTextRejectsInvalidBytes(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		supported     bool
	}{
		{"nul suffix", "true\x00", false},
		{"nul prefix", "\x00true", false},
		{"nul within quotes", "printf 'a\x00b'", false},
		{"invalid utf8", "true\xff", false},
		{"plain", "true", true},
		{"unicode and whitespace", "printf 'café 你好 �'\n\ttrue", true},
		{"shell syntax", "GREETING=value; printf '%s\\n' \"$GREETING\"", true},
		{"escaped nul output", "printf '\\000'", true},
		{"other control byte", "printf 'a\x01b'", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := fixture(t, "job")
			job.Action.Prompt, job.Action.WorkflowUID = "", "workflow"
			wf := WorkflowDef{Version: 1, Steps: []Step{{Key: "check", Kind: "command", Command: tc.command}}}
			_, support := Classify(job, &wf, cfg())
			if support.Supported != tc.supported {
				t.Fatalf("command support: %+v", support)
			}
			if !tc.supported && !strings.Contains(strings.Join(support.Reasons, " "), "command must be UTF-8 without NUL") {
				t.Fatalf("missing invalid-command reason: %+v", support)
			}
		})
	}
}

func TestEscapedNULCommandIsUnsupported(t *testing.T) {
	wf, err := DecodeWorkflow([]byte(`{"version":1,"steps":[{"key":"check","kind":"command","command":"true\u0000"}]}`))
	if err != nil {
		t.Fatal("valid JSON failed to decode", err)
	}
	job := fixture(t, "job")
	job.Action.Prompt, job.Action.WorkflowUID = "", "workflow"
	_, support := Classify(job, &wf, cfg())
	if support.Supported || !strings.Contains(strings.Join(support.Reasons, " "), "command must be UTF-8 without NUL") {
		t.Fatalf("escaped NUL authorized execution: %+v", support)
	}
}
