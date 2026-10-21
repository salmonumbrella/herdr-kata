//go:build crashpoint

package runner

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestCrashLoopWorkflowTransition(t *testing.T) {
	for _, point := range []string{"loop-edge.save.before", "loop-edge.save.after"} {
		t.Run(point, func(t *testing.T) {
			f := newAdmission(t)
			f.d.WFDef.Steps = []defs.Step{{Key: "make", Kind: "command", Command: `echo make >> visits`}, {Key: "check", Kind: "command", After: []string{"make"}, Command: `echo check >> visits; [ "$(wc -l < visits)" -ge 4 ]`, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":2}}`)}}
			h := herdrtest.New(t)
			if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.Root, "due.json"), f.d); e != nil {
				t.Fatal(e)
			}
			e := crashWorker(t, f.s.layout.Root, h.Path, point)
			var exit *exec.ExitError
			marker, _ := os.ReadFile(filepath.Join(f.s.layout.Root, "crash-point"))
			if string(marker) != point || !errors.As(e, &exit) {
				t.Fatal("did not crash at edge", e, string(marker))
			}
			r := loaded(t, f.s, f.uid)
			if point == "loop-edge.save.before" {
				if r.Loop.Used != 0 || r.Steps[0].Attempt != 1 {
					t.Fatal(r)
				}
			} else if r.Loop.Used != 1 || r.Steps[0].Attempt != 2 || r.Steps[1].Attempt != 2 {
				t.Fatal(r)
			}
			for range 2 {
				if e := crashWorker(t, f.s.layout.Root, h.Path, ""); e != nil {
					t.Fatal(e)
				}
			}
			r = loaded(t, f.s, f.uid)
			b, e := os.ReadFile(filepath.Join(r.Cwd, "visits"))
			if e != nil {
				t.Fatal(e)
			}
			if point == "loop-edge.save.before" {
				if r.Phase != Held || r.Reason != "loop_pending" || string(b) != "make\ncheck\n" || r.Loop.Used != 0 {
					t.Fatal(r, string(b))
				}
			} else if r.Outcome != "succeeded" || r.Loop.Used != 1 || string(b) != "make\ncheck\nmake\ncheck\n" {
				t.Fatal(r, string(b))
			}
		})
	}
}
