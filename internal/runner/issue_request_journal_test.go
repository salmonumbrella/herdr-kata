package runner

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

func TestIssueRequestJournalVersions(t *testing.T) {
	for _, kind := range []string{"request-v2", "cron-v1", "request-v1", "missing-envelope", "missing-proof", "foreign-receipt", "invalid-request", "fake-cron"} {
		t.Run(kind, func(t *testing.T) {
			proof := false
			r := &Run{Version: 2, UID: "01K00000003CJTVSTHD1P8EVCV", Source: "issue-request:01K00000000000000000000001", Phase: Prepared, IssueUID: "01K00000000000000000000001", JournalID: "journal", Admitted: &proof, Target: Target{Identity: kata.Identity{InstanceUID: "instance", ProjectUID: "01K00000000000000000000009", Actor: "worker"}, Executor: "installation"}, IssueRequest: &IssueRequestRun{Request: sched.ExecuteRequest{Version: 1, Kind: "execute", RequestUID: "01K00000000000000000000004", RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}, ParentUID: "01K00000000000000000000008", Binding: sched.Binding{Profile: "default"}, Ownership: sched.Ownership{Decision: "claimed", Revision: 1}, NotifyKey: "notify.d29ya2Vy", NotifyJSON: json.RawMessage(`{"message":"execute"}`)}}
			r.IssueRequest.Admission = requestReceipt(r)
			switch kind {
			case "cron-v1":
				r.Version = 1
				r.Source = "job"
				r.IssueRequest = nil
			case "request-v1":
				r.Version = 1
			case "missing-envelope":
				r.IssueRequest = nil
			case "missing-proof":
				r.Admitted = nil
			case "foreign-receipt":
				r.IssueRequest.Admission.JournalID = "another-journal"
			case "invalid-request":
				r.IssueRequest.Request.RequestUID = "invalid"
			case "fake-cron":
				r.Job.UID = "invented-delivery"
			}
			s := testStore(t)
			err := s.Create(r)
			valid := kind == "request-v2" || kind == "cron-v1"
			if valid {
				if err != nil {
					t.Fatal(err)
				}
				saved, err := s.Load(r.UID)
				if err != nil || saved.Version != r.Version {
					t.Fatal("journal did not round trip", err)
				}
			} else if err == nil {
				t.Fatal("invalid journal persisted")
			}
		})
	}
}
