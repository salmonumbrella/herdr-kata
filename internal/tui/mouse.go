package tui

import (
	tea "charm.land/bubbletea/v2"
)

type tabHit struct{ Index, X0, X1 int }
type hits struct {
	Rows                               map[int]rowHit
	Tabs                               []tabHit
	TabY, Top, Scroll, BodyRows, Width int
}

func (h hits) At(x, y int) (rowHit, bool) {
	if x < 0 || x >= h.Width || y < 0 {
		return rowHit{}, false
	}
	hit, ok := h.Rows[y]
	return hit, ok && x >= hit.X0 && x < hit.X1
}
func (h hits) TabAt(x, y int) (int, bool) {
	if x < 0 || x >= h.Width || y != h.TabY {
		return 0, false
	}
	for _, hit := range h.Tabs {
		if x >= hit.X0 && x < hit.X1 {
			return hit.Index, true
		}
	}
	return 0, false
}
func (h *hits) SetTabs(y int, tabs []tabHit, height int) {
	h.Tabs = nil
	h.TabY = y
	if y < 0 || y >= height {
		return
	}
	for _, hit := range tabs {
		hit.X0 = max(0, hit.X0)
		hit.X1 = min(h.Width, hit.X1)
		if hit.X0 < hit.X1 {
			h.Tabs = append(h.Tabs, hit)
		}
	}
}

func (m *model) mouse(msg tea.MouseMsg) tea.Cmd {
	_, click := msg.(tea.MouseClickMsg)
	_, wheel := msg.(tea.MouseWheelMsg)
	v := msg.Mouse()
	if m.searching || m.editor != nil || m.openingEditor || len(m.projectChoices) > 0 || m.mouseOff || !click && !wheel {
		return nil
	}
	if m.conflict != nil || len(m.confirm) > 0 {
		if v.Button == tea.MouseWheelUp {
			m.modalScroll = max(0, m.modalScroll-3)
		}
		if v.Button == tea.MouseWheelDown {
			m.modalScroll += 3
			if len(m.confirm) > 0 {
				m.modalScroll = min(m.modalScroll, len(m.confirm)-1)
			}
		}
		return nil
	}
	if v.Button == tea.MouseWheelUp {
		m.move(-3)
		return nil
	}
	if v.Button == tea.MouseWheelDown {
		m.move(3)
		return nil
	}
	if v.Button != tea.MouseLeft {
		return nil
	}
	if index, ok := m.hits.TabAt(v.X, v.Y); ok {
		if index == 0 {
			if _, ok := m.selectedOptions().Service.(kataCommander); !ok && !m.multipleProjects() {
				m.notify(statusInfo, "Kata unavailable")
				return nil
			}
			return m.requestKata()
		}
		m.selectTab(index - 1)
		return nil
	}

	hit, ok := m.hits.At(v.X, v.Y)
	if !ok {
		return nil
	}
	if m.currentView() != viewList {
		items := m.detailItems()
		for _, item := range items {
			if item.Key == hit.Key {
				same := m.detailKey == hit.Key
				m.detailKey, m.detailOrdinal = hit.Key, item.Index
				m.follow = true
				if same && m.currentView() == viewWorkflow {
					return m.workflowDetailKey(tea.KeyPressMsg{Code: 'l', Text: "l"})
				}
				return nil
			}
		}
		return nil
	}
	if hit.Index == m.cursor {
		return m.open()
	}
	m.cursor = hit.Index
	m.follow = true

	return nil
}
