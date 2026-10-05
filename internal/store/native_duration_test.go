package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
)

func TestNativeJobDurationProjectionBoundaries(t *testing.T) {
	for _, field := range []string{"interval_seconds", "timeout_seconds"} {
		for _, seconds := range []int64{9223372035, 9223372036, 9223372037} {
			t.Run(fmt.Sprintf("%s/%d", field, seconds), func(t *testing.T) {
				interval, timeout := int64(1), int64(0)
				if field == "interval_seconds" {
					interval = seconds
				} else {
					timeout = seconds
				}
				raw := fmt.Sprintf(`{"version":1,"kind":"job","trigger":{"kind":"interval","interval_seconds":%d},"timeout_seconds":%d,"action":{"kind":"execute","prompt":"Inspect"},"issue":{"kind":"per-run","title":"Inspect"},"overlap":"forbid","catchup":"latest"}`, interval, timeout)
				r := &NativeRepository{}
				j, err := r.JobFrom(katacli.Definition{Definition: []byte(raw)}, false)
				if seconds > 9223372036 {
					if err == nil {
						t.Fatal("overflowing portable duration projected without diagnostic")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if j.Timeout != time.Duration(timeout)*time.Second || int64(j.IntervalSeconds) != interval {
					t.Fatalf("duration changed: %+v", j)
				}
			})
		}
	}
}
