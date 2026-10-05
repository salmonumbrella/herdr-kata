package runner

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPersistentForkJobNeverPromptsLegacyAgent(t *testing.T) {
	for _, live := range []string{"legacy", "fork-and-legacy"} {
		t.Run(live, func(t *testing.T) {
			state := t.TempDir()
			recordWorkspace(t, state, "w1", WorkspaceLabel)
			h, calls := fakeContextCLI(t, state, t.TempDir(), "codex", live, "session", "")
			r := &Runner{Herdr: h, StateDir: state}
			run, err := r.Execute(context.Background(), Job{ID: "mail", Kind: "codex", Persistent: true, KeepContext: false, Prompt: "example work", Timeout: time.Second}, "run-2")
			if err != nil {
				t.Fatal(err)
			}
			if run.Outcome != OutcomeDone {
				t.Fatalf("fork run did not finish: %+v", run)
			}
			log := calls()
			for _, line := range strings.Split(log, "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 3 && fields[0] == "agent" && (fields[1] == "prompt" || fields[1] == "clear") && fields[2] == "bmp-mail" {
					t.Fatalf("fork cleared or prompted legacy agent: %s\n%s", line, log)
				}
			}
			if run.AgentName != "hkp-mail" || run.PaneID != "w1:p9" {
				t.Fatalf("run adopted another namespace/pane: %+v\n%s", run, log)
			}
			if !strings.Contains(log, "agent prompt hkp-mail ") {
				t.Fatalf("fork agent never received its work:\n%s", log)
			}
			starts := strings.Count(log, "agent start ")
			if live == "legacy" && starts != 1 {
				t.Fatalf("new fork run started %d agents, want 1:\n%s", starts, log)
			}
			if live == "fork-and-legacy" && starts != 0 {
				t.Fatalf("live fork agent was not reused:\n%s", log)
			}
		})
	}
}

func TestForkAgentNamespacesRemainLegalAndBounded(t *testing.T) {
	for _, id := range []string{"", "example-job", "9 leading punctuation!", strings.Repeat("example-job-", 20)} {
		for _, tc := range []struct{ name, prefix string }{
			{persistentAgentName(id), "hkp-"},
			{agentName(id, "20261003T120000Z-example-run"), "hk-"},
		} {
			if !strings.HasPrefix(tc.name, tc.prefix) && tc.name != strings.TrimSuffix(tc.prefix, "-") {
				t.Errorf("name %q must use fork namespace %q", tc.name, tc.prefix)
			}
			if problem := herdrNameProblem(tc.name); problem != "" {
				t.Errorf("name %q: %s", tc.name, problem)
			}
		}
	}
	for _, input := range []string{"", "!!!", "9leading"} {
		name := sanitizeAgentName(input)
		if !strings.HasPrefix(name, "hk-") {
			t.Errorf("fallback %q must use fork namespace", name)
		}
		if problem := herdrNameProblem(name); problem != "" {
			t.Errorf("fallback %q: %s", name, problem)
		}
	}
}
