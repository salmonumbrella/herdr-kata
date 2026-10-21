package state

import (
	"os"
	"path/filepath"
	"strconv"
)

const pluginID = "kata"

type Layout struct{ Root string }

func DefaultRoot() string {
	if p := os.Getenv("HERDR_PLUGIN_STATE_DIR"); p != "" {
		return p
	}
	if p := os.Getenv("XDG_STATE_HOME"); p != "" {
		return filepath.Join(p, "herdr", "plugins", pluginID)
	}
	home := HomeDir()
	return filepath.Join(home, ".local", "state", "herdr", "plugins", pluginID)
}
func (l Layout) StateFile() string               { return filepath.Join(l.Root, "state.json") }
func (l Layout) ServeLock() string               { return filepath.Join(l.Root, "serve.lock") }
func (l Layout) WorkerLock() string              { return filepath.Join(l.Root, "worker.lock") }
func (l Layout) ServeInfo() string               { return filepath.Join(l.Root, "serve.json") }
func (l Layout) StopFile() string                { return filepath.Join(l.Root, "stop") }
func (l Layout) UpgradeFile() string             { return filepath.Join(l.Root, "upgrade") }
func (l Layout) RequestsDir() string             { return filepath.Join(l.Root, "requests") }
func (l Layout) Workspace() string               { return filepath.Join(l.Root, "workspace.json") }
func (l Layout) EventsCursor() string            { return filepath.Join(l.Root, "events.cursor") }
func (l Layout) DefsSnapshot() string            { return filepath.Join(l.Root, "defs.json") }
func (l Layout) RouteFile() string               { return filepath.Join(l.Root, "route.json") }
func (l Layout) BinDir() string                  { return filepath.Join(l.Root, "bin") }
func (l Layout) JobCheckpoint(uid string) string { return filepath.Join(l.Root, "jobs", uid+".json") }
func (l Layout) RunDir(uid string) string        { return filepath.Join(l.Root, "runs", uid) }
func (l Layout) RunFile(uid string) string       { return filepath.Join(l.RunDir(uid), "run.json") }
func (l Layout) AttemptDir(uid, step string, n int) string {
	return filepath.Join(l.RunDir(uid), "steps", step, strconv.Itoa(n))
}
func (l Layout) QuarantineRunDir(uid string) string {
	return filepath.Join(l.Root, "quarantine", "runs", uid)
}
func (l Layout) LogFile() string { return filepath.Join(l.Root, "log", "herdr-kata.log") }

// Project scopes only lane files. Runs and process controls stay at the root.
func (l Layout) Project(uid string) Layout {
	return Layout{Root: filepath.Join(l.Root, "projects", uid)}
}
