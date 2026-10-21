package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func definitionChecks(fsys state.FS, root state.Layout, layouts []state.Layout, c config.Config, now time.Time) []CheckResult {
	var checks []CheckResult
	seen := map[string]bool{}
	for _, lane := range layouts {
		var pin kata.Installation
		err := state.ReadJSON(fsys, lane.StateFile(), 64<<10, &pin)
		project := pin.Name
		if err == nil && !c.HasProject(pin.Name) {
			continue
		}
		if project == "" {
			project = pin.ProjectUID
		}
		if project == "" {
			project = filepath.Base(lane.Root)
		}
		seen[project] = true
		if err != nil {
			checks = append(checks, CheckResult{Name: "definitions " + project, Detail: "cannot read lane pin: " + state.RepairError(err, root).Error()})
			continue
		}
		checks = append(checks, definitionSnapshot(fsys, lane, project, now)...)
	}
	for _, entry := range c.ProjectEntries() {
		if !seen[entry.Name] {
			checks = append(checks, CheckResult{Name: "definitions " + entry.Name, Passed: true, Detail: "snapshot missing; project not initialized yet; run herdr-kata up to initialize snapshots under " + filepath.Join(root.Root, "projects")})
		}
	}
	return checks
}

func definitionSnapshot(fsys state.FS, lane state.Layout, project string, now time.Time) []CheckResult {
	check := CheckResult{Name: "definitions " + project}
	snapshot, err := defs.ReadSnapshot(fsys, lane.DefsSnapshot())
	if err != nil {
		check.Detail = "cannot read snapshot: " + err.Error()
		if errors.Is(err, fs.ErrNotExist) {
			check.Detail = "snapshot missing"
		}
		check.Detail += "; snapshot " + lane.DefsSnapshot() + "; run herdr-kata up to refresh it; inspect herdr-kata status --json"
		return []CheckResult{check}
	}
	info, err := fsys.Lstat(lane.DefsSnapshot())
	if err != nil {
		check.Detail = "cannot read snapshot age: " + err.Error()
		return []CheckResult{check}
	}
	check.Passed = snapshot.Version == 2 && now.Sub(info.ModTime()) <= 30*time.Minute
	check.Detail = "cached snapshot last written " + info.ModTime().UTC().Format(time.RFC3339)
	if !check.Passed {
		check.Detail = "stale " + check.Detail
		if snapshot.Version != 2 {
			check.Detail += "; legacy snapshot has no enabled state"
		}
	}
	check.Detail += "; snapshot " + lane.DefsSnapshot() + "; run herdr-kata up to refresh it; inspect herdr-kata status --json"
	checks := []CheckResult{check}
	if snapshot.Version != 2 {
		return checks
	}
	for _, job := range snapshot.Jobs {
		if job.DeletedAt != nil {
			continue
		}
		var enabled *struct {
			Enabled bool `json:"enabled"`
		}
		decodeErr := json.Unmarshal(job.Definition, &enabled)
		if decodeErr == nil && enabled != nil && !enabled.Enabled {
			continue
		}
		if job.Supported {
			for _, warning := range job.Warnings {
				if defs.IsVolumeWarning(warning) {
					checks = append(checks, CheckResult{
						Name:   "issue volume " + project + " " + job.UID,
						Passed: true,
						Detail: fmt.Sprintf("warning: project %s; job %q (%s) %s", project, job.Name, job.UID, warning),
					})
				}
			}
			continue
		}
		reason := strings.Join(job.Reasons, "; ")
		if reason == "" {
			reason = "support reason unavailable"
		}
		if decodeErr != nil || enabled == nil {
			reason = "enabled state unavailable; " + reason
		}
		checks = append(checks, CheckResult{
			Name:   "unsupported definition " + project + " " + job.UID,
			Detail: fmt.Sprintf("project %s; job %q (%s): %s; inspect herdr-kata status --json; kata --project %s cron job show %s --json", project, job.Name, job.UID, reason, project, job.UID),
		})
	}
	return checks
}
