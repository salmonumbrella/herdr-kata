package katacli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// PlanningDate is a native-resolved source. Timezone is evidence, never an
// instruction to reinterpret Instant on this installation.
type PlanningDate struct {
	Field    string    `json:"field"`
	Value    string    `json:"value"`
	Timezone string    `json:"timezone"`
	Instant  time.Time `json:"instant"`
}
type PlanningDates struct {
	ProjectID   int64         `json:"project_id"`
	IssueUID    string        `json:"issue_uid"`
	Revision    int64         `json:"revision"`
	ScheduledOn *PlanningDate `json:"scheduled_on"`
	DeadlineOn  *PlanningDate `json:"deadline_on"`
}

func (c *Client) PlanningDates(ctx context.Context, projectUID, issueUID string) (PlanningDates, error) {
	var out PlanningDates
	if id, err := NormalizeUID(issueUID); err != nil || id != issueUID {
		return out, errors.New("planning-date issue UID must be canonical")
	}
	pid, err := c.ProjectID(ctx, projectUID)
	if err != nil {
		return out, err
	}
	var raw map[string]json.RawMessage
	if err := c.Call(ctx, []string{"show", "--planning-dates", "--", issueUID}, nil, &raw); err != nil {
		return out, err
	}
	for _, key := range []string{"scheduled_on", "deadline_on"} {
		if _, ok := raw[key]; !ok {
			return out, errors.New("planning-date response omitted a required nullable field")
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return out, err
	}
	if err := Decode(encoded, &out); err != nil {
		return out, err
	}
	if out.ProjectID != pid || out.IssueUID != issueUID || out.Revision < 1 {
		return out, errors.New("planning-date response has another project/issue identity or invalid revision")
	}
	for key, date := range map[string]*PlanningDate{"scheduled_on": out.ScheduledOn, "deadline_on": out.DeadlineOn} {
		if date == nil {
			continue
		}
		var fields map[string]json.RawMessage
		if err := Decode(raw[key], &fields); err != nil {
			return out, err
		}
		var instant string
		if err := Decode(fields["instant"], &instant); err != nil {
			return out, err
		}
		if date.Field != key || date.Value == "" || !utf8.ValidString(date.Value) || len(date.Value) > 256 || date.Timezone == "" || !utf8.ValidString(date.Timezone) || len(date.Timezone) > 256 || !strings.HasSuffix(instant, "Z") {
			return out, errors.New("invalid native-resolved planning-date field")
		}
	}
	return out, nil
}
