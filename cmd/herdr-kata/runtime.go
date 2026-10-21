package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/daemon"
	"github.com/salmonumbrella/herdr-kata/internal/discovery"
	"github.com/salmonumbrella/herdr-kata/internal/doctor"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/lease"
	"github.com/salmonumbrella/herdr-kata/internal/migration"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func runtimeCommand(args []string, stdout, stderr io.Writer) int {
	command := args[0]
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(stderr)
	var asJSON, pause, takeover bool
	var named repeatedFlag
	if command == "up" {
		f.BoolVar(&takeover, "takeover", false, "take over the configured Kata lanes from another installation")
		f.Var(&named, "project", "at first start, also configure this Kata project when it has no definitions (repeatable)")
	}
	if command == "status" || command == "doctor" {
		f.BoolVar(&asJSON, "json", false, "print JSON")
	}
	if command == "status" {
		f.BoolVar(&pause, "pause", false, "wait for Enter before closing")
	}
	if err := f.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected arguments for %s\n", command)
		return 2
	}
	l := state.Layout{Root: state.DefaultRoot()}
	fail := func(err error) int { fmt.Fprintln(stderr, err); return 1 }
	if command == "status" {
		return statusCommand(l, asJSON, pause, stdout, stderr)
	}
	if command == "stop" {
		if err := daemon.Stop(l); err != nil {
			return fail(err)
		}
		return 0
	}
	configPath := config.DefaultPath()
	if command == "up" || command == "serve" || command == "worker" {
		if err := migration.Ensure(filepath.Dir(configPath), l.Root); err != nil {
			return fail(fmt.Errorf("plugin ID migration: %w", err))
		}
	}
	migrationCheck := doctor.CheckResult{Name: "plugin ID migration", Passed: true}
	if command == "doctor" {
		detail, err := migration.Inspect(filepath.Dir(configPath), l.Root)
		migrationCheck.Detail = detail
		if err != nil {
			migrationCheck.Passed = false
			migrationCheck.Detail += ": " + err.Error()
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	if command == "up" {
		if _, err := os.Stat(configPath); err == nil && len(named) > 0 {
			return fail(fmt.Errorf("--project applies only to first-start discovery; %s exists, so add a [[projects]] entry for each project there, then run herdr-kata stop and herdr-kata up", configPath))
		}
		if _, err := discoverConfig(ctx, configPath, named); err != nil {
			return fail(err)
		}
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		err = fmt.Errorf("%s: %w", config.DefaultPath(), err)
		if command == "doctor" && asJSON {
			checks := []doctor.CheckResult{{Name: "config", Detail: err.Error()}}
			if !migrationCheck.Passed {
				checks = append(checks, migrationCheck)
			}
			_ = json.NewEncoder(stdout).Encode(doctor.Report{Checks: checks})
			return 1
		}
		if command == "doctor" {
			fmt.Fprintf(stdout, "plugin ID migration: %s\n", migrationCheck.Detail)
		}
		return fail(err)
	}
	switch command {
	case "doctor":
		// Kata's CLI uses a stable working directory without creating state.
		dir := l.Root
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			dir = os.TempDir()
		}
		report := doctor.Check(ctx, *cfg, kata.New(cfg.Kata, dir, nil, nil), &herdr.Client{Path: herdr.SocketPath(cfg.Session)}, l)
		report.Checks = append([]doctor.CheckResult{migrationCheck}, report.Checks...)
		if asJSON {
			if err := json.NewEncoder(stdout).Encode(report); err != nil {
				return fail(err)
			}
		} else {
			for _, c := range report.Checks {
				label := "PASS"
				if !c.Passed {
					label = "FAIL"
				}
				fmt.Fprintf(stdout, "%s %s: %s\n", label, c.Name, c.Detail)
			}
		}
		if !report.OK() {
			return 1
		}
		return 0
	case "up":
		if herdr.SessionFromSocket(herdr.SocketPath(cfg.Session)) != cfg.Session {
			return 0
		}
		if len(cfg.ProjectEntries()) == 0 {
			layouts, err := state.ProjectLayouts(state.OSFS{}, l)
			if err != nil {
				return fail(err)
			}
			if len(layouts) == 0 {
				fmt.Fprintf(stdout, "nothing started: %v\n", config.NoProjectsError())
				return 0
			}
		}
		if _, err := state.LayoutPresent(state.OSFS{}, l); err != nil {
			return fail(err)
		}
		for _, entry := range cfg.ProjectEntries() {
			if _, err := cfg.View(entry).Kata.Credential(); err != nil {
				fmt.Fprintf(stderr, "warning: project %s: %v; configure credential_file for startup hooks and run herdr-kata up after repair\n", entry.Name, err)
			}
		}
		for _, check := range doctor.ResourceChecks(*cfg) {
			if !check.Passed {
				return fail(fmt.Errorf("%s: %s", check.Name, check.Detail))
			}
		}
		if takeover {
			if err := daemon.Stop(l); err != nil {
				return fail(err)
			}
		}
		for _, entry := range cfg.ProjectEntries() {
			view := cfg.View(entry)
			if _, err := view.Kata.Credential(); err != nil {
				continue
			}
			k := kata.New(view.Kata, os.TempDir(), nil, nil)
			if takeover {
				if err := takeOverLane(ctx, k, view, l); err != nil {
					return fail(fmt.Errorf("project %s: take over the lane: %w", entry.Name, err))
				}
				continue
			}
			// An unreadable lease is not a refusal; the lane retries it.
			current, found, err := lease.Read(ctx, k)
			if err == nil && found && current.Owner != view.Executor && current.Live(time.Now()) {
				return fail(fmt.Errorf("project %s: %w", entry.Name, &lease.HeldError{Lease: current}))
			}
		}
		exe, err := os.Executable()
		if err != nil {
			return fail(err)
		}
		result, err := daemon.Up(ctx, *cfg, l, exe)
		if err != nil {
			return fail(err)
		}
		if result != daemon.WrongSession {
			if len(cfg.ProjectEntries()) == 0 {
				fmt.Fprintf(stdout, "%s: no projects configured; retained lanes need retirement and saved reports\n", result)
			} else {
				fmt.Fprintln(stdout, result)
			}
		}
	case "serve":
		if os.Getenv("HERDR_SOCKET_PATH") == "" {
			if err := os.Setenv("HERDR_SOCKET_PATH", herdr.SocketPath(cfg.Session)); err != nil {
				return fail(err)
			}
		}
		exe, err := os.Executable()
		if err != nil {
			return fail(err)
		}
		if err := daemon.Serve(ctx, l, exe); err != nil {
			return fail(err)
		}
	case "worker":
		// The supervisor owns rotation and continuously drains this pipe.
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
		if err := daemon.Worker(ctx, *cfg, l); err != nil {
			return fail(err)
		}
	}
	return 0
}

