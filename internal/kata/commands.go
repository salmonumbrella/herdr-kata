package kata

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// CheckCommands probes help through the regular finite-call transport. Help is
// text even with --json, so it is intentionally not decoded as a JSON envelope.
func (c *CLI) CheckCommands(ctx context.Context) error {
	for _, probe := range []struct {
		args []string
		word string
	}{{[]string{"cron", "--help"}, "capabilities"}, {[]string{"events", "--help"}, "--tail"}} {
		// Capture text with an isolated transport clone; keep the same env and route.
		var help []byte
		clone := New(c.cfg, c.stateDir, func(ctx context.Context, argv, env []string, dir string, stdin []byte) ([]byte, []byte, int, error) {
			out, stderr, exit, err := c.runner(ctx, argv, env, dir, stdin)
			help = out
			return out, stderr, exit, err
		}, nil)
		if err := clone.call(ctx, nil, nil, probe.args...); err != nil {
			return err
		}
		if !strings.Contains(string(help), probe.word) {
			return fmt.Errorf("kata %s missing %s", probe.args[0], probe.word)
		}
	}
	return nil
}

// typedReplyMinAPI matches the merged Kata #523 contract (f3a06c1c).
const typedReplyMinAPI = "0.28.0"

// TypedReplies probes only help and health. A failure selects plain mode for
// newly prepared operations; saved operations never consult this result.
func (c *CLI) TypedReplies(ctx context.Context) (bool, string) {
	var help []byte
	clone := New(c.cfg, c.stateDir, func(ctx context.Context, argv, env []string, dir string, stdin []byte) ([]byte, []byte, int, error) {
		out, stderr, exit, err := c.runner(ctx, argv, env, dir, stdin)
		help = out
		return out, stderr, exit, err
	}, nil)
	if err := clone.call(ctx, nil, nil, "comment", "--help"); err != nil {
		return false, err.Error()
	}
	for _, flag := range []string{"--reply", "--confirm", "--refute"} {
		if !strings.Contains(string(help), flag) {
			return false, "comment help missing " + flag
		}
	}
	var health struct {
		API string `json:"api_schema_version"`
	}
	if err := c.call(ctx, nil, &health, "health"); err != nil {
		return false, err.Error()
	}
	if !apiAtLeast(health.API, typedReplyMinAPI) {
		return false, "daemon api_schema_version requires " + typedReplyMinAPI + "; got " + health.API
	}
	return true, ""
}
func apiAtLeast(got, minimum string) bool {
	got, _, _ = strings.Cut(got, "-")
	got, _, _ = strings.Cut(got, "+")
	parse := func(s string) ([3]int, bool) {
		var v [3]int
		parts := strings.Split(s, ".")
		if len(parts) != 3 {
			return v, false
		}
		for i, part := range parts {
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 {
				return v, false
			}
			v[i] = n
		}
		return v, true
	}
	v, ok := parse(got)
	if !ok {
		return false
	}
	min, _ := parse(minimum)
	for i := range v {
		if v[i] != min[i] {
			return v[i] > min[i]
		}
	}
	return true
}
