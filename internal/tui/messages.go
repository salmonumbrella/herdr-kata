package tui

import (
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type editorClosedMsg struct {
	Message  string
	Target   editorTarget
	Saved    *kata.Job
	Favorite *bool
}

type newEditorMsg struct {
	generation int
	editor     form
}

type openKataRetryMsg struct{}

type tickMsg time.Time

type dataMsg struct {
	data Data
	err  error
}

type actionMsg struct {
	status string
	err    error
}
