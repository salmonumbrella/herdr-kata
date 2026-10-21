package tui

import (
	"fmt"
	"reflect"
	"testing"

	"charm.land/lipgloss/v2"
)

func role(name string) lipgloss.Style {
	switch name {
	case "DetailMeta":
		return detailMetaStyle
	case "Pass":
		return passStyle
	case "Fail":
		return failStyle
	case "Attention":
		return attentionStyle
	case "Neutral":
		return neutralStyle
	case "Open":
		return openStyle
	case "Closed":
		return closedStyle
	case "Error":
		return errorStyle
	default:
		return subtleStyle
	}
}
func TestRunLabel(t *testing.T) {
	preserveTheme(t)
	applyColorMode(colorDark, true)
	for _, c := range runLabelFixture() {
		before := fmt.Sprintf("%#v", c.Run)
		got, s := runLabel(c.Run)
		if got != c.Label || !reflect.DeepEqual(s, role(c.LabelRole)) {
			t.Errorf("%s label %q role %v", c.Name, got, s)
		}
		if fmt.Sprintf("%#v", c.Run) != before {
			t.Fatal("journal changed")
		}
	}
}
func TestReportState(t *testing.T) {
	preserveTheme(t)
	applyColorMode(colorDark, true)
	for _, c := range runLabelFixture() {
		got, s := reportState(c.Run)
		if got != c.Report || !reflect.DeepEqual(s, role(c.ReportRole)) {
			t.Errorf("%s report %q role %v", c.Name, got, s)
		}
	}
}
