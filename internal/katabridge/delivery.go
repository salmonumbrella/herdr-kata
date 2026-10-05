package katabridge

import (
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"path/filepath"
	"time"
)

type NativeDelivery struct {
	Version    int                     `json:"version"`
	TargetKey  string                  `json:"target_key"`
	ProjectUID string                  `json:"project_uid"`
	Actor      string                  `json:"actor"`
	Teammate   string                  `json:"teammate,omitempty"`
	RunUID     string                  `json:"run_uid"`
	Revision   int64                   `json:"acknowledged_revision"`
	Pending    *katacli.RunObservation `json:"pending,omitempty"`
	Unsent     *katacli.RunObservation `json:"unsent,omitempty"`
	Attempts   int                     `json:"attempt"`
	NextRetry  string                  `json:"next_retry,omitempty"`
	Error      string                  `json:"error,omitempty"`
}

// RetryDelay bounds ordinary write retries; it is not execution policy.
func RetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 7 {
		attempt = 7
	}
	delay := time.Second << (attempt - 1)
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}
func ObservationPaths(dir string) ([]string, error) {
	return filepath.Glob(filepath.Join(dir, "runs", "*", "native-delivery.json"))
}
func ObservationStatus(dir string) (pending, failed int, err error) {
	paths, e := ObservationPaths(dir)
	if e != nil {
		return 0, 0, e
	}
	for _, p := range paths {
		var d NativeDelivery
		if e := statefs.ReadJSON(p, 262144, &d); e != nil {
			return pending, failed, e
		}
		if d.Pending != nil {
			pending++
		}
		if d.Error != "" {
			failed++
		}
	}
	return
}
