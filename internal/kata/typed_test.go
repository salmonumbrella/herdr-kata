package kata

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
)

func TestTypedRepliesDetectionIsReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name, help, health string
		exit               int
		want               bool
	}{
		{"supported", "--reply --confirm --refute", `{"api_schema_version":"0.28.0"}`, 0, true},
		{"newer", "--reply --confirm --refute", `{"api_schema_version":"1.0.0+build"}`, 0, true},
		{"suffix", "--reply --confirm --refute", `{"api_schema_version":"0.28.0-dev"}`, 0, true},
		{"old cli", "--body-stdin", `{}`, 0, false},
		{"missing flag", "--reply --confirm", `{}`, 0, false},
		{"old daemon", "--reply --confirm --refute", `{"api_schema_version":"0.27.0"}`, 0, false},
		{"absent version", "--reply --confirm --refute", `{}`, 0, false},
		{"bad version", "--reply --confirm --refute", `{"api_schema_version":"0.28.bad"}`, 0, false},
		{"probe error", "--reply --confirm --refute", `{}`, 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			c := New(config.Kata{Bin: "kata", Project: "example-project", Actor: "worker"}, t.TempDir(), func(_ context.Context, argv, _ []string, _ string, stdin []byte) ([]byte, []byte, int, error) {
				args := argv[8:]
				calls = append(calls, strings.Join(args, " "))
				if len(stdin) != 0 {
					t.Fatal("probe supplied mutation input")
				}
				switch strings.Join(args, " ") {
				case "comment --help":
					return []byte(tc.help), nil, tc.exit, nil
				case "health":
					return []byte(tc.health), nil, tc.exit, nil
				default:
					t.Fatalf("unexpected probe %v", args)
					return nil, nil, 1, nil
				}
			}, nil)
			got, reason := c.TypedReplies(t.Context())
			if got != tc.want || !got && reason == "" {
				t.Fatal(got, reason)
			}
			want := []string{"comment --help"}
			if tc.exit == 0 && strings.Contains(tc.help, "--refute") {
				want = append(want, "health")
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatal(calls, want)
			}
		})
	}
}

func TestTypedCommentArguments(t *testing.T) {
	for _, kind := range []string{"", "reply", "confirm", "refute"} {
		t.Run(kind, func(t *testing.T) {
			c := fakeCLI(t, `{"comment":{"uid":"saved","reply_to_uid":"01ARZ3NDEKTSV4RRFFQ69G5FAV","reply_kind":"`+kind+`"}}`, func(_ context.Context, argv, _ []string, _ string, body []byte) {
				want := []string{"comment", "--body-stdin", "--idempotency-key", "key"}
				if kind != "" {
					want = append(want, "--"+kind, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
				}
				want = append(want, "--", "issue")
				if !reflect.DeepEqual(argv[10:], want) || string(body) != "evidence" {
					t.Fatal(argv, string(body))
				}
			})
			req := CommentReq{Ref: "issue", Body: "evidence", IdempotencyKey: "key"}
			if kind != "" {
				req.ReplyTo = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
				req.Kind = kind
			}
			got, e := c.Comment(t.Context(), req)
			if e != nil || got.UID != "saved" || got.ReplyKind != kind {
				t.Fatal(got, e)
			}
		})
	}
}

func TestDuplicateReplyDetails(t *testing.T) {
	err := decodeError([]byte(`{"error":{"code":"duplicate_reply","data":{"existing_uid":"existing","existing_handle":"c:abcdef"}}}`), nil, 5, nil)
	e := err.(*Error)
	if e.ExistingUID != "existing" || e.ExistingHandle != "c:abcdef" || Class(e) != Conflict {
		t.Fatal(e)
	}
}
