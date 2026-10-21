package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/jobform"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

// The snapshot can lag a successful write by several seconds. A second p
// must toggle the saved definition, including its new conditional-write guard.
func TestPauseTwiceBeforeSnapshotRefresh(t *testing.T) {
	m, _ := fixture(t)
	pinLegacyActions(m)
	spy := &jobActionSpy{}
	m.opts.Service = spy
	send(m, "p")
	m.Update(dataMsg{data: m.data}) // old snapshot arrives after the acknowledgement
	send(m, "p")
	if len(spy.paused) != 2 {
		t.Fatalf("pause requests: %+v", spy.paused)
	}
	var d defs.JobDef
	_ = json.Unmarshal(spy.paused[1].Definition, &d)
	if d.Enabled || spy.paused[1].DefinitionEventUID != "saved-event" {
		t.Fatalf("second toggle used stale job: %+v, enabled=%v", spy.paused[1], d.Enabled)
	}
	if !strings.Contains(ansi.Strip(m.viewContent()), "resumed") {
		t.Fatal(m.viewContent())
	}
}

func TestFreshDraftDefaultsCanBeAbandoned(t *testing.T) {
	for _, profiles := range []map[string]config.Profile{{"default": {Kind: "codex"}}, {"worker": {Kind: "codex"}}} {
		c := cfg()
		c.Profiles = profiles
		m := newJobForm(t.Context(), c, &fakeWriter{}, nil, nil, editorContext{DefaultCheckout: "repo"})
		_, cmd := m.Update(press("esc"))
		if cmd == nil {
			t.Fatal("untouched defaulted draft asks to discard", m.View())
		}
		if _, ok := cmd().(editorClosedMsg); !ok {
			t.Fatal("draft did not close")
		}
		m.Set("name", "changed")
		_, cmd = m.Update(press("esc"))
		if cmd != nil || !strings.Contains(ansi.Strip(m.View()), "discard changes?") {
			t.Fatal("edited draft lost discard guard", m.View())
		}
	}
}

func TestReloadAcknowledgedOnOriginatingScreen(t *testing.T) {
	m, _ := fixture(t)
	m.Update(press("r"))
	m.Update(dataMsg{data: m.data})
	if !strings.Contains(ansi.Strip(m.viewContent()), "reloaded") {
		t.Fatal("Jobs reload was not acknowledged", m.viewContent())
	}
	m.now = func() time.Time { return time.Now().Add(time.Minute) }
	m.Update(tickMsg(time.Now()))
	m.selectTab(2)
	m.Update(dataMsg{data: m.data})
	if strings.Contains(ansi.Strip(m.viewContent()), "reloaded") {
		t.Fatal("stale reload acknowledgement", m.viewContent())
	}
}

func TestNarrowListsKeepStatusBeforeModel(t *testing.T) {
	m, _ := fixture(t)
	m.width = 50
	for _, tab := range []int{0, 1} {
		m.selectTab(tab)
		var widths []int
		var cols []column
		if tab == 0 {
			widths = m.jobsWidths()
			cols = m.jobsColumns()
		} else {
			widths = m.runsWidths()
			cols = m.runsColumns()
		}
		// Status keeps its width; model gives way first when space runs out.
		_, _, rows := tabLayout(m, tab)
		for i, c := range cols {
			if c.Title == "status" && widths[i] != naturalWidth(cols, rows, i) {
				t.Errorf("tab %d status width = %d, needs %d", tab, widths[i], naturalWidth(cols, rows, i))
			}
		}
	}
}

func TestWideJobsGivesListMostOfTheWidth(t *testing.T) {
	m, _ := fixture(t)
	m.width, m.height = 160, 45
	text := ansi.Strip(m.viewContent())
	// A normal job name and a useful schedule fit while the inspector stays visible.
	if !strings.Contains(text, "Job 00") {
		t.Fatal(text)
	}
	if m.listWidth() < 100 {
		t.Fatalf("list only gets %d of 160 columns", m.listWidth())
	}
}

func TestRunModelApplicabilityAndDetailLabels(t *testing.T) {
	profile := config.Profile{Kind: "codex", Args: []string{"--model", "example-model", "-c", `model_reasoning_effort="high"`}}
	for _, tc := range []struct {
		name string
		job  defs.JobDef
		wf   *defs.WorkflowDef
		want string
	}{
		{"prompt", defs.JobDef{Action: defs.Action{Kind: "execute", Prompt: "work"}}, nil, "example-model high"},
		{"command", defs.JobDef{Action: defs.Action{Kind: "execute", WorkflowUID: "wf"}}, &defs.WorkflowDef{Steps: []defs.Step{{Key: "work", Kind: "command", Command: "true"}}}, "—"},
		{"notify", defs.JobDef{Action: defs.Action{Kind: "notify"}}, nil, "—"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := fixture(t)
			r := &m.data.Runs[0]
			r.JobDef = tc.job
			r.WFDef = tc.wf
			r.Profile = profile
			r.ProfileName = "default"
			m.Update(dataMsg{data: m.data})
			m.selectTab(1)
			cells, cols := m.runCells(m.rows[0]), m.runsColumns()
			if got := cellText(t, cols, cells, "model"); got != tc.want {
				t.Errorf("model=%q want %q", got, tc.want)
			}
			if detail := cellText(t, cols, cells, "detail"); tc.want == "—" && detail != "" {
				t.Errorf("agent-less list shows synthetic progress: %q", detail)
			}
			send(m, "l")
			text := ansi.Strip(runDetailText(t, m))
			if tc.want != "—" && !strings.Contains(text, "model          "+tc.want) {
				t.Errorf("detail model missing %q:\n%s", tc.want, text)
			}
			if tc.want == "—" {
				for _, label := range []string{"agent ", "model ", "tokens ", "prompts ", "herdr tab ", "cost "} {
					if strings.Contains(text, label) {
						t.Errorf("agent-less detail contains %q:\n%s", label, text)
					}
				}
				if strings.Contains(text, "steps ok") {
					t.Error("agent-less run shows a synthetic step summary", text)
				}
			}
		})
	}
}

