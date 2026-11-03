// Package doctor checks the effective core and project execution environments.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/attempt"
	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/control"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/report"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const HCFloor = "0.1.15"

func versionOK(v string) bool {
	var major, minor, patch int
	n, err := fmt.Sscanf(strings.TrimPrefix(v, "v"), "%d.%d.%d", &major, &minor, &patch)
	return err == nil && n == 3 && (major > 0 || minor > 1 || minor == 1 && patch >= 15)
}
func Check(ctx context.Context, c config.Config, l state.Layout, h hc.HC, clients map[string]kata.Kata, out io.Writer) error {
	var errs []error
	check := func(name string, err error) {
		if err != nil {
			fmt.Fprintf(out, "FAIL %s: %v\n", name, err)
			errs = append(errs, err)
		} else {
			fmt.Fprintf(out, "OK %s\n", name)
		}
	}
	socket, err := hc.Socket(c.HCSession)
	check("socket", err)
	fmt.Fprintf(out, "session %s; socket %s\n", c.HCSession, socket)
	if os.Getenv("HC_SOCKET") != "" {
		fmt.Fprintln(out, "warning: HC_SOCKET is set and ignored")
	}
	info, err := h.Info(ctx)
	check("core", err)
	if err == nil {
		if !versionOK(info.Version) {
			check("hc version", fmt.Errorf("hc %s is below %s", info.Version, HCFloor))
		} else {
			check("hc version "+info.Version, nil)
		}
		if info.UptimeMs < 1000 {
			fmt.Fprintln(out, "core just started")
		}
	}
	for name, p := range c.Profiles {
		if len(p.Argv) == 0 {
			check("profile "+name, errors.New("argv missing"))
			continue
		}
		resolved, e := exec.LookPath(p.Argv[0])
		if e == nil && !filepath.IsAbs(resolved) {
			e = errors.New("profile binary is not absolute")
		}
		check("profile "+name, e)
		if filepath.Base(p.Argv[0]) == "codex" && !slices.Contains(p.Argv, "--no-daemon") {
			fmt.Fprintf(out, "warning: profile %s needs codex --no-daemon\n", name)
		}
	}
	for name, path := range c.Checkouts {
		info, e := os.Stat(path)
		if e == nil && !info.IsDir() {
			e = errors.New("checkout is not a directory")
		}
		check("checkout "+name, e)
	}
	outbox, err := (report.Outbox{Layout: l}).All()
	check("unsupported outbox", err)
	pending, refused := 0, 0
	for _, r := range outbox {
		if r.State == "done" {
			continue
		}
		if r.State == "pending" {
			pending++
		} else {
			refused++
		}
		var b kata.ObserveCronRunBody
		_ = json.Unmarshal(r.Body, &b)
		fmt.Fprintf(out, "unsupported %s: report %s: %s\n", *b.JobUID, r.State, r.Error)
		if r.State == "refused" {
			fmt.Fprintf(out, "fix the cause, then delete %s to report anew under a new run UID\n", filepath.Join(l.Unsupported(), r.Cause+".json"))
		}
	}
	fmt.Fprintf(out, "unsupported reports: pending=%d refused=%d\n", pending, refused)
	active, err := journal.New(l).Active()
	check("run journals", err)
	for _, p := range c.Projects {
		held := []string{}
		count := 0
		for _, r := range active {
			if r.Project == p.Name && r.Phase != "settled" {
				count++
				if r.Phase == "held" {
					held = append(held, r.RunUID)
				}
			}
		}
		limit := p.MaxActiveRuns
		if limit == 0 {
			limit = 1
		}
		if count >= limit && len(held) > 0 {
			slices.Sort(held)
			fmt.Fprintf(out, "project %s: held runs hold capacity: %s\n", p.Name, strings.Join(held, ", "))
		}
		base := append(attempt.LaunchEnv(c.Limits.UnsetEnv, c.Kata.Env), c.Kata.Bin, "--project", p.Name, "--daemon", c.Kata.Daemon, "--as", p.Actor, "--teammate", "herdr-kata", "--json")
		for _, command := range [][]string{{"whoami"}, {"cron", "capabilities"}} {
			raw, e := h.Exec(ctx, append(slices.Clone(base), command...))
			if e == nil && command[0] == "cron" {
				var v struct {
					Features []string `json:"event_features"`
				}
				if json.Unmarshal([]byte(raw), &v) != nil || !slices.Contains(v.Features, "cron_v1") {
					e = errors.New("cron_v1 capability missing")
				}
			}
			check("project "+p.Name+" "+strings.Join(command, " "), e)
		}
		k := clients[p.Name]
		if k == nil {
			check("project "+p.Name, errors.New("client missing"))
			continue
		}
		jobs, e := k.Jobs()
		if e != nil {
			check("definitions "+p.Name, e)
			continue
		}
		workflows, e := k.Workflows()
		if e != nil {
			check("workflows "+p.Name, e)
			continue
		}
		wfs := map[string]kata.CronWorkflow{}
		for _, wf := range workflows {
			wfs[wf.UID] = wf
		}
		ownedJobs := []kata.CronJob{}
		for _, job := range jobs {
			if owned, _ := defs.Owned(job, p.Name, c); owned {
				ownedJobs = append(ownedJobs, job)
			}
		}
		var ready map[string]bool
		if defs.NeedsReadiness(ownedJobs) {
			ready, e = k.Ready()
			check("readiness "+p.Name, e)
		}
		for _, job := range jobs {
			if owned, reason := defs.Owned(job, p.Name, c); !owned {
				if strings.HasPrefix(reason, "job is pinned to executor ") {
					fmt.Fprintf(out, "pinned elsewhere %s: options.executor=%s\n", job.UID, job.Definition.Options["executor"])
				}
				continue
			}
			_, reasons := defs.Freeze(job, wfs, c, p.Name, exec.LookPath)
			if len(reasons) > 0 {
				fmt.Fprintf(out, "unsupported %s: %s\n", job.UID, strings.Join(reasons, ", "))
			}
			if e == nil && job.Definition.Issue != nil && job.Definition.Issue.Kind == "existing" && job.Definition.Issue.UID != nil && !ready[*job.Definition.Issue.UID] {
				fmt.Fprintf(out, "gated %s: existing issue %s not ready\n", job.UID, *job.Definition.Issue.UID)
			}
			executorEvidence(k, job.UID, c.Executor, clock.Now(ctx), out)
		}
	}
	terminal, err := h.Get(ctx, control.Name(l.Root))
	if err == nil && (terminal.State != "live" || terminal.Labels["kata.worker"] != control.Name(l.Root)) {
		err = errors.New("worker is not live")
	}
	check("worker", err)
	return errors.Join(errs...)
}

func executorEvidence(k kata.Kata, uid, executor string, now time.Time, out io.Writer) {
	runs, err := k.Runs(uid, 100)
	if err != nil {
		fmt.Fprintf(out, "job %s: executor check incomplete: %v\n", uid, err)
		return
	}
	cutoff := now.Add(-7 * 24 * time.Hour)
	labels := map[string]bool{}
	for _, r := range runs {
		if r.CreatedAt.Before(cutoff) || r.Summary.Message != nil && strings.HasPrefix(*r.Summary.Message, "reason: unsupported") {
			continue
		}
		label := "unknown"
		if r.ExecutorLabel != nil && *r.ExecutorLabel != "" {
			label = *r.ExecutorLabel
		}
		if r.ExecutorLabel != nil && *r.ExecutorLabel == executor {
			continue
		}
		if !labels[label] {
			fmt.Fprintf(out, "warning: another executor reported job %s: %s\n", uid, label)
			labels[label] = true
		}
	}
	if len(runs) == 100 && !runs[len(runs)-1].CreatedAt.Before(cutoff) {
		fmt.Fprintf(out, "job %s: executor sample truncated\n", uid)
	}
}
