package runner

import (
	"fmt"
	"slices"
	"strings"
)

// These rules mirror Kata internal/daemon/close_validation.go at 6ef782d6.
// The input is CLI evidence, so parsing also rejects unknown prefixes and
// duplicate flag values that Kata's close command cannot send to the daemon.
func ValidateClose(reason, message string, evidence []string) error {
	norm := strings.TrimRight(strings.ToLower(strings.Join(strings.Fields(message), " ")), ".?!")
	switch norm {
	case "done", "fixed", "complete", "completed", "ok", "okay", "yes", "no", "n/a", "na", "skip", "nope":
		return fmt.Errorf("message rejected as trivial (%q)", norm)
	}
	floor := 40
	switch reason {
	case "wontfix":
		floor = 60
	case "duplicate", "superseded":
		floor = 20
	}
	if len(norm) < floor {
		return fmt.Errorf("message too short for reason=%s (need >=%d chars after normalization, got %d)", reason, floor, len(norm))
	}
	counts := map[string]int{}
	seen := map[string]bool{}
	for i, raw := range evidence {
		kind, value, ok := strings.Cut(raw, ":")
		if !ok {
			return fmt.Errorf("evidence[%d]: expected type:value", i)
		}
		value = strings.TrimSpace(value)
		field := ""
		switch kind {
		case "commit":
			field = "sha"
		case "pr":
			field = "url"
		case "test":
			field = "command"
		case "reviewed-paths":
			field = "paths entry"
		case "external":
			field = "account"
		case "no-change-audit":
			field = "rationale"
		case "duplicate-of", "superseded-by":
			field = "issue_ref"
		default:
			return fmt.Errorf("evidence[%d]: unknown type %q", i, kind)
		}
		if value == "" {
			return fmt.Errorf("evidence[%d] %s requires non-empty %s", i, kind, field)
		}
		if kind == "commit" && !isHexSHA(value) {
			return fmt.Errorf("evidence[%d] commit sha %q is not a valid git SHA (expected 7-40 hex characters)", i, value)
		}
		// Kata detects duplicate raw paths and raw typed payloads, before shape
		// normalization. Preserve that distinction ("a" and " a " differ).
		if seen[raw] {
			return fmt.Errorf("duplicate evidence item %s", raw)
		}
		seen[raw] = true
		counts[kind]++
	}
	onlyAllow := func(allowed ...string) error {
		for _, raw := range evidence {
			kind, _, _ := strings.Cut(raw, ":")
			permitted := slices.Contains(allowed, kind)
			if !permitted {
				return fmt.Errorf("evidence type %q not allowed for reason=%s", kind, reason)
			}
		}
		return nil
	}
	switch reason {
	case "done":
		if err := onlyAllow("commit", "pr", "test", "reviewed-paths", "external"); err != nil {
			return err
		}
		if len(evidence) == 0 {
			return fmt.Errorf("evidence required for reason=done")
		}
	case "wontfix":
		if len(evidence) > 0 {
			return fmt.Errorf("evidence not allowed for reason=wontfix")
		}
	case "duplicate":
		if counts["duplicate-of"] != 1 {
			return fmt.Errorf("reason=duplicate requires exactly one duplicate-of evidence item")
		}
		return onlyAllow("duplicate-of")
	case "superseded":
		if counts["superseded-by"] != 1 {
			return fmt.Errorf("reason=superseded requires exactly one superseded-by evidence item")
		}
		return onlyAllow("superseded-by")
	case "audit-no-change":
		if counts["no-change-audit"] != 1 {
			return fmt.Errorf("reason=audit-no-change requires exactly one no-change-audit evidence item")
		}
		return onlyAllow("no-change-audit", "reviewed-paths")
	default:
		return fmt.Errorf("unknown reason %q", reason)
	}
	return nil
}

func isHexSHA(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}
