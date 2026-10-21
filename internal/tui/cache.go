package tui

// The cache reads the worker's local snapshots without polling Kata.

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type Data struct {
	Project                string
	Projects               map[string]Data
	Running, WorkerRunning bool
	Jobs                   []defs.JobEntry
	Workflows              []kata.Workflow
	Runs                   []runner.Run
	Inbox                  inbox.Snapshot
	Checkpoints            map[string]sched.Checkpoint
	Route                  map[string]any
	Health                 map[string]any
	Serve                  map[string]any
}
type cachedFile struct {
	mtime time.Time
	size  int64
	value any
	err   error
}

// Cache has one owner: the outstanding Bubble Tea poll command.
type snapshotCache struct {
	root  string
	fs    state.FS
	files map[string]cachedFile
	data  Data
}

func newCache(root string, fs state.FS) *snapshotCache {
	if fs == nil {
		fs = state.OSFS{}
	}
	return &snapshotCache{root: root, fs: fs, files: map[string]cachedFile{}}
}
func (c *snapshotCache) Poll() (Data, error) {
	paths := []string{"state.json", "serve.json"}
	// Directory listings detect added/removed lanes; file contents retain the
	// same mtime cache as the single-project board.
	entries, dirErr := c.fs.ReadDir(filepath.Join(c.root, "projects"))
	if dirErr != nil && !errors.Is(dirErr, fs.ErrNotExist) {
		return c.data, dirErr
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		base := filepath.Join("projects", entry.Name())
		for _, name := range []string{"state.json", "defs.json", "inbox.json", "route.json"} {
			paths = append(paths, filepath.Join(base, name))
		}
		jobs, e := c.fs.ReadDir(filepath.Join(c.root, base, "jobs"))
		if e != nil && !errors.Is(e, fs.ErrNotExist) {
			return c.data, e
		}
		for _, job := range jobs {
			if !job.IsDir() && strings.HasSuffix(job.Name(), ".json") {
				paths = append(paths, filepath.Join(base, "jobs", job.Name()))
			}
		}
	}
	for _, dir := range []string{"runs"} {
		entries, err := c.fs.ReadDir(filepath.Join(c.root, dir))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return c.data, err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if dir == "runs" && e.IsDir() {
				paths = append(paths, filepath.Join(dir, e.Name(), "run.json"))
			}
		}
	}
	live := map[string]bool{}
	changed := false
	var problem error
	for _, p := range paths {
		full := filepath.Join(c.root, p)
		info, err := c.fs.Lstat(full)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		live[p] = true
		if err != nil {
			problem = err
			continue
		}
		old, ok := c.files[p]

		if ok && old.mtime.Equal(info.ModTime()) && old.size == info.Size() {
			if old.err != nil {
				problem = old.err
			}
			continue
		}
		value, err := c.read(p, full)
		old.mtime, old.size, old.err = info.ModTime(), info.Size(), err
		if err != nil {
			problem = err
			c.files[p] = old
			continue
		}
		old.value = value
		c.files[p] = old
		changed = true

	}
	for p := range c.files {
		if !live[p] {
			delete(c.files, p)
			changed = true
		}
	}
	marker, _ := c.files["state.json"].value.(map[string]any)
	if marker != nil && (marker["version"] != float64(1) || len(marker) != 1) {
		return c.data, fmt.Errorf("%s: unsupported state layout; use the matching or newer herdr-kata build", filepath.Join(c.root, "state.json"))
	}
	if changed {
		d := Data{Checkpoints: map[string]sched.Checkpoint{}, Projects: map[string]Data{}}
		keys := make([]string, 0, len(c.files))
		for p := range c.files {
			keys = append(keys, p)
		}
		sort.Strings(keys)
		// Read pins before decorating the corresponding snapshots.
		for _, p := range keys {
			parts := strings.Split(filepath.ToSlash(p), "/")
			if len(parts) == 3 && parts[0] == "projects" && parts[2] == "state.json" {
				pin, _ := c.files[p].value.(map[string]any)
				name, _ := pin["name"].(string)
				if name == "" {
					name = parts[1]
				}
				d.Projects[parts[1]] = Data{Project: name, Health: pin, Checkpoints: map[string]sched.Checkpoint{}}
			}
		}
		for _, p := range keys {
			file := c.files[p]
			parts := strings.Split(filepath.ToSlash(p), "/")
			uid := ""
			lane := &d
			if len(parts) >= 3 && parts[0] == "projects" {
				uid = parts[1]
				copy, pinned := d.Projects[uid]
				if !pinned {
					continue
				}
				lane = &copy
			}
			switch v := file.value.(type) {
			case defs.Snapshot:
				v.Jobs = append([]defs.JobEntry(nil), v.Jobs...)
				v.Workflows = append([]kata.Workflow(nil), v.Workflows...)
				for i := range v.Jobs {
					v.Jobs[i].Project = lane.Project
					v.Jobs[i].ProjectUID = uid
				}
				for i := range v.Workflows {
					v.Workflows[i].Project = lane.Project
					v.Workflows[i].ProjectUID = uid
				}
				lane.Jobs = v.Jobs
				lane.Workflows = v.Workflows
			case inbox.Snapshot:
				v.Requests = append([]inbox.Request(nil), v.Requests...)
				for i := range v.Requests {
					v.Requests[i].Project = lane.Project
					v.Requests[i].ProjectUID = uid
				}
				lane.Inbox = v
			case runner.Run:
				d.Runs = append(d.Runs, v)
			case sched.Checkpoint:
				lane.Checkpoints[v.JobUID] = v
			case map[string]any:
				switch filepath.Base(p) {
				case "serve.json":
					d.Serve = v
				case "route.json":
					lane.Route = v
				case "state.json":
					lane.Health = v
				}
			}
			if uid != "" {
				d.Projects[uid] = *lane
			}
		}
		for _, entry := range entries {
			if lane, ok := d.Projects[entry.Name()]; ok {
				d.Jobs = append(d.Jobs, lane.Jobs...)
				d.Workflows = append(d.Workflows, lane.Workflows...)
				d.Inbox.Requests = append(d.Inbox.Requests, lane.Inbox.Requests...)
				for k, v := range lane.Checkpoints {
					d.Checkpoints[entry.Name()+"\x00"+k] = v
				}
				if len(d.Projects) == 1 {
					d.Route = lane.Route
					d.Health = lane.Health
					d.Inbox = lane.Inbox
				}
			}
		}
		if len(d.Projects) > 1 {
			initialized := false
			scanErrors := []string{}
			for _, entry := range entries {
				lane := d.Projects[entry.Name()]
				if lane.Inbox.Version == 0 {
					continue
				}
				d.Inbox.Version = 1
				if !initialized || lane.Inbox.LastSuccessfulScan.Before(d.Inbox.LastSuccessfulScan) {
					d.Inbox.LastSuccessfulScan = lane.Inbox.LastSuccessfulScan
				}
				initialized = true
				if lane.Inbox.Error != "" {
					scanErrors = append(scanErrors, lane.Project+": "+lane.Inbox.Error)
				}
			}
			d.Inbox.Error = strings.Join(scanErrors, "; ")
		}
		problems := []string{}
		for _, entry := range entries {
			lane, ok := d.Projects[entry.Name()]
			if !ok {
				continue
			}
			if lane.Route["allowed"] == false {
				detail, _ := lane.Route["error"].(string)
				problems = append(problems, lane.Project+": "+detail)
			}
		}
		if len(d.Projects) > 1 && len(problems) > 0 {
			d.Route = map[string]any{"allowed": false, "error": strings.Join(problems, "; ")}
		}
		for i := range d.Runs {
			r := &d.Runs[i]
			lane, ok := d.Projects[r.Target.Identity.ProjectUID]
			detail, _ := lane.Route["error"].(string)
			if !ok {
				r.Reason = "waiting for project"
				if r.ProjectName != "" {
					r.Reason += " " + r.ProjectName
				}
			} else if strings.HasPrefix(detail, "waiting for project ") {
				r.Reason = detail
			}
		}
		sort.Slice(d.Runs, func(i, j int) bool { return d.Runs[i].UID > d.Runs[j].UID })
		c.data = d
	}

	c.data.Running = livePID(c.data.Serve, "pid")
	c.data.WorkerRunning = false
	lockPath := filepath.Join(c.root, "worker.lock")
	if _, err := c.fs.Lstat(lockPath); err == nil {
		c.data.WorkerRunning, err = state.Held(lockPath)
		if err != nil {
			problem = err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		problem = err
	}
	return c.data, problem
}

func (c *snapshotCache) read(p, full string) (any, error) {
	switch {
	case filepath.Base(p) == "defs.json":
		return defs.ReadSnapshot(c.fs, full)
	case filepath.Base(p) == "inbox.json":
		return inbox.ReadSnapshot(c.fs, filepath.Dir(full))
	case strings.HasPrefix(p, "runs/"):
		v := runner.Run{}
		err := state.ReadJSON(c.fs, full, 4<<20, &v)
		return v, err
	case strings.HasPrefix(p, "jobs/") || strings.Contains(filepath.ToSlash(p), "/jobs/"):
		v := sched.Checkpoint{}
		err := state.ReadJSON(c.fs, full, 64<<10, &v)
		return v, err
	default:
		v := map[string]any{}
		err := state.ReadJSON(c.fs, full, 64<<10, &v)
		return v, err
	}
}

func livePID(row map[string]any, key string) bool {
	pid, ok := row[key].(float64)
	if !ok || pid <= 0 || pid > 1<<30 || pid != float64(int(pid)) {
		return false
	}
	err := syscall.Kill(int(pid), 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
