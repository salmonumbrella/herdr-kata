package inbox

import (
	"context"
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"testing"
)

func TestExplicitIssueRequestDiscovery(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary notification", true: "explicit request"}[explicit], func(t *testing.T) {
			s, reader, _, got := scanFixture(t)
			child := reader.issues[reader.requests[0].Ref]
			delete(child.Metadata, "cron.occurrence")
			if explicit {
				child.Metadata["herdr_kata.request"] = json.RawMessage(`{"version":1,"kind":"execute","request_uid":"01K00000000000000000000004","requested_at":"2026-01-02T03:04:05Z"}`)
			}
			reader.issues[child.QualifiedID] = child
			parent := reader.issues[parentUID]
			parent.Status = "open"
			reader.issues[parentUID] = parent
			if err := s.scan(t.Context()); err != nil {
				t.Fatal(err)
			}
			want := 0
			if explicit {
				want = 1
			}
			if len(got) != want {
				t.Fatalf("dispatched %d runs, want %d", len(got), want)
			}
		})
	}
}

func TestExplicitIssueRequestRejectsInvalidTargets(t *testing.T) {
	for _, field := range []string{"closed", "foreign-project", "no-parent", "foreign-owner", "not-ready", "unknown-profile", "closed-parent", "bad-request"} {
		t.Run(field, func(t *testing.T) {
			s, reader, _, got := scanFixture(t)
			ref := reader.requests[0].Ref
			child := reader.issues[ref]
			delete(child.Metadata, "cron.occurrence")
			child.Metadata["herdr_kata.request"] = json.RawMessage(`{"version":1,"kind":"execute","request_uid":"01K00000000000000000000004","requested_at":"2026-01-02T03:04:05Z"}`)
			parent := reader.issues[parentUID]
			parent.Status = "open"
			switch field {
			case "closed":
				child.Status = "closed"
			case "foreign-project":
				child.ProjectUID = "01K00000000000000000000006"
			case "no-parent":
				child.ParentUID = ""
				child.Links = nil
			case "foreign-owner":
				child.Owner = "someone-else"
			case "not-ready":
				reader.ready = nil
			case "unknown-profile":
				parent.Metadata = map[string]json.RawMessage{"herdr_kata.profile": json.RawMessage(`"missing"`)}
			case "closed-parent":
				parent.Status = "closed"
			case "bad-request":
				child.Metadata["herdr_kata.request"] = json.RawMessage(`{"version":2,"kind":"execute"}`)
			}
			reader.issues[ref] = child
			reader.issues[parentUID] = parent
			if err := s.scan(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatal("invalid request dispatched")
			}
		})
	}
}

func TestScannerPreventsExecutionSourceSwitch(t *testing.T) {
	for _, key := range []string{"herdr_kata.request", "herdr_kata.admission"} {
		t.Run(key, func(t *testing.T) {
			s, reader, _, got := scanFixture(t)
			child := reader.issues[reader.requests[0].Ref]
			child.Metadata[key] = json.RawMessage(`{"version":1,"kind":"execute","request_uid":"01K00000000000000000000004","requested_at":"2026-01-02T03:04:05Z"}`)
			reader.issues[child.QualifiedID] = child
			parent := reader.issues[parentUID]
			parent.Status = "open"
			reader.issues[parentUID] = parent
			if err := s.scan(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatal("child switched execution sources despite permanent provenance")
			}
		})
	}
}

func TestScannerExplicitRequestDisposition(t *testing.T) {
	s, reader, _, got := scanFixture(t)
	child := reader.issues[reader.requests[0].Ref]
	delete(child.Metadata, "cron.occurrence")
	child.Metadata["herdr_kata.request"] = json.RawMessage(`{"version":1,"kind":"execute","request_uid":"01K00000000000000000000004","requested_at":"2026-01-02T03:04:05Z"}`)
	reader.issues[child.QualifiedID] = child
	// An admission veto or a lost journal remains meaningful even if readiness
	// or the parent binding has changed since the original request.
	reader.ready = nil
	s.CheckRequest = func(_ context.Context, issue kata.IssueView) (string, string, error) {
		if issue.UID != childUID {
			t.Fatal(issue.UID)
		}
		return IgnoredRequest, "local state lost; inspect the original run", nil
	}
	if err := s.scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatal("vetoed request dispatched")
	}
	if len(s.snapshot.Requests) != 1 || s.snapshot.Requests[0].Status != IgnoredRequest || s.snapshot.Requests[0].Reason != "local state lost; inspect the original run" {
		t.Fatalf("incorrect disposition: %+v", s.snapshot)
	}
}
