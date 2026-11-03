// Package doctor checks the effective core and project execution environments.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/attempt"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/control"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
	for _, p := range c.Projects {
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
		}
	}
	terminal, err := h.Get(ctx, control.Name(l.Root))
	if err == nil && (terminal.State != "live" || terminal.Labels["kata.worker"] != control.Name(l.Root)) {
		err = errors.New("worker is not live")
	}
	check("worker", err)
	return errors.Join(errs...)
}
