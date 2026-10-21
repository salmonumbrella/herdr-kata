//go:build kata

package runner

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

func TestIntegCloseValidationMirror(t *testing.T) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	home := t.TempDir()
	c := kata.New(config.Kata{Bin: bin, Project: "close-test", Actor: "worker", Env: map[string]string{"KATA_HOME": home}}, home, nil, nil)
	startKataDaemon(t, bin, home, c.Env(), "close-test")
	message := "Updated the parser and verified the focused tests pass."
	for n, tc := range []struct {
		reason, message string
		evidence        []string
	}{
		{"done", message, []string{"commit:ABCDEF0"}},
		{"done", message, []string{"commit:fixed"}},
		{"done", message, []string{"pr:any nonblank string"}},
		{"done", message, []string{"test: "}},
		{"done", " done!!! ", []string{"test:check"}},
		{"done", strings.Repeat("x", 39), []string{"test:check"}},
		{"done", message, nil},
		{"done", message, []string{"reviewed-paths:a", "reviewed-paths:b"}},
		{"done", message, []string{"test:check", "test:check"}},
		{"audit-no-change", message, []string{"no-change-audit:reviewed", "reviewed-paths:a"}},
		{"audit-no-change", message, []string{"no-change-audit:reviewed", "test:check"}},
		{"audit-no-change", message, []string{"no-change-audit:a", "no-change-audit:b"}},
		{"wontfix", strings.Repeat("x", 60), nil},
		{"wontfix", strings.Repeat("x", 59), nil},
	} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			i, err := c.CreateIssue(t.Context(), kata.CreateReq{Title: fmt.Sprintf("Validation case %d", n), ForceNew: true})
			if err != nil {
				t.Fatal(err)
			}
			local := ValidateClose(tc.reason, tc.message, tc.evidence)
			remote := c.Close(t.Context(), kata.CloseReq{Ref: i.UID, Reason: tc.reason, Message: tc.message, Evidence: tc.evidence, IfMatch: strconv.FormatInt(i.Revision, 10), IdempotencyKey: fmt.Sprintf("close-case-%d", n)})
			if (local == nil) != (remote == nil) {
				t.Fatalf("mirror=%v, Kata=%v", local, remote)
			}
		})
	}
}
