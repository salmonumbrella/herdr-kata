package katacli

import (
	"context"
	"encoding/json"
	"errors"
)

// RunObservation is the planned ordinary data-write DTO. It carries evidence,
// never execution context, permission or a process/session handle.
type RunObservation struct {
	JobUID                 string          `json:"job_uid,omitempty"`
	DefinitionEventUID     string          `json:"definition_event_uid,omitempty"`
	FlowUID                string          `json:"flow_uid,omitempty"`
	FlowDefinitionEventUID string          `json:"flow_definition_event_uid,omitempty"`
	OccurrenceKey          string          `json:"occurrence_key,omitempty"`
	IssueUID               string          `json:"issue_uid,omitempty"`
	Teammate               string          `json:"teammate,omitempty"`
	ExecutorLabel          string          `json:"executor_label,omitempty"`
	Status                 string          `json:"status"`
	Summary                json.RawMessage `json:"summary"`
	StartedAt              string          `json:"started_at,omitempty"`
	EndedAt                string          `json:"ended_at,omitempty"`
	ExpectedRevision       int64           `json:"expected_revision"`
}
type RunObservationResult struct {
	Run struct {
		UID      string `json:"uid"`
		Revision int64  `json:"revision"`
	} `json:"run"`
	Replayed bool `json:"replayed"`
}

func (c *Client) ObserveRun(ctx context.Context, uid string, observation RunObservation) (RunObservationResult, error) {
	var out RunObservationResult
	id, err := NormalizeUID(uid)
	if err != nil {
		return out, err
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		return out, err
	}
	if len(raw) > 98304 || len(observation.Summary) > 65536 {
		return out, errors.New("run observation exceeds byte bound")
	}
	if err = c.Call(ctx, []string{"cron", "run", "observe", id, "--json-input", "-"}, raw, &out); err != nil {
		return out, err
	}
	if out.Run.UID != id || out.Run.Revision < 1 {
		return out, errors.New("invalid ordinary run observation response")
	}
	return out, nil
}
