package defs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestInvalidDefinitionBytesStayUnsupportedWithHealthyPeer(t *testing.T) {
	f, cfg, l := cacheFixture(t)
	f.workflows = []kata.Workflow{
		{UID: "invalid-utf8", Definition: json.RawMessage("{\"version\":1,\"steps\":[{\"key\":\"check\",\"kind\":\"command\",\"command\":\"true\xff\"}]}")},
		{UID: "nul-command", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"check","kind":"command","command":"true\u0000"}]}`)},
	}
	job := func(uid, action string) kata.Job {
		return kata.Job{UID: uid, Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":` + action + `,"issue":{"kind":"per-run","title":"Check"},"overlap":"forbid","catchup":"latest"}`)}
	}
	f.jobs = []kata.Job{
		job("invalid-job", "{\"kind\":\"execute\",\"prompt\":\"Review \xff\"}"),
		job("invalid-workflow", `{"kind":"execute","workflow_uid":"invalid-utf8"}`),
		job("invalid-command", `{"kind":"execute","workflow_uid":"nul-command"}`),
		job("healthy", `{"kind":"execute","prompt":"Review café 你好 �"}`),
	}
	cache := NewCache(cfg, f, state.OSFS{}, l)
	if err := cache.Refresh(t.Context()); err != nil {
		t.Fatal("invalid definition stopped peer refresh", err)
	}
	raw, err := os.ReadFile(l.DefsSnapshot())
	if err != nil || !utf8.Valid(raw) {
		t.Fatal("unsupported definitions made the snapshot invalid UTF-8", err)
	}
	visible, err := json.Marshal(cache.Snapshot())
	if err != nil || !utf8.Valid(visible) {
		t.Fatal("unsupported definitions made status invalid UTF-8", err)
	}
	saved, err := ReadSnapshot(state.OSFS{}, l.DefsSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, entries := range [][]JobEntry{cache.Snapshot(), saved.Jobs} {
		if len(entries) != 4 {
			t.Fatal("missing definitions", len(entries))
		}
		for _, entry := range entries {
			if entry.UID == "healthy" {
				if !entry.Supported || len(entry.Reasons) != 0 {
					t.Fatalf("healthy peer became unsupported: %+v", entry)
				}
				continue
			}
			reason := "invalid UTF-8 in definition"
			if entry.UID == "invalid-command" {
				reason = "command must be UTF-8 without NUL"
			}
			if entry.Supported || !strings.Contains(strings.Join(entry.Reasons, " "), reason) {
				t.Fatalf("invalid bytes authorized execution or lost reason: %+v", entry)
			}
		}
	}
}