// discoverConfig performs the only automatic configuration mutation. A
// missing config outside a named Herdr session falls through to config.Load's
// existing setup guidance; an active session gets a read-only route probe and
// an atomic first file.
func discoverConfig(ctx context.Context, path string, named []string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	// Without a config there is no session name to derive from Herdr's
	// default path. HERDR_SOCKET_PATH is the selected live session boundary;
	// an unset override means this invocation is outside Herdr.
	socket := os.Getenv("HERDR_SOCKET_PATH")
	if socket == "" {
		return false, nil
	}
	session := herdr.SessionFromSocket(socket)
	if session == "" {
		return false, nil
	}
	k, err := discovery.NewCommandKata(ctx)
	if err != nil {
		return false, err
	}
	d := discovery.Discoverer{Kata: k, Herdr: &herdr.Client{Path: socket}, Session: session, Named: named}
	return d.Ensure(ctx, path)
}

// takeOverLane writes the next generation of the lane lease for this
// installation, on a checked route that matches any saved pin. The previous
// owner stands down when it next sees the lease.
func takeOverLane(ctx context.Context, k *kata.CLI, view config.Config, l state.Layout) error {
	identity, route, err := kata.ResolveIdentity(ctx, k)
	if err != nil {
		return err
	}
	if identity.Actor != view.Kata.Actor {
		return errors.New("authenticated actor differs from configured actor")
	}
	if state.SafeComponent(identity.ProjectUID) {
		var pin kata.Installation
		err := state.ReadJSON(state.OSFS{}, l.Project(identity.ProjectUID).StateFile(), 64<<10, &pin)
		if err == nil {
			err = kata.ComparePin(pin, identity, route, view.Executor)
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	_, err = lease.Claim(ctx, k, view.Executor, true, time.Now())
	return err
}
