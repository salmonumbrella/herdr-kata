// Package kata transports pinned project/daemon/actor requests through Kata's CLI.
package kata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Class string

const (
	Definitive  Class = "definitive"
	Conflict    Class = "conflict"
	Unavailable Class = "unavailable"
	Uncertain   Class = "uncertain"
)

type Error struct {
	Code    int
	Message string
	Err     error
}

func (e *Error) Error() string { return fmt.Sprintf("kata exit %d: %s: %v", e.Code, e.Message, e.Err) }
func (e *Error) Unwrap() error { return e.Err }
func Classify(err error) Class {
	code := -1
	if e, ok := errors.AsType[*Error](err); ok {
		code = e.Code
	} else if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		code = exit.ExitCode()
	}
	switch code {
	case 2, 3, 4, 6:
		return Definitive
	case 5:
		return Conflict
	case 7:
		return Unavailable
	default:
		return Uncertain
	}
}

type Kata interface {
	Jobs() ([]CronJob, error)
	Workflows() ([]CronWorkflow, error)
	Ready() (map[string]bool, error)
	PlanningDates(string) (*DateValue, error)
	IssueTitle(string) (string, error)
	CreateIssue(string, string, string) (string, string, error)
	SetDeadline(string, time.Time) error
	Notify(string, string, string) error
	Observe(string, []byte, int) (int, bool, error)
	ShowRun(string) (int, []byte, error)
	Whoami() error
	Capabilities() error
}
type cli struct {
	bin         string
	prefix, env []string
}

func New(bin, daemon string, overrides map[string]string, project, actor string) Kata {
	c := &cli{bin: bin, prefix: []string{"--project", project, "--daemon", daemon, "--as", actor, "--teammate", "herdr-kata", "--json"}}
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if key == "KATA_AUTH_TOKEN" {
			continue
		}
		if _, ok := overrides[key]; !ok {
			c.env = append(c.env, e)
		}
	}
	for key, value := range overrides {
		if key == "KATA_HOME" || key == "KATA_SERVER" || strings.HasPrefix(key, "XDG_") {
			c.env = append(c.env, key+"="+value)
		}
	}
	return c
}

func (c *cli) call(input []byte, out any, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, append(append([]string{}, c.prefix...), args...)...)
	cmd.Env = c.env
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		code := -1
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			code = exit.ExitCode()
		}
		return &Error{Code: code, Message: strings.TrimSpace(stderr.String()), Err: err}
	}
	if out == nil {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(out)
}
func (c *cli) Jobs() ([]CronJob, error) {
	var v struct{ Jobs []CronJob }
	err := c.call(nil, &v, "cron", "job", "list")
	return v.Jobs, err
}
func (c *cli) Workflows() ([]CronWorkflow, error) {
	var v struct{ Workflows []CronWorkflow }
	err := c.call(nil, &v, "cron", "workflow", "list")
	return v.Workflows, err
}
func (c *cli) Ready() (map[string]bool, error) {
	var v struct{ Issues []struct{ UID string } }
	if err := c.call(nil, &v, "ready", "--limit", "0"); err != nil {
		return nil, err
	}
	ready := make(map[string]bool)
	for _, i := range v.Issues {
		ready[i.UID] = true
	}
	return ready, nil
}
func (c *cli) PlanningDates(uid string) (*DateValue, error) {
	var v struct {
		Scheduled *DateValue `json:"scheduled_on"`
	}
	err := c.call(nil, &v, "show", uid, "--planning-dates")
	return v.Scheduled, err
}
func (c *cli) IssueTitle(uid string) (string, error) {
	var v struct{ Issue struct{ Title string } }
	err := c.call(nil, &v, "show", uid)
	return v.Issue.Title, err
}
func (c *cli) CreateIssue(title, body, key string) (string, string, error) {
	var v struct {
		Issue struct {
			Ref string `json:"short_id"`
			UID string `json:"uid"`
		}
	}
	err := c.call(nil, &v, "create", title, "--body", body, "--force-new", "--idempotency-key", key)
	return v.Issue.Ref, v.Issue.UID, err
}
func (c *cli) SetDeadline(ref string, at time.Time) error {
	return c.call(nil, nil, "deadline", ref, at.UTC().Format(time.RFC3339))
}
func (c *cli) Notify(ref, to, msg string) error {
	return c.call(nil, nil, "notify", ref, "--to", to, "--message", msg)
}
func (c *cli) Observe(uid string, body []byte, expected int) (int, bool, error) {
	var identity struct {
		ExpectedRevision int `json:"expected_revision"`
	}
	if err := json.Unmarshal(body, &identity); err != nil {
		return 0, false, &Error{Code: 3, Message: "invalid observation", Err: err}
	}
	if identity.ExpectedRevision != expected {
		return 0, false, &Error{Code: 3, Message: "observation revision mismatch"}
	}
	var v struct {
		Run      struct{ Revision int }
		Replayed bool
	}
	err := c.call(body, &v, "cron", "run", "observe", uid, "--json-input", "-")
	return v.Run.Revision, v.Replayed, err
}
func (c *cli) ShowRun(uid string) (int, []byte, error) {
	var v struct{ Run json.RawMessage }
	if err := c.call(nil, &v, "cron", "run", "show", uid); err != nil {
		return 0, nil, err
	}
	var r struct{ Revision int }
	err := json.Unmarshal(v.Run, &r)
	return r.Revision, v.Run, err
}
func (c *cli) Whoami() error       { return c.call(nil, nil, "whoami") }
func (c *cli) Capabilities() error { return c.call(nil, nil, "cron", "capabilities") }
