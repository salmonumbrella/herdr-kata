package sched

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/store"
)

// RunAt here is an ordinary native planning-date projection, never a civil
// timestamp parsed in the plugin. Lead subtracts exactly once, in UTC.
func FuzzNativeDateDueCheckedLead(f *testing.F) {
	f.Add(int64(1760000000), int64(1800), true)
	f.Add(int64(-62135596800), int64(1), true)
	f.Add(int64(-62167219200), int64(0), true)
	f.Add(int64(1760000000), int64(math.MaxInt64), true)
	f.Add(int64(1760000000), int64(-1), true)
	f.Add(int64(1760000000), int64(0), false)
	f.Fuzz(func(t *testing.T, seconds, lead int64, deadline bool) {
		// Materialize one finite time; the drawn integers retain their full domain.
		source := time.Unix(seconds%253402300800, 0).UTC()
		kind := "issue-scheduled"
		if deadline {
			kind = "issue-deadline"
		}
		j := store.Job{ID: "date-source", Enabled: true, Schedule: store.ScheduleType(kind), Catchup: store.CatchupLatest, RunAt: &source, NativeDefinition: json.RawMessage(fmt.Sprintf(`{"trigger":{"kind":%q,"lead_seconds":%d}}`, kind, lead))}
		now := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
		fires, err := Due(j, time.Time{}, now)
		valid := lead >= 0 && lead <= math.MaxInt64/int64(time.Second) && (deadline || lead == 0) && source.Year() >= 0 && source.Year() <= 9999
		var want time.Time
		if valid {
			want = time.Unix(source.Unix()-lead, 0).UTC()
			valid = want.Year() >= 0 && want.Year() <= 9999
		}
		if !valid {
			if err == nil {
				t.Fatalf("unsafe lead/time accepted: source=%s lead=%d deadline=%v fires=%v", source, lead, deadline, fires)
			}
			return
		}
		if err != nil || len(fires) != 1 || !fires[0].Equal(want) {
			t.Fatalf("native UTC source/lead changed: source=%s lead=%d want=%s fires=%v err=%v", source, lead, want, fires, err)
		}
	})
}
