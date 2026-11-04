package prompt

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"hegel.dev/go/hegel"
)

func TestNormalizeNoteExactBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"crlf-collapse", "a\r\nb\r c\n", "a b  c "},
		{"shortened-before-limit", strings.Repeat("\r\n", 141), strings.Repeat(" ", 141)},
		{"invalid-expansion", strings.Repeat("\xffa", 140), strings.Repeat("�a", 67) + " [truncated]"},
		{"four-byte-cut", strings.Repeat("🙂", 70) + "a", strings.Repeat("🙂", 67) + " [truncated]"},
		{"three-byte-cut", strings.Repeat("界", 94), strings.Repeat("界", 89) + " [truncated]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeNote(tc.raw); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeNoteAlwaysValid(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		raw := hegel.Draw(ht, hegel.Binary(0, -1))
		got := NormalizeNote(string(raw))
		if len(got) > 280 || !utf8.ValidString(got) || strings.ContainsAny(got, "\r\n") {
			ht.Fatalf("invalid normalized note %q from %q", got, raw)
		}
	}, hegel.WithTestCases(1000))
}

func TestNormalizeNotePreservesValidInput(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		note := hegel.Draw(ht, hegel.Text())
		if utf8.ValidString(note) && len(note) <= 280 && !strings.ContainsAny(note, "\r\n") {
			if got := NormalizeNote(note); got != note {
				ht.Fatalf("valid note changed: %q -> %q", note, got)
			}
		}
	}, hegel.WithTestCases(1000))
}

func TestNormalizeNoteIdempotent(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		note := NormalizeNote(string(hegel.Draw(ht, hegel.Binary(0, -1))))
		if got := NormalizeNote(note); got != note {
			ht.Fatalf("normalized note changed again: %q -> %q", note, got)
		}
	}, hegel.WithTestCases(1000))
}

func TestParseResultNoteBytesPreserveOutcome(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		note := hegel.Draw(ht, hegel.Text())
		status := hegel.Draw(ht, hegel.SampledFrom([]string{"succeeded", "failed", "needs-human"}))
		r := Result{RunUID: "run", Step: "main", Attempt: 1, Status: status, Summary: "Complete", Note: note}
		b, err := json.Marshal(r)
		if err != nil {
			ht.Fatal(err)
		}
		got, err := ParseResult(b, "run", "main", 1)
		if err != nil || got.Status != status || got.Summary != "Complete" || len(got.Note) > 280 || !utf8.ValidString(got.Note) || strings.ContainsAny(got.Note, "\r\n") {
			ht.Fatalf("optional note changed outcome: %+v %v", got, err)
		}
	}, hegel.WithTestCases(1000))
}

func TestWriteResultBadNotePreservesDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	r := Result{RunUID: "run", Step: "main", Attempt: 1, Status: "succeeded", Summary: "Complete", Note: "Original"}
	if err := WriteResult(path, r); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hegel.Test(t, func(ht *hegel.T) {
		r.Note = string(hegel.Draw(ht, hegel.Binary(0, -1)))
		if utf8.ValidString(r.Note) && len(r.Note) <= 280 && !strings.ContainsAny(r.Note, "\r\n") {
			return
		}
		if err := WriteResult(path, r); err == nil || !strings.Contains(err.Error(), "invalid_note") {
			ht.Fatalf("strict writer accepted bad note: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			ht.Fatalf("rejected note replaced destination: %q %v", got, err)
		}
	}, hegel.WithTestCases(1000))
}
