package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestJobBoxesMeasured(t *testing.T) {
	m, _ := actionJobs(t)
	m.finished = true
	m.rebuild()
	m.height = 24
	m.confirmMode = "prune"
	for i := range 30 {
		m.confirm = append(m.confirm, kata.Job{UID: fmt.Sprint(i), Name: fmt.Sprintf("target-%02d", i), Project: "example-project"})
	}
	m.Update(struct{}{})
	text := ansi.Strip(m.viewContent())
	if !strings.Contains(text, "example-a") || !strings.Contains(text, "prune 30 finished one-shot(s)") || !strings.Contains(text, "run history is kept") {
		t.Fatal(text)
	}
	for range 22 {
		send(m, "j")
	}
	text = ansi.Strip(m.viewContent())
	if !strings.Contains(text, "target-22") || !strings.Contains(text, "example-a") {
		t.Fatal(text)
	}
	for y := range m.hits.Rows {
		if y >= m.height-12 {
			t.Fatal("confirmation row clickable", m.hits)
		}
	}
}
func TestJobConflictBox(t *testing.T) {
	m, _ := actionJobs(t)
	v := m.jobs.byKey[jobKey{"p", "a"}]
	newer := v.job
	newer.Definition = []byte(`{"enabled":false,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"}}`)
	m.conflict = &ConflictError{Pending: v.job, Newer: newer, Operation: "pause", Cause: errors.New("guard")}
	text := ""
	for _, l := range m.renderJobConflict() {
		text += ansi.Strip(l.Text) + "\n"
	}
	for _, want := range []string{"Kata has a newer definition", "yours:", "Kata:", "pause not applied", "original definition guard kept"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	if strings.Contains(text, `"action"`) {
		t.Fatal("raw record in conflict", text)
	}
	m.conflict.Newer = kata.Job{}
	m.conflict.Operation = "delete"
	text = ""
	for _, l := range m.renderJobConflict() {
		text += ansi.Strip(l.Text) + "\n"
	}
	if !strings.Contains(text, "Readback unavailable") || !strings.Contains(text, "outcome uncertain: the job may or may not be deleted") {
		t.Fatal(text)
	}
}

func TestPruneTargetsRemainVisibleInShortPane(t *testing.T) {
	for _, height := range []int{10, 12} {
		t.Run(fmt.Sprint(height), func(t *testing.T) {
			m, _ := actionJobs(t)
			m.height = height
			m.confirmMode = "prune"
			for i := range 5 {
				m.confirm = append(m.confirm, kata.Job{UID: fmt.Sprint(i), Name: fmt.Sprintf("target-%02d", i)})
			}
			m.Update(struct{}{})
			for i := range 5 {
				text := ansi.Strip(m.viewContent())
				if !strings.Contains(text, fmt.Sprintf("target-%02d", i)) || !strings.Contains(text, "y delete") || len(strings.Split(text, "\n")) > height {
					t.Fatal(text)
				}
				send(m, "j")
			}
		})
	}
}
