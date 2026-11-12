package kata

import (
	"encoding/json"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/session"
)

// These wire types follow go.kenn.io/kata v0.20.0's generated models.
// The generated package also imports a validator beyond the oapi runtime;
// local copies keep this CLI adapter independent of that dependency graph.
type CronJob struct {
	Author             string          `json:"author"`
	CreatedAt          time.Time       `json:"created_at"`
	Definition         JobDefinition   `json:"definition"`
	DefinitionEventUID string          `json:"definition_event_uid"`
	DefinitionHlc      json.RawMessage `json:"definition_hlc"`
	DeletedAt          *time.Time      `json:"deleted_at,omitempty"`
	ID                 int64           `json:"id"`
	Name               string          `json:"name"`
	ProjectID          int64           `json:"project_id"`
	Revision           int64           `json:"revision"`
	UID                string          `json:"uid"`
	UpdatedAt          time.Time       `json:"updated_at"`
}
type JobDefinition struct {
	Action         Action            `json:"action"`
	Catchup        string            `json:"catchup"`
	CheckoutKey    *string           `json:"checkout_key,omitempty"`
	Enabled        bool              `json:"enabled"`
	GraceSeconds   *int64            `json:"grace_seconds,omitempty"`
	Issue          *IssuePolicy      `json:"issue,omitempty"`
	Kind           string            `json:"kind"`
	Options        map[string]any    `json:"options,omitempty"`
	Overlap        string            `json:"overlap"`
	SecretRefs     map[string]string `json:"secret_refs,omitempty"`
	TimeoutSeconds *int64            `json:"timeout_seconds,omitempty"`
	Trigger        Trigger           `json:"trigger"`
	Version        int64             `json:"version"`
}
type Action struct {
	Kind        string  `json:"kind"`
	Message     *string `json:"message,omitempty"`
	Prompt      *string `json:"prompt,omitempty"`
	Recipient   *string `json:"recipient,omitempty"`
	WorkflowUID *string `json:"workflow_uid,omitempty"`
}
type IssuePolicy struct {
	Body                   *string `json:"body,omitempty"`
	DeadlineOffsetSeconds  *int64  `json:"deadline_offset_seconds,omitempty"`
	Kind                   string  `json:"kind"`
	ParentUID              *string `json:"parent_uid,omitempty"`
	ScheduledOffsetSeconds *int64  `json:"scheduled_offset_seconds,omitempty"`
	Title                  *string `json:"title,omitempty"`
	UID                    *string `json:"uid,omitempty"`
}
type Trigger struct {
	At              *string `json:"at,omitempty"`
	Cron            *string `json:"cron,omitempty"`
	IntervalSeconds *int64  `json:"interval_seconds,omitempty"`
	IssueUID        *string `json:"issue_uid,omitempty"`
	Kind            string  `json:"kind"`
	LeadSeconds     *int64  `json:"lead_seconds,omitempty"`
	Timezone        *string `json:"timezone,omitempty"`
}
type CronWorkflow struct {
	Author             string             `json:"author"`
	CreatedAt          time.Time          `json:"created_at"`
	Definition         WorkflowDefinition `json:"definition"`
	DefinitionEventUID string             `json:"definition_event_uid"`
	DefinitionHlc      json.RawMessage    `json:"definition_hlc"`
	DeletedAt          *time.Time         `json:"deleted_at,omitempty"`
	ID                 int64              `json:"id"`
	Name               string             `json:"name"`
	ProjectID          int64              `json:"project_id"`
	Revision           int64              `json:"revision"`
	UID                string             `json:"uid"`
	UpdatedAt          time.Time          `json:"updated_at"`
}
type WorkflowDefinition struct {
	About   *string        `json:"about,omitempty"`
	Input   *string        `json:"input,omitempty"`
	Options map[string]any `json:"options,omitempty"`
	Steps   []WorkflowStep `json:"steps"`
	Version int64          `json:"version"`
}
type WorkflowStep struct {
	After   []string       `json:"after,omitempty"`
	Command *string        `json:"command,omitempty"`
	Key     string         `json:"key"`
	Kind    string         `json:"kind"`
	Options map[string]any `json:"options,omitempty"`
	Prompt  *string        `json:"prompt,omitempty"`
	Retries *int64         `json:"retries,omitempty"`
}
type ObserveCronRunBody struct {
	Actor                      *string    `json:"actor,omitempty"`
	DefinitionEventUID         *string    `json:"definition_event_uid,omitempty"`
	EndedAt                    *time.Time `json:"ended_at,omitempty"`
	ExecutorLabel              *string    `json:"executor_label,omitempty"`
	ExpectedRevision           int64      `json:"expected_revision"`
	IssueUID                   *string    `json:"issue_uid,omitempty"`
	JobUID                     *string    `json:"job_uid,omitempty"`
	OccurrenceKey              *string    `json:"occurrence_key,omitempty"`
	StartedAt                  *time.Time `json:"started_at,omitempty"`
	Status                     string     `json:"status"`
	Summary                    Summary    `json:"summary"`
	Teammate                   *string    `json:"teammate,omitempty"`
	WorkflowDefinitionEventUID *string    `json:"workflow_definition_event_uid,omitempty"`
	WorkflowUID                *string    `json:"workflow_uid,omitempty"`
}
type SummaryStep struct {
	Key       string  `json:"key"`
	Status    string  `json:"status"`
	Attempts  int     `json:"attempts"`
	ExitCode  *int    `json:"exit_code,omitempty"`
	Reason    *string `json:"reason,omitempty"`
	Message   string  `json:"message,omitempty"`
	StartedAt string  `json:"started_at,omitempty"`
	EndedAt   string  `json:"ended_at,omitempty"`
}
type Summary struct {
	InputTokens  *int64  `json:"input_tokens,omitempty"`
	Message      *string `json:"message,omitempty"`
	OutputTokens *int64  `json:"output_tokens,omitempty"`
	Version      int64   `json:"version"`

	Reason             *string          `json:"reason,omitempty"`
	Steps              []SummaryStep    `json:"steps,omitempty"`
	Sessions           []SummarySession `json:"sessions,omitempty"`
	SessionsIncomplete bool             `json:"sessions_incomplete,omitempty"`
}
type SummarySession struct {
	session.Session
	Step string `json:"step,omitempty"`
}
type DateValue struct {
	Field    string    `json:"field"`
	Value    string    `json:"value"`
	Timezone string    `json:"timezone"`
	Instant  time.Time `json:"instant"`
}

// CronRun contains the evidence used by the bounded executor diagnostic.
type CronRun struct {
	UID           string    `json:"uid"`
	CreatedAt     time.Time `json:"created_at"`
	ExecutorLabel *string   `json:"executor_label,omitempty"`
	Summary       Summary   `json:"summary"`
}
