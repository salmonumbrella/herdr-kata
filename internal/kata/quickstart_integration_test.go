//go:build kata

package kata

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Execute the guide's issue-inspection command, rather than asserting doc text.
func TestIntegQuickstartCloseInspection(t *testing.T) {
	c := newKata(t)
	rawKata(t, c, "projects", "create", "example-project")
	c.cfg.Project = "example-project"
	issue := fixtureIssue(t, c)
	requireNoError(t, c.Close(t.Context(), CloseReq{Ref: issue.UID, Reason: "done", Message: "Reviewed README.md and verified the documented project heading.", Evidence: []string{"reviewed-paths:README.md"}, IdempotencyKey: "close"}))
	doc, err := os.ReadFile("../../docs/get-started/quickstart.md")
	requireNoError(t, err)
	_, section, ok := strings.Cut(string(doc), "## Inspect the result")
	if !ok {
		t.Fatal("missing quickstart result example")
	}
	_, section, ok = strings.Cut(section, "```sh\n")
	if !ok {
		t.Fatal("missing quickstart shell example")
	}
	section, _, _ = strings.Cut(section, "\n```")
	lines := strings.Split(section, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "kata ") {
			start = i
		}
	}
	if start < 0 {
		t.Fatal("no issue-inspection command to execute")
	}
	end := start + 1
	for end < len(lines) && (strings.HasSuffix(strings.TrimSpace(lines[end-1]), "|") || strings.HasSuffix(strings.TrimSpace(lines[end-1]), "\\")) {
		end++
	}
	binDir := t.TempDir()
	requireNoError(t, os.Symlink(c.cfg.Bin, filepath.Join(binDir, "kata")))
	cmd := exec.CommandContext(t.Context(), "sh", "-c", strings.Join(lines[start:end], "\n"))
	cmd.Dir = c.stateDir
	cmd.Env = append(c.Env(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "issue_uid="+issue.UID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("guide command: %v\n%s", err, out)
	}
	var closeEvent struct {
		Reason   string
		Evidence []struct {
			Type  string
			Paths []string
		}
	}
	if err := json.Unmarshal(out, &closeEvent); err != nil || closeEvent.Reason != "done" || len(closeEvent.Evidence) != 1 || closeEvent.Evidence[0].Type != "reviewed-paths" || len(closeEvent.Evidence[0].Paths) != 1 || closeEvent.Evidence[0].Paths[0] != "README.md" {
		t.Fatalf("guide did not expose close reason and evidence: %v\n%s", err, out)
	}
}
