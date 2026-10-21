package inbox

import (
	"encoding/json"
	"fmt"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

// ValidateRequest requires explicit metadata; notification text is never authority.
func ValidateRequest(issue kata.IssueView, project string) (sched.ExecuteRequest, error) {
	var request sched.ExecuteRequest
	if _, cron := issue.Metadata["cron.occurrence"]; cron {
		return request, fmt.Errorf("%w: cron children cannot switch to explicit execution", ErrUnsupported)
	}
	parent, err := parentOf(issue)
	if issue.ProjectUID != project || !validUID(project) || !validUID(issue.UID) || issue.Status != "open" || err != nil || !validUID(parent) {
		return request, fmt.Errorf("%w: request must be an open child in the configured project", ErrUnsupported)
	}
	if err := json.Unmarshal(issue.Metadata["herdr_kata.request"], &request); err != nil || request.Version != 1 || request.Kind != "execute" || !validUID(request.RequestUID) || request.RequestedAt.IsZero() {
		return request, fmt.Errorf("%w: invalid herdr_kata.request", ErrUnsupported)
	}
	_, offset := request.RequestedAt.Zone()
	if offset != 0 {
		return request, fmt.Errorf("%w: requested_at must be UTC", ErrUnsupported)
	}
	request.RequestedAt = request.RequestedAt.UTC()
	return request, nil
}

// Native children cannot replace or inherit an explicit request's admission.
func ValidateNativeSource(issue kata.IssueView) error {
	for _, key := range []string{"herdr_kata.request", "herdr_kata.admission"} {
		if _, exists := issue.Metadata[key]; exists {
			return fmt.Errorf("%w: explicit request cannot switch to native execution", ErrUnsupported)
		}
	}
	return nil
}
