package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"go.kenn.io/kit/secretref"
)

func detailFixture(t testing.TB, name string, width, height int) *model {
	t.Helper()
	raw := `{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"cron","cron":"0 8 * * *","timezone":"Europe/Berlin"},"action":{"kind":"execute","prompt":"Review the example report.\nKeep this literal JSON: {\"version\":1}"},"issue":{"kind":"per-run","title":"Example report"},"overlap":"forbid","catchup":"skip","grace_seconds":600,"secret_refs":{"API_TOKEN":"mapped-name","OTHER_TOKEN":"unmapped-name"},"options":{"agent":"worker","tags":["daily","example"],"description":"Example scheduled report","future_setting":9007199254740993}}`
	switch name {
	case "workflow":
		raw = `{"enabled":true,"action":{"kind":"execute","workflow_uid":"flow"},"options":{"agent":"worker","input":"example input"}}`
	case "command-workflow":
		raw = `{"enabled":true,"action":{"kind":"execute","workflow_uid":"command-flow"}}`
	case "notify":
		raw = `{"enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"notify","recipient":"worker","message":"Review the example"},"issue":{"kind":"existing","uid":"example-issue"}}`
	case "recurring":
		raw = `{"enabled":false,"trigger":{"kind":"interval","interval_seconds":3600},"action":{"kind":"notify","recipient":"worker","message":"Optional delivery note"},"issue":{"kind":"per-run","parent_uid":"example-parent","title":"Example recurring child","body":"Inspect the example project and report the result."},"overlap":"forbid","catchup":"latest"}`
	}
	m := jobDetailFixture(t, raw)
	m.width, m.height = width, height
	m.now = func() time.Time { return runNow }
	m.notice.Text = ""
	m.opts.Config.Secrets = map[string]secretref.Ref{"mapped-name": {Env: "EXAMPLE_SECRET"}}
	m.opts.Config.Profiles["worker"] = config.Profile{Kind: "codex", Args: []string{"-m", "example-model", "--dangerously-bypass-approvals-and-sandbox"}}
	m.data.Workflows = []kata.Workflow{{UID: "flow", Name: "Example workflow", Definition: json.RawMessage(`{"version":1,"about":"Review, verify and summarize","steps":[{"key":"review","kind":"prompt","prompt":"Review the example"},{"key":"verify","kind":"command","command":"true","after":["review"]}]}`), ProjectUID: "p"}, {UID: "command-flow", Name: "Example command workflow", Definition: json.RawMessage(`{"version":1,"about":"Verify the example","steps":[{"key":"verify","kind":"command","command":"true"}]}`), ProjectUID: "p"}}
	for i, phase := range []runner.Phase{runner.Held, runner.Settled, runner.Settled} {
		start := runNow.Add(-time.Duration(i+1) * time.Hour)
		r := runner.Run{UID: fmt.Sprintf("run-%d", i), Target: runner.Target{Identity: kata.Identity{ProjectUID: "p"}}, Job: kata.Job{UID: "job"}, Phase: phase, StartedAt: &start, Herdr: runner.HerdrRef{PaneID: "pane"}, Reason: "review required", Steps: []runner.StepState{{Note: "Example note"}}}
		if i == 1 {
			r.Outcome = "succeeded"
		}
		if i == 2 {
			r.Outcome = "failed"
		}
		m.data.Runs = append(m.data.Runs, r)
	}
	if name == "empty" {
		m.data.Runs = nil
	}
	if name == "notify" {
		m.data.Jobs[0].IssueRefs = map[string]string{"example-issue": "example-project#cd34"}
	}
	if name == "recurring" {
		m.data.Jobs[0].IssueRefs = map[string]string{"example-parent": "example-project#ab12"}
	}
	if name == "unsupported" {
		m.data.Jobs[0].Supported = false
		m.data.Jobs[0].Reasons = []string{"unmapped profile"}
		m.data.Jobs[0].Warnings = []string{"Example warning"}
	}
	m.rebuildJobIndex()
	m.rebuild()
	m.detailKey = ""
	m.detailOrdinal = 0
	m.detailItems()
	m.scroll = 0
	m.follow = name != "prompt-top" && name != "unsupported"
	if name == "removed" {
		m.data.Jobs = nil
		m.rebuildJobIndex()
	}
	return m
}
func TestJobDetailGolden(t *testing.T) {
	for _, name := range []string{"prompt", "prompt-top", "workflow", "command-workflow", "notify", "recurring", "unsupported", "empty", "removed"} {
		t.Run(name, func(t *testing.T) {
			assertFrames(t, "job-detail", name, [][2]int{{120, 40}, {80, 24}}, func(w, h int) string { return detailFixture(t, name, w, h).viewContent() })
		})
	}
}
func TestJobDetailSecretRefRoles(t *testing.T) {
	_, err := renderFrames(120, 40, func(_ string) string {
		w, h := 120, 40
		t.Setenv("EXAMPLE_SECRET", "synthetic-secret-material")
		m := detailFixture(t, "prompt-top", w, h)
		text := bodyTextForTest(m.buildJobDetail().Body())
		if strings.Contains(text, "synthetic-secret-material") {
			t.Fatal("secret value leaked")
		}
		for _, want := range []string{detailMetaStyle.Render("enabled"), detailMetaStyle.Render(" (Europe/Berlin)"), detailMetaStyle.Render("2h0m0s (default)"), detailMetaStyle.Render("API_TOKEN ← "), detailMetaStyle.Render("mapped-name"), detailMetaStyle.Render("OTHER_TOKEN ← "), failStyle.Render("unmapped-name"), subtleStyle.Render("Example scheduled report")} {
			if !strings.Contains(text, want) {
				t.Fatalf("missing role %q in %q", want, text)
			}
		}
		if strings.Contains(text, failStyle.Render("mapped-name")) && failStyle.Render("mapped-name") != detailMetaStyle.Render("mapped-name") {
			t.Fatal("mapped name styled as failure")
		}
		for _, line := range m.buildJobDetail().Body() {
			if strings.Contains(ansi.Strip(line.Text), "secret refs") && (line.Hit != nil || len(line.Animations) > 0) {
				t.Fatal("secret row selectable or animated")
			}
		}
		return m.viewContent()
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestJobDetailNativeRole(t *testing.T) {
	_, err := renderFrames(120, 40, func(_ string) string {
		w, h := 120, 40
		m := detailFixture(t, "recurring", w, h)
		text := bodyTextForTest(m.buildJobDetail().Body())
		for _, want := range []string{attentionStyle.Render("paused"), detailMetaStyle.Render(" · native (Kata delivers)")} {
			if !strings.Contains(text, want) {
				t.Fatal(want, text)
			}
		}
		line := jobDetailKVLine("state", []cell{{Text: "enabled", Style: detailMetaStyle}, {Text: " · unsupported", Style: attentionStyle}}, 28)
		if ansi.StringWidth(line.Text) > 28 || line.Hit != nil || len(line.Animations) > 0 {
			t.Fatal(line)
		}
		return m.viewContent()
	})
	if err != nil {
		t.Fatal(err)
	}
}
