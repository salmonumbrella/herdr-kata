package main

import "testing"

func TestRemovedCollaborationCommandsUnavailable(t *testing.T) {
	for _, name := range []string{"thread", "room", "forum", "memory", "index", "check"} {
		if _, ok := commands()[name]; ok {
			t.Errorf("removed command %q remains dispatchable", name)
		}
	}
}
