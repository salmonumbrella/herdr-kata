package board

import (
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"sort"
)

// Shared rows live only in the board's bounded view. Never PutRun: local runtime
// journals and session handles belong exclusively to this installation.
func mergeReportedRuns(local []store.Run, shared map[string]katacli.ReportedRun, job string, limit int) []store.Run {
	rows := map[string]store.Run{}
	for _, r := range local {
		rows[r.ID] = r
	}
	for uid, r := range shared {
		if _, owned := rows[uid]; owned {
			continue
		}
		start := r.CreatedAt
		if r.StartedAt != nil {
			start = *r.StartedAt
		}
		rows[uid] = store.Run{ID: uid, JobID: r.JobUID, Workflow: r.WorkflowUID, Ref: r.IssueUID, Trigger: "reported", Outcome: r.Status, Note: r.Summary.Message, InputTokens: r.Summary.InputTokens, OutputTokens: r.Summary.OutputTokens, StartedAt: start, EndedAt: r.EndedAt}
	}
	out := []store.Run{}
	for _, r := range rows {
		if job == "" || r.JobID == job {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
func cloneReportedRuns(rows map[string]katacli.ReportedRun) map[string]katacli.ReportedRun {
	copy := map[string]katacli.ReportedRun{}
	for uid, r := range rows {
		copy[uid] = r
	}
	return copy
}
