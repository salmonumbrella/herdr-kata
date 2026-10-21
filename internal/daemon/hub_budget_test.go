package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// budgetHub is a fake Kata hub that counts every one-shot call and event
// stream start per simulated minute. From refuseAt, every lane is refused:
// capabilities answers refusal and open event streams end. A timing-out hub
// answers every call with timeout and drops each new stream after a minute.
type budgetHub struct {
	mu       sync.Mutex
	started  time.Time
	perMin   map[int]int
	refuseAt time.Duration
	refusal  string
	idle     bool           // projects have no job definitions
	inboxes  bool           // every lane has a runtime inbox, which stays empty
	startup  map[string]int // calls per command in the first ten seconds
	// resets opens each healthy stream with a reset, as Kata does for an
	// empty or stale cursor.
	resets bool
	// replay answers a stream from event id 0 with the project's history, as
	// Kata 0.19.0 does: project.created, then the workflow and job creations.
	replay bool
	// rival gives each lane an interval job due every minute, and the lane
	// lease to installation "rival", kept live as if it renewed on time.
	rival bool
	calls map[string]int // calls per command, for diagnostics
	// leases is the hub's lease issues, shared by every installation using it.
	leases *hubLeases
}

// hubLeases holds each project's lease issue with Kata's revision checks.
type hubLeases struct {
	mu      sync.Mutex
	issues  map[string]*kata.IssueView // by project UID
	live    string                     // an owner whose lease reads as freshly renewed
	streams map[string][]chan string   // open event streams by project UID
	eventID int64
}

// subscribe returns project's lease events for one event stream.
func (l *hubLeases) subscribe(project string) chan string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.streams == nil {
		l.streams = map[string][]chan string{}
	}
	ch := make(chan string, 64)
	l.streams[project] = append(l.streams[project], ch)
	return ch
}

// changed tells project's open streams that its lease issue changed.
// Callers hold mu.
func (l *hubLeases) changed(project, issue string) {
	l.eventID++
	line := fmt.Sprintf(`{"event_id":%d,"type":"issue.metadata_updated","issue_uid":%q,"payload":{"revision_new":%d}}`, 1000+l.eventID, issue, l.issues[project].Revision)
	for _, ch := range l.streams[project] {
		select {
		case ch <- line:
		default:
		}
	}
}

// takeover gives every listed project's lease to owner at the next
// generation, as up --takeover on another installation does.
func (l *hubLeases) takeover(owner string, projects ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.live = owner
	for _, project := range projects {
		issue := l.issues[project]
		var current struct {
			Generation int64 `json:"generation"`
		}
		_ = json.Unmarshal(issue.Metadata["herdr_kata.lease"], &current)
		b, _ := json.Marshal(map[string]any{"version": 1, "owner": owner, "generation": current.Generation + 1, "heartbeat": time.Now().UTC()})
		issue.Metadata = map[string]json.RawMessage{"herdr_kata.lease": b}
		issue.Revision++
		l.changed(project, issue.UID)
	}
}

