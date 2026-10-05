package board

import (
	"fmt"
	"strings"
	"time"
)

// leasesPane is a compact operational view of execution coordinator leases.
// Scope and run stay visible so a holder can be found without guessing which
// local/shared coordinator owns a resource.
func (m *Model) leasesPane(p pane) pane {
	var b strings.Builder
	b.WriteString(headerStyle.Render("  SCOPE · RESOURCE · HOLDER · RUN · EXPIRY") + "\n")
	count := 0
	for _, l := range m.leases {
		line := fmt.Sprintf("%s · %s · %s · %s", l.Scope, l.Resource, l.Holder.Short(), l.Holder.RunID)
		if m.query != "" && !strings.Contains(strings.ToLower(line+" "+l.Why), strings.ToLower(m.query)) {
			continue
		}
		expiry := "no expiry"
		if l.ExpiresAt != nil {
			expiry = l.ExpiresAt.Format(time.RFC3339)
		}
		for _, wrapped := range wrapText(line+" · "+expiry, max(m.contentWidth()-2, 1)) {
			b.WriteString("  " + wrapped + "\n")
		}
		if l.Why != "" {
			for _, wrapped := range wrapText(l.Why, max(m.contentWidth()-4, 1)) {
				b.WriteString(dimStyle.Render("    "+wrapped) + "\n")
			}
		}
		count++
	}
	if count == 0 {
		b.WriteString(dimStyle.Render("  no live leases"))
	}
	p.body = strings.TrimSuffix(b.String(), "\n")
	p.bottom = m.renderFooter() + "\n" + helpStyle.Render("tab lists · / search · j/k scroll · r reload · M mouse · q quit")
	return p
}
