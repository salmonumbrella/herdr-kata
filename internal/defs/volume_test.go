package defs

import (
	"strings"
	"testing"
)

// Spec §23.5: per-run adapter jobs that open more than 24 issues a day warn,
// without refusing the job.
func TestClassifyWarnsAboutIssueVolume(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger Trigger
		policy  string
		want    string
	}{
		{"interval half hour", Trigger{Kind: "interval", IntervalSeconds: 1800}, "per-run", "creates about 48 issues a day; consider an existing issue, a run parent, or a native recurring job"},
		{"cron half hour", Trigger{Kind: "cron", Cron: "*/30 * * * *"}, "per-run", "creates about 48 issues a day; consider an existing issue, a run parent, or a native recurring job"},
		{"cron every minute in a zone", Trigger{Kind: "cron", Cron: "* * * * *", Timezone: "America/New_York"}, "per-run", "creates about 1440 issues a day; consider an existing issue, a run parent, or a native recurring job"},
		// The busiest day counts, whatever weekday or day of the month it is.
		{"cron weekends only", Trigger{Kind: "cron", Cron: "* * * * 0,6"}, "per-run", "creates about 1440 issues a day; consider an existing issue, a run parent, or a native recurring job"},
		{"cron on the first of the month", Trigger{Kind: "cron", Cron: "*/5 * 1 * *"}, "per-run", "creates about 288 issues a day; consider an existing issue, a run parent, or a native recurring job"},
		{"cron on the 31st", Trigger{Kind: "cron", Cron: "*/30 * 31 * *"}, "per-run", "creates about 48 issues a day; consider an existing issue, a run parent, or a native recurring job"},
		{"interval hourly", Trigger{Kind: "interval", IntervalSeconds: 3600}, "per-run", ""},
		{"cron hourly", Trigger{Kind: "cron", Cron: "0 * * * *"}, "per-run", ""},
		{"cron weekdays", Trigger{Kind: "cron", Cron: "*/30 9-17 * * 1-5"}, "per-run", ""},
		{"existing issue", Trigger{Kind: "interval", IntervalSeconds: 60}, "existing", ""},
		{"once", Trigger{Kind: "once", At: "2026-01-02T03:04:05Z"}, "per-run", ""},
		{"manual", Trigger{Kind: "manual"}, "per-run", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := fixture(t, "job")
			j.Trigger = tc.trigger
			if tc.policy == "existing" {
				j.Issue = &IssuePolicy{Kind: "existing", UID: "issue"}
			}
			_, s := Classify(j, nil, cfg())
			if !s.Supported {
				t.Fatalf("refused: %+v", s)
			}
			var got []string
			for _, w := range s.Warnings {
				if strings.Contains(w, "issues a day") {
					got = append(got, w)
				}
			}
			if tc.want == "" && len(got) != 0 || tc.want != "" && (len(got) != 1 || got[0] != tc.want) {
				t.Fatalf("warnings=%q, want %q", s.Warnings, tc.want)
			}
			if IsVolumeWarning(tc.want) != (tc.want != "") {
				t.Fatalf("IsVolumeWarning(%q) disagrees", tc.want)
			}
		})
	}
	// A job that already groups its runs is not told to add a run parent.
	j := fixture(t, "job")
	j.Trigger = Trigger{Kind: "interval", IntervalSeconds: 1800}
	j.Options = []byte(`{"run_parent":"01K00000000000000000000001"}`)
	_, s := Classify(j, nil, cfg())
	want := "creates about 48 issues a day; consider an existing issue or a native recurring job"
	if !s.Supported || len(s.Warnings) != 1 || s.Warnings[0] != want || !IsVolumeWarning(want) {
		t.Fatalf("support=%+v, want warning %q", s, want)
	}
	// Kata creates native children; the adapter does not warn for them.
	_, s = Classify(fixture(t, "native"), nil, cfg())
	for _, w := range s.Warnings {
		if IsVolumeWarning(w) {
			t.Fatalf("native job warned: %q", w)
		}
	}
}
