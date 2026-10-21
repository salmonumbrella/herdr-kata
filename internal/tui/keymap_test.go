package tui

import (
	"reflect"
	"strings"
	"testing"
)

func TestEveryBindingAppearsInFooterHints(t *testing.T) {
	m, _ := fixture(t)
	hints := []footerHelp{jobsHelp(), runsHelp(), workflowHelp(), m.jobDetailHelp(), m.runDetailHelp(), workflowDetailHelp(), formEditingHelp(), kataHelp()}
	shown := map[string]bool{}
	for _, h := range hints {
		for _, item := range h.Full {
			for _, binding := range item.bindings {
				shown[binding] = true
			}
		}
	}
	value := reflect.ValueOf(newKeymap())
	for i := 0; i < value.NumField(); i++ {
		binding := value.Field(i).Interface().(key)
		for _, name := range binding.Keys {
			if !shown[name] {
				t.Errorf("binding %s (%s) missing from footer hints", value.Type().Field(i).Name, name)
			}
		}
	}
}

func TestFooterReflowsWithoutDroppingActions(t *testing.T) {
	preserveTheme(t)
	applyColorMode(colorNone, false)
	lines := jobsHelp().Lines(80)
	if len(lines) < 2 {
		t.Fatal("expected narrow help to reflow", lines)
	}
	text := strings.Join(lines, "\n")
	for _, want := range []string{"run", "delete", "attach", "parent", "mouse", "quit"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing action %q: %s", want, text)
		}
	}
}
