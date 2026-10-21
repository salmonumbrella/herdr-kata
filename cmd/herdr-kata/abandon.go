package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func abandonCommand(args []string, stdout, stderr io.Writer) int {
	return abandonWithFS(args, stdout, stderr, state.OSFS{})
}

func abandonWithFS(args []string, stdout, stderr io.Writer, fsys state.FS) int {
	args, err := interspersed(args, "--project", "-project")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	flags := flag.NewFlagSet("abandon", flag.ContinueOnError)
	flags.SetOutput(stderr)
	commandUsage(flags, stderr, "abandon <run> [--local] [--project <name>]")
	local := flags.Bool("local", false, "acknowledge local quarantine without Kata writes")
	project := flags.String("project", "", "Kata project name")
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
	args = flags.Args()
	l := state.Layout{Root: state.DefaultRoot()}
	if _, err := state.LayoutPresent(fsys, l); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *local {
		name := runProjectLabel(l, args[0])
		// The socket may be unavailable offline; only occupied runs require it.
		var h *herdr.Client
		if cfg, err := config.Load(config.DefaultPath()); err == nil {
			h = &herdr.Client{Path: herdr.SocketPath(cfg.Session)}
		}
		err := runner.AbandonLocal(context.Background(), fsys, l, args[0], h)
		// Store.Load preserves a corrupt journal in quarantine before returning its
		// error. Finish this explicit acknowledgement only after that move is proven.
		// A joined preservation error must remain visible rather than being retried.
		if runner.JournalQuarantined(fsys, l, args[0], err) {
			err = runner.AbandonLocal(context.Background(), fsys, l, args[0], h)
		}
		if err != nil {
			message := err.Error()
			if _, corrupt := errors.AsType[*state.CorruptError](err); !corrupt {
				message = controlError(l, message)
			}
			fmt.Fprintln(stderr, message)
			return 1
		}
		fmt.Fprintf(stdout, "%s: quarantined (local resolution)%s\n", args[0], projectSuffix(name))
		return 0
	}
	cfg, e := config.Load(config.DefaultPath())
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}

	view, e := cfg.SelectProject(*project)
	if e != nil {
		if *project != "" {
			e = fmt.Errorf("unknown project %q", *project)
		}
		fmt.Fprintln(stderr, e)
		return 1
	}
	cfg = &view
	_, e = state.ProjectLayouts(state.OSFS{}, l)
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	k := kata.New(cfg.Kata, l.Root, nil, nil)
	identity, _, e := kata.ResolveIdentity(context.Background(), k)
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if !state.SafeComponent(identity.ProjectUID) {
		fmt.Fprintln(stderr, "invalid project UID")
		return 1
	}
	pinLayout := l.Project(identity.ProjectUID)
	guard := kata.NewRouteGuard(*cfg, k, state.OSFS{}, pinLayout)
	gone, e := runner.AbandonProject(context.Background(), *cfg, k, guard, state.OSFS{}, l, pinLayout, args[0])
	if e != nil {
		if kataErr, ok := errors.AsType[*kata.Error](e); ok && kataErr.Code == "not_found" {
			e = fmt.Errorf("run %q is unavailable in project %q; inspect herdr-kata doctor", args[0], cfg.Kata.Project)
		}
		fmt.Fprintln(stderr, e)
		return 1
	}
	detail := ""
	if gone {
		detail = "; issue is gone; no issue to report to"
	}
	fmt.Fprintf(stdout, "%s: unknown (local state lost)%s%s\n", args[0], projectSuffix(cfg.Kata.Project), detail)
	return 0
}

func projectSuffix(name string) string {
	if name == "" {
		return ""
	}
	return " · project " + name
}
