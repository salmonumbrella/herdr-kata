package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func commandUsage(flags *flag.FlagSet, stderr io.Writer, synopsis string) {
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: herdr-kata "+synopsis)
		flags.PrintDefaults()
	}
}

func interspersed(args []string, valueFlags ...string) ([]string, error) {
	var reordered, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if slices.Contains(valueFlags, arg) {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--%s requires a value", strings.TrimLeft(arg, "-"))
			}
			reordered = append(reordered, arg, args[i+1])
			i++
		} else if strings.HasPrefix(arg, "-") {
			reordered = append(reordered, arg)
		} else {
			positional = append(positional, arg)
		}
	}
	return append(reordered, positional...), nil
}

func resumeCommand(args []string, stdout, stderr io.Writer) int {
	args, err := interspersed(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	flags := flag.NewFlagSet("resume", flag.ContinueOnError)
	flags.SetOutput(stderr)
	commandUsage(flags, stderr, "resume <run> [--reset-loops]")
	resetLoops := flags.Bool("reset-loops", false, "start a new loop visit")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 || !validLogRunID(flags.Arg(0)) {
		flags.Usage()
		return 2
	}
	l := state.Layout{Root: state.DefaultRoot()}
	ref := flags.Arg(0)
	projectName := runProjectLabel(l, ref)
	req, err := runner.SendResumeRequest(state.OSFS{}, l, ref, *resetLoops)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, req.RunUID)
	if projectName != "" {
		fmt.Fprintf(stdout, "resume requested · project %s\n", projectName)
	}
	return awaitControlAck(l, req, "resume", stdout, stderr)
}

func releaseCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("release", flag.ContinueOnError)
	flags.SetOutput(stderr)
	commandUsage(flags, stderr, "release <run>")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 || !validLogRunID(flags.Arg(0)) {
		flags.Usage()
		return 2
	}
	l := state.Layout{Root: state.DefaultRoot()}
	ref := flags.Arg(0)
	projectName := runProjectLabel(l, ref)
	req, err := runner.SendProjectRequest(state.OSFS{}, l, "release", ref, "", nil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, req.RunUID)
	if projectName != "" {
		fmt.Fprintf(stdout, "release requested · project %s\n", projectName)
	}
	return awaitControlAck(l, req, "release", stdout, stderr)
}

func awaitControlAck(l state.Layout, req runner.Request, kind string, stdout, stderr io.Writer) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 70*time.Second)
	defer timeout()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var result runner.RequestResult
		err := state.ReadJSON(state.OSFS{}, runner.RequestResultPath(l, req.UID), 64<<10, &result)
		if err == nil {
			if message := controlResultError(l, req, result); message != "" {
				fmt.Fprintln(stderr, message)
				return 1
			}
			fmt.Fprintf(stdout, "%s accepted\n", kind)
			return 0
		}
		if !os.IsNotExist(err) {
			fmt.Fprintln(stderr, err)
			return 1
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(stderr, "request still pending; inspect herdr-kata status --json")
			return 1
		case <-ticker.C:
		}
	}
}

func controlResultError(l state.Layout, req runner.Request, result runner.RequestResult) string {
	if result.RunUID != req.RunUID {
		return "control response differs from request"
	}
	return controlError(l, result.Error)
}

// Worker errors can contain local paths when the requested journal is missing.
// Keep CLI acknowledgements and status consistent without changing saved evidence.
func controlError(l state.Layout, message string) string {
	if strings.Contains(message, l.Root) || strings.Contains(message, "lstat ") {
		return "local run state unavailable; inspect herdr-kata doctor"
	}
	return message
}

// Run outcome polling. A refusal is a small file the worker writes at intake,
// so it is read often; the journal is read at the slower cadence.
var (
	runResultPoll  = 100 * time.Millisecond
	runJournalPoll = 2 * time.Second
)

func awaitRunOutcome(l state.Layout, req runner.Request, stdout, stderr io.Writer) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ticker := time.NewTicker(runResultPoll)
	defer ticker.Stop()
	var journalAt time.Time
	for {
		var result runner.RequestResult
		if e := state.ReadJSON(state.OSFS{}, runner.RequestResultPath(l, req.UID), 64<<10, &result); e == nil {
			if message := controlResultError(l, req, result); message != "" {
				fmt.Fprintln(stderr, message)
				return 1
			}
		} else if !os.IsNotExist(e) {
			fmt.Fprintln(stderr, e)
			return 1
		}
		if now := time.Now(); !now.Before(journalAt) {
			journalAt = now.Add(runJournalPoll)
			var r runner.Run
			if e := state.ReadJSON(state.OSFS{}, l.RunFile(req.RunUID), 4<<20, &r); e == nil && r.EndedAt != nil {
				if r.Phase == runner.Held {
					fmt.Fprintf(stdout, "%s: held (%s)\n", r.UID, runner.HoldReason(&r))
					return 1
				}
				fmt.Fprintf(stdout, "%s: %s\n", r.UID, r.Outcome)
				if r.Outcome == "succeeded" {
					return 0
				}
				return 1
			} else if e != nil && !os.IsNotExist(e) {
				fmt.Fprintln(stderr, e)
				return 1
			}
		}
		select {
		case <-ctx.Done():
			return 1
		case <-ticker.C:
		}
	}
}

// Display identity comes from the frozen journal and pin, never the current row or selector.
func runProjectLabel(l state.Layout, uid string) string {
	var r runner.Run
	if err := state.ReadJSON(state.OSFS{}, l.RunFile(uid), 4<<20, &r); err != nil {
		if err := state.ReadJSON(state.OSFS{}, filepath.Join(l.QuarantineRunDir(uid), "run.json"), 4<<20, &r); err != nil {
			return ""
		}
	}
	return projectLabel(l, r.Target.Identity.ProjectUID)
}
func projectLabel(l state.Layout, uid string) string {
	if uid == "" {
		return ""
	}
	var pin kata.Installation
	if state.SafeComponent(uid) {
		if err := state.ReadJSON(state.OSFS{}, l.Project(uid).StateFile(), 64<<10, &pin); err == nil && pin.Name != "" {
			return pin.Name
		}
	}
	return uid
}
