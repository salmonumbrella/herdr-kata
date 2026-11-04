package worker

import (
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
