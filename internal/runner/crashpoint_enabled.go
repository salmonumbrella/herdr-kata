//go:build crashpoint

package runner

import "encoding/json"

func journalSnapshot(r *Run) []byte { b, _ := json.Marshal(r); return b }
func journalPoint(before []byte, r *Run) string {
	var old Run
	_ = json.Unmarshal(before, &old)
	switch {
	case len(old.ConsumedResumes) != len(r.ConsumedResumes):
		return "resume-acceptance"
	case old.Loop.Used != r.Loop.Used:
		return "loop-edge"
	case old.Herdr.PaneID == "" && r.Herdr.PaneID != "":
		return "tab-provenance"
	case old.IssueUID != r.IssueUID:
		return "create-result"
	case !old.Herdr.LaunchIntent && r.Herdr.LaunchIntent:
		return "launch-intent"
	}
	for i, s := range r.Steps {
		if i >= len(old.Steps) {
			continue
		}
		prev := old.Steps[i]
		if s.Herdr != nil && s.Herdr.LaunchIntent && (prev.Herdr == nil || !prev.Herdr.LaunchIntent) {
			return "fresh-launch-intent"
		}
		if prev.Submission != s.Submission {
			return "submission-" + string(s.Submission)
		}
		if prev.Result == nil && s.Result != nil {
			return "result"
		}
	}
	for i, op := range r.Ops {
		if i < len(old.Ops) && !old.Ops[i].Done && op.Done {
			return "op-" + op.Kind + "-result"
		}
	}
	if r.Phase == Admitted && old.Phase != Admitted {
		return "admitted"
	}
	if old.EndedAt == nil && r.EndedAt != nil {
		return "settlement"
	}
	if old.Herdr.Session == nil && r.Herdr.Session != nil {
		return "identity"
	}
	return "transition"
}
