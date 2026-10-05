package board

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/salmonumbrella/herdr-kata/internal/store"
)

// seedLongLeases fills the conversation with more messages than any pane here
// can show, which is the only state in which scrolling — and therefore the
// chrome that has to survive it — means anything.
func seedLongLeases(t *testing.T, m *Model, n int) {
	t.Helper()
	m.leases = nil
	for i := 0; i < n; i++ {
		m.leases = append(m.leases, store.Lease{Scope: "local:example", Resource: "resource " + itoa(i), Holder: store.Identity{Name: "worker"}, Since: time.Now(), Why: "inspection"})
	}
}

// scrollToTop drives the board up through a long thread the way a reader does.
func scrollToTop(t *testing.T, m *Model) {
	t.Helper()
	for i := 0; i < 200 && m.scroll > 0; i++ {
		m.pressSpecial(t, tea.KeyPgUp)
		m.View()
	}
}

// The tab bar is the one line saying which of the three lists is on screen.
// Windowing the whole view used to carry it off the top the moment the reader
// scrolled back through a conversation — losing where they were at exactly the
// moment they were furthest from it.
func TestTheTabBarSurvivesScrollingLeases(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 100, 20
	m.press(t, "4")
	seedLongLeases(t, m, 60)
	m.View()
	scrollToTop(t, m)

	out := m.View()
	if !strings.Contains(out, "LEASES") {
		t.Errorf("the tab bar scrolled away with the thread:\n%s", out)
	}
	if !strings.Contains(out, "Herdr Kata") {
		t.Error("the brand line, which carries the scheduler's health dot, scrolled away")
	}
	// dead-agent posted the oldest message in the seeded thread, so its being on
	// screen is what says the reader really did reach the top.
	if !strings.Contains(out, "resource 0") {
		t.Errorf("scrolling to the top did not reach the oldest message:\n%s", out)
	}
}

// The help line is pinned to the bottom for the same reason: a reader lost in
// history is the one most likely to need the key that gets them back to live.
func TestTheHelpLineIsStillThereWhileScrolledUp(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 100, 20
	m.press(t, "4")
	seedLongLeases(t, m, 60)
	m.View()
	scrollToTop(t, m)

	out := m.View()
	if !strings.Contains(out, "q quit") {
		t.Errorf("the help line scrolled away:\n%s", out)
	}
	if !strings.Contains(out, "LEASES") {
		t.Error("the holds block scrolled away, so who has the browser is now a scroll away")
	}
}

// A view one row taller than the pane makes the terminal scroll, which smears
// the whole board upwards and leaves half-drawn frames behind it. The chrome
// now takes rows of its own, so the arithmetic has to hold for every shape of
// pane and every length of thread.
func TestTheViewNeverRendersMoreRowsThanThePaneHas(t *testing.T) {
	for _, msgs := range []int{0, 1, 40} {
		m := newTestModel(t)
		m.width = 100
		if msgs > 0 {
			seedLongLeases(t, m, msgs)
		}
		for _, focus := range []focus{focusJobs, focusRuns, focusLeases, focusFlows} {
			m.focus = focus
			for height := 4; height <= 40; height++ {
				m.height = height
				for _, scrolled := range []bool{false, true} {
					if scrolled {
						m.scrollUp(3)
					}
					out := m.View()
					if rows := strings.Count(out, "\n") + 1; rows > height {
						t.Fatalf("focus %d, %d messages, pane %d rows, scrolled %v: "+
							"the view renders %d rows", focus, msgs, height, scrolled, rows)
					}
				}
			}
		}
	}
}

// The status line, the search line and the compose box are all chrome too, and
// every row they take has to come out of the body rather than off the bottom of
// the pane.
func TestChromeThatAppearsStillFitsThePane(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 100, 18
	m.press(t, "4")
	seedLongLeases(t, m, 40)

	m.status = "reading global"
	m.press(t, "/")
	m.typeText(t, "resource")
	if rows := strings.Count(m.View(), "\n") + 1; rows > m.height {
		t.Errorf("with a status and an open search the view renders %d rows of %d",
			rows, m.height)
	}
	m.pressSpecial(t, tea.KeyEnter) // keeping the filter

}

// The scroll hint belongs to the body it describes. Left to the pane it would
// have counted the chrome's rows as hidden ones.
func TestTheScrollHintCountsTheBodyAndNotTheChrome(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 100, 20
	m.press(t, "4")
	seedLongLeases(t, m, 60)
	m.View()
	m.pressSpecial(t, tea.KeyPgUp)

	out := m.View()
	if !strings.Contains(out, "more") {
		t.Fatalf("a scrolled thread shows no hint:\n%s", out)
	}
	hint := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "more") {
			hint = line
		}
	}
	// The hint sits under the conversation, not under the help line: it is the
	// body's own footer.
	if strings.HasSuffix(strings.TrimRight(out, "\n"), hint) {
		t.Error("the scroll hint is the last row of the pane, below the pinned help")
	}
}
