package kata

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type Caps struct {
	EventFeatures []string   `json:"event_features"`
	Features      []string   `json:"features"`
	Controls      []string   `json:"controls"`
	Execution     *Execution `json:"execution"`
}
type Execution struct {
	Replica     bool   `json:"replica"`
	Mode        string `json:"mode"`
	MemberActor string `json:"member_actor"`
}

// Excludes applies the daemon's advertised policy after local ownership.
func (c Caps) Excludes(author string) string {
	if c.Execution == nil || c.Execution.Mode == "all" {
		return ""
	}
	if c.Execution.Mode == "owned" {
		if c.Execution.MemberActor != "" && c.Execution.MemberActor == author {
			return ""
		}
		return fmt.Sprintf("replica execution is limited to jobs authored by %s", c.Execution.MemberActor)
	}
	return "replica execution is not enabled on this daemon"
}
func (c *cli) Capabilities() (Caps, error) {
	var v Caps
	err := c.call(nil, &v, "cron", "capabilities")
	return v, err
}

// JobList keeps the pause advertisement even when definitions are empty.
type JobList struct {
	Jobs         []CronJob       `json:"jobs"`
	ProjectPause json.RawMessage `json:"project_pause"`
}

func (j JobList) Paused() bool {
	return len(j.ProjectPause) > 0 && !bytes.Equal(bytes.TrimSpace(j.ProjectPause), []byte("null"))
}
