package jobform

import (
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"testing"
)

func TestProfileFacts(t *testing.T) {
	for _, tc := range []struct {
		name, kind                string
		args                      []string
		model, sandbox, approvals string
		sb, ab                    bool
	}{
		{"explicit", "codex", []string{"-m", "example-model", "--sandbox", "read-only", "--ask-for-approval", "never"}, "example-model", "read-only", "never", false, false},
		{"equals", "codex", []string{"--model=example-model", "--sandbox=danger-full-access", "--ask-for-approval=on-request"}, "example-model", "danger-full-access", "on-request", true, false},
		{"assignments", "codex", []string{"-c", `model="example-model"`, "-c", "sandbox_mode='read-only'", "-c", "approval_policy=never"}, "example-model", "read-only", "never", false, false},
		{"last", "codex", []string{"--model", "first", "--model=last", "-s", "workspace-write", "-s", "read-only", "-a", "never", "-a", "on-request"}, "last", "read-only", "on-request", false, false},
		{"auto", "codex", []string{"--full-auto"}, "", "workspace-write", "agent default", false, false},
		{"auto explicit", "codex", []string{"-s", "read-only", "--full-auto"}, "", "read-only", "agent default", false, false},
		{"bypass", "codex", []string{"--dangerously-bypass-approvals-and-sandbox", "-s", "read-only", "-a", "never"}, "", "read-only", "never", true, true},
		{"claude", "claude", []string{"--model", "example-model", "--permission-mode=bypassPermissions"}, "example-model", "agent default", "bypassPermissions", false, true},
		{"claude continue skipped", "claude", []string{"-c", "--dangerously-skip-permissions", "--model", "example-model"}, "example-model", "agent default", "permissions skipped", false, true},
		{"claude continue model", "claude", []string{"-c", "--model", "example-model"}, "example-model", "agent default", "agent default", false, false},
		{"claude skipped", "claude", []string{"--dangerously-skip-permissions"}, "", "agent default", "permissions skipped", false, true},
		{"unknown", "other", []string{"--sandbox=danger-full-access", "--permission-mode=bypassPermissions"}, "", "agent default", "agent default", false, false},
		{"default", "codex", nil, "", "agent default", "agent default", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ProfileFacts(config.Profile{Kind: tc.kind, Args: tc.args})
			if got.Kind != tc.kind || got.Model != tc.model || got.Sandbox != tc.sandbox || got.Approvals != tc.approvals || got.SandboxBypass != tc.sb || got.ApprovalsBypass != tc.ab {
				t.Fatalf("facts: %+v", got)
			}
		})
	}
	p := ProfileFacts(config.Profile{Kind: "codex", Args: []string{"--model", "two words", "a'b", ""}})
	if p.Args != `--model 'two words' 'a'\''b' ''` || p.Argv != `codex --model 'two words' 'a'\''b' ''` {
		t.Fatalf("display quoting: %+v", p)
	}
}

func TestProfileFactsKeepsExplicitReasoningEffort(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-c", `model_reasoning_effort="xhigh"`}, "xhigh"},
		{[]string{"-c=model_reasoning_effort='high'"}, "high"},
		{[]string{"-c", "model_reasoning_effort=low", "-c", "model_reasoning_effort=medium"}, "medium"},
		{nil, ""},
	} {
		got := ProfileFacts(config.Profile{Kind: "codex", Args: tc.args})
		if got.Effort != tc.want {
			t.Fatalf("args %q effort=%q, want %q", tc.args, got.Effort, tc.want)
		}
	}
}
