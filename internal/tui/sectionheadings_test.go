package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Kata's issue document uses capitalized, bold section names without a color.
func TestDetailAndInspectorSectionHeadingsMatchKata(t *testing.T) {
	preserveTheme(t)
	for _, mode := range []colorMode{colorDark, colorLight, colorNone} {
		applyColorMode(mode, false)
		d := newDetail(80, 14, detailSelection{}, noSelection, nil)
		for _, label := range []string{"RESULT", "used by 2", "PREVIOUS ATTEMPTS"} {
			d.Section(label)
		}
		body := bodyTextForTest(d.Body())
		for _, label := range []string{"Result", "Used by 2", "Previous attempts"} {
			if !strings.Contains(body, "\x1b[1m"+label+"\x1b[m") {
				t.Fatalf("mode %d detail heading %q: %q", mode, label, body)
			}
		}
		m := workflowModel(t, workflowSnapshot())
		inspector := bodyTextForTest(m.workflowInspector(m.selectedWorkflow(), 64))
		for _, label := range []string{"Steps", "Last run"} {
			if !strings.Contains(inspector, "\x1b[1m"+label) {
				t.Fatalf("mode %d inspector heading %q: %q", mode, label, inspector)
			}
		}
		header := ansi.Strip(m.renderWorkflows()[0].Text)
		for _, label := range []string{"status", "workflow", "steps", "input", "updated"} {
			if !strings.Contains(header, label) {
				t.Fatalf("column heading %q: %q", label, header)
			}
		}
	}
}
