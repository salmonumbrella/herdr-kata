package runner

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
)

// WorkflowSpace groups one workflow run's step tabs in a reusable workspace.
type WorkflowSpace struct {
	WorkspaceID string
	Label       string
	// RootTabID is the tab herdr insists on making when it creates a workspace.
	// Nothing ever runs in it — every step opens a tab of its own — so it sits in
	// the room as an empty shell that an operator has to look past to find the
	// one that matters. Carrying its id is what lets the run reap it once the
	// space holds a tab somebody actually wants.
	//
	// Deliberately not written to the run record. A resume finds the space
	// already there, which means the attempt that created it has already reaped
	// its root tab: there is nothing left to close and nothing worth remembering
	// across processes. Persisting it would add a schema column whose only
	// possible value on read is "already handled".
	RootTabID string
}

// Usable reports whether steps can be pointed at this space.
func (s *WorkflowSpace) Usable() bool {
	return s != nil && strings.TrimSpace(s.WorkspaceID) != ""
}

func SpaceLabel(workflowID string) string {
	id := strings.TrimSpace(workflowID)
	if id == "" {
		id = "workflow"
	}
	return "WORKFLOWS:" + id + ":" + nanoID6()
}

// parkedMark separates a space's name from the verdict appended when its run
// parked. A separator rather than a rewrite, so the original name is recoverable
// by cutting at it: a resume has to put the label back, and recomputing it is
// not possible — SpaceLabel's suffix is random, and a resumed run that renamed
// its space to a fresh name would be a different room to anyone watching.
const parkedMark = " · "

// LabelParked is the space's name with why its run stopped on the end.
//
// The room is what an operator sees first, and an open space named exactly as it
// was while running says only that something happened. It is the label, not a
// message, because a label survives being scrolled past.
func LabelParked(label, stoppedAt string, reason ParkReason) string {
	base := LabelBase(label)
	verdict := "parked"
	if stoppedAt != "" {
		verdict += " " + stoppedAt
	}
	if reason != "" {
		verdict += " (" + string(reason) + ")"
	}
	return base + parkedMark + verdict
}

// LabelBase is the space's name without any parked verdict, which is what a
// resume renames it back to.
func LabelBase(label string) string {
	base, _, found := strings.Cut(label, parkedMark)
	if !found {
		return label
	}
	return base
}

const nanoIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-"

// nanoID6 returns six characters from Nano ID's URL-safe alphabet.
//
// There are exactly 64 choices, so taking the low six bits of each
// cryptographically random byte gives every character the same probability.
// crypto/rand.Read on the Go version this module targets either fills the
// buffer or terminates the process if the operating system's random source is
// irrecoverably unavailable, which leaves no weaker fallback id to collide.
func nanoID6() string {
	var random [6]byte
	rand.Read(random[:])
	var id [6]byte
	for i, b := range random {
		id[i] = nanoIDAlphabet[b&63]
	}
	return string(id[:])
}

// HerdrKataBin resolves this executable for explicit step commands.
func HerdrKataBin() string {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		return "herdr-kata"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != "" {
		return resolved
	}
	return exe
}
