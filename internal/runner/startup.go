package runner

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
)

// Codex's hook review owns the input on several screens: the startup modal,
// its "Trusting hooks..." state, and the hooks browser that "Review hooks"
// opens. Match whole lines of that UI, not conversation text mentioning it.
var hookReviewScreen = regexp.MustCompile(`(?m)^\s*(?:[^\pL\pN\s]+\s*)?(?:` + strings.Join([]string{
	`[0-9]+ (?:hook needs|hooks need) review(?: before (?:it|they) can run\.)?`,
	`Hooks need review`,
	`[0-9]+ (?:hook is|hooks are) new or changed\.`,
	`Hooks can run outside the sandbox after you trust them\.`,
	`(?:[0-9]+\. )?(?:Review hooks|Trust all and continue|Continue without trusting \(hooks won't run\))`,
	`Trusting hooks\.\.\.`,
	`Lifecycle hooks from config and enabled plugins\.`,
	`t trust all · enter review · esc close`,
}, "|") + `)\s*$` +
	// A hook's row in the browser starts with its event, such as
	// SessionStart, and ends with its review status.
	`|(?m)^\s*(?:[^\pL\pN\s]+\s*)?[A-Z][a-z]+(?:[A-Z][a-z]+)+\s.*\s(?:New hook|Modified since last trusted) - review required\s*$`)

// Codex can advertise interactive readiness before its hook review has released
// input. Read only the visible screen: scrollback can retain a cleared review.
func (m *Manager) hookReview(ctx context.Context, r *Run, index int, a herdr.Agent) (bool, error) {
	if _, p := StepProfile(r, index); p.Kind != "codex" || !readyAgent(a) {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	screen, err := m.h.AgentRead(ctx, a.Name, "visible", nil)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ref := AgentReference(r)
	if screen.PaneID != ref.PaneID || screen.TabID != ref.TabID || screen.WorkspaceID != ref.WorkspaceID || screen.Source != "visible" || screen.Format != "text" || screen.Truncated {
		return false, errUnproven
	}
	return hookReviewScreen.MatchString(screen.Text), nil
}
