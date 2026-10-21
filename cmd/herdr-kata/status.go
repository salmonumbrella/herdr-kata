package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/daemon"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/runstatus"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func statusCommand(l state.Layout, asJSON, pause bool, stdout, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, err); return 1 }
	var configs []config.Config
	if cfg, e := config.Load(config.DefaultPath()); e == nil {
		configs = append(configs, *cfg)
	}
	s, err := daemon.ReadStatus(l, configs...)
	if err != nil {
		return fail(err)
	}
	s.ControlResults, err = currentControlResults(l)
	if err != nil {
		return fail(fmt.Errorf("saved control results could not be read; preserve state and inspect herdr-kata doctor: %w", err))
	}
	if asJSON {
		if err := json.NewEncoder(stdout).Encode(s); err != nil {
			return fail(err)
		}
	} else {
		fmt.Fprintf(stdout, "runtime running: %t; worker running: %t\n", s.Running, s.WorkerRunning)
		if len(s.Projects) > 0 {
			for _, project := range s.Projects {
				fmt.Fprintf(stdout, "project %s\n", project.Project)
				printProjectStatus(stdout, project)
			}
		} else {
			printProjectStatus(stdout, s)
		}
		for _, report := range s.AbandonReports {
			fmt.Fprintf(stdout, "abandon report %s\n", report.Detail)
		}
		for _, result := range s.ControlResults {
			fmt.Fprintf(stdout, "control %s: %s\n", result.RunUID, result.Error)
		}
		for _, r := range s.Runs {
			job, _ := r["Job"].(map[string]any)
			var saved runner.Run
			raw, err := json.Marshal(r)
			if err != nil {
				return fail(err)
			}
			if err = json.Unmarshal(raw, &saved); err != nil {
				return fail(err)
			}
			reason := runner.HoldReason(&saved)
			if saved.Reason == "secret_unavailable" || saved.Reason == "herdr_env_unclean" {
				for _, step := range saved.Steps {
					if step.Key == saved.ActiveStep && step.Reason != "" {
						reason = saved.Reason + ": " + step.Reason
						break
					}
				}
			}
			agent := runner.AgentReference(&saved)
			uid, _ := r["UID"].(string)
			project := ""
			if name, ok := r["project"].(string); ok && name != "" {
				project = name + " | "
			}
			fmt.Fprintf(stdout, "run %.8s | %s%s | %s | %s | %s | %s | %s\n", uid, project, statusText(job["name"]), runstatus.Run(saved).Word, statusText(r["Outcome"]), statusText(reason), statusText(statusIssueReference(saved)), statusText(agent.AgentName))
		}
	}
	if pause {
		fmt.Fprintln(stdout, "Press Enter to close.")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
	return 0
}

func statusText(v any) string {
	s, _ := v.(string)
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "-"
	}
	return s
}

