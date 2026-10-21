package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/runstatus"
	"github.com/salmonumbrella/herdr-kata/internal/state"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

type runsReader interface {
	ListJobs(context.Context) ([]kata.Job, error)
	ShowJob(context.Context, string) (kata.Job, error)
	ListRuns(context.Context, string, string, int) ([]kata.Run, string, error)
	Capabilities(context.Context) (kata.Capabilities, error)
	FederationIdentity(context.Context) (kata.Identity, error)
}

func runsCommand(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("runs", flag.ContinueOnError)
	f.SetOutput(stderr)
	commandUsage(f, stderr, "runs [--project <name>] [--job <job>] [--limit N]")
	project := f.String("project", "", "filter by project")
	job := f.String("job", "", "filter by job UID or name")
	limit := f.Int("limit", 100, "maximum runs")
	if e := f.Parse(args); e != nil {
		if e == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 || *limit < 1 {
		f.Usage()
		return 2
	}
	if _, e := state.LayoutPresent(state.OSFS{}, state.Layout{Root: state.DefaultRoot()}); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	cfg, e := config.Load(config.DefaultPath())
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	dir := state.DefaultRoot()
	if _, e = os.Stat(dir); os.IsNotExist(e) {
		dir = os.TempDir()
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	if e = listRunsProjects(ctx, *cfg, *project, *job, *limit, stdout, func(view config.Config) runsReader { return kata.New(view.Kata, dir, nil, nil) }); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}

	return 0
}
func listRuns(ctx context.Context, k runsReader, job string, limit int, out io.Writer) error {
	_, err := listRunsProject(ctx, k, job, limit, out, "")
	return err
}
func listRunsProject(ctx context.Context, k runsReader, job string, limit int, out io.Writer, project string) (int, error) {
	if job != "" {
		if !validLogRunID(job) {
			rows, err := k.ListJobs(ctx)
			if err != nil {
				return 0, err
			}
			found := ""
			for _, row := range rows {
				if row.Name != job && row.UID != job {
					continue
				}
				if found != "" {
					return 0, fmt.Errorf("ambiguous job %q; use its UID", job)
				}
				found = row.UID
			}
			if found == "" {
				return 0, errFilteredJobAbsent
			}
			job = found
		}
		j, err := k.ShowJob(ctx, job)
		if err != nil {
			if kata.NotFound(err) {
				rows, proofErr := k.ListJobs(ctx)
				if proofErr == nil {
					found := false
					for _, row := range rows {
						found = found || row.UID == job
					}
					if !found {
						return 0, errFilteredJobAbsent
					}
				}
			}
			return 0, err
		}
		job = j.UID
	}
	runs, _, e := k.ListRuns(ctx, job, "", limit)
	if e != nil {
		return 0, e
	}
	written := 0
	journalUnavailable := false
	var identity *kata.Identity
	// Request runs have no cron observation. Merge their local journals with
	// the remote page before applying the limit, using the selected identity.
	if job == "" {
		l := state.Layout{Root: state.DefaultRoot()}
		entries, err := os.ReadDir(filepath.Join(l.Root, "runs"))
		if err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		known := map[string]bool{}
		for _, r := range runs {
			known[r.UID] = true
		}
		for _, entry := range slices.Backward(entries) {
			if !entry.IsDir() || !validLogRunID(entry.Name()) || known[entry.Name()] {
				continue
			}
			var saved runner.Run
			err := state.ReadJSON(state.OSFS{}, l.RunFile(entry.Name()), 4<<20, &saved)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				journalUnavailable = true
				continue
			}
			if saved.Version != 2 || !strings.HasPrefix(saved.Source, "issue-request:") {
				continue
			}
			if saved.UID != entry.Name() {
				return 0, fmt.Errorf("invalid local run identity")
			}
			if identity == nil {
				caps, err := k.Capabilities(ctx)
				if err != nil {
					return 0, err
				}
				id, err := k.FederationIdentity(ctx)
				if err != nil {
					return 0, err
				}
				id.ProjectUID = caps.ProjectUID
				identity = &id
			}
			if identity.ProjectUID == "" || identity.InstanceUID == "" || saved.Target.Identity.ProjectUID != identity.ProjectUID || saved.Target.Identity.InstanceUID != identity.InstanceUID {
				continue
			}
			status := runstatus.Run(saved).Word
			var requested time.Time
			if saved.IssueRequest != nil {
				requested = saved.IssueRequest.Request.RequestedAt
			}
			runs = append(runs, kata.Run{UID: saved.UID, Status: status, OccurrenceKey: saved.Source, CreatedAt: requested, StartedAt: saved.StartedAt, Summary: kata.Summary{Version: 1, Message: saved.Reason}})
		}
		if len(runs) > len(known) {
			slices.SortStableFunc(runs, func(a, b kata.Run) int { return runHistoryTime(b).Compare(runHistoryTime(a)) })
		}
	}
	for _, r := range runs[:min(len(runs), limit)] {
		var local *runner.Run
		if validLogRunID(r.UID) {
			var saved runner.Run
			l := state.Layout{Root: state.DefaultRoot()}
			e = state.ReadJSON(state.OSFS{}, l.RunFile(r.UID), 4<<20, &saved)
			if e != nil && !os.IsNotExist(e) {
				if job != "" {
					return written, e
				}
				journalUnavailable = true
			}
			if e == nil {
				if (saved.Version != 1 && saved.Version != 2) || saved.UID != r.UID {
					return written, fmt.Errorf("invalid local run identity")
				}
				if identity == nil {
					caps, err := k.Capabilities(ctx)
					if err != nil {
						return written, err
					}
					i, err := k.FederationIdentity(ctx)
					if err != nil {
						return written, err
					}
					i.ProjectUID = caps.ProjectUID
					identity = &i
				}
				if identity.ProjectUID != "" && identity.InstanceUID != "" && saved.Target.Identity.ProjectUID == identity.ProjectUID && saved.Target.Identity.InstanceUID == identity.InstanceUID {
					local = &saved
				}
			}
		}
		if project != "" {
			if _, e = fmt.Fprintf(out, "%s\t", project); e != nil {
				return written, e
			}
		}
		status := runstatus.Remote(r.Status, local).Word
		summary := strings.Join(strings.Fields(r.Summary.Message), " ")
		if _, e = fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", r.UID, status, r.JobUID, r.OccurrenceKey, summary); e != nil {
			return written, e
		}
		written++
		if local == nil {
			continue
		}
		if local.PromptCap > 0 {
			if _, e = fmt.Fprintf(out, "  prompts %d/%d\n", local.PromptsSent, local.PromptCap); e != nil {
				return written, e
			}
		}
		for _, step := range local.Steps {
			status := runstatus.Step(*local, step).Word
			if _, e = fmt.Fprintf(out, "  %s · attempt %d · %s\n", step.Key, step.Attempt, status); e != nil {
				return written, e
			}
		}
	}
	if journalUnavailable {
		if _, err := fmt.Fprintln(out, "local run journals unavailable; inspect herdr-kata doctor"); err != nil {
			return written, err
		}
	}
	return written, nil
}

func runHistoryTime(r kata.Run) time.Time {
	if r.StartedAt != nil {
		return *r.StartedAt
	}
	return r.CreatedAt
}

var errFilteredJobAbsent = errors.New("job absent from project")

func listRunsProjects(ctx context.Context, cfg config.Config, project, job string, limit int, out io.Writer, cli func(config.Config) runsReader) error {
	selected, found := false, false
	var failures error
	remaining := limit
	for _, entry := range cfg.ProjectEntries() {
		if remaining <= 0 {
			break
		}
		if project != "" && project != entry.Name {
			continue
		}
		selected = true
		written, err := listRunsProject(ctx, cli(cfg.View(entry)), job, remaining, out, entry.Name)
		remaining -= written
		if errors.Is(err, errFilteredJobAbsent) {
			continue
		}
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("project %s: %w", entry.Name, err))
			continue
		}
		found = true
	}
	if !selected {
		return fmt.Errorf("unknown project %q", project)
	}
	if failures != nil {
		return failures
	}
	if !found && job != "" {
		return fmt.Errorf("unknown job %q", job)
	}
	return nil
}
