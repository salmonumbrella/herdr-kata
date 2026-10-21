package daemon

import (
	"fmt"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestProjectStatusUsesSharedProcessLocks(t *testing.T) {
	for _, processes := range []struct{ running, worker bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("%t-%t", processes.running, processes.worker), func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			f := state.OSFS{}
			if err := state.InitializeLayout(f, l); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"project-a", "project-b"} {
				if err := state.WriteJSON(f, l.Project(name).StateFile(), kata.Installation{Version: 1, Name: name, ProjectUID: name, CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			for _, lock := range []struct {
				held bool
				path string
			}{{processes.running, l.ServeLock()}, {processes.worker, l.WorkerLock()}} {
				if !lock.held {
					continue
				}
				held, ok, err := state.TryLock(lock.path)
				if err != nil || !ok {
					t.Fatal(ok, err)
				}
				defer held.Unlock()
			}
			s, err := ReadStatus(l, config.Config{Projects: []config.Project{{Name: "project-a"}, {Name: "project-b"}, {Name: "waiting-project"}}})
			if err != nil {
				t.Fatal(err)
			}
			if s.Running != processes.running || s.WorkerRunning != processes.worker || len(s.Projects) != 3 {
				t.Fatalf("shared status: %+v", s)
			}
			for _, lane := range s.Projects {
				if lane.Running != processes.running || lane.WorkerRunning != processes.worker {
					t.Fatalf("project %s contradicts shared process locks: %+v", lane.Project, lane)
				}
			}
		})
	}
}
