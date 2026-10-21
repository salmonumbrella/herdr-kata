package kata

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
)

func (c *CLI) Capabilities(ctx context.Context) (Capabilities, error) {
	var v Capabilities
	err := c.call(ctx, nil, &v, "cron", "capabilities")
	return v, err
}
func (c *CLI) FederationIdentity(ctx context.Context) (Identity, error) {
	var v struct {
		InstanceUID string `json:"instance_uid"`
		Auth        struct {
			Actor string `json:"actor"`
		} `json:"auth"`
	}
	err := c.call(ctx, nil, &v, "federation", "identity")
	actor := v.Auth.Actor
	if actor == "" {
		actor = c.cfg.Actor
	}
	return Identity{InstanceUID: v.InstanceUID, Actor: actor}, err
}
func (c *CLI) DaemonDiagnose(ctx context.Context, projectUID string) (Diagnosis, error) {
	var raw json.RawMessage
	err := c.call(ctx, nil, &raw, "daemon", "diagnose", "--expect-project-uid", projectUID)
	if err != nil {
		return Diagnosis{}, err
	}
	var v struct {
		State          string       `json:"state"`
		Endpoint       string       `json:"endpoint"`
		FederationRole string       `json:"federation_role"`
		ProjectUID     string       `json:"project_uid"`
		HubOrigin      string       `json:"hub_origin"`
		RootSigning    *RootSigning `json:"root_signing"`
	}
	if err = json.Unmarshal(raw, &v); err != nil {
		return Diagnosis{}, err
	}
	d := Diagnosis{ProjectUID: v.ProjectUID, FederationRole: v.FederationRole, HubOrigin: v.HubOrigin, RootSigning: v.RootSigning, State: v.State, Endpoint: v.Endpoint, Authoritative: v.State == "ready" && v.ProjectUID == projectUID && (v.FederationRole == "" || v.FederationRole == "hub"), Raw: raw}
	c.mu.Lock()
	c.diagnosis = d
	c.mu.Unlock()
	return d, nil
}

// DiagnosedRole is information from the last route check, not an admission gate.
func (c *CLI) DiagnosedRole() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.diagnosis.FederationRole
}

// ResolveRoute returns the configured route and the latest diagnosis. The caller
// must refresh diagnosis before using this snapshot to authorize a batch.
func (c *CLI) ResolveRoute() Route {
	c.mu.Lock()
	defer c.mu.Unlock()
	env := cloneEnv(c.cfg.Env)
	delete(env, c.cfg.CredentialEnv)
	return Route{Bin: c.cfg.Bin, Daemon: c.cfg.Daemon, Env: env, Endpoint: c.diagnosis.Endpoint, DiagnoseState: c.diagnosis.State}
}
func (c *CLI) ListJobs(ctx context.Context) ([]Job, error) {
	var v struct {
		Jobs []Job `json:"jobs"`
	}
	err := c.call(ctx, nil, &v, "cron", "job", "list")
	if err == nil && v.Jobs == nil {
		err = errors.New("incomplete job list response")
	}
	return v.Jobs, err
}
func (c *CLI) ShowJob(ctx context.Context, uid string) (Job, error) {
	var v struct {
		Job Job `json:"job"`
	}
	err := c.call(ctx, nil, &v, "cron", "job", "show", uid)
	return v.Job, err
}
func (c *CLI) ListWorkflows(ctx context.Context) ([]Workflow, error) {
	var v struct {
		Workflows []Workflow `json:"workflows"`
	}
	err := c.call(ctx, nil, &v, "cron", "workflow", "list")
	if err == nil && v.Workflows == nil {
		err = errors.New("incomplete workflow list response")
	}
	return v.Workflows, err
}
func (c *CLI) ShowWorkflow(ctx context.Context, uid string) (Workflow, error) {
	var v struct {
		Workflow Workflow `json:"workflow"`
	}
	err := c.call(ctx, nil, &v, "cron", "workflow", "show", uid)
	return v.Workflow, err
}
func (c *CLI) Observe(ctx context.Context, uid string, observation Observation) (Run, bool, error) {
	// Freeze the runtime teammate in the JSON as well as argv so replay cannot
	// inherit a different attribution from a later process environment.
	if observation.Teammate == nil {
		teammate := "herdr-kata"
		observation.Teammate = &teammate
	}
	body, err := json.Marshal(observation)
	if err != nil {
		return Run{}, false, err
	}
	var v struct {
		Run      Run  `json:"run"`
		Replayed bool `json:"replayed"`
	}
	err = c.call(ctx, body, &v, "cron", "run", "observe", uid, "--json-input", "-")
	return v.Run, v.Replayed, err
}
func (c *CLI) ShowRun(ctx context.Context, uid string) (Run, error) {
	var v struct {
		Run Run `json:"run"`
	}
	err := c.call(ctx, nil, &v, "cron", "run", "show", uid)
	return v.Run, err
}
func (c *CLI) ListRuns(ctx context.Context, jobUID, beforeUID string, limit int) ([]Run, string, error) {
	args := []string{"cron", "run", "list", "--limit", strconv.Itoa(limit)}
	if jobUID != "" {
		args = append(args, "--job-uid", jobUID)
	}
	if beforeUID != "" {
		args = append(args, "--before-uid", beforeUID)
	}
	var v struct {
		Runs          []Run  `json:"runs"`
		NextBeforeUID string `json:"next_before_uid"`
	}
	err := c.call(ctx, nil, &v, args...)
	return v.Runs, v.NextBeforeUID, err
}

func (c *CLI) Health(ctx context.Context) (Health, error) {
	var h Health
	err := c.call(ctx, nil, &h, "health")
	return h, err
}

// A passing access probe never proves an adjacent not-found is genuine.
func ProbeAccess(ctx context.Context, reader any) {
	if p, ok := reader.(interface{ ProbeAccess(context.Context) error }); ok {
		_ = p.ProbeAccess(ctx)
	}
}
