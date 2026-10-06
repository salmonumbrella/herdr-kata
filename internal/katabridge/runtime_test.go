package katabridge

import (
	"context"
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"path/filepath"
	"testing"
)

// Standalone transport oracle owns a conversation and draft atomically. It does
// not import Herdr or implement submission by separate check/type operations.
type guardedRuntime struct {
	current   RuntimeState
	submits   int
	race      bool
	draftRace bool
}

func (g *guardedRuntime) Inspect(context.Context, Registration) (RuntimeState, error) {
	return g.current, nil
}
func (g *guardedRuntime) GuardedSubmit(_ context.Context, r Registration, conversation, prompt string) error {
	if g.draftRace {
		g.current.Draft = true
	}
	if g.race {
		g.current.Conversation = "foreign-session"
	}
	if g.current.Conversation != conversation || g.current.Status != "idle" || g.current.Draft || g.current.Pane != r.Pane || g.current.Workspace != r.Workspace || g.current.Agent != r.Agent || g.current.SessionKind != r.SessionKind || g.current.SessionSource != r.SessionSource {
		return errors.New("atomic conversation/safe-input guard rejected")
	}
	g.submits++
	return nil
}

func TestGuardedRuntimeCoalescesRestartsAndRejectsUnsafeInput(t *testing.T) {
	scope := Scope{TargetKey: "target", ProjectUID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Actor: "worker"}
	r := Registration{Recipient: "worker/child", Workspace: "w1", Pane: "p1", Conversation: "session-1"}
	b := Bridge{Dir: t.TempDir(), Scope: scope}
	if err := b.Connect(r); err != nil {
		t.Fatal(err)
	}
	request := []Request{{Ref: "abcd", Message: "Inspect change"}}
	for _, tc := range []struct {
		name, status, conversation string
		draft, race                bool
		want                       int
	}{
		{"working", "working", "session-1", false, false, 0},
		{"blocked", "blocked", "session-1", false, false, 0},
		{"missing", "missing", "", false, false, 0},
		{"foreign", "idle", "foreign-session", false, false, 0},
		{"draft", "idle", "session-1", true, false, 0},
		{"atomic race", "idle", "session-1", false, true, 0},
		{"idle", "idle", "session-1", false, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &guardedRuntime{current: RuntimeState{Workspace: "w1", Pane: "p1", Status: tc.status, Conversation: tc.conversation, Draft: tc.draft}, race: tc.race}
			_, err := b.Deliver(t.Context(), r.Recipient, request, g)
			if tc.want > 0 && err != nil {
				t.Fatal(err)
			}
			if g.submits != tc.want {
				t.Fatalf("unsafe/duplicate submission: %d", g.submits)
			}
		})
	}
	// Same request is coalesced across restart, without clearing the native inbox.
	b = Bridge{Dir: b.Dir, Scope: scope}
	g := &guardedRuntime{current: RuntimeState{Workspace: "w1", Pane: "p1", Status: "idle", Conversation: "session-1"}}
	if _, err := b.Deliver(t.Context(), r.Recipient, request, g); err != nil {
		t.Fatal(err)
	}
	if g.submits != 0 {
		t.Fatal("restart redelivered already submitted current request")
	}
	request[0].Message = "Replacement"
	if _, err := b.Deliver(t.Context(), r.Recipient, request, g); err != nil {
		t.Fatal(err)
	}
	if g.submits != 1 {
		t.Fatal("replacement was not delivered")
	}
	regs, err := b.Registrations()
	if err != nil || len(regs) != 1 {
		t.Fatal("registration lost")
	}
	b.Scope.ProjectUID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	regs, err = b.Registrations()
	if err != nil || len(regs) != 0 {
		t.Fatal("registration leaked projects")
	}
}

type manualRuntime struct{ guardedRuntime }

// Explicitly omit GuardedSubmit from this wrapper's method set.
type genericRuntime struct{ state RuntimeState }

