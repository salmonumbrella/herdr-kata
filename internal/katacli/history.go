package katacli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

// ReportedRun is ordinary shared evidence, never a local execution or handle.
// Read rows intentionally have no expected_revision or raw runtime snapshot.
type ReportedRun struct {
	UID                        string     `json:"uid"`
	ProjectID                  int64      `json:"project_id"`
	JobUID                     string     `json:"job_uid"`
	DefinitionEventUID         string     `json:"definition_event_uid"`
	WorkflowUID                string     `json:"workflow_uid"`
	WorkflowDefinitionEventUID string     `json:"workflow_definition_event_uid"`
	OccurrenceKey              string     `json:"occurrence_key"`
	IssueUID                   string     `json:"issue_uid"`
	Actor                      string     `json:"actor"`
	Teammate                   string     `json:"teammate"`
	ExecutorLabel              string     `json:"executor_label"`
	Status                     string     `json:"status"`
	Summary                    RunSummary `json:"summary"`
	Revision                   int64      `json:"revision"`
	CreatedAt                  time.Time  `json:"created_at"`
	UpdatedAt                  time.Time  `json:"updated_at"`
	StartedAt                  *time.Time `json:"started_at"`
	EndedAt                    *time.Time `json:"ended_at"`
}
type RunSummary struct {
	Version      int    `json:"version"`
	Message      string `json:"message"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}
type RunPage struct {
	Runs          []ReportedRun `json:"runs"`
	NextBeforeUID string        `json:"next_before_uid,omitempty"`
}

func (c *Client) RunHistory(ctx context.Context, projectUID, before string) (RunPage, error) {
	var page RunPage
	if id, err := NormalizeUID(projectUID); err != nil || id != projectUID {
		return page, errors.New("shared history project must be canonical")
	}
	projectID, err := c.ProjectID(ctx, projectUID)
	if err != nil {
		return page, err
	}
	var wire struct {
		Runs          []json.RawMessage `json:"runs"`
		NextBeforeUID string            `json:"next_before_uid"`
	}
	if err := c.Runs(ctx, "", before, &wire); err != nil {
		return page, err
	}
	if wire.Runs == nil {
		return page, errors.New("shared history response has no runs array")
	}
	if len(wire.Runs) > 100 {
		return page, errors.New("shared history page exceeds100 rows")
	}
	if wire.NextBeforeUID != "" {
		if id, err := NormalizeUID(wire.NextBeforeUID); err != nil || id != wire.NextBeforeUID {
			return page, errors.New("invalid shared history cursor")
		}
	}
	seen := map[string]bool{}
	for _, raw := range wire.Runs {
		var r ReportedRun
		if len(raw) > 98304 {
			return page, errors.New("shared history row exceeds byte bound")
		}
		if err := Decode(raw, &r); err != nil {
			return page, fmt.Errorf("invalid shared history row: %w", err)
		}
		invalid := func(why string) (RunPage, error) { return RunPage{}, fmt.Errorf("shared run %s: %s", r.UID, why) }
		for _, id := range []string{r.UID, r.JobUID, r.DefinitionEventUID, r.WorkflowUID, r.WorkflowDefinitionEventUID, r.IssueUID} {
			if id != "" {
				if norm, err := NormalizeUID(id); err != nil || norm != id {
					return invalid("invalid identity")
				}
			}
		}
		if r.UID == "" || r.ProjectID != projectID || seen[r.UID] {
			return invalid("duplicate, missing or foreign project identity")
		}
		if (r.JobUID == "") != (r.DefinitionEventUID == "") || (r.WorkflowUID == "") != (r.WorkflowDefinitionEventUID == "") || (r.JobUID == "" && r.WorkflowUID == "") {
			return invalid("missing definition revision pair")
		}
		switch r.Status {
		case "running", "succeeded", "failed", "cancelled", "unknown":
		default:
			return invalid("invalid reported status")
		}
		for _, s := range []string{r.Actor, r.Teammate, r.ExecutorLabel} {
			if !utf8.ValidString(s) || len(s) > 256 {
				return invalid("identity label exceeds text bound")
			}
		}
		if r.Actor == "" || !utf8.ValidString(r.OccurrenceKey) || utf8.RuneCountInString(r.OccurrenceKey) > 1024 || r.Revision < 1 || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
			return invalid("invalid evidence metadata")
		}
		var fields map[string]json.RawMessage
		if err := Decode(raw, &fields); err != nil {
			return invalid("invalid evidence object")
		}
		if len(fields["summary"]) > 65536 || r.Summary.Version != 1 || !utf8.ValidString(r.Summary.Message) || r.Summary.InputTokens < 0 || r.Summary.OutputTokens < 0 {
			return invalid("invalid bounded summary")
		}
		seen[r.UID] = true
		page.Runs = append(page.Runs, r)
	}
	page.NextBeforeUID = wire.NextBeforeUID
	return page, nil
}
