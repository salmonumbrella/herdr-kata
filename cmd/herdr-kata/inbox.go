package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/herdrcli"
	"github.com/salmonumbrella/herdr-kata/internal/katabridge"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"io"
	"os"
	"time"
)

func localBridge(s *store.Store) (katabridge.Bridge, error) {
	if s.Native == nil || s.Native.Client == nil {
		return katabridge.Bridge{}, store.ErrNativeUnconfigured
	}
	t := s.Native.Client.Target
	return katabridge.Bridge{Dir: s.Native.StateDir, Scope: katabridge.Scope{TargetKey: nativeTargetKey(t), ProjectUID: s.Native.Binding.ProjectUID, Actor: t.Actor}}, nil
}

// Generic Herdr deliberately implements only Inspect: it has no atomic guarded
// submit capability and therefore cannot automatically type into a conversation.
type inboxHerdr struct{ client *herdrcli.Client }

func (h inboxHerdr) Inspect(ctx context.Context, r katabridge.Registration) (katabridge.RuntimeState, error) {
	agents, err := h.client.AgentList(ctx)
	if err != nil {
		return katabridge.RuntimeState{}, err
	}
	for _, a := range agents {
		if a.WorkspaceID == r.Workspace && a.PaneID == r.Pane {
			out := katabridge.RuntimeState{Workspace: a.WorkspaceID, Pane: a.PaneID, Status: string(a.AgentStatus), Draft: !a.InteractiveReady}
			if a.AgentSession != nil {
				out.Conversation = a.AgentSession.Value
				out.Agent, out.SessionKind, out.SessionSource = a.AgentSession.Agent, a.AgentSession.Kind, a.AgentSession.Source
			}
			return out, nil
		}
	}
	return katabridge.RuntimeState{Status: "missing"}, nil
}
func teammateCmd(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: teammate connect|disconnect|list")
	}
	fs := flag.NewFlagSet("teammate", flag.ContinueOnError)
	recipient := fs.String("for", "", "exact actor or actor/teammate")
	workspace := fs.String("workspace", "", "Herdr workspace identity")
	pane := fs.String("pane", "", "Herdr pane identity")
	conversation := fs.String("conversation", "", "expected conversation identity")
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	b, err := localBridge(s)
	if err != nil {
		return err
	}
	switch argv[0] {
	case "connect":
		r := katabridge.Registration{Recipient: *recipient, Workspace: *workspace, Pane: *pane, Conversation: *conversation}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		state, err := (inboxHerdr{herdrcli.New()}).Inspect(ctx, r)
		if err != nil {
			return err
		}
		if state.Status == "missing" || state.Conversation != r.Conversation || r.Conversation == "" {
			return errors.New("runtime is missing or its conversation differs; connect the exact current conversation")
		}
		r.Agent, r.SessionKind, r.SessionSource = state.Agent, state.SessionKind, state.SessionSource
		return b.Connect(r)
	case "disconnect":
		return b.Disconnect(*recipient)
	case "list":
		regs, err := b.Registrations()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(regs)
	default:
		return errors.New("usage: teammate connect|disconnect|list")
	}
}
func inboxCmd(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: inbox list|open|deliver --for actor[/teammate] [--ref issue]")
	}
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	recipient := fs.String("for", "", "exact inbox recipient")
	ref := fs.String("ref", "", "one addressed issue")
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}
	if *recipient == "" {
		return errors.New("exact --for recipient required")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	b, err := localBridge(s)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.Native.Client.ProjectID(ctx, s.Native.Binding.ProjectUID); err != nil {
		return err
	}
	inbox, err := katabridge.ReadInbox(ctx, *s.Native.Client, *recipient)
	if err != nil {
		return err
	}
	if *ref != "" {
		requests := []katabridge.Request{}
		for _, r := range inbox.Requests {
			if r.Ref == *ref {
				requests = append(requests, r)
			}
		}
		if len(requests) == 0 {
			return errors.New("issue is not in the exact recipient inbox")
		}
		inbox.Requests = requests
	}
	switch argv[0] {
	case "list":
		return json.NewEncoder(os.Stdout).Encode(inbox)
	case "open":
		if len(inbox.Requests) != 1 {
			return errors.New("select one addressed issue with --ref")
		}
		cmd, err := s.Native.Client.TUICommand(context.Background(), inbox.Requests[0].Ref)
		if err != nil {
			return err
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	case "deliver":
		result, err := b.Deliver(ctx, *recipient, inbox.Requests, inboxHerdr{herdrcli.New()})
		if err != nil {
			return err
		}
		if result.State == "needs-human" && result.Reason != "generic Herdr requires manual wake" {
			return errors.New(result.Reason)
		}
		fmt.Printf("%s: %s\n%s\n", *recipient, result.State, result.Prompt)
		if result.State == "needs-human" {
			fmt.Println("Generic Herdr requires manual wake: open the connected runtime and submit the quoted request yourself.")
		}
		return nil
	default:
		return errors.New("usage: inbox list|open|deliver")
	}
}
func pollNativeInboxes(ctx context.Context, s *store.Store) error {
	b, err := localBridge(s)
	if errors.Is(err, store.ErrNativeUnconfigured) {
		return nil
	}
	if err != nil {
		return err
	}
	// No local recipients means no remote work. Keep empty polling from
	// delaying other ordinary outboxes behind a needless project read.
	regs, err := b.Registrations()
	if err != nil || len(regs) == 0 {
		return err
	}
	regs, err = b.PollBatch()
	if err != nil || len(regs) == 0 {
		return err
	}
	if _, err := s.Native.Client.ProjectID(ctx, s.Native.Binding.ProjectUID); err != nil {
		return err
	}
	var problems []error
	for _, r := range regs {
		if ctx.Err() != nil {
			break
		}
		inbox, err := katabridge.ReadInbox(ctx, *s.Native.Client, r.Recipient)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		result, err := b.Deliver(ctx, r.Recipient, inbox.Requests, inboxHerdr{herdrcli.New()})
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if result.State == "needs-human" && r.Recipient != b.Scope.Actor {
			// Address a separate ordinary request to the accountable actor, retaining the
			// child's exact request. Never replace an occupied parent attention slot.
			parent, err := katabridge.ReadInbox(ctx, *s.Native.Client, b.Scope.Actor)
			if err != nil {
				problems = append(problems, err)
				continue
			}
			occupied := map[string]bool{}
			for _, p := range parent.Requests {
				occupied[p.Ref] = true
			}
			for _, p := range inbox.Requests {
				if occupied[p.Ref] {
					continue
				}
				if err := s.Native.Client.Notify(ctx, p.Ref, b.Scope.Actor, "Needs human: inbox for "+r.Recipient+" requires manual wake or reconnect.", false, new(any)); err != nil {
					problems = append(problems, err)
				}
				occupied[p.Ref] = true
			}
		}
	}
	return errors.Join(problems...)
}
func flushHistoricalComments(ctx context.Context, s *store.Store) error {
	b, err := localBridge(s)
	if errors.Is(err, store.ErrNativeUnconfigured) {
		return nil
	}
	if err != nil {
		return err
	}
	out := katabridge.CommentOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: s.Native.Client.Target.Teammate}
	pending, _, err := out.Status()
	if err != nil || pending == 0 {
		return err
	}
	if _, err := s.Native.Client.ProjectID(ctx, s.Native.Binding.ProjectUID); err != nil {
		return err
	}
	c := *s.Native.Client
	c.Timeout = 3 * time.Second
	return out.Flush(ctx, katabridge.CLIComments{Client: c}, time.Now())
}

