package board

import "strings"

// defaultHeight is used before the first WindowSizeMsg arrives.
const defaultHeight = 24

// window trims a whole view to the pane height, scrolling to keep the selected
// row visible.
//
// This is for the views that are one thing from top to bottom — the job detail,
// the run detail, the edit form. The list views split themselves into pinned
// chrome and a scrolling body and call windowBody through renderPane instead,
// because their tab bar has to survive being scrolled past.
func (m *Model) window(content string) string {
	out := m.windowBody(content, m.paneHeight())
	// A whole-view render has no chrome above it, so its first line is the top
	// of the pane. See mouse.go.
	m.recordWindow(0, m.scroll)
	return out
}

// windowBody trims content to avail rows, scrolling to keep the selected row
// visible.
//
// The selected row is found by its cursor marker rather than by line number, so
// scrolling follows the selection in every view that has one without any of
// them tracking where their rows ended up after wrapping and styling.
//
// avail is the body's share of the pane, not the pane: the caller has already
// spent rows on chrome, and a body that measured itself against the whole pane
// would push that chrome off the bottom.
func (m *Model) windowBody(content string, avail int) string {
	if avail < 1 {
		avail = 1
	}
	lines := strings.Split(content, "\n")

	if len(lines) <= avail {
		m.scroll = 0
		// Everything fits, so the newest message is already on screen.
		m.hitRows = len(lines)
		return content
	}

	view := avail
	hinted := avail > 1
	if hinted {
		view = avail - 1
	}

	// Follow the selection: find the marked row and pull the window to it.
	cursorLine := -1
	for i, l := range lines {
		if strings.Contains(l, cursorMark) {
			cursorLine = i
			break
		}
	}
	if cursorLine >= 0 {
		if cursorLine < m.scroll+1 {
			m.scroll = cursorLine - 1
		}
		if cursorLine > m.scroll+view-2 {
			m.scroll = cursorLine - view + 2
		}
	}

	maxScroll := len(lines) - view
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}
	if m.scroll < 0 {
		m.scroll = 0
	}

	end := m.scroll + view
	if end > len(lines) {
		end = len(lines)
	}
	// How many of the rows on screen are content. The scroll hint below them is
	// not a row of anything, and a click on it must not reach the line it is
	// counting — the first one that did not fit. See mouse.go.
	m.hitRows = end - m.scroll
	out := strings.Join(lines[m.scroll:end], "\n")

	above, below := m.scroll, len(lines)-end
	if hinted && (above > 0 || below > 0) {
		out += "\n" + dimStyle.Render(scrollHint(above, below))
	}
	return out
}

func scrollHint(above, below int) string {
	switch {
	case above > 0 && below > 0:
		return "↑ " + itoa(above) + " more   ↓ " + itoa(below) + " more"
	case above > 0:
		return "↑ " + itoa(above) + " more"
	default:
		return "↓ " + itoa(below) + " more"
	}
}

// scrollBy moves the window manually, for keys that page through content that
// has no selectable rows.
func (m *Model) scrollBy(delta int) {
	m.scroll += delta
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func (m *Model) scrollUp(delta int) {
	m.scrollBy(-delta)
}

// pageSize is how far the paging keys move.
func (m *Model) pageSize() int {
	h := m.paneHeight()
	if h > 4 {
		return h - 3
	}
	return 1
}

// cursorMark is the glyph every view uses to mark its selected row. Windowing
// finds the selection by searching for it, so it must be unique to that role.
const cursorMark = "▸"
