package store

import (
	"strings"
	"time"
)

type Identity struct {
	Name  string
	JobID string
	RunID string
	// PID is an explicit interactive holder discriminator, such as a pane id.
	PID string
}

// Valid reports whether the identity can be attributed to anyone.
func (i Identity) Valid() bool { return strings.TrimSpace(i.Name) != "" }

// Equal compares every holder discriminator, including empty values.
func (i Identity) Equal(other Identity) bool { return i == other }

// String renders the identity for a human reading a status line.
func (i Identity) String() string {
	name := i.Short()
	switch {
	case i.RunID != "":
		return name + " (" + i.RunID + ")"
	case i.JobID != "" && i.JobID != i.Name:
		return name + " (" + i.JobID + ")"
	default:
		return name
	}
}

// Short formats the name and optional interactive holder discriminator.
func (i Identity) Short() string {
	if i.PID == "" {
		return i.Name
	}
	return i.Name + "#" + i.PID
}

func resolveNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}
