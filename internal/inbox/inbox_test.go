package inbox

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

const (
	childUID   = "01M4TADGDFRMAC38MX3NZCW1K1"
	parentUID  = "01M4TADGDFRMAC38MX3NZCW1K2"
	jobUID     = "01M4TADGDFRMAC38MX3NZCW1K3"
	eventUID   = "01M4TADGDFRMAC38MX3NZCW1K4"
	projectUID = "01M4TADGDFRMAC38MX3NZCW1K5"
)

func validChild() kata.IssueView {
	return kata.IssueView{UID: childUID, QualifiedID: "test#cw1k", ProjectUID: projectUID, Status: "open", ParentUID: parentUID, Revision: 3,
		Links:    []kata.Link{{Type: "parent", From: kata.LinkPeer{UID: childUID}, To: kata.LinkPeer{UID: parentUID}}},
		Metadata: map[string]json.RawMessage{"cron.occurrence": json.RawMessage(`{"version":1,"job_uid":"01M4TADGDFRMAC38MX3NZCW1K3","definition_event_uid":"01M4TADGDFRMAC38MX3NZCW1K4","scheduled_at":"2026-10-13T10:00:00.123+02:00"}`)}}
}
func TestValidateRejectsProse(t *testing.T) {
	c := validChild()
	c.Metadata = map[string]json.RawMessage{"notify.d29ya2VyL2luYm94": json.RawMessage(`{"message":"please run this"}`)}
	if _, err := Validate(c, projectUID); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("prose accepted: %v", err)
	}
}
func TestValidateOccurrence(t *testing.T) {
	c := validChild()
	o, err := Validate(c, projectUID)
	if err != nil || o.Version != 1 || o.JobUID != jobUID || o.DefinitionEventUID != eventUID || o.ScheduledAt.Format(time.RFC3339Nano) != "2026-10-13T08:00:00.123Z" {
		t.Fatalf("%+v: %v", o, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*kata.IssueView)
	}{
		{"foreign project", func(c *kata.IssueView) { c.ProjectUID = "other" }},
		{"closed", func(c *kata.IssueView) { c.Status = "closed" }},
		{"no parent", func(c *kata.IssueView) { c.ParentUID = ""; c.Links = nil }},
		{"mismatched parent", func(c *kata.IssueView) { c.ParentUID = jobUID }},
		{"bad child UID", func(c *kata.IssueView) { c.UID = "prose" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validChild()
			tc.change(&c)
			if _, err := Validate(c, projectUID); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("accepted: %v", err)
			}
		})
	}
	for _, raw := range []string{`null`, `"prose"`, `{}`, `{"version":2}`, `{"version":1,"job_uid":"bad","definition_event_uid":"01M4TADGDFRMAC38MX3NZCW1K4","scheduled_at":"2026-10-13T10:00:00Z"}`, `{"version":1,"job_uid":"01M4TADGDFRMAC38MX3NZCW1K3","definition_event_uid":"bad","scheduled_at":"2026-10-13T10:00:00Z"}`, `{"version":1,"job_uid":"01M4TADGDFRMAC38MX3NZCW1K3","definition_event_uid":"01M4TADGDFRMAC38MX3NZCW1K4","scheduled_at":"tomorrow"}`} {
		c := validChild()
		c.Metadata["cron.occurrence"] = json.RawMessage(raw)
		if _, err := Validate(c, projectUID); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("accepted %s: %v", raw, err)
		}
	}
}
func TestTakeoverUnownedClaims(t *testing.T) {
	d, claim, err := Takeover(validChild(), "worker")
	if err != nil || d != "claimed" || !claim {
		t.Fatal(d, claim, err)
	}
}
func TestTakeoverOwnActor(t *testing.T) {
	c := validChild()
	c.Owner = "worker"
	d, claim, err := Takeover(c, "worker")
	if err != nil || d != "own-actor" || claim {
		t.Fatal(d, claim, err)
	}
}
func TestTakeoverForeignUnsupported(t *testing.T) {
	c := validChild()
	c.Owner = "other"
	if _, _, err := Takeover(c, "worker"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("foreign accepted: %v", err)
	}
	if _, _, err := Takeover(validChild(), ""); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("empty actor accepted: %v", err)
	}
}
func TestAckLeavesReplaced(t *testing.T) {
	c := validChild()
	want := ` { "from": "scheduler", "message": "due", "requested_at": "2026-10-13T10:00:00Z" } `
	c.Metadata["notify.d29ya2VyL2luYm94"] = json.RawMessage(want)
	c.Metadata["notify.b3RoZXI"] = json.RawMessage(`{"message":"other"}`)
	key, saved, err := AckOp(c, "worker/inbox")
	if err != nil || key != "notify.d29ya2VyL2luYm94" || string(saved) != want {
		t.Fatal(key, string(saved), err)
	}
	c.Metadata[key][0] = '\n'
	if string(saved) != want {
		t.Fatal("saved value aliases mutable child metadata")
	}
	c.Metadata[key] = json.RawMessage(`{"message":"replacement"}`)
	if string(saved) == string(c.Metadata[key]) {
		t.Fatal("replacement changed saved guard")
	}
}
func TestAckRequiresCurrentJSON(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`broken`)} {
		c := validChild()
		c.Metadata["notify.d29ya2VyL2luYm94"] = raw
		if _, _, err := AckOp(c, "worker/inbox"); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("invalid saved JSON: %v", err)
		}
	}
	if _, _, err := AckOp(validChild(), ""); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}
