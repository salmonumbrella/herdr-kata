package kata

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/oklog/ulid/v2"
)

type carrierIssue struct {
	UID                         string
	ShortID                     string `json:"short_id"`
	ProjectUID                  string `json:"project_uid"`
	Title, Body, Author, Status string
	Owner                       *string
	Priority                    *int
	DeletedAt                   *string `json:"deleted_at"`
	Metadata                    map[string]json.RawMessage
	Labels, Links               []json.RawMessage
}

// CreateAlertCarrier uses an explicit nil priority and requires immutable create
// evidence before resolving a daily carrier. Ordinary per-run creates retain
// their existing defaults and conflict behavior.
func (c *cli) CreateAlertCarrier(title, body, key string) (string, error) {
	parent := c.context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, CallTimeout)
	defer cancel()
	client := *c
	client.context = ctx
	type event struct {
		UID, Type, Actor, Payload string
		IssueUID                  string `json:"issue_uid"`
		ProjectUID                string `json:"project_uid"`
		ProjectName               string `json:"project_name"`
	}
	var created struct {
		Issue         carrierIssue
		Event         *event
		OriginalEvent *event `json:"original_event"`
	}
	if err := client.call(nil, &created, "create", title, "--body", body, "--force-new", "--idempotency-key", key, "--priority", "none"); err != nil {
		return "", err
	}
	refused := func() (string, error) {
		return "", &Error{Code: 3, Message: "daily alert carrier identity was not verified"}
	}
	if (created.Event == nil) == (created.OriginalEvent == nil) {
		return refused()
	}
	e := created.Event
	if e == nil {
		e = created.OriginalEvent
	}
	var original struct {
		carrierIssue
		Key         string `json:"idempotency_key"`
		Fingerprint string `json:"idempotency_fingerprint"`
	}
	if json.Unmarshal([]byte(e.Payload), &original) != nil {
		return refused()
	}
	fingerprint, err := hex.DecodeString(original.Fingerprint)
	if err != nil || len(fingerprint) != 32 || key == "" || original.Key != key ||
		!validCarrierUID(e.UID) || !validCarrierUID(created.Issue.UID) || !validCarrierUID(e.ProjectUID) ||
		e.Type != "issue.created" || e.ProjectName != c.project || e.Actor != c.actor ||
		e.IssueUID != created.Issue.UID || e.ProjectUID != created.Issue.ProjectUID ||
		original.UID != created.Issue.UID || !original.matches(title, body, c.actor, true) ||
		!created.Issue.matches(title, body, c.actor, false) {
		return refused()
	}
	var current struct {
		Issue         carrierIssue
		Labels, Links *[]json.RawMessage
	}
	if err := client.call(nil, &current, "show", created.Issue.UID); err != nil {
		return "", err
	}
	if current.Issue.UID != created.Issue.UID || current.Issue.ShortID == "" ||
		current.Issue.ShortID != created.Issue.ShortID || current.Issue.ProjectUID != e.ProjectUID ||
		!current.Issue.matches(title, body, c.actor, false) ||
		current.Labels == nil || current.Links == nil || len(*current.Labels) != 0 || len(*current.Links) != 0 {
		return refused()
	}
	return current.Issue.UID, nil
}

func validCarrierUID(value string) bool { _, err := ulid.ParseStrict(value); return err == nil }

func (i carrierIssue) matches(title, body, actor string, initial bool) bool {
	if i.Title != title || i.Body != body || i.Author != actor || i.Status != "open" ||
		i.Owner != nil || i.Priority != nil || i.DeletedAt != nil || len(i.Labels) != 0 || len(i.Links) != 0 {
		return false
	}
	var teammate string
	if json.Unmarshal(i.Metadata["teammate"], &teammate) != nil || teammate != "herdr-kata" {
		return false
	}
	for key := range i.Metadata {
		if key != "teammate" && (initial || !strings.HasPrefix(key, "notify.")) {
			return false
		}
	}
	return true
}
