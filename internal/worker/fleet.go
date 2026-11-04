package worker

import (
	"context"
	"github.com/salmonumbrella/herdr-kata/internal/attempt"
	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/report"
	"slices"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type capsEntry struct {
	at    time.Time
	caps  kata.Caps
	known bool
}

func (w *Worker) capsFor(project string, now time.Time) (kata.Caps, bool, error) {
	entry := w.caps[project]
	if !entry.at.IsZero() && now.Before(entry.at.Add(5*time.Minute)) {
		return entry.caps, entry.known, nil
	}
	caps, err := w.Clients[project].Capabilities()
	entry.at = now
	if err == nil {
		entry.caps = caps
		entry.known = true
	}
	w.caps[project] = entry
	return entry.caps, entry.known, err
}

func (w *Worker) controls(ctx context.Context, k kata.Kata, project string, caps kata.Caps, now time.Time) ([]kata.CronRequest, error) {
	if !slices.Contains(caps.Controls, "run_request") && !slices.Contains(caps.Controls, "cancel_request") {
		return nil, nil
	}
	requests, err := k.Requests()
	if err != nil {
		return nil, err
	}
	runs, err := w.Store.Active()
	if err != nil {
		return nil, err
	}
	now = clock.Current(ctx, now)
	for _, req := range requests {
		if req.Kind != "cancel" || !slices.Contains(caps.Controls, "cancel_request") || !now.Before(req.ExpiresAt) {
			continue
		}
		for _, r := range runs {
			if r.Project == project && r.RunUID == req.RunUID && r.Phase == "running" && r.Detail == "" {
				if err = w.Store.Update(r.Key, func(r *journal.Run) error {
					r.ExpiresAt = now
					r.Detail = "cancel requested: request:" + req.UID
					return nil
				}); err != nil {
					return nil, err
				}
			}
		}
	}
	return requests, nil
}
func cancelled(r journal.Run) bool { return strings.HasPrefix(r.Detail, "cancel requested: request:") }
func (w *Worker) cancel(ctx context.Context, k kata.Kata, r journal.Run, now time.Time) error {
	for idx := range r.Attempts {
		out, err := attempt.Cancel(ctx, w.HC, w.Store, r.Key, idx, now)
		if err != nil {
			return err
		}
		switch o := out.(type) {
		case attempt.Pending:
			return nil
		case attempt.Held:
			return w.held(ctx, k, r, o.Reason, now)
		}
	}
	if err := w.Store.Update(r.Key, func(r *journal.Run) error {
		r.Phase, r.Outcome, r.Hold = "settled", "cancelled", "timeout"
		r.EndedAt = new(now.UTC())
		return nil
	}); err != nil {
		return err
	}
	return report.Final(ctx, k, w.Store, r.Key)
}
