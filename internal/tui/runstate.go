package tui

import (
	"charm.land/lipgloss/v2"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/runstatus"
)

// runLabel names a run with the shared status table and colors it by role.
func runLabel(r runner.Run) (string, lipgloss.Style) {
	s := runstatus.Run(r)
	return s.Word, statusRole(s.Role)
}

// statusRole maps a status role onto Kata's list colors: open green for work
// that is moving, closed gray for work that needs nothing more, the error red
// for failures, and the board's attention pink for work that needs you.
func statusRole(role runstatus.Role) lipgloss.Style {
	switch role {
	case runstatus.Active:
		return openStyle
	case runstatus.Finished:
		return closedStyle
	case runstatus.Failure:
		return errorStyle
	case runstatus.Idle:
		return subtleStyle
	}
	return attentionStyle
}

func reportState(r runner.Run) (string, lipgloss.Style) {
	for _, op := range r.Ops {
		if op.SigningRefused {
			return "signing refused", failStyle
		}
	}
	if r.Inspect != "" {
		return "inspect: " + plain(r.Inspect), attentionStyle
	}
	for _, op := range r.Ops {
		if !op.Done {
			if op.Blocked() {
				return "inspect: " + plain(op.Inspect), attentionStyle
			}
			resumed := r.Resume != nil || len(r.ConsumedResumes) > 0
			if resumed && (r.Phase == runner.Settled || r.Phase == runner.Held || r.Phase == runner.HandedOff) {
				return "", subtleStyle
			}
			return "reporting…", subtleStyle
		}
	}
	return "", subtleStyle
}