func TestInspectorShowsModelEffort(t *testing.T) {
	m, _ := fixture(t)
	v, _ := m.selectedJobView()
	v.facts = jobform.ProfileInfo{Model: "example-model", Effort: "high"}
	var text string
	for _, l := range m.renderJobInspector(v, 80) {
		text += ansi.Strip(l.Text) + "\n"
	}
	if !strings.Contains(text, "example-model high") {
		t.Fatal(text)
	}
}

func TestRunDetailKeepsIdentifiersAndDeadlinesReadable(t *testing.T) {
	m, _ := fixture(t)
	r := &m.data.Runs[0]
	r.JobDef = defs.JobDef{Action: defs.Action{Kind: "execute", Prompt: "work"}}
	r.WFDef = nil
	r.UID = "01K00000000000000000000001"
	r.Herdr.AgentName = "hk-" + r.UID
	r.Herdr.ServerID = herdr.ServerID{Dev: 1}
	r.Phase = runner.Settled
	r.Outcome = "succeeded"
	r.ExpiresAt = time.Now().Add(-time.Hour)
	m.Update(dataMsg{data: m.data})
	m.selectTab(1)
	send(m, "l")
	text := ansi.Strip(runDetailText(t, m))
	if strings.Contains(text, r.UID) {
		t.Fatal("raw run UID shown", text)
	}
	if strings.Contains(text, "deadline") {
		t.Fatal("settled deadline shown", text)
	}
}

func TestFootersOfferGlobalActionsOnlyWhereTheyWork(t *testing.T) {
	m, _ := fixture(t)
	for name, h := range map[string]footerHelp{"jobs": jobsHelp(), "runs": runsHelp(), "workflows": workflowHelp(), "job": m.jobDetailHelp(), "run": m.runDetailHelp(), "workflow": workflowDetailHelp()} {
		text := ansi.Strip(strings.Join(h.Lines(200), "\n"))
		for _, key := range []string{"K ", "r "} {
			if !strings.Contains(text, key) {
				t.Errorf("%s missing %q: %s", name, key, text)
			}
		}
		if strings.Contains(text, "ctrl+]") {
			t.Errorf("%s offers Kata-only leave key", name)
		}
	}
	m.kataActive = true
	m.frameValid = false
	if !strings.Contains(ansi.Strip(m.viewContent()), "ctrl+] leave Kata") {
		t.Fatal("embedded Kata lacks exit hint", m.viewContent())
	}
}

func TestNewWorkflowDraftNamesUnselectedWorkflow(t *testing.T) {
	m := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{})
	m.Set("mode", "workflow")
	text := ansi.Strip(m.View())
	if strings.Contains(text, "workflow unavailable") || !strings.Contains(text, "select a workflow") {
		t.Fatal(text)
	}
}

func TestModelEffortOnJobDetailAndEditorHelp(t *testing.T) {
	m, _ := fixture(t)
	m.opts.Config = cfg()
	m.opts.Config.Profiles["default"] = config.Profile{Kind: "codex", Args: []string{"--model", "example-model", "-c", `model_reasoning_effort="high"`}}
	m.data.Jobs[0].Definition = json.RawMessage(`{"enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"}}`)
	m.Update(dataMsg{data: m.data})
	send(m, "l")
	if !strings.Contains(ansi.Strip(m.viewContent()), "example-model high") {
		t.Fatal(m.viewContent())
	}
	form := newJobForm(t.Context(), m.opts.Config, &fakeWriter{}, nil, nil, editorContext{})
	for _, field := range form.fields() {
		if field.Key == "profile" && !strings.Contains(ansi.Strip(form.fieldHelp(field)), "model example-model high") {
			t.Fatal(form.fieldHelp(field))
		}
	}
}

func TestReloadDoesNotFollowNavigation(t *testing.T) {
	m, _ := fixture(t)
	m.Update(press("r"))
	m.selectTab(2)
	m.Update(dataMsg{data: m.data})
	if strings.Contains(ansi.Strip(m.viewContent()), "reloaded") {
		t.Fatal("reload acknowledged after leaving its screen", m.viewContent())
	}
}

func TestEditorSaveFeedsNextToggleBeforeSnapshot(t *testing.T) {
	m, _ := fixture(t)
	pinLegacyActions(m)
	spy := &jobActionSpy{}
	m.opts.Service = spy
	saved := m.job(m.rows[0])
	saved.Definition = json.RawMessage(`{"enabled":false,"trigger":{"kind":"manual"}}`)
	saved.DefinitionEventUID = "editor-event"
	saved.ProjectUID = "" // The editor target retains the local project scope.
	m.Update(editorClosedMsg{Message: "Job 00 saved", Target: editorTarget{ProjectUID: "p", UID: saved.UID}, Saved: &saved})
	m.Update(dataMsg{data: m.data})
	send(m, "p")
	if len(spy.paused) != 1 || spy.paused[0].DefinitionEventUID != "editor-event" {
		t.Fatal("toggle ignored saved editor guard", spy.paused)
	}
	var d defs.JobDef
	_ = json.Unmarshal(spy.paused[0].Definition, &d)
	if d.Enabled || !strings.Contains(ansi.Strip(m.viewContent()), "resumed") {
		t.Fatal("toggle ignored saved editor state", m.viewContent())
	}
}
