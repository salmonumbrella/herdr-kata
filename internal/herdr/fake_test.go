package herdr_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
)

func fakePane(t *testing.T, s *herdrtest.Server) (*herdr.Client, herdr.TabInfo, herdr.PaneInfo) {
	t.Helper()
	c := &herdr.Client{Path: s.Path}
	ws, err := c.WorkspaceCreate(context.Background(), "Kata", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tab, pane, err := c.TabCreate(context.Background(), ws.WorkspaceID, "Run", t.TempDir(), map[string]string{"HERDR_KATA_RUN": "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	return c, tab, pane
}
func TestServerIDChangesOnRestart(t *testing.T) {
	s := herdrtest.New(t)
	_, tab, _ := fakePane(t, s)
	before, err := herdr.ReadServerID(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Restart(); err != nil {
		t.Fatal(err)
	}
	after, err := herdr.ReadServerID(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if before == after || before.Ino == after.Ino {
		t.Fatalf("identity reused: %+v -> %+v", before, after)
	}
	_, fresh, _ := fakePane(t, s)
	if tab.TabID == fresh.TabID {
		t.Fatal("tab ids not renumbered")
	}
}
func TestFakeLaunchPendingThenReady(t *testing.T) {
	s := herdrtest.New(t)
	c, _, pane := fakePane(t, s)
	s.Script("worker", herdrtest.Script{Start: herdrtest.PendingReady})
	a, err := c.AgentStart(context.Background(), "worker", "codex", pane.PaneID, []string{"--model", "example"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.LaunchPending || a.InteractiveReady {
		t.Fatalf("expected launch pending: %+v", a)
	}
	a, err = c.AgentGet(context.Background(), "worker")
	if err != nil {
		t.Fatal(err)
	}
	if a.LaunchPending || !a.InteractiveReady || a.Status != "idle" || a.Session == nil {
		t.Fatalf("expected ready: %+v", a)
	}
	_, err = c.AgentStart(context.Background(), "worker", "codex", pane.PaneID, nil)
	var e *herdr.Error
	if !errors.As(err, &e) || e.Code != "agent_name_taken" {
		t.Fatalf("duplicate start: %v", err)
	}
}
func TestFakeLaunchAndPromptOutcomes(t *testing.T) {
	for _, start := range []herdrtest.Start{herdrtest.Ready, herdrtest.PendingBlocked, herdrtest.NameTaken, herdrtest.DropStart} {
		t.Run(string(start), func(t *testing.T) {
			s := herdrtest.New(t)
			c, _, pane := fakePane(t, s)
			s.Script("worker", herdrtest.Script{Start: start})
			a, err := c.AgentStart(context.Background(), "worker", "codex", pane.PaneID, nil)
			switch start {
			case herdrtest.NameTaken:
				var e *herdr.Error
				if !errors.As(err, &e) || e.Code != "agent_name_taken" {
					t.Fatal(err)
				}
			case herdrtest.DropStart:
				if err == nil {
					t.Fatal("expected dropped reply")
				}
				a, err = c.AgentGet(context.Background(), "worker")
				if err != nil || a.PaneID != pane.PaneID {
					t.Fatalf("lost launch not retained: %+v %v", a, err)
				}
			case herdrtest.PendingBlocked:
				a, err = c.AgentGet(context.Background(), "worker")
				if err != nil || a.Status != "blocked" || a.LaunchPending {
					t.Fatalf("blocked: %+v %v", a, err)
				}
			default:
				if err != nil || !a.InteractiveReady {
					t.Fatalf("ready %+v %v", a, err)
				}
			}
		})
	}
	for _, out := range []herdrtest.Outcome{herdrtest.Idle, herdrtest.Blocked, herdrtest.Stalled, herdrtest.Timeout, herdrtest.Drop} {
		t.Run(string(out), func(t *testing.T) {
			s := herdrtest.New(t)
			c, _, pane := fakePane(t, s)
			s.Script("worker", herdrtest.Script{Prompt: []herdrtest.Outcome{out}})
			if _, err := c.AgentStart(context.Background(), "worker", "codex", pane.PaneID, nil); err != nil {
				t.Fatal(err)
			}
			a, err := c.AgentPrompt(context.Background(), "worker", "task", nil, time.Second)
			switch out {
			case herdrtest.Idle:
				if err != nil || a.Status != "idle" || a.CompletionSeq == nil || *a.CompletionSeq != 1 {
					t.Fatalf("idle: %+v %v", a, err)
				}
			case herdrtest.Blocked:
				if err != nil || a.Status != "blocked" {
					t.Fatalf("blocked: %+v %v", a, err)
				}
			default:
				if err == nil {
					t.Fatal("expected failure")
				}
				a, err = c.AgentGet(context.Background(), "worker")
				if err != nil || a.Status != "working" {
					t.Fatalf("ambiguous prompt: %+v %v", a, err)
				}
			}
			count := 0
			for _, r := range s.Requests() {
				if r.Method == "agent.prompt" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("prompt count %d", count)
			}
		})
	}
}
func TestFakeCompatibilityAndInjectedErrors(t *testing.T) {
	s := herdrtest.New(t)
	s.OmitCompletionSeq = true
	s.OmitSession = true
	c, tab, pane := fakePane(t, s)
	a, err := c.AgentStart(context.Background(), "worker", "codex", pane.PaneID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Session != nil || a.CompletionSeq != nil {
		t.Fatalf("omitted fields: %+v", a)
	}
	s.Fail("tab.close", herdr.Error{Code: "injected", Message: "keep tab"})
	if err := c.TabClose(context.Background(), tab.TabID); err == nil {
		t.Fatal("error not injected")
	}
	if _, err := c.TabGet(context.Background(), tab.TabID); err != nil {
		t.Fatal("failed close removed tab", err)
	}
	if err := c.PaneReportMetadata(context.Background(), pane.PaneID, map[string]string{"phase": "held"}); err != nil {
		t.Fatal(err)
	}
	p, err := c.PaneGet(context.Background(), pane.PaneID)
	if err != nil || p.Tokens["phase"] != "held" {
		t.Fatalf("metadata: %+v %v", p, err)
	}
	info, err := c.PaneProcessInfo(context.Background(), pane.PaneID)
	if err != nil || info.ShellPID == nil || info.ForegroundProcessGroupID == nil || *info.ShellPID == *info.ForegroundProcessGroupID {
		t.Fatalf("agent process: %+v %v", info, err)
	}
	if err := c.TabClose(context.Background(), tab.TabID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AgentGet(context.Background(), "worker"); err == nil {
		t.Fatal("closed occupant still present")
	}
}
func TestLongWaitDoesNotBlockOtherCalls(t *testing.T) {
	s := herdrtest.New(t)
	c, _, pane := fakePane(t, s)
	a, err := c.AgentStart(context.Background(), "worker", "codex", pane.PaneID, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Status = "working"
	a.InteractiveReady = false
	s.SetAgent(a)
	ctx := t.Context()
	done := make(chan error, 1)
	go func() { _, err := c.AgentWait(ctx, "worker", []string{"idle"}, 0); done <- err }()
	// Wait for receipt without using time to order the two calls.
	deadline := time.Now().Add(time.Second)
	for {
		seen := false
		for _, r := range s.Requests() {
			if r.Method == "agent.wait" {
				seen = true
			}
		}
		if seen {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("wait not received")
		}
		time.Sleep(time.Millisecond)
	}
	pingCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err := c.Ping(pingCtx); err != nil {
		t.Fatal("wait blocked ping", err)
	}
	a.Status = "idle"
	a.InteractiveReady = true
	s.SetAgent(a)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not unblock")
	}
}

func TestFakeCallerCannotMutateStoredAgent(t *testing.T) {
	s := herdrtest.New(t)
	c, _, p := fakePane(t, s)
	a, err := c.AgentStart(context.Background(), "worker", "codex", p.PaneID, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAgent(a)
	a.Session.Value = "foreign"
	*a.CompletionSeq = 42
	got, err := c.AgentGet(context.Background(), "worker")
	if err != nil {
		t.Fatal(err)
	}
	if got.Session.Value == "foreign" || *got.CompletionSeq != 0 {
		t.Fatalf("caller mutated server state: %+v", got)
	}
}
func TestFakeWaitWithMissingSession(t *testing.T) {
	s := herdrtest.New(t)
	s.OmitSession = true
	c, _, p := fakePane(t, s)
	a, err := c.AgentStart(context.Background(), "worker", "codex", p.PaneID, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAgent(a)
	if _, err := c.AgentWait(context.Background(), "worker", []string{"idle"}, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestFakeReplacesPaneOccupant(t *testing.T) {
	s := herdrtest.New(t)
	c, _, p := fakePane(t, s)
	a, err := c.AgentStart(context.Background(), "worker", "codex", p.PaneID, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Name = "replacement"
	s.SetAgent(a)
	if _, err := c.AgentGet(context.Background(), "worker"); err == nil {
		t.Fatal("replaced name still resolves")
	}
	agents, err := c.AgentList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Name != "replacement" {
		t.Fatalf("occupants: %+v", agents)
	}
}
func TestFakeConcurrentMetadataRead(t *testing.T) {
	s := herdrtest.New(t)
	c, _, p := fakePane(t, s)
	if err := c.PaneReportMetadata(context.Background(), p.PaneID, map[string]string{"phase": "running"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			if err := c.PaneReportMetadata(context.Background(), p.PaneID, map[string]string{"phase": "held"}); err != nil {
				t.Error(err)
			}
		}
	}()
	for range 100 {
		if _, err := c.PaneGet(context.Background(), p.PaneID); err != nil {
			t.Error(err)
		}
	}
	<-done
}

func TestFakeDiscoveryAndRead(t *testing.T) {
	s := herdrtest.New(t)
	c, tab, p := fakePane(t, s)
	ws, err := c.WorkspaceGet(context.Background(), tab.WorkspaceID)
	if err != nil || ws.TabCount != 2 {
		t.Fatalf("workspace %+v %v", ws, err)
	}
	list, err := c.WorkspaceList(context.Background())
	if err != nil || len(list) != 1 || list[0].WorkspaceID != tab.WorkspaceID {
		t.Fatalf("workspaces %+v %v", list, err)
	}
	_, err = c.AgentStart(context.Background(), "worker", "codex", p.PaneID, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetRead("worker", "approval requested\n")
	read, err := c.AgentRead(context.Background(), "worker", "recent_unwrapped", nil)
	if err != nil || read.Text != "approval requested\n" || read.PaneID != p.PaneID || read.Source != "recent_unwrapped" {
		t.Fatalf("read %+v %v", read, err)
	}
}

func TestFakeCallerCannotMutateStoredPane(t *testing.T) {
	s := herdrtest.New(t)
	c, _, p := fakePane(t, s)
	p.Tokens = map[string]string{"phase": "running"}
	s.SetPane(p)
	p.Tokens["phase"] = "foreign"
	got, err := c.PaneGet(context.Background(), p.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tokens["phase"] != "running" {
		t.Fatalf("caller mutated stored pane: %+v", got)
	}
}
func TestFakeStopsOutstandingWait(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprint(restart), func(t *testing.T) {
			s := herdrtest.New(t)
			c, _, p := fakePane(t, s)
			a, err := c.AgentStart(context.Background(), "worker", "codex", p.PaneID, nil)
			if err != nil {
				t.Fatal(err)
			}
			a.Status = "working"
			s.SetAgent(a)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := c.AgentWait(ctx, "worker", []string{"idle"}, 0); done <- err }()
			for {
				seen := false
				for _, r := range s.Requests() {
					if r.Method == "agent.wait" {
						seen = true
					}
				}
				if seen {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				default:
					time.Sleep(time.Millisecond)
				}
			}
			if restart {
				if err := s.Restart(); err != nil {
					t.Fatal(err)
				}
			} else {
				s.Close()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("closed wait succeeded")
				}
			case <-ctx.Done():
				t.Fatal("shutdown did not end wait")
			}
		})
	}
}

func TestFakeSessionKindMatchesSchema(t *testing.T) {
	s := herdrtest.New(t)
	c, _, p := fakePane(t, s)
	a, err := c.AgentStart(context.Background(), "worker", "codex", p.PaneID, nil)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/schema-22.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schemas map[string]struct {
			Defs map[string]struct {
				Enum []string `json:"enum"`
			} `json:"$defs"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		t.Fatal(err)
	}
	kinds := doc.Schemas["success_response"].Defs["AgentSessionRefKind"].Enum
	if a.Session == nil || !slices.Contains(kinds, a.Session.Kind) {
		t.Fatalf("session kind %+v is outside schema enum %v", a.Session, kinds)
	}
}

func TestFakeWaitMaximumDurationDoesNotExpireEarly(t *testing.T) {
	s := herdrtest.New(t)
	c, _, p := fakePane(t, s)
	a, err := c.AgentStart(context.Background(), "worker", "codex", p.PaneID, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Status = "working"
	a.InteractiveReady = false
	s.SetAgent(a)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_, err = c.AgentWait(ctx, "worker", []string{"idle"}, time.Duration(1<<63-1))
	// The socket deadline can fire before the context's timer runs. In either
	// case the enormous server timeout must not return before that deadline.
	if err == nil || time.Now().Before(deadline) {
		t.Fatalf("maximum wait expired before context deadline: %v", err)
	}
}

// A wait result consumes the pending launch; get must retain the live occupant.
func TestFakeWaitConsumesPendingLaunch(t *testing.T) {
	s := herdrtest.New(t)
	c, _, pane := fakePane(t, s)
	s.Script("worker", herdrtest.Script{Start: herdrtest.PendingExit, Wait: []herdrtest.Outcome{herdrtest.Idle}})
	if _, err := c.AgentStart(t.Context(), "worker", "codex", pane.PaneID, nil); err != nil {
		t.Fatal(err)
	}
	a, err := c.AgentWait(t.Context(), "worker", []string{"idle"}, time.Second)
	if err != nil || a.Status != "idle" || a.LaunchPending {
		t.Fatalf("wait: %+v %v", a, err)
	}
	a, err = c.AgentGet(t.Context(), "worker")
	if err != nil || a.Status != "idle" || !a.InteractiveReady || a.LaunchPending {
		t.Fatalf("get after wait: %+v %v", a, err)
	}
}

// Removing the workspace must notify a wait even when no later request signals.
func TestFakeWorkspaceCloseWakesWait(t *testing.T) {
	s := herdrtest.New(t)
	c, _, pane := fakePane(t, s)
	a, err := c.AgentStart(t.Context(), "worker", "codex", pane.PaneID, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Status = "working"
	a.InteractiveReady = false
	s.SetAgent(a)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.AgentWait(ctx, "worker", []string{"idle"}, 0); done <- err }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
received:
	for {
		for _, r := range s.Requests() {
			if r.Method == "agent.wait" {
				break received
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("wait not received")
		}
	}
	if err := c.Call(ctx, "workspace.close", map[string]any{"workspace_id": pane.WorkspaceID}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var apiErr *herdr.Error
		if !errors.As(err, &apiErr) || apiErr.Code != "agent_not_running" {
			t.Fatalf("closed workspace wait: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("workspace close did not end wait")
	}
}
