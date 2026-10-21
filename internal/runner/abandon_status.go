package runner

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// AbandonReportProblem describes unfinished reporting without loading executable
// journals or mutating them. Both doctor and status use the same local evidence.
type AbandonReportProblem struct {
	RunUID string `json:"run_uid"`
	Detail string `json:"detail"`
}

func AbandonReportProblems(f state.FS, l state.Layout) ([]AbandonReportProblem, error) {
	reports := abandonReportsLayout(l)
	problems := map[string]string{}
	retry := func(uid string, target Target) string {
		project := target.Identity.ProjectUID
		if state.SafeComponent(project) {
			var pin kata.Installation
			if err := state.ReadJSON(f, l.Project(project).StateFile(), 64<<10, &pin); err == nil && pin.Name != "" {
				project = pin.Name
			}
		}
		command := "herdr-kata abandon " + uid
		if project != "" {
			command += fmt.Sprintf(" --project %q", project)
		}
		return "repair Kata access, then run " + command
	}
	inspect := func(uid string) string {
		return "herdr-kata stop; inspect the preserved report and resolve the issue separately; preserve the complete directory by moving " + reports.RunDir(uid) + " to " + reports.QuarantineRunDir(uid) + "; then run herdr-kata up"
	}
	automaticQuarantine := func(uid string) string {
		return "loss report automatically quarantined; herdr-kata stop; inspect the preserved journal in " + reports.QuarantineRunDir(uid) + "; resolve the issue separately; move the complete remaining directory " + reports.RunDir(uid) + " to a new directory under " + reports.QuarantineRunDir(uid) + "; then run herdr-kata up"
	}
	pendingDir := filepath.Join(l.Root, "abandoned")
	entries, err := f.ReadDir(pendingDir)
	if err != nil && !missing(err) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		uid := strings.TrimSuffix(entry.Name(), ".json")
		if !validUID(uid) {
			continue
		}
		// An automatic journal quarantine leaves the source directory behind.
		// Only a whole-directory resolution acknowledges the remaining report.
		if _, err := f.Lstat(reports.RunDir(uid)); missing(err) && state.Quarantined(f, reports, uid) {
			continue
		}
		var pending struct {
			Version     int
			Target      Target
			Observation kata.Observation
		}
		err := state.ReadJSON(f, filepath.Join(pendingDir, entry.Name()), 64<<10, &pending)
		if missing(err) {
			continue
		}
		if err != nil {
			problems[uid] = "loss observation unreadable: " + state.RepairError(err, l).Error()
			continue
		}
		problems[uid] = "loss report pending; " + retry(uid, pending.Target)
	}
	entries, err = f.ReadDir(filepath.Join(reports.Root, "runs"))
	if err != nil && !missing(err) {
		return nil, err
	}
	for _, entry := range entries {
		uid := entry.Name()
		if !entry.IsDir() || !validUID(uid) {
			continue
		}
		var r Run
		err := state.ReadJSON(f, reports.RunFile(uid), maxRunBytes, &r)
		if missing(err) {
			if state.Quarantined(f, reports, uid) {
				problems[uid] = automaticQuarantine(uid)
			}
			continue
		}
		if err != nil {
			problems[uid] = "loss report unreadable: " + err.Error() + "; " + inspect(uid)
			continue
		}
		op := head(&r)
		if op == nil && r.Inspect == "" {
			delete(problems, uid)
			continue
		}
		if r.Inspect != "" {
			problems[uid] = "loss report requires inspection: " + r.Inspect + "; " + inspect(uid)
			continue
		}
		detail := "loss report pending"
		if op != nil && !op.NextAt.IsZero() {
			detail += "; retry after " + op.NextAt.UTC().Format(time.RFC3339)
		}
		if r.Source == "request-refusal" {
			detail = strings.Replace(detail, "loss report", "request acknowledgement", 1)
			problems[uid] = detail + "; if the saved notification remains, the next inbox scan rechecks it; otherwise " + inspect(uid)
		} else {
			problems[uid] = detail + "; " + retry(uid, r.Target)
		}
	}
	uids := make([]string, 0, len(problems))
	for uid := range problems {
		uids = append(uids, uid)
	}
	sort.Strings(uids)
	result := make([]AbandonReportProblem, 0, len(uids))
	for _, uid := range uids {
		result = append(result, AbandonReportProblem{RunUID: uid, Detail: uid + ": " + problems[uid]})
	}
	return result, nil
}
