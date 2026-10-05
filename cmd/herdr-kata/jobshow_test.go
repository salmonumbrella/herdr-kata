package main

import (
	"testing"
)

// The read-only half of the job verbs: show, list, prune.
//
// None of them changes anything, which is why a wrong answer here is expensive
// rather than obvious. `job show` is what somebody reads before deciding a job
// is configured correctly, so a field it silently omits is a field nobody
// checks; the agent argv line is the only place the flags a run will actually
// carry are visible before it runs. `job list` and `job prune` already have
// their inner halves tested against a real database in prune_test.go -- what is
// tested here is the argv layer above them, where a flag that is parsed but
// never passed down turns --tag into "show everything" and --yes into a
// deletion nobody confirmed.

// showJob stores a job and returns what `job show` printed for it.
func TestJobShowNeedsAnIDThatExists(t *testing.T) {
	jobCmdEnv(t)
	if err := jobShow(nil); err == nil {
		t.Error("job show with no id must fail rather than pick a job")
	}
	if _, err := captureStdout(t, func() error { return jobShow([]string{"nope"}) }); err == nil {
		t.Error("job show on an unknown id must fail, not print an empty job")
	}
}

// --tag has to reach the filter. A parsed-but-ignored flag is invisible on a
// machine with few jobs and wrong on the one this runs on.