func (g genericRuntime) Inspect(context.Context, Registration) (RuntimeState, error) {
	return g.state, nil
}
func TestGenericRuntimeNeverTypesAndReadsDoNotClear(t *testing.T) {
	b := Bridge{Dir: t.TempDir(), Scope: Scope{TargetKey: "target", ProjectUID: "project", Actor: "worker"}}
	r := Registration{Recipient: "worker/child", Workspace: "w1", Pane: "p1", Conversation: "session"}
	if err := b.Connect(r); err != nil {
		t.Fatal(err)
	}
	result, err := b.Deliver(t.Context(), r.Recipient, []Request{{Ref: "abcd", Message: "Review"}}, genericRuntime{RuntimeState{Workspace: "w1", Pane: "p1", Conversation: "session", Status: "idle"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "needs-human" || result.Prompt == "" {
		t.Fatalf("generic runtime offered unsafe typing: %+v", result)
	}
	// No native clear or reassignment method exists on the bridge/transport.
	if regs, err := b.Registrations(); err != nil || len(regs) != 1 {
		t.Fatal("read removed registration")
	}
	if err := b.Disconnect(r.Recipient); err != nil {
		t.Fatal(err)
	}
	if regs, _ := b.Registrations(); len(regs) != 0 {
		t.Fatal("disconnect did not remove local registration")
	}
}

func TestGenericCompletedRuntimeOffersManualWake(t *testing.T) {
	for _, tc := range []struct {
		name, status, conversation, want string
		draft                            bool
	}{
		{"completed", "done", "session", "needs-human", false},
		{"completed with draft", "done", "session", "pending", true},
		{"working", "working", "session", "pending", false},
		{"blocked", "blocked", "session", "pending", false},
		{"foreign", "done", "foreign", "needs-human", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := Bridge{Dir: t.TempDir(), Scope: Scope{TargetKey: "target", ProjectUID: "project", Actor: "worker"}}
			r := Registration{Recipient: "worker/child", Workspace: "w1", Pane: "p1", Conversation: "session"}
			if err := b.Connect(r); err != nil {
				t.Fatal(err)
			}
			result, err := b.Deliver(t.Context(), r.Recipient, []Request{{Ref: "abcd", Message: "Review"}}, genericRuntime{RuntimeState{Workspace: "w1", Pane: "p1", Conversation: tc.conversation, Status: tc.status, Draft: tc.draft}})
			if err != nil || result.State != tc.want {
				t.Fatalf("delivery=%+v err=%v", result, err)
			}
			if tc.name == "completed" && (result.Reason != "generic Herdr requires manual wake" || result.Prompt == "") {
				t.Fatalf("completed conversation has no manual handoff: %+v", result)
			}
		})
	}
}

func TestGuardedTransportAtomicallyRejectsDraftAppearingAfterInspection(t *testing.T) {
	b := Bridge{Dir: t.TempDir(), Scope: Scope{TargetKey: "target", ProjectUID: "project", Actor: "worker"}}
	r := Registration{Recipient: "worker/child", Workspace: "w1", Pane: "p1", Conversation: "session"}
	if err := b.Connect(r); err != nil {
		t.Fatal(err)
	}
	g := &guardedRuntime{current: RuntimeState{Workspace: "w1", Pane: "p1", Conversation: "session", Status: "idle"}, draftRace: true}
	result, err := b.Deliver(t.Context(), r.Recipient, []Request{{Ref: "abcd", Message: "Inspect"}}, g)
	if err != nil || g.submits != 0 || result.State != "pending" {
		t.Fatalf("safe-input race bypassed guarded transport: %+v %v", result, err)
	}
}

func TestRuntimeSavedRecipientMismatchNeverSubmitsToAnotherRuntime(t *testing.T) {
	b := Bridge{Dir: t.TempDir(), Scope: Scope{TargetKey: "target", ProjectUID: "project", Actor: "worker"}}
	r := Registration{Recipient: "worker/child", Workspace: "w1", Pane: "p1", Conversation: "session"}
	if err := b.Connect(r); err != nil {
		t.Fatal(err)
	}
	f, err := b.read()
	if err != nil {
		t.Fatal(err)
	}
	entry := f.Entries[b.key(r.Recipient)]
	entry.Registration.Recipient = "worker"
	f.Entries[b.key(r.Recipient)] = entry
	if err := statefs.WriteJSON(filepath.Join(b.Dir, "runtime-registrations.json"), f, 262144); err != nil {
		t.Fatal(err)
	}
	g := &guardedRuntime{current: RuntimeState{Workspace: "w1", Pane: "p1", Conversation: "session", Status: "idle"}}
	if _, err := b.Deliver(t.Context(), r.Recipient, []Request{{Ref: "abcd", Message: "Review"}}, g); err == nil || g.submits != 0 {
		t.Fatal("recipient mismatch submitted to another registered runtime")
	}
}
