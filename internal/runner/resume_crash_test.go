//go:build crashpoint

package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestCrashResumeAcceptance(t *testing.T) {
	for _, point := range []string{"resume-acceptance.save.before", "resume-acceptance.save.after"} {
		t.Run(point, func(t *testing.T) {
			f := heldResumeFixture(t)
			h := herdrtest.New(t)
			f.s.Transition(f.uid, func(r *Run) error {
				r.WFDef.Steps[0].Command = "echo resumed >> visits"
				r.PromptCap = 12
				r.PromptsSent = 3
				return nil
			})
			if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.Root, "due.json"), f.d); e != nil {
				t.Fatal(e)
			}
			if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.Root, "remote-issue.json"), kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open"}); e != nil {
				t.Fatal(e)
			}
			publishResume(t, f, Request{Version: 1, UID: "request", Kind: "resume", RunUID: f.uid})
			inspected := false
			e := crashWorker(t, f.s.layout.Root, h.Path, point, func() error {
				inspected = true
				r, err := f.s.Load(f.uid)
				if err != nil {
					return err
				}
				if point == "resume-acceptance.save.before" {
					if r.Resume != nil || r.Steps[0].Attempt != 1 || r.PromptsSent != 3 {
						return fmt.Errorf("resume changed before acceptance: %+v", r)
					}
				} else if r.Resume == nil || !r.Resume.Pending || r.Steps[0].Attempt != 2 || r.SlotReleased || r.PromptsSent != 0 {
					return fmt.Errorf("resume acceptance incomplete: %+v", r)
				}
				return nil
			})
			if !inspected {
				t.Fatal("acknowledged resume boundary was not inspected")
			}
			var exit *exec.ExitError
			marker, _ := os.ReadFile(filepath.Join(f.s.layout.Root, "crash-point"))
			if string(marker) != point || !errors.As(e, &exit) {
				t.Fatal("did not crash at resume acceptance", e, string(marker))
			}
			for range 2 {
				if e := crashWorker(t, f.s.layout.Root, h.Path, ""); e != nil {
					t.Fatal(e)
				}
			}
			r := loaded(t, f.s, f.uid)
			b, e := os.ReadFile(filepath.Join(r.Cwd, "visits"))
			if e != nil || string(b) != "resumed\n" || r.Steps[0].Attempt != 2 || r.Resume.Generation != 1 || len(r.ConsumedResumes) != 1 || r.Outcome != "succeeded" || r.PromptsSent != 0 || r.PromptCap != 12 {
				t.Fatal(r, string(b), e)
			}
		})
	}
}

func TestCrashPendingEdgeThenResume(t *testing.T) {
	f := newAdmission(t)
	h := herdrtest.New(t)
	f.d.WFDef.Steps = []defs.Step{{Key: "make", Kind: "command", Command: `echo make >> visits`}, {Key: "check", Kind: "command", After: []string{"make"}, Command: `echo check >> visits; [ "$(wc -l < visits)" -ge 4 ]`, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":2}}`)}}
	if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.Root, "due.json"), f.d); e != nil {
		t.Fatal(e)
	}
	if e := crashWorker(t, f.s.layout.Root, h.Path, "loop-edge.save.before"); e == nil {
		t.Fatal("did not crash")
	}
	if e := crashWorker(t, f.s.layout.Root, h.Path, ""); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Reason != "loop_pending" || r.Loop.Used != 0 {
		t.Fatal(r)
	}
	issue := crashIssue(f.s.layout.Root)
	issue.ProjectUID = "project"
	if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.Root, "remote-issue.json"), issue); e != nil {
		t.Fatal(e)
	}
	publishResume(t, f, Request{Version: 1, UID: "request", Kind: "resume", RunUID: f.uid})
	for range 2 {
		if e := crashWorker(t, f.s.layout.Root, h.Path, ""); e != nil {
			t.Fatal(e)
		}
	}
	r = loaded(t, f.s, f.uid)
	b, e := os.ReadFile(filepath.Join(r.Cwd, "visits"))
	if e != nil || string(b) != "make\ncheck\nmake\ncheck\n" || r.Loop.Used != 1 || r.Loop.ConsumedAttempt != 1 || r.Steps[0].Attempt != 2 || r.Steps[1].Attempt != 2 || r.Outcome != "succeeded" {
		t.Fatal(r, string(b), e)
	}
}
