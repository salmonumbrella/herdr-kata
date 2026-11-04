package attempt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/prompt"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestCommandNoteBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"shrunk-invalid-byte", "\x80", "�"},
		{"invalid-run", "ok\xff\xfeend", "ok�end"},
		{"limit-lf", strings.Repeat("a", 280) + "\n", strings.Repeat("a", 280)},
		{"limit-crlf", strings.Repeat("a", 280) + "\r\n", strings.Repeat("a", 280)},
		{"terminal-cr", "ok\r", "ok"},
		{"overflow", strings.Repeat("a", 281), strings.Repeat("a", 268) + " [truncated]"},
		{"rune-cut", strings.Repeat("a", 267) + "é" + strings.Repeat("z", 12), strings.Repeat("a", 267) + " [truncated]"},
		{"clipped-crlf", strings.Repeat("\r\n", 200), strings.Repeat(" ", 142) + " [truncated]"},
		{"complete-282", strings.Repeat("\r\n", 141), strings.Repeat(" ", 140)},
		{"clipped-283", strings.Repeat("\r\n", 141) + "\r", strings.Repeat(" ", 142) + " [truncated]"},
		{"clipped-rune", strings.Repeat("a", 281) + "🙂", strings.Repeat("a", 268) + " [truncated]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, f, r := command(t, 0)
			if err := os.WriteFile(filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "note.txt"), []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			out := advance(t, st, f, r, now.Add(time.Second))
			if d, ok := out.(Done); !ok || d.Status != "succeeded" || d.Note != tc.want {
				t.Fatalf("got %#v want %q", out, tc.want)
			}
		})
	}
}

func TestCommandNoteFrozenBeforeCleanupRetry(t *testing.T) {
	st, f, r := command(t, 0)
	path := filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "note.txt")
	if err := os.WriteFile(path, []byte("ready\ncontinue"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	failure := errors.New("temporary core read failure")
	f.onGet = func(target string) (hc.Terminal, error) {
		calls++
		if calls == 2 {
			return hc.Terminal{}, failure
		}
		return f.terms[target], nil
	}
	if _, err := Settle(context.Background(), f, st, r.Key, 0, now); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	saved, err := st.Load(r.Key)
	if err != nil || saved.Attempts[0].SettlementNote != "ready continue" || saved.Attempts[0].SettlementStatus != "succeeded" || len(f.calls) != 0 {
		t.Fatalf("cleanup attempted without saved settlement: %+v %v", saved, err)
	}
	f.onGet = nil
	if err := os.WriteFile(path, []byte("changed after settlement"), 0600); err != nil {
		t.Fatal(err)
	}
	if out := advance(t, st, f, r, now); out != (Done{"succeeded", "ready continue"}) || len(f.terms) != 0 {
		t.Fatalf("cleanup reread optional note: %v terms=%v", out, f.terms)
	}
}

func TestCommandUnreadableOptionalNote(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "dangling-symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			st, f, r := command(t, 0)
			path := filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "note.txt")
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(path, 0700)
			case "symlink", "dangling-symlink":
				target := filepath.Join(t.TempDir(), "target")
				if kind == "symlink" {
					if err := os.WriteFile(target, []byte("do not read"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				err = os.Symlink(target, path)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			out := advance(t, st, f, r, now.Add(time.Second))
			if d, ok := out.(Done); !ok || d.Status != "succeeded" || d.Note != "[unreadable note]" {
				t.Fatalf("optional %s changed known outcome: %v", kind, out)
			}
		})
	}
}

func TestBadAgentNotePreservesResult(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "needs-human"} {
		t.Run(status, func(t *testing.T) {
			st, f, r := setup(t, "prompt")
			path := filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "result.json")
			if err := state.WriteJSON(path, prompt.Result{RunUID: r.RunUID, Step: "main", Attempt: 1, Status: status, Summary: "Complete", Note: "two\nlines"}); err != nil {
				t.Fatal(err)
			}
			term := f.terms["t1"]
			term.State, term.Reason = "finished", "exited"
			f.terms["t1"] = term
			out := advance(t, st, f, r, now)
			if d, ok := out.(Done); !ok || d.Status != status || d.Note != "two lines" {
				t.Fatal(out)
			}
			saved, err := st.Load(r.Key)
			if err != nil || saved.Attempts[0].Summary != "Complete" || saved.Attempts[0].Hold != "" {
				t.Fatal(saved, err)
			}
		})
	}
}

func TestRawAgentNoteChangeResetsStability(t *testing.T) {
	st, f, r := setup(t, "prompt")
	path := filepath.Join(st.Layout.Attempt(r.Key, "main", 1), "result.json")
	write := func(tail string) {
		t.Helper()
		if err := state.WriteJSON(path, prompt.Result{RunUID: r.RunUID, Step: "main", Attempt: 1, Status: "succeeded", Summary: "Complete", Note: strings.Repeat("a", 300) + tail}); err != nil {
			t.Fatal(err)
		}
	}
	write("first")
	advance(t, st, f, r, now)
	write("second")
	advance(t, st, f, r, now.Add(29*time.Second))
	if out := advance(t, st, f, r, now.Add(30*time.Second)); out != (Pending{}) || len(f.calls) != 0 {
		t.Fatalf("changing raw result cleaned up early: %v %v", out, f.calls)
	}
	out := advance(t, st, f, r, now.Add(59*time.Second))
	if d, ok := out.(Done); !ok || d.Status != "succeeded" || d.Note != strings.Repeat("a", 268)+" [truncated]" {
		t.Fatalf("stable bad note did not settle: %v", out)
	}
}

func TestLegacyInvalidNoteHoldRequiresRelease(t *testing.T) {
	st, f, r := command(t, 0)
	if err := st.Update(r.Key, func(r *journal.Run) error {
		r.Attempts[0].Status, r.Attempts[0].Hold = "held", "invalid_note"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if out := advance(t, st, f, r, now); out != (Held{"invalid_note"}) || len(f.calls) != 0 {
		t.Fatalf("existing hold changed: %v %v", out, f.calls)
	}
}
