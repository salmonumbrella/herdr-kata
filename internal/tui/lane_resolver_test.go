package tui

import (
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func TestActionLaneResolution(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		embedded, legacy, removed, unknown bool
		laneUID, resultUID, resultName     string
		factory, missingService, want      bool
	}{
		{name: "configured", want: true},
		{name: "embedded", embedded: true, laneUID: "project", want: true},
		{name: "health pin", legacy: true, want: true},
		{name: "foreign embedded pin", embedded: true, laneUID: "other"},
		{name: "foreign configured pin", laneUID: "other"},
		{name: "removed snapshot lane", removed: true, factory: true},
		{name: "unconfigured lane", unknown: true, factory: true},
		{name: "factory", factory: true, want: true},
		{name: "factory foreign uid", factory: true, resultUID: "other"},
		{name: "factory foreign name", factory: true, resultName: "other"},
		{name: "factory missing service", factory: true, missingService: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeActions{}
			lane := Lane{ProjectUID: tc.laneUID, Project: "example-project", Service: svc}
			opts := Options{Root: t.TempDir()}
			if tc.embedded {
				opts.Lane = lane
			} else {
				opts.Projects = map[string]*Lane{"example-project": &lane}
			}
			if tc.factory {
				opts.ForProject = func(uid, name string) Lane {
					result := Lane{ProjectUID: uid, Project: name, Service: svc}
					if tc.resultUID != "" {
						result.ProjectUID = tc.resultUID
					}
					if tc.resultName != "" {
						result.Project = tc.resultName
					}
					if tc.missingService {
						result.Service = nil
					}
					return result
				}
			}
			m := newModel(opts)
			m.data = Data{Projects: map[string]Data{"project": {Project: "example-project"}}}
			if tc.legacy {
				m.data = Data{Health: map[string]any{"version": float64(1), "project_uid": "project"}}
			}
			if tc.removed {
				m.data.Projects = map[string]Data{"replacement": {Project: "example-project"}}
			}
			if tc.unknown {
				m.data.Projects["project"] = Data{Project: "unconfigured"}
			}
			m.data.Jobs = []defs.JobEntry{{UID: "job", ProjectUID: "project", Project: "example-project"}}
			m.data.Workflows = []kata.Workflow{{UID: "workflow", ProjectUID: "project"}}
			runEntry := runner.Run{ProjectName: "historical-project", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}}
			m.data.Runs = []runner.Run{runEntry}
			job, err := m.jobActionOptions(&jobView{key: jobKey{"project", "job"}, entry: m.data.Jobs[0]})
			run, runOK := m.runOptions(runEntry)
			workflow, workflowOK := m.workflowOptions("project", "")
			rowLane := m.optionsForRow(row{kind: "job", projectUID: "project", uid: "job", job: 0}, true)
			runRowLane := m.optionsForRow(row{kind: "run", projectUID: "project", run: 0}, true)
			for _, got := range []struct {
				name string
				lane Lane
				ok   bool
			}{
				{"job", job, err == nil}, {"run", run, runOK}, {"workflow", workflow, workflowOK}, {"row", rowLane, rowLane.Service != nil}, {"run row", runRowLane, runRowLane.Service != nil},
			} {
				if got.ok != tc.want {
					t.Errorf("%s availability=%v want=%v lane=%+v", got.name, got.ok, tc.want, got.lane)
				}
				if tc.want && (got.lane.ProjectUID != "project" || got.lane.Project != "example-project" || got.lane.Service != svc) {
					t.Errorf("%s retargeted: %+v", got.name, got.lane)
				}
			}
		})
	}
}
