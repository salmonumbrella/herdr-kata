// Package jobform provides pure job presentation and form facts.
package jobform

import (
	"strings"
	"unicode"

	"github.com/salmonumbrella/herdr-kata/internal/config"
)

type ProfileInfo struct {
	Kind, Model, Effort, Sandbox, Approvals string
	SandboxBypass, ApprovalsBypass          bool
	Args, Argv                              string
}

// ProfileFacts reports explicit argv facts without inferring agent policy.
func ProfileFacts(p config.Profile) ProfileInfo {
	f := ProfileInfo{Kind: p.Kind, Sandbox: "agent default", Approvals: "agent default"}
	fullAuto, explicitSandbox := false, false
	for i := 0; i < len(p.Args); i++ {
		flag, value, assigned := strings.Cut(p.Args[i], "=")
		takesValue := flag == "--model" || flag == "-m"
		if p.Kind == "codex" {
			switch flag {
			case "-s", "--sandbox", "-a", "--ask-for-approval", "-c", "--config":
				takesValue = true
			}
		}
		if p.Kind == "claude" && (flag == "--permission-mode" || flag == "--effort") {
			takesValue = true
		}
		if takesValue && !assigned && i+1 < len(p.Args) {
			i++
			value = p.Args[i]
		}

		if flag == "--model" || flag == "-m" {
			f.Model = value
		}
		if p.Kind == "codex" {
			if flag == "-c" || flag == "--config" {
				key, v, ok := strings.Cut(value, "=")
				if ok {
					value = unquote(v)
					switch key {
					case "model":
						flag = "--model"
					case "model_reasoning_effort":
						f.Effort = value
					case "sandbox_mode":
						flag = "--sandbox"
					case "approval_policy":
						flag = "--ask-for-approval"
					}
				}
			}
			switch flag {
			case "--model", "-m":
				f.Model = value
			case "--sandbox", "-s":
				f.Sandbox = value
				explicitSandbox = true
				f.SandboxBypass = f.SandboxBypass || value == "danger-full-access"
			case "--ask-for-approval", "-a":
				f.Approvals = value
			case "--full-auto":
				fullAuto = true
			case "--dangerously-bypass-approvals-and-sandbox":
				f.Sandbox = "bypassed"
				f.Approvals = "bypassed"
				f.SandboxBypass = true
				f.ApprovalsBypass = true
				explicitSandbox = true
			}
		}
		if p.Kind == "claude" {
			switch flag {
			case "--effort":
				f.Effort = value
			case "--permission-mode":
				f.Approvals = value
				f.ApprovalsBypass = f.ApprovalsBypass || value == "bypassPermissions"
			case "--dangerously-skip-permissions":
				f.Approvals = "permissions skipped"
				f.ApprovalsBypass = true
			}
		}
	}
	if fullAuto && !explicitSandbox {
		f.Sandbox = "workspace-write"
	}
	f.Args = quoteArgs(p.Args)
	f.Argv = quoteArgs(append([]string{p.Kind}, p.Args...))
	return f
}
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' && s[len(s)-1] == '\'' || s[0] == '"' && s[len(s)-1] == '"') {
		return s[1 : len(s)-1]
	}
	return s
}
func quoteArgs(args []string) string {
	out := make([]string, len(args))
	for i, s := range args {
		if s != "" && strings.IndexFunc(s, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("_@%+=:,./-", r)
		}) < 0 {
			out[i] = s
		} else {
			out[i] = "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return strings.Join(out, " ")
}