func printProjectStatus(stdout io.Writer, s daemon.Status) {
	if s.Route != nil {
		fmt.Fprintf(stdout, "route allowed: %t %s\n", s.Route.Allowed, s.Route.Error)
	}
	if s.Lease != nil {
		if s.Lease.Held {
			fmt.Fprintf(stdout, "lane lease: held at generation %d\n", s.Lease.Generation)
		} else {
			fmt.Fprintf(stdout, "lane lease: not held: %s\n", s.Lease.Error)
		}
	}
	for _, e := range s.Definitions {
		fmt.Fprintf(stdout, "job %s %s: supported=%t %v", e.UID, e.Name, e.Supported, e.Reasons)
		if len(e.Warnings) > 0 {
			fmt.Fprintf(stdout, " warnings=%v", e.Warnings)
		}
		if e.LastSkip != "" {
			fmt.Fprintf(stdout, " last_skip=%s", e.LastSkip)
		}
		if e.Waiting != "" {
			fmt.Fprintf(stdout, " waiting=%s", e.Waiting)
		}
		fmt.Fprintln(stdout)
	}
	for uid, next := range s.NextFires {
		fmt.Fprintf(stdout, "next fire %s: %s\n", uid, next.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"))
	}
	for _, j := range s.Jobs {
		b, _ := json.Marshal(j)
		fmt.Fprintf(stdout, "schedule %s\n", b)
	}
	if s.Inbox != nil {
		fmt.Fprintf(stdout, "inbox last scan: %s %s\n", s.Inbox.LastSuccessfulScan.Format("2006-01-02T15:04:05Z07:00"), s.Inbox.Error)
		for _, r := range s.Inbox.Requests {
			fmt.Fprintf(stdout, "inbox %s: %s %s\n", r.Ref, r.Status, r.Reason)
		}
	}

}

func statusIssueReference(r runner.Run) string {
	if ref := r.IssueRefs[r.IssueUID]; ref != "" {
		return ref
	}
	if strings.Contains(r.IssueRef, "#") {
		return r.IssueRef
	}
	return ""
}

// Keep acknowledgement files available to waiting CLI callers; only the status
// view expires refusals after newer journal activity, a successful control,
// a completed resolution, or saved definitions showing the requested job is gone.
func currentControlResults(l state.Layout) ([]runner.RequestResult, error) {
	dir := filepath.Join(l.Root, "request-results")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type refusal struct {
		result runner.RequestResult
		at     time.Time
	}
	var failures []refusal
	var current []runner.RequestResult
	accepted := map[string]time.Time{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		var result runner.RequestResult
		if err := state.ReadJSON(state.OSFS{}, path, 64<<10, &result); err != nil {
			return nil, err
		}
		if !validLogRunID(result.RunUID) {
			// An unreadable request has no decoded run identity. Its intake refusal
			// still needs to remain visible; no journal can prove it resolved.
			if result.Error != "" {
				result.Error = controlError(l, result.Error)
				current = append(current, result)
			}
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if result.Error != "" {
			failures = append(failures, refusal{result, info.ModTime()})
		} else if info.ModTime().After(accepted[result.RunUID]) {
			accepted[result.RunUID] = info.ModTime()
		}
	}
	for _, failure := range failures {
		result := failure.result
		if accepted[result.RunUID].After(failure.at) {
			continue
		}
		resolved, err := resolvedControlRun(l, result.RunUID, failure.at)
		if err != nil {
			return nil, err
		}
		if resolved {
			continue
		}
		info, err := os.Lstat(l.RunFile(result.RunUID))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil && info.ModTime().After(failure.at) {
			continue
		}
		obsolete, err := obsoleteJobRefusal(l, result)
		if err != nil {
			return nil, err
		}
		if obsolete {
			continue
		}
		result.Error = controlError(l, result.Error)
		current = append(current, result)
	}
	return current, nil
}

// Manual refusals carry the project and job selected by the scheduler. Only a
// saved definitions snapshot can prove the job is gone; missing evidence keeps
// the refusal visible. Resume/release refusals have no job selector.
func obsoleteJobRefusal(l state.Layout, result runner.RequestResult) (bool, error) {
	if result.Kind != "run" || result.Project == "" || result.Job == "" || !state.SafeComponent(result.Project) {
		return false, nil
	}
	snapshot, err := defs.ReadSnapshot(state.OSFS{}, l.Project(result.Project).DefsSnapshot())
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range snapshot.Jobs {
		if (entry.UID == result.Job || entry.Name == result.Job) && entry.DeletedAt == nil {
			return false, nil
		}
	}
	return true, nil
}

// Lost execution journals never return after abandonment. Completed reporting
// receipts and explicit local acknowledgements prove resolution across restarts.
// Unreadable evidence cannot prove resolution; diagnostics own its repair advice.
func resolvedControlRun(l state.Layout, uid string, refusedAt time.Time) (bool, error) {
	f := state.OSFS{}
	var ack struct {
		Acknowledged bool `json:"acknowledged"`
	}
	ackPath := filepath.Join(l.QuarantineRunDir(uid), "acknowledged.json")
	err := state.ReadJSON(f, ackPath, 4096, &ack)
	if err != nil && !os.IsNotExist(err) {
		return false, nil
	}
	if err == nil && ack.Acknowledged {
		info, err := os.Lstat(ackPath)
		if err != nil {
			return false, err
		}
		if info.ModTime().After(refusedAt) {
			return true, nil
		}
	}
	reports := state.Layout{Root: filepath.Join(l.Root, "abandoned", "reports")}
	var report runner.Run
	if err := state.ReadJSON(f, reports.RunFile(uid), 4<<20, &report); err != nil {
		return false, nil
	}
	if report.Version != 1 || report.UID != uid || report.Source != "abandon-report" || report.Phase != runner.Settled || report.Inspect != "" {
		return false, nil
	}
	for _, op := range report.Ops {
		if !op.Done {
			return false, nil
		}
	}
	info, err := os.Lstat(reports.RunFile(uid))
	if err != nil {
		return false, err
	}
	return info.ModTime().After(refusedAt), nil
}
