//go:build crashpoint

// Package crashpoint exposes process-kill boundaries only in fault-test builds.
package crashpoint

var Hook func(string)

func Hit(point string) {
	if Hook != nil {
		Hook(point)
	}
}
