package main

import (
	"context"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/attempt"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/control"
	"github.com/salmonumbrella/herdr-kata/internal/doctor"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent, err := canonical(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}
func defaultState() string {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(root, "herdr-kata")
}
func operatorCommand(args []string, configPath, root string, out io.Writer) int {
	if configPath == "" {
		configPath = config.DefaultPath()
	}
	if root == "" {
		root = defaultState()
	}
	path, err := filepath.Abs(configPath)
	if err != nil {
		fmt.Fprintln(out, err)
		return 2
	}
	c, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(out, err)
		return 2
	}
	root, err = canonical(root)
	if err != nil {
		fmt.Fprintln(out, err)
		return 2
	}
	l := state.Layout{Root: root}
	st := journal.New(l)
	h := hc.New(c.HCBin, c.HCSession)
	clients := make(map[string]kata.Kata)
	for _, p := range c.Projects {
		clients[p.Name] = kata.New(c.Kata.Bin, c.Kata.Daemon, c.Kata.Env, p.Name, p.Actor)
	}
	bin, err := os.Executable()
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if args[0] != "run" && args[0] != "release" && args[0] != "logs" && len(args) != 1 {
		fmt.Fprintln(out, "unexpected arguments")
		return 2
	}
	switch args[0] {
	case "run", "release":
		return postRequest(ctx, args, st, out)
	case "logs":
		return logs(ctx, args, st, h, out)
	case "up":
		if err = state.EnsureDir(root); err == nil {
			var t hc.Terminal
			t, err = control.Up(ctx, h, l, bin, path, attempt.LaunchEnv(c.Limits.UnsetEnv, c.Kata.Env))
			if err == nil {
				for _, q := range c.Quarantined {
					fmt.Fprintln(out, "warning: quarantined", q)
				}
				fmt.Fprintln(out, "worker", t.Address)
			}
		}
	case "stop":
		err = control.Stop(ctx, h, l)
	case "serve":
		err = serve(ctx, *c, st, h, clients, bin, out)
	case "doctor":
		err = doctor.Check(ctx, *c, l, h, clients, out)
	}
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	return 0
}
