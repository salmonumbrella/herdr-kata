package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/usage"
)

func TestRunDetailReadsSavedUsage(t *testing.T) {
	cost := int64(1234567)
	for _, tc := range []struct {
		name         string
		u            *usage.Usage
		tokens, cost string
	}{
		{"legacy", nil, "0 in · 0 out · 0 cache read · 0 cache write", ""},
		{"saved", &usage.Usage{InputTokens: 1200, OutputTokens: 45, CacheReadTokens: 6, CacheCreationTokens: 7, CostMicroUSD: &cost, Found: true}, "1.2k in · 45 out · 6 cache read · 7 cache write", "~$1.23"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			r := runner.Run{UID: "run", Phase: runner.Settled, Outcome: "succeeded", Usage: tc.u}
			text := detailBody(t, r, 120)
			if !strings.Contains(text, "tokens         "+tc.tokens) {
				t.Fatal(text)
			}
			if tc.cost != "" && !strings.Contains(text, "cost           "+tc.cost) {
				t.Fatal(text)
			}
			if tc.cost == "" && strings.Contains(text, "\ncost ") {
				t.Fatal("invented cost", text)
			}
		})
	}
}

// Run detail names the model of each step that runs on its own profile.
func TestRunDetailShowsStepModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := runner.Run{UID: "run", Phase: runner.Settled, Outcome: "succeeded", ProfileName: "default", Profile: config.Profile{Kind: "codex", Args: []string{"--model", "maker-model"}},
		StepProfiles: map[string]config.Profile{"checker": {Kind: "claude", Args: []string{"--model", "checker-model"}}},
		JobDef:       defs.JobDef{Action: defs.Action{Kind: "execute", WorkflowUID: "flow"}},
		WFDef:        &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "make", Kind: "prompt", Prompt: "make"}, {Key: "check", Kind: "prompt", Prompt: "check", After: []string{"make"}, Options: json.RawMessage(`{"agent":"checker"}`)}}},
		Steps:        []runner.StepState{{Key: "make", Kind: "prompt", Attempt: 1}, {Key: "check", Kind: "prompt", Attempt: 1}}}
	if text := detailBody(t, r, 120); !strings.Contains(text, "model          maker-model · check checker-model") {
		t.Fatal(text)
	}
}
