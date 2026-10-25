// Package kata implements the pinned Kata CLI contract without a shell, the
// route pin and guard, and the event loop.
package kata

import (
	"encoding/json"
	"time"
)

type Identity struct {
	InstanceUID string   `json:"instance_uid"`
	ProjectUID  string   `json:"project_uid"`
	Actor       string   `json:"actor"`
	Features    []string `json:"features"`
}
type Route struct {
	Bin, Daemon             string
	Env                     map[string]string
	Endpoint, DiagnoseState string
}
type Capabilities struct {
	ProjectUID    string   `json:"project_uid"`
	EventFeatures []string `json:"event_features"`
	CLIFeatures   []string `json:"cli_features,omitempty"`
}
type Job struct {
	Author             string          `json:"-"`
	ProjectUID         string          `json:"-"`
	Project            string          `json:"-"`
	UID                string          `json:"uid"`
	Name               string          `json:"name"`
	DefinitionEventUID string          `json:"definition_event_uid"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	DeletedAt          *time.Time      `json:"deleted_at,omitempty"`
	Definition         json.RawMessage `json:"definition"`
}

// UnmarshalJSON retains live author provenance without adding it to frozen
// run/definition snapshots. Automatic scheduling always reads fresh envelopes.
func (j *Job) UnmarshalJSON(data []byte) error {
	type envelope Job
	var wire struct {
		envelope
		Author string `json:"author"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*j = Job(wire.envelope)
	j.Author = wire.Author
	return nil
}

type Workflow Job
type Summary struct {
	Version      int    `json:"version"`
	Message      string `json:"message"`
	InputTokens  int64  `json:"input_tokens,omitempty"`
	OutputTokens int64  `json:"output_tokens,omitempty"`
}
type Transcript struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	URL       string `json:"url,omitempty"`
}

type Observation struct {
	Transcripts                []Transcript `json:"transcripts,omitempty"`
	TranscriptsIncomplete      bool         `json:"transcripts_incomplete,omitempty"`
	JobUID                     string       `json:"job_uid,omitempty"`
	DefinitionEventUID         string       `json:"definition_event_uid,omitempty"`
	WorkflowUID                string       `json:"workflow_uid,omitempty"`
	WorkflowDefinitionEventUID string       `json:"workflow_definition_event_uid,omitempty"`
	OccurrenceKey              string       `json:"occurrence_key,omitempty"`
	IssueUID                   string       `json:"issue_uid,omitempty"`
	Actor                      string       `json:"actor,omitempty"`
	ExecutorLabel              string       `json:"executor_label,omitempty"`
	Status                     string       `json:"status"`
	Teammate                   *string      `json:"teammate,omitempty"`
	Summary                    Summary      `json:"summary"`
	StartedAt                  *time.Time   `json:"started_at,omitempty"`
	EndedAt                    *time.Time   `json:"ended_at,omitempty"`
	ExpectedRevision           int64        `json:"expected_revision"`
}
type Run struct {
	Transcripts                []Transcript `json:"transcripts,omitempty"`
	TranscriptsIncomplete      bool         `json:"transcripts_incomplete,omitempty"`
	CreatedAt                  time.Time    `json:"created_at"`
	UpdatedAt                  time.Time    `json:"updated_at"`
	UID                        string       `json:"uid"`
	JobUID                     string       `json:"job_uid"`
	DefinitionEventUID         string       `json:"definition_event_uid"`
	WorkflowUID                string       `json:"workflow_uid"`
	WorkflowDefinitionEventUID string       `json:"workflow_definition_event_uid"`
	OccurrenceKey              string       `json:"occurrence_key"`
	IssueUID                   string       `json:"issue_uid"`
	Actor                      string       `json:"actor"`
	ExecutorLabel              string       `json:"executor_label"`
	Status                     string       `json:"status"`
	Teammate                   *string      `json:"teammate"`
	Revision                   int64        `json:"revision"`
	Summary                    Summary      `json:"summary"`
	StartedAt                  *time.Time   `json:"started_at"`
	EndedAt                    *time.Time   `json:"ended_at"`
}
type CommentReq struct {
	Ref, Body, IdempotencyKey, ReplyTo, Kind string
}
type Comment struct {
	ReplyToUID string `json:"reply_to_uid"`
	ReplyKind  string `json:"reply_kind"`
	UID        string `json:"uid"`
	Body       string `json:"body"`
	Author     string `json:"author"`
	Teammate   string `json:"teammate"`
}
type IssueView struct {
	Comments    []Comment                  `json:"comments"`
	UID         string                     `json:"uid"`
	ShortID     string                     `json:"short_id"`
	QualifiedID string                     `json:"qualified_id"`
	ProjectUID  string                     `json:"project_uid"`
	Title       string                     `json:"title"`
	Body        string                     `json:"body"`
	Status      string                     `json:"status"`
	Owner       string                     `json:"owner"`
	Author      string                     `json:"author"`
	Revision    int64                      `json:"revision"`
	Labels      []string                   `json:"labels"`
	Metadata    map[string]json.RawMessage `json:"metadata"`
	ParentUID   string                     `json:"parent_uid"`
	Links       []Link                     `json:"links"`
}
type LinkPeer struct {
	UID         string `json:"uid"`
	ShortID     string `json:"short_id"`
	Project     string `json:"project"`
	QualifiedID string `json:"qualified_id"`
}
type Link struct {
	Type string   `json:"type"`
	From LinkPeer `json:"from"`
	To   LinkPeer `json:"to"`
}
type InboxRequest struct {
	Ref      string `json:"ref"`
	Project  string `json:"project"`
	Title    string `json:"title"`
	From     string `json:"from"`
	Teammate string `json:"teammate"`
	Message  string `json:"message"`
}
type Health struct {
	Version       string `json:"version"`
	SchemaVersion int    `json:"schema_version"`
}
type RootSigning struct {
	Projects []string `json:"projects"`
}
type Diagnosis struct {
	ProjectUID      string       `json:"project_uid,omitempty"`
	FederationRole  string       `json:"federation_role,omitempty"`
	HubOrigin       string       `json:"hub_origin,omitempty"`
	RootSigning     *RootSigning `json:"root_signing,omitempty"`
	State, Endpoint string
	Authoritative   bool // Informational; admission does not require a federation role.
	Raw             json.RawMessage
}
type CreateReq struct {
	Parent                      string   `json:",omitempty"`
	Labels                      []string `json:",omitempty"`
	Title, Body, IdempotencyKey string
	Meta                        map[string]string
	ForceNew                    bool
}
type CloseReq struct {
	Ref, Reason, Message, IfMatch, IdempotencyKey string
	Evidence                                      []string
}
type Cond struct {
	IfMatch   string
	IfAbsent  bool
	IfValue   *string
	JSONValue bool
}
type Event struct {
	EventID  int64           `json:"event_id"`
	Type     string          `json:"type"`
	IssueUID string          `json:"issue_uid"`
	Payload  json.RawMessage `json:"payload"`
}
type Reset struct{ AfterID int64 }

// TailItem is one wire line; Reset is non-nil only for a transport reset.
type TailItem struct {
	Event Event
	Reset *Reset
}
