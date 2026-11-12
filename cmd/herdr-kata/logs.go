package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/attempt"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"io"
	"os"
	"path/filepath"
)

func logs(ctx context.Context, args []string, st *journal.Store, h hc.HC, out io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(out, "logs requires a run UID or key")
		return 2
	}
	r, err := st.Find(args[1])
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	if r.Plan.Agent != "" {
		state := "queued"
		hold := r.Hold
		if len(r.Attempts) > 0 {
			a := r.Attempts[len(r.Attempts)-1]
			if a.Address != "" {
				state = "delivered"
			}
			if a.TaskStarted != nil && *a.TaskStarted {
				state = "acknowledged"
			}
			if a.StopReason == "not_delivered" {
				state = "not delivered"
			} else if a.Address != "" && a.SettlementStatus != "" && (a.Status == "succeeded" || a.Status == "failed" || a.Status == "needs-human") {
				state = "completed"
			}
			if a.Hold != "" {
				hold = a.Hold
			}
			if a.Status == "launching" && a.Address != "" {
				state = "uncertain"
			}
		}
		if hold != "" {
			state = hold
			if hold == "wake_uncertain" {
				state = "uncertain (wake_uncertain)"
			}
		}
		if r.Outcome == "cancelled" {
			state = "cancelled"
		}
		fmt.Fprintf(out, "agent %s: %s\n", r.Plan.Agent, state)
	}
	if len(r.Attempts) == 0 {
		fmt.Fprintf(out, "no attempts; phase %s; reason %s\n", r.Phase, r.Hold)
		if r.Plan.Agent != "" {
			return 0
		}
		return 1
	}
	for _, item := range r.Attempts {
		if item.Session != nil {
			fmt.Fprintf(out, "attempt %s/%d session %s %s\n", item.Step, item.N, item.Session.Agent, item.Session.SessionID)
		}
	}
	a := r.Attempts[len(r.Attempts)-1]
	path := filepath.Join(st.Layout.Attempt(r.Key, a.Step, a.N), "output.log")
	useSnapshot := true
	promptStep := false
	for _, step := range r.Plan.Steps {
		if step.Key == a.Step && step.Kind == "prompt" {
			promptStep = true
			useSnapshot = a.StopReason != ""
		}
	}
	var f *os.File
	err = os.ErrNotExist
	if useSnapshot {
		f, err = os.Open(path)
	}
	if err == nil {
		defer func() { _ = f.Close() }()
		_, err = io.Copy(out, f)
	} else if os.IsNotExist(err) && a.Address != "" {
		if r.Plan.Agent != "" {
			err = attempt.GuardWake(ctx, h, a)
		} else {
			err = attempt.Guard(ctx, h, a)
		}
		if err == nil {
			var text string
			var complete bool
			text, complete, err = h.Read(ctx, a.Address, 200)
			if err == nil {
				fmt.Fprintln(out, text)
				if !complete {
					fmt.Fprintln(out, "[terminal output incomplete]")
				}
			}
		}
		if errors.Is(err, hc.ErrNotFound) && errors.Is(err, attempt.ErrOccupantChanged) && promptStep && a.StopReason == "" && a.Cleanup == "" && a.SettlementStatus != "" && a.Status == a.SettlementStatus && a.Status != "needs-human" {
			fmt.Fprintln(out, "[terminal output unavailable after cleanup]")
			err = nil
		}
	}
	if os.IsNotExist(err) && r.Plan.Agent != "" && a.Address == "" {
		return 0
	}
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	return 0
}