func (l *hubLeases) handle(project string, args []string) (any, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.issues == nil {
		l.issues = map[string]*kata.IssueView{}
	}
	issue := l.issues[project]
	cmd := strings.Join(args, " ")
	switch {
	case cmd == "list --status all --label herdr-kata:lease --sort oldest --limit 1":
		if issue == nil {
			return map[string]any{"issues": []kata.IssueView{}}, true, nil
		}
		view := *issue
		if l.live != "" {
			var lease map[string]any
			if json.Unmarshal(view.Metadata["herdr_kata.lease"], &lease) == nil && lease["owner"] == l.live {
				lease["heartbeat"] = time.Now().UTC()
				b, _ := json.Marshal(lease)
				view.Metadata = map[string]json.RawMessage{"herdr_kata.lease": b}
			}
		}
		return map[string]any{"issues": []kata.IssueView{view}}, true, nil
	case strings.HasPrefix(cmd, "create --body-stdin --label herdr-kata:lease "):
		if issue == nil {
			issue = &kata.IssueView{UID: "01K0000000000000000000LS" + project[len(project)-2:], ProjectUID: project, Status: "open", Revision: 1, Labels: []string{"herdr-kata:lease"}, Metadata: map[string]json.RawMessage{}}
			l.issues[project] = issue
		}
		return map[string]any{"issue": issue}, true, nil
	case issue != nil && strings.HasPrefix(cmd, "close ") && args[len(args)-1] == issue.UID:
		issue.Status = "closed"
		issue.Revision++
		return map[string]any{"issue": issue}, true, nil
	case issue != nil && len(args) == 9 && args[0] == "meta" && args[1] == "set" && args[4] == "--json-value" && args[6] == issue.UID && args[7] == "herdr_kata.lease":
		if args[3] != fmt.Sprint(issue.Revision) {
			return nil, true, &kata.Error{Code: "revision_conflict"}
		}
		issue.Metadata = map[string]json.RawMessage{"herdr_kata.lease": json.RawMessage(args[8])}
		issue.Revision++
		l.changed(project, issue.UID)
		return map[string]any{"issue": issue}, true, nil
	}
	return nil, false, nil
}

// reply answers a lane lease command as the Kata CLI would, if args is one.
func (l *hubLeases) reply(project string, args []string) ([]byte, int, bool) {
	reply, ok, err := l.handle(project, args)
	if !ok {
		return nil, 0, false
	}
	if ke, isKata := errors.AsType[*kata.Error](err); isKata {
		return []byte(`{"error":{"code":"` + ke.Code + `"}}`), 5, true
	}
	b, _ := json.Marshal(reply)
	return b, 0, true
}

// hold gives project's lease to owner at generation, renewed now.
func (l *hubLeases) hold(project, owner string, generation int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.issues == nil {
		l.issues = map[string]*kata.IssueView{}
	}
	b, _ := json.Marshal(map[string]any{"version": 1, "owner": owner, "generation": generation, "heartbeat": time.Now().UTC()})
	l.issues[project] = &kata.IssueView{UID: "01K0000000000000000000LS" + project[len(project)-2:], ProjectUID: project, Status: "closed", Revision: 3, Labels: []string{"herdr-kata:lease"}, Metadata: map[string]json.RawMessage{"herdr_kata.lease": b}}
}

// rivalJob is the lane's own job; job UIDs differ between projects.
func (h *budgetHub) rivalJob(project string) workerJobEnvelope {
	return workerJobEnvelope{Author: "worker", UID: "01K0000000000000000000J0" + project[len(project)-2:], Name: "quick", DefinitionEventUID: "event", CreatedAt: h.started.Add(-time.Hour), Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"interval","interval_seconds":60},"action":{"kind":"execute","workflow_uid":"wf"},"issue":{"kind":"per-run","title":"Inspect"},"overlap":"forbid","catchup":"skip"}`)}
}

func rivalWorkflow() kata.Workflow {
	return kata.Workflow{UID: "wf", DefinitionEventUID: "wf-event", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"main","kind":"command","command":"true"}]}`)}
}

func (h *budgetHub) count() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.perMin[int(time.Since(h.started)/time.Minute)]++
}

func (h *budgetHub) record(cmd string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Since(h.started) < 10*time.Second {
		h.startup[cmd]++
	}
}

func (h *budgetHub) refused() bool { return h.refusal != "" && time.Since(h.started) >= h.refuseAt }

