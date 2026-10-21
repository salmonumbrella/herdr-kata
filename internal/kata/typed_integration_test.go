//go:build kata_replies

package kata

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

func newTypedKata(t *testing.T, daemonBin, cliBin string) *CLI {
	t.Helper()
	home := t.TempDir()
	cfg := config.Kata{Bin: daemonBin, Project: "example-project", Actor: "worker", Env: map[string]string{"KATA_HOME": home}}
	c := New(cfg, home, nil, nil)
	rawKata(t, c, "daemon", "start")
	t.Cleanup(func() { rawKata(t, c, "daemon", "stop") })
	rawKata(t, c, "projects", "create", "example-project")
	cfg.Bin = cliBin
	return New(cfg, home, nil, nil)
}

func TestIntegTypedDetectionMatrix(t *testing.T) {
	typed := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	baseline := os.Getenv("HERDR_KATA_TEST_LEGACY_KATA_BIN")
	for _, tc := range []struct {
		name, daemon, cli string
		want              bool
	}{
		{"both new", typed, typed, true}, {"old daemon", baseline, typed, false}, {"old CLI", typed, baseline, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.daemon == "" || tc.cli == "" {
				t.Skip("optional legacy detection probe requires HERDR_KATA_TEST_LEGACY_KATA_BIN")
			}
			c := newTypedKata(t, tc.daemon, tc.cli)
			got, reason := c.TypedReplies(t.Context())
			if got != tc.want || !got && reason == "" {
				t.Fatal(got, reason)
			}
		})
	}
}

func TestIntegTypedReplies(t *testing.T) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	c := newTypedKata(t, bin, bin)
	issue, e := c.CreateIssue(t.Context(), CreateReq{Title: "Verify finding replies", IdempotencyKey: "issue"})
	requireNoError(t, e)
	root, e := c.Comment(t.Context(), CommentReq{Ref: issue.UID, Body: "Initial finding", IdempotencyKey: "root"})
	requireNoError(t, e)
	for _, kind := range []string{"reply", "confirm", "refute"} {
		req := CommentReq{Ref: issue.UID, ReplyTo: root.UID, Kind: kind, Body: strings.Repeat("界", 40), IdempotencyKey: kind}
		if kind == "confirm" {
			short := req
			short.Body = strings.Repeat("界", 39)
			short.IdempotencyKey = "short"
			_, err := c.Comment(t.Context(), short)
			var ke *Error
			if !errors.As(err, &ke) || ke.Code != "validation" {
				t.Fatal("39-rune assertion accepted", err)
			}
		}
		got, e := c.Comment(t.Context(), req)
		requireNoError(t, e)
		if got.UID == "" || got.ReplyToUID != root.UID || got.ReplyKind != kind {
			t.Fatal(got)
		}
		replay, e := c.Comment(t.Context(), req)
		requireNoError(t, e)
		if replay.UID != got.UID {
			t.Fatal(got, replay)
		}
		req.IdempotencyKey += "-duplicate"
		_, e = c.Comment(t.Context(), req)
		var ke *Error
		if !errors.As(e, &ke) || ke.Code != "duplicate_reply" || ke.ExistingUID != "" && ke.ExistingUID != got.UID {
			t.Fatal(e)
		}
	}
	// Operator notes exclude runtime comments by this read-back attribution.
	shown, e := c.ShowIssue(t.Context(), issue.UID)
	requireNoError(t, e)
	for _, comment := range shown.Comments {
		if comment.Author != "worker" || comment.Teammate != "herdr-kata" {
			t.Fatal("runtime comment attribution", comment)
		}
	}
	if len(shown.Comments) != 4 {
		t.Fatal(shown.Comments)
	}
}

// Kata's duplicate key covers the whole project, including replies published
// on another issue. Diagnostic handles are opaque and may be omitted.
func TestIntegDuplicateReplyAcrossIssues(t *testing.T) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	c := newTypedKata(t, bin, bin)
	first, err := c.CreateIssue(t.Context(), CreateReq{Title: "First finding", IdempotencyKey: "first"})
	requireNoError(t, err)
	other, err := c.CreateIssue(t.Context(), CreateReq{Title: "Other finding", IdempotencyKey: "other"})
	requireNoError(t, err)
	root, err := c.Comment(t.Context(), CommentReq{Ref: first.UID, Body: "Original claim", IdempotencyKey: "root"})
	requireNoError(t, err)
	req := CommentReq{Ref: first.UID, Body: "Continuation", ReplyTo: root.UID, Kind: "reply", IdempotencyKey: "reply"}
	existing, err := c.Comment(t.Context(), req)
	requireNoError(t, err)
	req.Ref, req.IdempotencyKey = other.UID, "cross-issue"
	_, err = c.Comment(t.Context(), req)
	var refusal *Error
	if !errors.As(err, &refusal) || refusal.Code != "duplicate_reply" || refusal.ExistingUID != "" && refusal.ExistingUID != existing.UID {
		t.Fatal(err)
	}
	unchanged, err := c.ShowIssue(t.Context(), other.UID)
	requireNoError(t, err)
	if len(unchanged.Comments) != 0 {
		t.Fatal("duplicate refusal appended a comment", unchanged.Comments)
	}
}
