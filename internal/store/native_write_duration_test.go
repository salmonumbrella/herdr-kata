package store

import (
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"testing"
	"time"
)

func TestNativeJobDraftRejectsNegativeTimeoutAndAllowsDefault(t *testing.T) {
	r := &NativeRepository{Client: &katacli.Client{}}
	for _, duration := range []time.Duration{-time.Nanosecond, -time.Minute, 0, time.Second} {
		_, err := r.JobDraft(Job{Name: "Inspect", Prompt: "Inspect", Timeout: duration})
		if (err != nil) != (duration < 0) {
			t.Errorf("timeout %s: %v", duration, err)
		}
	}
}
