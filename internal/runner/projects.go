package runner

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

var ErrAmbiguousProject = errors.New("ambiguous job")
var ErrJobUnavailable = errors.New("unavailable in project snapshots")

// ResolveJob uses only lane snapshots. An ambiguous or unknown project never
// falls back to a CLI selected by the worker's environment.
func ResolveJob(c config.Config, f state.FS, l state.Layout, ref, project string) (string, string, error) {
	layouts, e := state.ProjectLayouts(f, l)
	if e != nil {
		return "", "", e
	}
	configured := map[string]bool{}
	for _, entry := range c.ProjectEntries() {
		configured[entry.Name] = true
	}
	type candidate struct {
		layout    state.Layout
		name, uid string
	}
	candidates := []candidate{}
	bound := map[string]string{}
	conflicting := map[string]bool{}
	for _, pl := range layouts {
		var pin kata.Installation
		if e = state.ReadJSON(f, pl.StateFile(), 65536, &pin); e != nil {
			return "", "", e
		}
		name := pin.Name
		if name == "" && len(c.ProjectEntries()) == 1 {
			name = c.ProjectEntries()[0].Name
		}
		if !configured[name] {
			continue
		}
		candidates = append(candidates, candidate{pl, name, pin.ProjectUID})
		var route map[string]any
		e = state.ReadJSON(f, pl.RouteFile(), 65536, &route)
		if e != nil && !errors.Is(e, fs.ErrNotExist) {
			return "", "", e
		}
		// Startup marks old pins waiting. Only a checked binding for the same
		// entry can prove that an older UID was superseded; an unavailable entry
		// still participates in name ambiguity checks.
		if e == nil && route["project"] == name && route["allowed"] == true {
			if previous := bound[name]; previous != "" && previous != pin.ProjectUID {
				conflicting[name] = true
			} else {
				bound[name] = pin.ProjectUID
			}
		}
	}
	if project != "" && !configured[project] {
		known := false
		for _, candidate := range candidates {
			known = known || candidate.uid == project
		}
		if !known {
			return "", "", fmt.Errorf("unknown project %q", project)
		}
	}
	var uid, job string
	complete := map[string]bool{}
	matchedUID := false
	for _, candidate := range candidates {
		if current := bound[candidate.name]; current != "" && !conflicting[candidate.name] && current != candidate.uid {
			continue
		}
		if project != "" && project != candidate.name && project != candidate.uid {
			continue
		}
		snapshot, err := defs.ReadSnapshot(f, candidate.layout.DefsSnapshot())
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		complete[candidate.name] = true
		for _, entry := range snapshot.Jobs {
			if entry.UID != ref && entry.Name != ref {
				continue
			}
			if job != "" {
				return "", "", fmt.Errorf("%w %q; select --project", ErrAmbiguousProject, ref)
			}
			uid, job = candidate.uid, entry.UID
			matchedUID = entry.UID == ref
		}
	}
	if job != "" && project == "" && !matchedUID {
		for name := range configured {
			if !complete[name] {
				return "", "", fmt.Errorf("project snapshots unavailable; select --project")
			}
		}
	}

	if job == "" {
		return "", "", fmt.Errorf("job %q %w", ref, ErrJobUnavailable)
	}
	return uid, job, nil
}

// The worker publishes the scheduler after lane recovery. Controls must wait
// for that publication before starting an execution through the dispatcher.
func (m *Manager) SetScheduler(s ManualScheduler, wake ...func()) {
	m.lanesMu.Lock()
	defer m.lanesMu.Unlock()
	m.scheduler = s
	m.wake = nil
	if len(wake) > 0 {
		m.wake = wake[0]
	} else if p, ok := s.(interface{ Poke() }); ok {
		m.wake = p.Poke
	}
}

// WakeProjects lets every lane reconsider work after shared capacity or
// operator requests change. Callbacks run outside the registry lock.
func (m *Manager) WakeProjects() {
	m.lanesMu.RLock()
	var wake []func()
	if m.projects {
		for _, lane := range m.lanes {
			if lane.wake != nil {
				wake = append(wake, lane.wake)
			}
		}
	} else if m.wake != nil {
		wake = append(wake, m.wake)
	}
	m.lanesMu.RUnlock()
	for _, poke := range wake {
		poke()
	}
}
func (m *Manager) projectScheduler() ManualScheduler {
	m.lanesMu.RLock()
	defer m.lanesMu.RUnlock()
	return m.scheduler
}
