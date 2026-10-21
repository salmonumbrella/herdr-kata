package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func runCommand(args []string, stdout, stderr io.Writer) int {
	args, err := interspersed(args, "--input", "-input", "--project", "-project")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	commandUsage(flags, stderr, "run <job> [--project <name>] [--wait] [--input text]")
	var input *string
	flags.Func("input", "shared workflow input (at most 4 KiB)", func(v string) error { input = &v; return nil })
	project := flags.String("project", "", "Kata project name")
	wait := flags.Bool("wait", false, "wait for the run result")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return 2
	}
	l := state.Layout{Root: state.DefaultRoot()}
	ref := flags.Arg(0)
	present, e := state.LayoutPresent(state.OSFS{}, l)
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if present {
		cfg, e := config.Load(config.DefaultPath())
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		*project, ref, e = resolveRunJob(ctx, *cfg, state.OSFS{}, l, ref, *project)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
	}
	projectName := projectLabel(l, *project)
	req, err := runner.SendProjectRequest(state.OSFS{}, l, "run", ref, *project, input)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, req.RunUID)
	if projectName != "" {
		fmt.Fprintf(stdout, "run requested · project %s\n", projectName)
	}
	if !*wait {
		return 0
	}
	return awaitRunOutcome(l, req, stdout, stderr)
}

func resolveRunJob(ctx context.Context, cfg config.Config, f state.FS, l state.Layout, ref, project string) (string, string, error) {
	uid, job, err := runner.ResolveJob(cfg, f, l, ref, project)
	if !errors.Is(err, runner.ErrJobUnavailable) {
		return checkedManualJob(f, l, uid, job, err)
	}
	layouts, e := state.ProjectLayouts(f, l)
	if e != nil {
		return "", "", e
	}
	var failures error
	entries := cfg.ProjectEntries()
	for _, pl := range layouts {
		var pin kata.Installation
		if e := state.ReadJSON(f, pl.StateFile(), 65536, &pin); e != nil {
			return "", "", e
		}
		name := pin.Name
		if name == "" && len(entries) == 1 {
			name = entries[0].Name
		}
		if project != "" && project != name && project != pin.ProjectUID {
			continue
		}
		for _, entry := range entries {
			if entry.Name != name {
				continue
			}
			view := cfg.View(entry)
			k := kata.New(view.Kata, l.Root, nil, nil)
			guard := kata.NewRouteGuard(view, k, f, pl, pin.ProjectUID)
			if e := guard.Check(ctx); e != nil {
				failures = errors.Join(failures, e)
				continue
			}
			if e := defs.NewCache(view, k, f, pl).RefreshForJob(ctx, ref); e != nil {
				failures = errors.Join(failures, e)
			}
		}
	}
	uid, job, err = runner.ResolveJob(cfg, f, l, ref, project)
	if err != nil {
		if errors.Is(err, runner.ErrJobUnavailable) {
			err = fmt.Errorf("unknown job %q", ref)
		}
		err = errors.Join(err, failures)
	}
	return checkedManualJob(f, l, uid, job, err)
}

// The worker revalidates current definitions before admission. The snapshot
// lets the CLI explain known refusals before allocating a manual run UID.
func checkedManualJob(f state.FS, l state.Layout, project, job string, err error) (string, string, error) {
	if err != nil {
		return project, job, err
	}
	snapshot, e := defs.ReadSnapshot(f, l.Project(project).DefsSnapshot())
	if e != nil {
		return "", "", e
	}
	for _, entry := range snapshot.Jobs {
		if entry.UID != job {
			continue
		}
		if entry.Owner == defs.Native {
			return "", "", fmt.Errorf("job %q is owned by Kata's native scheduler", entry.Name)
		}
		if entry.Owner != defs.Adapter || !entry.Supported || entry.DeletedAt != nil {
			reason := strings.Join(entry.Reasons, "; ")
			if reason == "" {
				reason = "not supported for manual execution"
			}
			return "", "", fmt.Errorf("job %q: %s", entry.Name, reason)
		}
		return project, job, nil
	}
	return "", "", fmt.Errorf("unknown job %q", job)
}