func (h *budgetHub) factory(t *testing.T, root string) func(config.Config) *kata.CLI {
	return func(view config.Config) *kata.CLI {
		// Each lane needs a distinct ULID-shaped project UID.
		project := "01K000000000000000000000" + strings.TrimPrefix(view.Kata.Project, "project-")
		run := func(_ context.Context, argv, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
			cmd := strings.Join(argv[8:], " ")
			h.count()
			h.record(cmd)
			h.mu.Lock()
			h.calls[strings.Join(argv[8:min(len(argv), 11)], " ")]++
			h.mu.Unlock()
			if h.refused() && (cmd == "cron capabilities" || h.refusal == "timeout") {
				return []byte(`{"error":{"code":"` + h.refusal + `"}}`), nil, 1, nil
			}
			if out, code, ok := h.leases.reply(project, argv[8:]); ok {
				return out, nil, code, nil
			}
			var reply any
			switch {
			case cmd == "health":
				reply = kata.Health{SchemaVersion: 33}
			case cmd == "cron capabilities":
				reply = kata.Capabilities{ProjectUID: project, EventFeatures: []string{"cron_v1"}}
			case cmd == "federation identity":
				reply = map[string]any{"instance_uid": "instance", "auth": map[string]string{"actor": "worker"}}
			case strings.HasPrefix(cmd, "daemon diagnose --expect-project-uid "):
				reply = kata.Diagnosis{State: "ready", Endpoint: "local", ProjectUID: project}
			case h.rival && cmd == "cron job list":
				reply = map[string]any{"jobs": []workerJobEnvelope{h.rivalJob(project)}}
			case h.rival && cmd == "cron workflow list":
				reply = map[string]any{"workflows": []kata.Workflow{rivalWorkflow()}}
			case h.rival && strings.HasPrefix(cmd, "cron workflow show "):
				reply = map[string]any{"workflow": rivalWorkflow()}
			case h.rival && strings.HasPrefix(cmd, "cron job show "):
				reply = map[string]any{"job": h.rivalJob(project)}
			case cmd == "cron job list":
				jobs := []workerJobEnvelope{manualJob()}
				if h.idle {
					jobs = []workerJobEnvelope{}
				}
				reply = map[string]any{"jobs": jobs}
			case cmd == "cron workflow list":
				reply = map[string]any{"workflows": []kata.Workflow{}}
			case strings.HasPrefix(cmd, "inbox --for "):
				reply = map[string]any{"requests": []kata.InboxRequest{}}
			default:
				t.Errorf("unexpected call: %s", cmd)
				return nil, nil, 1, errors.New("unexpected call")
			}
			b, err := json.Marshal(reply)
			return b, nil, 0, err
		}
		tail := func(ctx context.Context, argv, _ []string, _ string) (kata.Proc, error) {
			h.count()
			if h.refused() && h.refusal == "not_found" {
				return kata.Proc{Stdout: strings.NewReader(""), Stderr: strings.NewReader("refused"), Wait: func() error { return errors.New("exit status 1") }}, nil
			}
			if h.refused() {
				// A stalled hub holds the stream for a minute, then drops it.
				r, w := io.Pipe()
				done := make(chan struct{})
				go func() {
					defer close(done)
					timer := time.NewTimer(61 * time.Second)
					defer timer.Stop()
					select {
					case <-ctx.Done():
					case <-timer.C:
					}
					_ = w.Close()
				}()
				return kata.Proc{Stdout: r, Stderr: strings.NewReader(""), Wait: func() error { <-done; return errors.New("exit status 1") }}, nil
			}
			r, w := io.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				if h.resets {
					_, _ = io.WriteString(w, `{"reset_required":true,"reset_after_id":1}`+"\n")
				}
				if h.replay && strings.HasSuffix(strings.Join(argv, " "), "--last-event-id 0") {
					for _, line := range []string{
						`{"event_id":4,"type":"project.created"}`,
						`{"event_id":5,"type":"cron.workflow.created"}`,
						`{"event_id":6,"type":"cron.job.created"}`,
					} {
						_, _ = io.WriteString(w, line+"\n")
					}
					// The stream also replays the lane's own lease setup: the
					// issue's creation, closing and first lease write.
					h.leases.mu.Lock()
					if issue := h.leases.issues[project]; issue != nil {
						for n, line := range []string{
							`{"event_id":%d,"type":"issue.created","issue_uid":%q,"payload":{}}`,
							`{"event_id":%d,"type":"issue.closed","issue_uid":%q,"payload":{}}`,
							`{"event_id":%d,"type":"issue.metadata_updated","issue_uid":%q,"payload":{"revision_new":3}}`,
						} {
							_, _ = io.WriteString(w, fmt.Sprintf(line, 7+n, issue.UID)+"\n")
						}
					}
					h.leases.mu.Unlock()
				}
				events := h.leases.subscribe(project)
				go func() {
					for {
						select {
						case <-ctx.Done():
							return
						case line := <-events:
							if _, err := io.WriteString(w, line+"\n"); err != nil {
								return
							}
						}
					}
				}()
				var cut <-chan time.Time
				if h.refusal != "" {
					timer := time.NewTimer(h.refuseAt - time.Since(h.started))
					defer timer.Stop()
					cut = timer.C
				}
				select {
				case <-ctx.Done():
				case <-cut:
				}
				_ = w.Close()
			}()
			return kata.Proc{Stdout: r, Stderr: strings.NewReader(""), Wait: func() error { <-done; return nil }}, nil
		}
		return kata.New(view.Kata, root, run, tail)
	}
}

