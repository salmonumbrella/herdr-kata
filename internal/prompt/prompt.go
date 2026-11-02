// Package prompt builds one-shot prompt files and validates attempt results.
package prompt

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
)

var placeholders = regexp.MustCompile(`\{\{[^{}]*\}\}`)
var ErrInvalidPrompt = errors.New("invalid_prompt")
var shellWord = regexp.MustCompile(`^[A-Za-z0-9_./:@=-]+$`)

func Quote(s string) string {
	if shellWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
func substitute(s, input, previous string) (string, error) {
	for _, p := range placeholders.FindAllString(s, -1) {
		if p != "{{input}}" && p != "{{previous}}" {
			return "", fmt.Errorf("%w: unknown placeholder %s", ErrInvalidPrompt, p)
		}
	}
	return strings.NewReplacer("{{input}}", input, "{{previous}}", previous).Replace(s), nil
}
func Build(r journal.Run, s defs.Step, n int, issueRef, issueTitle, previous, resultPath, reportBin, kataPrefix string) (string, error) {
	task, err := substitute(s.Text, r.Plan.Input, previous)
	if err != nil {
		return "", err
	}
	about, err := substitute(r.Plan.About, r.Plan.Input, previous)
	if err != nil {
		return "", err
	}
	body := fmt.Sprintf("Issue: %s — %s\nJob: %s\nRun: %s\nOccurrence: %s\nStep: %s\nAttempt: %d\nWorkflow: %s\nAbout: %s\nInput: %s\n\nTask:\n%s\n\nWrite your result with:\n%s report --result %s --status <status> --summary <text> [--note <one-line note>]\nStatus is succeeded, failed or needs-human. A note is one line, at most 280 bytes.\n\nClose the issue yourself with `%s close %s --done --message <completed work> --evidence <evidence>` when finished; if not finished, do not close it.\n", issueRef, issueTitle, r.Plan.Name, r.RunUID, r.Occurrence, s.Key, n, r.Plan.WorkflowName, about, r.Plan.Input, task, Quote(reportBin), Quote(resultPath), kataPrefix, Quote(issueRef))
	if len(body) > 256*1024 {
		return "", fmt.Errorf("%w: exceeds 256 KiB", ErrInvalidPrompt)
	}
	return body, nil
}
func Line(path string) string { return "Read " + path + " and do exactly what it says." }
