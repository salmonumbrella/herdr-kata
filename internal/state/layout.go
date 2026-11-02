// Package state defines the private on-disk layout.
package state

import (
	"fmt"
	"path/filepath"
)

type Layout struct{ Root string }

func (l Layout) Run(key string) string { return filepath.Join(l.Root, "runs", key) }
func (l Layout) Attempt(key, step string, n int) string {
	return filepath.Join(l.Run(key), "attempts", step, fmt.Sprint(n))
}
func (l Layout) Job(uid string) string { return filepath.Join(l.Root, "jobs", uid+".json") }
func (l Layout) Requests() string      { return filepath.Join(l.Root, "requests") }
func (l Layout) Lock() string          { return filepath.Join(l.Root, "serve.lock") }
func (l Layout) PID() string           { return filepath.Join(l.Root, "serve.pid") }
