package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/salmonumbrella/herdr-kata/internal/control"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func Current() Manager {
	home, _ := os.UserHomeDir()
	return Manager{runtime.GOOS, home, strconv.Itoa(os.Getuid()), os.Getenv, func(ctx context.Context, name string, args ...string) error {
		b, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, b)
		}
		return nil
	}}
}

func (m Manager) Command(ctx context.Context, args []string, cfg, root, exe string, out io.Writer) int {
	if m.OS != "darwin" && m.OS != "linux" {
		fmt.Fprintf(out, "service: unsupported on %s; run herdr-kata up from your login environment\n", m.OS)
		return 1
	}
	if !m.directoriesValid() {
		fmt.Fprintln(out, ErrInput, ": HOME and service directory must be absolute printable UTF-8")
		return 2
	}
	if err := m.apply(ctx, args, cfg, root, exe, out); err != nil {
		fmt.Fprintln(out, err)
		if errors.Is(err, ErrInput) {
			return 2
		}
		return 1
	}
	return 0
}

func (m Manager) directoriesValid() bool {
	dir := m.Getenv("XDG_CONFIG_HOME")
	return filepath.IsAbs(m.Home) && valid(m.Home) && (m.OS != "linux" || dir == "" || filepath.IsAbs(dir) && valid(dir))
}

func (m Manager) path(root string) string {
	if m.OS == "darwin" {
		return filepath.Join(m.Home, "Library", "LaunchAgents", control.Name(root)+".plist")
	}
	dir := m.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(m.Home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", control.Name(root)+".service")
}

func (m Manager) call(ctx context.Context, args ...string) error {
	bin := "launchctl"
	if m.OS == "linux" {
		bin, args = "systemctl", append([]string{"--user"}, args...)
	}
	err := m.Run(ctx, bin, args...)
	var exit interface{ ExitCode() int }
	if m.OS == "darwin" && args[0] == "bootout" && errors.As(err, &exit) && exit.ExitCode() == 3 {
		return nil
	}
	return err
}

func (m Manager) apply(ctx context.Context, args []string, cfg, root, exe string, out io.Writer) error {
	path, name := m.path(root), control.Name(root)
	if args[0] == "uninstall" {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fmt.Fprintf(out, "service %s not installed\n", name)
			return nil
		} else if err != nil {
			return err
		}
		command := []string{"disable", name + ".service"}
		if m.OS == "darwin" {
			command = []string{"bootout", "gui/" + m.UID + "/" + name}
		}
		if err := m.call(ctx, command...); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if m.OS == "linux" {
			if err := m.call(ctx, "daemon-reload"); err != nil {
				return err
			}
		}
		fmt.Fprintf(out, "service %s removed; the worker keeps running (herdr-kata stop stops it)\n", name)
		return nil
	}
	b, err := m.render(cfg, root, exe)
	if err != nil {
		return err
	}
	if len(args) == 2 {
		_, err = out.Write(b)
		return err
	}
	if m.OS == "darwin" {
		if err = state.EnsureDir(filepath.Join(m.Home, "Library", "Logs")); err != nil {
			return err
		}
	}
	if err = state.WriteFile(path, b); err != nil {
		return err
	}
	steps := [][]string{{"daemon-reload"}, {"enable", name + ".service"}, {"restart", name + ".service"}}
	if m.OS == "darwin" {
		steps = [][]string{{"bootout", "gui/" + m.UID + "/" + name}, {"bootstrap", "gui/" + m.UID, path}}
	}
	for _, step := range steps {
		if err = m.call(ctx, step...); err != nil {
			return fmt.Errorf("service %s written to %s; activation failed: %w", name, path, err)
		}
	}
	fmt.Fprintf(out, "service %s installed: %s\n", name, path)
	return nil
}

func (m Manager) Status(root string) string {
	if m.OS != "darwin" && m.OS != "linux" {
		return fmt.Sprintf("service %s: unknown (unsupported on %s)", control.Name(root), m.OS)
	}
	if !m.directoriesValid() {
		return fmt.Sprintf("service %s: unknown (%v)", control.Name(root), ErrInput)
	}
	path, status := m.path(root), "not installed"
	if _, err := os.Stat(path); err == nil {
		status = "installed " + path
	} else if !os.IsNotExist(err) {
		status = fmt.Sprintf("unknown (%v)", err)
	}
	return fmt.Sprintf("service %s: %s", control.Name(root), status)
}
