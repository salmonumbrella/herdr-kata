package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// LocalChecks reads journals without using Store.Load, which may quarantine
// corrupt state. Inspection must work even while Kata is inaccessible.
func LocalChecks(fsys state.FS, l state.Layout, configs ...config.Config) ([]CheckResult, error) {
	var checks []CheckResult
	results := filepath.Join(l.Root, "request-results")
	entries, err := fsys.ReadDir(results)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w; restore read and search permissions on %s; rerun herdr-kata doctor", err, results)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var result runner.RequestResult
		if err := state.ReadJSON(fsys, filepath.Join(results, entry.Name()), 64<<10, &result); err != nil {
			remedy := "preserve the state directory and inspect this result before attempting repair"
			if _, corrupt := errors.AsType[*state.CorruptError](err); corrupt {
				remedy = "this file may require a newer build; use the current build for status and controls; " + remedy
			} else if errors.Is(err, fs.ErrPermission) {
				remedy = "restore read and search permissions for the runtime user; " + remedy
			}
			checks = append(checks, CheckResult{Name: "control result " + strings.TrimSuffix(entry.Name(), ".json"), Detail: fmt.Sprintf("%v; %s", err, remedy)})
		}
	}
	reports, err := runner.AbandonReportProblems(fsys, l)
	if err != nil {
		return nil, fmt.Errorf("%w; restore read and search permissions under %s; rerun herdr-kata doctor", err, filepath.Join(l.Root, "abandoned"))
	}
	for _, report := range reports {
		checks = append(checks, CheckResult{Name: "abandon report " + report.RunUID, Detail: report.Detail})
	}
	for _, root := range []string{filepath.Join(l.Root, "runs"), filepath.Join(l.Root, "quarantine", "runs")} {
		entries, err := fsys.ReadDir(root)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return checks, fmt.Errorf("%w; restore read and search permissions on %s for the runtime user; rerun herdr-kata doctor", err, root)
		}
		quarantined := root != filepath.Join(l.Root, "runs")
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			remedy := "; inspect the preserved artifacts and stop any original occupants; herdr-kata stop; herdr-kata abandon " + entry.Name() + " --local acknowledges quarantine without retrying execution"
			if quarantined {
				var ack struct {
					Acknowledged bool `json:"acknowledged"`
				}
				if err := state.ReadJSON(fsys, filepath.Join(root, entry.Name(), "acknowledged.json"), 4096, &ack); err == nil && ack.Acknowledged {
					continue
				}
			}
			files, err := fsys.ReadDir(filepath.Join(root, entry.Name()))
			if err != nil {
				return checks, fmt.Errorf("%w; restore read and search permissions on %s for the runtime user; rerun herdr-kata doctor", err, root)
			}
			for _, file := range files {
				if file.IsDir() || !(file.Name() == "run.json" || quarantined && strings.HasSuffix(file.Name(), "-run.json")) {
					continue
				}
				var r runner.Run
				err := state.ReadJSON(fsys, filepath.Join(root, entry.Name(), file.Name()), 4<<20, &r)
				if err != nil {
					checks = append(checks, CheckResult{Name: "local run " + entry.Name(), Detail: err.Error() + remedy})
					continue
				}
				environmentHeld := r.Phase == runner.Held && r.Reason == "herdr_env_unclean"
				if len(configs) > 0 && state.SafeComponent(r.Target.Identity.ProjectUID) {
					var pin kata.Installation
					if err := state.ReadJSON(fsys, l.Project(r.Target.Identity.ProjectUID).StateFile(), 64<<10, &pin); err == nil && !configs[0].HasProject(pin.Name) {
						continue
					}
				}
				blocked := environmentHeld || r.Reason == "create_rejected" || r.Reason == "admission_rejected"
				var ops []string
				signing := false
				for _, op := range r.Ops {
					if op.SigningRefused {
						blocked = true
						signing = true
					}
					if !op.Done || op.SigningRefused {
						status := "unsent"
						if op.FirstSent != nil {
							status = "in-flight"
						}
						if op.Done {
							status = "retired"
						}
						if op.SigningRefused {
							status += " signing_refused"
						}
						ops = append(ops, op.ID+" ("+status+")")
					}
				}
				if !blocked && !quarantined {
					continue
				}
				issue := "no issue created"
				if r.IssueRef != "" && r.IssueUID != "" {
					issue = fmt.Sprintf("issue %s (%s)", r.IssueRef, r.IssueUID)
				} else if r.IssueRef != "" {
					issue = "issue " + r.IssueRef
				} else if r.IssueUID != "" {
					issue = "issue " + r.IssueUID
				}
				detail := fmt.Sprintf("%s: %s; %s; unresolved: %s", entry.Name(), r.Reason, issue, strings.Join(ops, ", "))
				if r.Reason == "admission_rejected" && r.Inspect != "" {
					detail += "; " + r.Inspect
				}
				if signing {
					project := r.Target.Identity.ProjectUID
					if state.SafeComponent(project) {
						var pin kata.Installation
						if e := state.ReadJSON(fsys, l.Project(project).StateFile(), 65536, &pin); e == nil && pin.Name != "" {
							project = pin.Name
						}
					} else {
						project = "<name>"
					}
					detail += "; root owner: kata federation root-key repin --project " + project
				}
				if quarantined {
					detail = "quarantined " + detail + remedy
				} else if r.Reason == "create_rejected" {
					detail += "; " + r.Inspect + "; herdr-kata release " + r.UID + " or herdr-kata stop; herdr-kata abandon " + r.UID + " --local"
				} else if environmentHeld && !signing {
					for _, step := range r.Steps {
						if step.Key == r.ActiveStep && step.Reason != "" {
							detail += "; " + step.Reason
						}
					}
					detail += "; repair Herdr's launch environment; herdr-kata resume " + r.UID
				} else {
					detail += "; herdr-kata stop; herdr-kata abandon " + entry.Name() + " --local"
				}
				checks = append(checks, CheckResult{Name: "local run " + entry.Name(), Detail: detail})
			}
		}
	}
	return checks, nil
}