// The worker subscribes to SIGUSR1. Start the runtime's signal goroutine
// outside any bubble so it never selects on a bubbled channel.
func warmSignals() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGUSR1)
	signal.Stop(c)
}

// startBudgetWorker runs a worker with the given lanes against the hub. Its
// stop function returns the hub calls per minute.
func startBudgetWorker(t *testing.T, lanes int, h *budgetHub) func() map[int]int {
	t.Helper()
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(l.Root, "absent.sock"))
	t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	cfg := config.Config{Executor: "worker", Kata: config.Kata{Bin: "/bin/kata", Actor: "worker", Env: map[string]string{"KATA_HOME": t.TempDir()}}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}, Limits: config.Limits{MaxActiveRuns: max(2, lanes), DefaultTimeout: time.Hour, MaxTimeout: time.Hour}}
	for i := range lanes {
		project := config.Project{Name: fmt.Sprintf("project-%02d", i), Actor: "worker"}
		if h.inboxes {
			project.Inbox = "worker/runtime"
		}
		cfg.Projects = append(cfg.Projects, project)
	}
	h.started, h.perMin, h.startup, h.calls = time.Now(), map[int]int{}, map[string]int{}, map[string]int{}
	if h.leases == nil {
		h.leases = &hubLeases{}
	}
	if h.rival {
		h.leases.live = "rival"
		for i := range lanes {
			h.leases.hold(fmt.Sprintf("01K000000000000000000000%02d", i), "rival", 1)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- workerWithFactory(ctx, cfg, l, h.factory(t, l.Root)) }()
	return func() map[int]int {
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.perMin
	}
}

// After startup settles (minutes 10 to 30), healthy lanes stay near their
// 30-45 second route checks of four calls each. Lanes without jobs and
// waiting lanes back off to a few calls every few minutes, whatever made
// them wait.
func TestHubCallsPerMinuteStayBounded(t *testing.T) {
	warmSignals()
	const lanes = 10
	for _, tc := range []struct {
		name     string
		refusal  string
		refuseAt time.Duration
		idle     bool
		inboxes  bool
		perLane  float64
	}{
		{"healthy", "", 0, false, false, 9},
		{"healthy without jobs", "", 0, true, false, 2},
		// An empty inbox scans on events and a ten-minute fallback, so it
		// adds about one call per lane every ten minutes.
		{"healthy with inboxes", "", 0, false, true, 9.2},
		{"healthy without jobs, with inboxes", "", 0, true, true, 2.2},
		{"access lost from start", "not_found", 0, false, false, 1.5},
		{"access lost after start", "not_found", 2 * time.Minute, false, false, 1.5},
		{"hub timeouts after start", "timeout", 2 * time.Minute, false, false, 1.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				stop := startBudgetWorker(t, lanes, &budgetHub{refusal: tc.refusal, refuseAt: tc.refuseAt, idle: tc.idle, inboxes: tc.inboxes})
				time.Sleep(30 * time.Minute)
				perMin := stop()
				total := 0
				for minute := 10; minute < 30; minute++ {
					total += perMin[minute]
				}
				if average := float64(total) / 20; average > tc.perLane*lanes {
					t.Fatalf("%.1f hub calls per minute for %d lanes, want at most %.0f: %v", average, lanes, tc.perLane*lanes, perMin)
				}
			})
		})
	}
}

