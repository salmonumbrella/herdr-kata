package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

// stoodDownActions refuses controls as a lane another installation took over.
type stoodDownActions struct{ fakeActions }

var stoodDown = &runner.LeaseRefusal{Owner: "elsewhere", Generation: 2}

func (*stoodDownActions) RunNow(string) error { return stoodDown }
func (*stoodDownActions) Resume(string) error { return stoodDown }

// R and u on a stood-down lane show the lease refusal, not an acknowledgement.
func TestStoodDownLaneRefusalShownForRunAndResume(t *testing.T) {
	const want = "another installation (elsewhere) owns this lane: lease generation 2"
	t.Run("R", func(t *testing.T) {
		m, _ := actionJobs(t)
		m.opts.Service = &stoodDownActions{}
		m.data.Jobs = m.data.Jobs[:1]
		m.data.Jobs[0].Definition = []byte(`{"enabled":true,"action":{"kind":"execute"}}`)
		m.data.Jobs[0].Supported, m.data.Jobs[0].Owner = true, defs.Adapter
		m.rebuildJobIndex()
		m.rebuild()
		send(m, "R")
		if m.notice.Text != want || strings.Contains(ansi.Strip(uncachedView(m)), "run requested") {
			t.Fatalf("notice %q", m.notice.Text)
		}
	})
	t.Run("u", func(t *testing.T) {
		d := workflowSnapshot()
		d.Runs = d.Runs[:1]
		d.Runs[0].Phase = runner.Held
		d.Runs[0].Job.Name = "Example job"
		m := workflowModel(t, d)
		m.opts.Service = &stoodDownActions{}
		m.width, m.height = 180, 50
		send(m, "u")
		if text := ansi.Strip(uncachedView(m)); !strings.Contains(text, want) || !strings.Contains(text, "parked") {
			t.Fatal(text)
		}
	})
}