func queueHistoricalResult(s *store.Store, c runner.NativeExecutionContext, rec store.Run) error {
	if c.IssueUID == "" || rec.EndedAt == nil {
		return nil
	}
	b, err := localBridge(s)
	if err != nil {
		return err
	}
	out := katabridge.CommentOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: c.Target.Teammate}
	body := fmt.Sprintf("Local run %s reported %s.\n%s", c.RunUID, rec.Outcome, rec.Note)
	stamp := rec.EndedAt.UTC().Truncate(time.Second)
	sum := sha256.Sum256([]byte(stamp.Format(time.RFC3339) + "\n" + body))
	item := katabridge.Comment{UID: c.RunUID + ":" + hex.EncodeToString(sum[:16]), IssueUID: c.IssueUID, Body: body, CreatedAt: stamp}
	return out.Queue(item)
}

func reportNativeDelivery(w io.Writer) error {
	cfg, err := store.LoadNativeConfig(stateDir())
	if errors.Is(err, store.ErrNativeUnconfigured) {
		return nil
	}
	if err != nil {
		return err
	}
	scope := katabridge.Scope{TargetKey: nativeTargetKey(cfg.Client.Target), ProjectUID: cfg.Binding.ProjectUID, Actor: cfg.Client.Target.Actor}
	pending, failed, err := katabridge.ObservationStatus(stateDir())
	if err != nil {
		return err
	}
	comments := katabridge.CommentOutbox{Dir: stateDir(), Scope: scope, Teammate: cfg.Client.Target.Teammate}
	n, f, err := comments.Status()
	if err != nil {
		return err
	}
	pending += n
	failed += f
	attention := katabridge.AttentionOutbox{Dir: stateDir(), Scope: scope, Teammate: cfg.Client.Target.Teammate}
	n, f, err = attention.Status()
	if err != nil {
		return err
	}
	pending += n
	failed += f
	bridge := katabridge.Bridge{Dir: stateDir(), Scope: scope}
	requests, err := bridge.PendingCount()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "herdr-kata: %d pending deliveries, %d failed deliveries, %d inbox requests awaiting handling\n", pending, failed, requests)
	regs, err := bridge.Registrations()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, r := range regs {
		state, err := (inboxHerdr{herdrcli.New()}).Inspect(ctx, r)
		status := state.Status
		if err != nil || state.Conversation != r.Conversation {
			status = "missing or foreign conversation"
		}
		fmt.Fprintf(w, "  %s → %s/%s: %s; generic Herdr requires manual wake\n", r.Recipient, r.Workspace, r.Pane, status)
	}
	return nil
}