// A lane whose lease another live installation holds refuses at once: one
// route check and one lease read, before listing definitions, opening its
// event stream or admitting anything. It re-reads the lease on a slow
// backoff. Learning by an admission conflict cost about 61 calls a lane.
func TestRivalLanesFirstMinutesStayBounded(t *testing.T) {
	warmSignals()
	const lanes = 10
	synctest.Test(t, func(t *testing.T) {
		h := &budgetHub{rival: true}
		stop := startBudgetWorker(t, lanes, h)
		time.Sleep(15 * time.Minute)
		perMin := stop()
		t.Logf("first two minutes: %d calls; per minute %v; calls %v", perMin[0]+perMin[1], perMin, h.calls)
		if first := perMin[0] + perMin[1]; first > 6*lanes {
			t.Fatalf("%d hub calls in the first two minutes for %d refused lanes, want at most %d: %v; calls %v", first, lanes, 6*lanes, perMin, h.calls)
		}
		steady := 0
		for minute := 2; minute < 15; minute++ {
			steady += perMin[minute]
		}
		if average := float64(steady) / 13; average > 1.5*lanes {
			t.Fatalf("%.1f hub calls per minute for refused lanes, want at most %.0f: %v", average, 1.5*lanes, perMin)
		}
		for _, cmd := range []string{"cron job list", "cron workflow list", "create --body-stdin --idempotency-key", "cron run observe"} {
			if h.calls[cmd] != 0 {
				t.Fatalf("a refused lane called %q: %v", cmd, h.calls)
			}
		}
	})
}

// Another installation that takes the lanes over with up --takeover ends this
// installation's hold at once: each lane sees the lease event, reads the
// lease, and stands down. It then costs only a slow lease re-read.
func TestTakenOverLanesStandDownAtOnce(t *testing.T) {
	warmSignals()
	const lanes = 10
	synctest.Test(t, func(t *testing.T) {
		h := &budgetHub{}
		stop := startBudgetWorker(t, lanes, h)
		time.Sleep(3 * time.Minute)
		var projects []string
		for i := range lanes {
			projects = append(projects, fmt.Sprintf("01K000000000000000000000%02d", i))
		}
		h.mu.Lock()
		before := maps.Clone(h.calls)
		h.mu.Unlock()
		h.leases.takeover("taker", projects...)
		time.Sleep(10 * time.Minute)
		perMin := stop()
		h.mu.Lock()
		defer h.mu.Unlock()
		t.Logf("after takeover: %d calls in two minutes; per minute %v; calls %v", perMin[3]+perMin[4], perMin, h.calls)
		if first := perMin[3] + perMin[4]; first > 6*lanes {
			t.Fatalf("%d hub calls in the two minutes after the takeover for %d lanes, want at most %d: %v", first, lanes, 6*lanes, perMin)
		}
		steady := 0
		for minute := 5; minute < 13; minute++ {
			steady += perMin[minute]
		}
		if average := float64(steady) / 8; average > 1.5*lanes {
			t.Fatalf("%.1f hub calls per minute after standing down, want at most %.0f: %v", average, 1.5*lanes, perMin)
		}
		for _, cmd := range []string{"cron job list", "cron workflow list"} {
			if h.calls[cmd] != before[cmd] {
				t.Fatalf("a lane that stood down still called %q: before %v, after %v", cmd, before, h.calls)
			}
		}
	})
}
