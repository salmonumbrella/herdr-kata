package runner

import (
	"strings"
	"testing"
)

func TestValidateCloseNormalizedFloors(t *testing.T) {
	for _, tc := range []struct {
		reason   string
		floor    int
		evidence []string
	}{
		{"done", 40, []string{"test:go test ./..."}},
		{"audit-no-change", 40, []string{"no-change-audit:reviewed all callers"}},
		{"wontfix", 60, nil},
		{"duplicate", 20, []string{"duplicate-of:proj#1234"}},
		{"superseded", 20, []string{"superseded-by:proj#1234"}},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			if err := ValidateClose(tc.reason, " \n"+strings.Repeat("x", tc.floor)+"?! ", tc.evidence); err != nil {
				t.Fatal(err)
			}
			if err := ValidateClose(tc.reason, strings.Repeat("x", tc.floor-1), tc.evidence); err == nil {
				t.Fatal("accepted short message")
			}
			if err := ValidateClose(tc.reason, strings.Repeat("x ", tc.floor/2), tc.evidence); err == nil {
				t.Fatal("counted padding")
			}
		})
	}
	// Kata uses byte length, even though its diagnostics call these characters.
	if err := ValidateClose("done", strings.Repeat("é", 20), []string{"test:check"}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateCloseTrivialList(t *testing.T) {
	for _, s := range []string{"done", "fixed", "complete", "completed", "ok", "okay", "yes", "no", "n/a", "na", "skip", "nope"} {
		if err := ValidateClose("done", " \n"+strings.ToUpper(s)+"!!! ", []string{"test:check"}); err == nil || !strings.Contains(err.Error(), "trivial") {
			t.Fatalf("%q: %v", s, err)
		}
	}
}

func TestValidateCloseEvidenceShapes(t *testing.T) {
	m := strings.Repeat("x", 60)
	for _, tc := range []struct {
		e      string
		reason string
		valid  bool
	}{
		{"commit:abcdef0", "done", true}, {"commit:ABCDEF0123456789", "done", true},
		{"commit: " + strings.Repeat("a", 40) + " ", "done", true},
		{"commit:abcdef", "done", false}, {"commit:" + strings.Repeat("a", 41), "done", false},
		{"commit:fixed", "done", false}, {"commit:abcdeg0", "done", false},
		{"pr:any nonblank string", "done", true}, {"pr: \t", "done", false},
		{"test:echo https://example.com", "done", true}, {"test: ", "done", false},
		{"reviewed-paths:src/file.go", "done", true}, {"reviewed-paths: ", "done", false},
		{"external:account", "done", true}, {"external:", "done", false},
		{"no-change-audit:rationale", "audit-no-change", true}, {"no-change-audit: ", "audit-no-change", false},
		{"duplicate-of:ref", "duplicate", true}, {"duplicate-of: ", "duplicate", false},
		{"superseded-by:ref", "superseded", true}, {"superseded-by:", "superseded", false},
		{"unknown:value", "done", false}, {"test", "done", false},
	} {
		t.Run(tc.e, func(t *testing.T) {
			err := ValidateClose(tc.reason, m, []string{tc.e})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}

func TestValidateCloseReasonMatrix(t *testing.T) {
	m := strings.Repeat("x", 60)
	for _, tc := range []struct {
		name, reason string
		e            []string
		valid        bool
	}{
		{"done-empty", "done", nil, false},
		{"done-all", "done", []string{"commit:abcdef0", "pr:url", "test:check", "reviewed-paths:a", "reviewed-paths:b", "external:account"}, true},
		{"done-audit", "done", []string{"test:check", "no-change-audit:checked"}, false},
		{"audit-empty", "audit-no-change", nil, false},
		{"audit-paths", "audit-no-change", []string{"no-change-audit:checked", "reviewed-paths:a", "reviewed-paths:b"}, true},
		{"audit-two", "audit-no-change", []string{"no-change-audit:one", "no-change-audit:two"}, false},
		{"audit-test", "audit-no-change", []string{"no-change-audit:checked", "test:check"}, false},
		{"wontfix", "wontfix", nil, true}, {"wontfix-evidence", "wontfix", []string{"test:check"}, false},
		{"duplicate-two", "duplicate", []string{"duplicate-of:a", "duplicate-of:b"}, false},
		{"duplicate-extra", "duplicate", []string{"duplicate-of:a", "test:check"}, false},
		{"superseded-wrong", "superseded", []string{"duplicate-of:a"}, false},
		{"unknown-reason", "bogus", []string{"test:check"}, false},
		{"duplicate-path", "done", []string{"reviewed-paths:a", "reviewed-paths:a"}, false},
		{"duplicate-test", "done", []string{"test:check", "test:check"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateClose(tc.reason, m, tc.e)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}
