// Package hc isolates the herdr-core CLI and its short/full response formats.
package hc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

var ErrNotFound = errors.New("terminal not found")
var ErrLive = errors.New("terminal still live")
var ErrNotSent = errors.New("terminal did not accept send")

type Launched struct{ Address, Name, State string }
type Terminal struct {
	Address        string            `json:"address"`
	Name           string            `json:"name"`
	State          string            `json:"state"`
	Reason         string            `json:"reason"`
	Status         string            `json:"-"`
	App            string            `json:"-"`
	Turn           uint64            `json:"turn"`
	Reported       bool              `json:"reported"`
	CreatedMs      int64             `json:"created_ms"`
	PID            uint32            `json:"pid"`
	ForegroundPID  uint32            `json:"-"`
	ForegroundArgv []string          `json:"-"`
	Argv           []string          `json:"argv"`
	Labels         map[string]string `json:"labels"`
	ExitCode       *int              `json:"exit_code"`
	Signal         *int              `json:"signal"`
}
type RunSpec struct {
	Name, Cwd    string
	Labels       map[string]string
	Prefix, Argv []string
}
type HC interface {
	Run(context.Context, RunSpec) (Launched, error)
	Send(context.Context, string, string, string) (uint64, error)
	Get(context.Context, string) (Terminal, error)
	List(context.Context, bool, map[string]string) ([]string, error)
	Read(context.Context, string, int) (string, bool, error)
	Interrupt(context.Context, string) error
	Kill(context.Context, string) error
	Remove(context.Context, string) error
	CoreID(context.Context) (string, error)
	Info(context.Context) (ServerInfo, error)
	Exec(context.Context, []string) (string, error)
}
type cli struct{ bin, session string }

func New(bin, session string) HC { return &cli{bin, session} }

func (c *cli) call(ctx context.Context, args ...string) ([]byte, error) {
	limit := 10 * time.Second
	if args[0] == "send" {
		limit = 75 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, args...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "HC_SOCKET=") && !strings.HasPrefix(e, "HC_SESSION=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "HC_SESSION="+c.session)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	var body struct{ Error struct{ Kind string } }
	if json.Unmarshal(stderr.Bytes(), &body) == nil {
		if body.Error.Kind == "live" {
			return nil, ErrLive
		}
		if args[0] == "send" && slices.Contains([]string{"status_mismatch", "blocked", "not_found", "finished"}, body.Error.Kind) {
			return nil, ErrNotSent
		}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 3 {
		return nil, ErrNotFound
	}
	return nil, fmt.Errorf("hc %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
}

func (c *cli) Send(ctx context.Context, address, text, status string) (uint64, error) {
	args := []string{"send", "--json"}
	if status == "idle" {
		args = append(args, "--if", "idle")
	} else if status != "done" {
		return 0, ErrNotSent
	}
	b, err := c.call(ctx, append(args, address, text)...)
	if err != nil {
		return 0, err
	}
	var result struct{ Turn uint64 }
	err = json.Unmarshal(b, &result)
	return result.Turn, err
}

func labelsArgs(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var args []string
	for _, k := range keys {
		args = append(args, "--label", k+"="+labels[k])
	}
	return args
}

func (c *cli) Run(ctx context.Context, s RunSpec) (Launched, error) {
	args := []string{"run", "--json", "--bg", "--no-parent", "--name", s.Name, "--cwd", s.Cwd}
	args = append(args, labelsArgs(s.Labels)...)
	args = append(args, "--")
	args = append(args, s.Prefix...)
	args = append(args, s.Argv...)
	b, err := c.call(ctx, args...)
	if err != nil {
		return Launched{}, err
	}
	var v Launched
	err = json.Unmarshal(b, &v)
	return v, err
}

func (c *cli) Get(ctx context.Context, target string) (Terminal, error) {
	b, err := c.call(ctx, "get", "--json", target)
	if err != nil {
		return Terminal{}, err
	}
	var wire struct {
		Terminal
		Status     *struct{ State, App string } `json:"status"`
		Foreground *struct {
			PID  uint32
			Argv []string
		} `json:"foreground"`
	}
	if err = json.Unmarshal(b, &wire); err != nil {
		return Terminal{}, err
	}
	if wire.Status != nil {
		wire.Terminal.Status = wire.Status.State
		wire.App = wire.Status.App
	}
	if wire.Foreground != nil {
		wire.ForegroundPID = wire.Foreground.PID
		wire.ForegroundArgv = wire.Foreground.Argv
	}
	return wire.Terminal, nil
}

func (c *cli) List(ctx context.Context, all bool, labels map[string]string) ([]string, error) {
	args := []string{"ls", "--json"}
	if all {
		args = append(args, "--all")
	}
	args = append(args, labelsArgs(labels)...)
	b, err := c.call(ctx, args...)
	if err != nil {
		return nil, err
	}
	var records struct{ Terminals []struct{ Address string } }
	if err = json.Unmarshal(b, &records); err != nil {
		return nil, err
	}
	addresses := make([]string, 0, len(records.Terminals))
	for _, r := range records.Terminals {
		addresses = append(addresses, r.Address)
	}
	return addresses, nil
}

func (c *cli) Read(ctx context.Context, target string, lines int) (string, bool, error) {
	b, err := c.call(ctx, "read", "--json", "--lines", strconv.Itoa(lines), target)
	if err != nil {
		return "", false, err
	}
	var v struct {
		Text     string
		Complete bool
	}
	err = json.Unmarshal(b, &v)
	return v.Text, v.Complete, err
}
func (c *cli) Interrupt(ctx context.Context, target string) error {
	_, err := c.call(ctx, "interrupt", "--json", target)
	return err
}
func (c *cli) Kill(ctx context.Context, target string) error {
	_, err := c.call(ctx, "kill", "--json", target)
	return err
}
func (c *cli) Remove(ctx context.Context, target string) error {
	_, err := c.call(ctx, "rm", "--json", target)
	return err
}
func (c *cli) CoreID(ctx context.Context) (string, error) {
	v, err := c.Info(ctx)
	return v.CoreID, err
}

type ServerInfo struct {
	CoreID, Version string
	PID             int
	UptimeMs        int64
}

func (c *cli) Exec(ctx context.Context, argv []string) (string, error) {
	b, err := c.call(ctx, append([]string{"exec", "--"}, argv...)...)
	return string(b), err
}
func (c *cli) Info(ctx context.Context) (ServerInfo, error) {
	b, err := c.call(ctx, "server", "status", "--json")
	if err != nil {
		return ServerInfo{}, err
	}
	var v struct {
		Core     struct{ ID string }
		Version  string
		PID      int
		UptimeMs int64 `json:"uptime_ms"`
	}
	err = json.Unmarshal(b, &v)
	if err == nil && v.Core.ID == "" {
		err = errors.New("hc status has no core id")
	}
	return ServerInfo{CoreID: v.Core.ID, Version: v.Version, PID: v.PID, UptimeMs: v.UptimeMs}, err
}

// EnvPrefix carries values as argv elements without shell interpolation.
func EnvPrefix(unset []string, set map[string]string) []string {
	unset = append(slices.Clone(unset), "HC_SOCKET", "KATA_AUTH_TOKEN")
	slices.Sort(unset)
	unset = slices.Compact(unset)
	args := []string{"env"}
	for _, k := range unset {
		args = append(args, "-u", k)
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		args = append(args, k+"="+set[k])
	}
	return args
}
