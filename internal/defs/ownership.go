package defs

import (
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

// Owned filters definitions before readiness, freezing and manual admission.
// Malformed executor pins remain owned so their defect can be reported.
func Owned(job kata.CronJob, project string, cfg config.Config) (bool, string) {
	configured := false
	for _, p := range cfg.Projects {
		if p.Name == project {
			configured = true
			break
		}
	}
	switch {
	case !configured:
		return false, "project is not configured"
	case job.DeletedAt != nil:
		return false, "job is deleted"
	case !job.Definition.Enabled:
		return false, "job is disabled"
	case job.Definition.Action.Kind != "execute":
		return false, "job action is not execute"
	}
	if adapter, ok := job.Definition.Options["adapter"]; ok && adapter != "herdr-kata" {
		return false, fmt.Sprintf("unsupported job: adapter: %v", adapter)
	}
	if pin, ok := job.Definition.Options["executor"].(string); ok && pin != "" && pin != cfg.Executor {
		return false, fmt.Sprintf("job is pinned to executor %s", pin)
	}
	return true, ""
}

// NeedsReadiness is called only with owned jobs.
func NeedsReadiness(jobs []kata.CronJob) bool {
	for _, job := range jobs {
		d := job.Definition
		if job.DeletedAt == nil && d.Enabled && (d.Trigger.Kind == "issue-scheduled" || d.Issue != nil && d.Issue.Kind == "existing") {
			return true
		}
	}
	return false
}
