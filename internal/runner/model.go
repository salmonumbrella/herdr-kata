// Package run owns durable execution state and ordered Kata mutations.
package runner

import (
	"encoding/json"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/agentlimit"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/usage"
)

type Phase string

const (
	Prepared  Phase = "prepared"
	Admitted  Phase = "admitted"
	Running   Phase = "running"
	Blocked   Phase = "blocked"
	Held      Phase = "held"
	HandedOff Phase = "handed_off"
	Settled   Phase = "settled"
)

type Submission string

const (
	None     Submission = "none"
	Rejected Submission = "rejected"
	Sending  Submission = "sending"
	Accepted Submission = "accepted"
)

type Target struct {
	Identity kata.Identity
	Route    kata.Route
	Executor string
}
type Run struct {
	Usage     *usage.Usage `json:",omitempty"`
	Version   int
	JournalID string `json:"journal_id,omitempty"`
	Admitted  *bool  `json:"admitted,omitempty"`
	// LeaseGeneration is the lane lease generation the run was prepared
	// under; its pre-admission writes need that generation still held.
	LeaseGeneration    int64 `json:"lease_generation,omitempty"`
	UID, Source        string
	Target             Target
	Job                kata.Job
	JobDef             defs.JobDef
	Workflow           *kata.Workflow
	WFDef              *defs.WorkflowDef
	Occurrence         string
	Input              string
	ActiveStep         string
	PastAttempts       []StepState
	Manual             bool
	ProfileName        string
	Profile            config.Profile
	StepProfiles       map[string]config.Profile `json:"step_profiles,omitempty"` // Differing prompt step profiles, frozen at admission.
	Cwd                string
	ExpiresAt          time.Time `json:"Deadline"` // Preserve the journal key used by older binaries.
	Timeout            time.Duration
	PromptCap          int // Zero preserves unlimited legacy runs.
	PromptsSent        int
	Resume             *ResumeState
	ConsumedResumes    []string
	IssueUID, IssueRef string
	ProjectName        string            `json:",omitempty"`
	IssueRefs          map[string]string `json:"issue_refs,omitempty"`
	IssueOwned         bool
	Phase              Phase
	Outcome, Reason    string
	Steps              []StepState
	Herdr              HerdrRef
	Ops                []Op
	Admission          *AdmissionRequests
	StartedAt, EndedAt *time.Time
	SlotReleased       bool
	Inspect            string
	Loop               LoopState
	Findings           Findings
	IssueRequest       *IssueRequestRun `json:"issue_request,omitempty"`
	Child              *ChildRun
}

// AdmissionRequests freezes request data before any side effect. IssueUID is
// filled from the create result in the same transition that saves that result.
type AdmissionRequests struct {
	Body       kata.Observation
	DeadlineAt string
}

// ChildRun freezes native delivery, binding, request and takeover evidence.
type ChildRun struct{ sched.ChildDue }
type StepState struct {
	Usage          *usage.Usage `json:",omitempty"`
	LoopRerun      bool
	FindingContext *FindingContext
	Note           string
	Herdr          *HerdrRef
	Key, Kind      string
	Attempt        int
	Uncharged      int               `json:",omitempty"`
	Limit          *agentlimit.Limit `json:",omitempty"`
	Submission     Submission
	SubmittedAt    *time.Time
	SawActivity    bool // Working or blocked observed after this attempt's submission.
	Status, Reason string
	Receipt        *Receipt
	Result         *ResultRef
}

// Receipt and ResultRef retain attempt-bound evidence in the journal.
type Receipt struct {
	Version            int
	Run, Step          string
	Attempt, ExitCode  int
	StartedAt, EndedAt time.Time
	// These survive a lost reply: a zero exit after TERM and uncertain
	// descendants must never authorize success or an automatic retry.
	PGID        int
	Interrupted bool
	GroupAlive  bool
	Uncertain   bool // Wait or output draining did not complete normally.
}
type ResultRef struct {
	Version         int
	Run, Step       string
	Attempt         int
	Status, Summary string
	Note            string
	Assert, Target  string
	Evidence        []string
	CloseReason     string
}
type HerdrRef struct {
	ServerID                                          herdr.ServerID
	WorkspaceID, TabID, PaneID, TerminalID, AgentName string
	Session                                           *herdr.AgentSession
	ReportBin                                         string
	PreSubmitSeq                                      *uint64
	LaunchIntent                                      bool
	LaunchedAt                                        time.Time `json:",omitempty"`
	// Nil is a legacy journal; new occupants persist whether input may have been sent.
	HasSubmitted *bool
}
type Op struct {
	CommentUID string
	ID, Kind   string
	Payload    json.RawMessage
	Key        string
	Guard      *kata.Cond
	Safety     kata.Safety
	Attempts   int
	FirstSent  *time.Time
	NextAt     time.Time
	// Done and Inspect encode pending (false, empty), blocked (false, nonempty),
	// landed (true, empty), or retired with evidence (true, nonempty).
	Done           bool
	Inspect        string
	Replayed       bool `json:"replayed,omitempty"`
	RetryIdentical bool `json:"retry_identical,omitempty"`
	// Sticky across later refusals, including retirement after a conflict.
	// Only this operation's successful delivery or whole-run quarantine clears it.
	SigningRefused bool `json:"signing_refused,omitempty"`
}

// Landed reports completion without retained inspection evidence.
func (op Op) Landed() bool { return op.Done && op.Inspect == "" }

// Blocked reports a pending operation that requires inspection.
func (op Op) Blocked() bool { return !op.Done && op.Inspect != "" }

// retire completes an operation while preserving the caller's diagnostic.
func (op *Op) retire(why string) {
	op.Done = true
	op.Inspect = why
}

// ResumeState saves acceptance before any queued running observation is sent.
type ResumeState struct {
	UID        string
	Generation int
	Step       string
	Attempt    int
	Pending    bool
}
