package daemon

import (
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type Status struct {
	AbandonReports []runner.AbandonReportProblem `json:"abandon_reports,omitempty"`
	ControlResults []runner.RequestResult        `json:"control_results,omitempty"`
	Project        string                        `json:"project,omitempty"`
	ProjectUID     string                        `json:"project_uid,omitempty"`
	Projects       []Status                      `json:"projects,omitempty"`
	Inbox          *inbox.Snapshot               `json:"inbox,omitempty"`
	NextFires      map[string]time.Time          `json:"next_fires,omitempty"`
	Running        bool                          `json:"running"`
	WorkerRunning  bool                          `json:"worker_running"`
	Serve          *ServeInfo                    `json:"serve,omitempty"`
	Definitions    []defs.JobEntry               `json:"definitions"`
	Route          *RouteStatus                  `json:"route,omitempty"`
	Lease          *LeaseStatus                  `json:"lease,omitempty"`
	Jobs           []map[string]any              `json:"jobs,omitempty"`
	Runs           []map[string]any              `json:"runs,omitempty"`
}
type RouteStatus = kata.RouteStatus

func heldExisting(p string) (bool, error) {
	if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return state.Held(p)
}
func readRuntime(l state.Layout) (Status, error) {
	s := Status{Definitions: []defs.JobEntry{}}
	var err error
	s.AbandonReports, err = runner.AbandonReportProblems(state.OSFS{}, l)
	if err != nil {
		return s, err
	}
	s.Running, err = heldExisting(l.ServeLock())
	if err != nil {
		return s, err
	}
	s.WorkerRunning, err = heldExisting(l.WorkerLock())
	if err != nil {
		return s, err
	}
	var info ServeInfo
	if err = state.ReadJSON(state.OSFS{}, l.ServeInfo(), 4096, &info); err == nil {
		s.Serve = &info
	} else if !errors.Is(err, fs.ErrNotExist) {
		return s, err
	}
	entries, err := os.ReadDir(filepath.Join(l.Root, "runs"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return s, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var row map[string]any
		if err := state.ReadJSON(state.OSFS{}, l.RunFile(entry.Name()), 4<<20, &row); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return s, err
		}
		s.Runs = append(s.Runs, row)
	}
	return s, nil
}

func readLane(l state.Layout, installation kata.Installation) (Status, error) {
	s := Status{Definitions: []defs.JobEntry{}, NextFires: map[string]time.Time{}}
	if _, err := os.Lstat(filepath.Join(l.Root, "inbox.json")); err == nil {
		snapshot, err := inbox.ReadSnapshot(state.OSFS{}, l.Root)
		if err != nil {
			return s, err
		}
		s.Inbox = &snapshot
	} else if !errors.Is(err, fs.ErrNotExist) {
		return s, err
	}
	snapshot, err := defs.ReadSnapshot(state.OSFS{}, l.DefsSnapshot())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return s, err
	}
	if snapshot.Jobs != nil {
		s.Definitions = snapshot.Jobs
	}

	var route RouteStatus
	if err = state.ReadJSON(state.OSFS{}, l.RouteFile(), 64<<10, &route); err == nil {
		s.Route = &route
	} else if !errors.Is(err, fs.ErrNotExist) {
		return s, err
	}
	var held LeaseStatus
	if err = state.ReadJSON(state.OSFS{}, leaseFile(l), 64<<10, &held); err == nil {
		s.Lease = &held
	} else if !errors.Is(err, fs.ErrNotExist) {
		return s, err
	}
	entries, err := os.ReadDir(filepath.Join(l.Root, "jobs"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return s, err
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var row map[string]any
		if err := state.ReadJSON(state.OSFS{}, filepath.Join(l.Root, "jobs", entry.Name()), 4<<20, &row); err != nil {
			return s, err
		}
		s.Jobs = append(s.Jobs, row)
	}
	for _, entry := range s.Definitions {
		if !entry.Supported || entry.Owner != defs.Adapter || entry.Trigger == nil || slices.Contains(entry.Warnings, defs.AutomaticAuthorWarning) {
			continue
		}
		through := installation.CreatedAt
		if entry.CreatedAt.After(through) {
			through = entry.CreatedAt
		}
		for _, job := range s.Jobs {
			if job["job_uid"] == entry.UID {
				if text, ok := job["handled_through"].(string); ok {
					parsed, err := time.Parse(time.RFC3339Nano, text)
					if err != nil {
						return s, err
					}
					through = parsed
				}
			}
		}
		schedule, err := sched.Compile(*entry.Trigger, entry.CreatedAt)
		if err != nil {
			return s, err
		}
		if next := schedule.Next(through); !next.IsZero() {
			s.NextFires[entry.UID] = next
		}
	}
	return s, nil
}

// ReadStatus reads every lane only after the layout marker is complete.
func ReadStatus(l state.Layout, configs ...config.Config) (Status, error) {
	layouts, e := state.ProjectLayouts(state.OSFS{}, l)
	if e != nil {
		return Status{}, e
	}
	s, e := readRuntime(l)
	if e != nil {
		return s, e
	}
	lanesByUID := map[string]int{}
	names := map[string]bool{}
	signingRefused := map[string]bool{}
	removed := map[string]bool{}
	for _, pl := range layouts {
		var pin kata.Installation
		if e = state.ReadJSON(state.OSFS{}, pl.StateFile(), 65536, &pin); e != nil {
			return s, e
		}
		if len(configs) > 0 && !configuredLane(configs[0], pin) {
			removed[pin.ProjectUID] = true
			continue
		}
		lane, e := readLane(pl, pin)
		if e != nil {
			return s, e
		}
		lane.Running, lane.WorkerRunning = s.Running, s.WorkerRunning
		lane.ProjectUID = pin.ProjectUID
		lane.Project = pin.Name
		if lane.Project == "" {
			lane.Project = pin.ProjectUID
		}
		lanesByUID[pin.ProjectUID] = len(s.Projects)
		names[lane.Project] = true
		for i := range lane.Definitions {
			lane.Definitions[i].Project = lane.Project
			s.Definitions = append(s.Definitions, lane.Definitions[i])
		}
		for _, row := range lane.Jobs {
			row["project"] = lane.Project
			s.Jobs = append(s.Jobs, row)
		}
		s.Projects = append(s.Projects, lane)
	}
	visibleRuns := s.Runs[:0]
	for _, row := range s.Runs {
		target, _ := row["Target"].(map[string]any)
		identity, _ := target["Identity"].(map[string]any)
		uid, _ := identity["project_uid"].(string)
		if removed[uid] {
			continue
		}
		visibleRuns = append(visibleRuns, row)
		index, found := lanesByUID[uid]
		if !found {
			row["project"] = uid
			row["waiting_for_project"] = true
			row["Reason"] = "waiting for project " + uid
			continue
		}
		lane := &s.Projects[index]
		row["project"] = lane.Project
		lane.Runs = append(lane.Runs, row)
		if ops, ok := row["Ops"].([]any); ok {
			for _, raw := range ops {
				op, _ := raw.(map[string]any)
				if op["signing_refused"] == true {
					signingRefused[uid] = true
					break
				}
			}
		}
	}
	s.Runs = visibleRuns
	if len(configs) > 0 {
		for _, entry := range configs[0].ProjectEntries() {
			if !names[entry.Name] {
				s.Projects = append(s.Projects, Status{Project: entry.Name, Running: s.Running, WorkerRunning: s.WorkerRunning})
				names[entry.Name] = true
			}
		}
	}
	for i := range s.Projects {
		lane := &s.Projects[i]
		lane.Route = deriveLaneRoute(*lane, signingRefused[lane.ProjectUID])
		if lane.Route != nil && strings.HasPrefix(lane.Route.Error, "waiting for project ") {
			for _, row := range lane.Runs {
				row["waiting_for_project"] = true
				row["Reason"] = lane.Route.Error
			}
		}
	}
	startup, e := readStartupErrors(l)
	if e != nil {
		return s, e
	}
	for i := range s.Projects {
		lane := &s.Projects[i]
		if reason := startup[lane.Project]; reason != "" {
			lane.Route = &RouteStatus{Project: lane.Project, Allowed: false, Error: reason}
		}
	}
	// Keep the aggregate fields useful for single-project clients.
	if len(s.Projects) == 1 {
		lane := s.Projects[0]
		s.Route = lane.Route
		s.Inbox = lane.Inbox
		s.NextFires = lane.NextFires
	}
	return s, nil
}

// Derive the route before decorating any runs. Startup failures override it later.
func deriveLaneRoute(lane Status, signingRefused bool) *RouteStatus {
	if signingRefused {
		route := RouteStatus{Project: lane.Project, Allowed: true}
		if lane.Route != nil {
			route = *lane.Route
		}
		route.SigningBlocked = true
		route.Error = "signing_blocked: root owner: kata federation root-key repin --project " + lane.Project
		return &route
	}
	if lane.Route != nil {
		return lane.Route
	}
	if lane.ProjectUID == "" {
		return &RouteStatus{Project: lane.Project, Allowed: false, Error: "waiting for project " + lane.Project + "; run doctor for route details"}
	}
	return nil
}
