//go:build kata

package kata

import (
	"encoding/json"
	"testing"
)

func TestIntegJobOriginalAuthor(t *testing.T) {
	c := newKata(t)
	j := fixtureJob(t, c)
	var wire struct {
		Job struct {
			Author string `json:"author"`
		} `json:"job"`
	}
	requireNoError(t, json.Unmarshal(rawKata(t, c, "cron", "job", "show", j.UID, "--project", c.cfg.Project), &wire))
	if wire.Job.Author != "worker" {
		t.Fatalf("released CLI original author = %q, want worker", wire.Job.Author)
	}
	t.Log("released CLI job show exposes original author worker")
	cfg := c.cfg
	cfg.Actor = "colleague"
	colleague := New(cfg, c.stateDir, nil, nil)
	j.Name = "Edited shared job"
	updated, err := colleague.SaveJob(t.Context(), j, "")
	requireNoError(t, err)
	got, err := colleague.ShowJob(t.Context(), j.UID)
	requireNoError(t, err)
	jobs, err := colleague.ListJobs(t.Context())
	requireNoError(t, err)
	if updated.Author != "worker" || got.Author != "worker" || len(jobs) != 1 || jobs[0].Author != "worker" || got.Name != "Edited shared job" {
		t.Fatalf("shared job must remain readable with original author after colleague edit: updated=%+v shown=%+v listed=%+v", updated, got, jobs)
	}
}
