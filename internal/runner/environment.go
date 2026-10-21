package runner

import (
	"context"
	"maps"
	"os"

	"github.com/salmonumbrella/herdr-kata/internal/agentlimit"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
)

type launchEnvironment interface {
	Check(context.Context, *herdr.Client, func(context.Context) (string, error), map[string]string, []string, string, string) (herdr.ServerID, error)
}

func (m *Manager) tabEnvironment(uid, dir, report string, secrets map[string]string) map[string]string {
	env := make(map[string]string)
	maps.Copy(env, m.cfg.Kata.Env)
	maps.Copy(env, secrets)
	env["HERDR_KATA_RUN"] = uid
	env["HERDR_KATA_RUN_DIR"] = dir
	env["HERDR_KATA_REPORT"] = report
	env["KATA_PROJECT"] = m.cfg.Kata.Project
	if m.cfg.Kata.Daemon != "" {
		env["KATA_DAEMON"] = m.cfg.Kata.Daemon
	} else {
		delete(env, "KATA_DAEMON")
	}
	delete(env, "KATA_AUTH_TOKEN")
	for _, name := range m.cfg.CredentialVariables() {
		delete(env, name)
	}
	return env
}

// agentEnv reads the agent home directories the launch probe saw in a pane
// like the agent's. Before any probe has passed it reads the worker's own.
func (m *Manager) agentEnv() agentlimit.Env {
	if p, ok := m.environment.(interface{ AgentHomes() map[string]string }); ok {
		if homes := p.AgentHomes(); homes != nil {
			return func(key string) string { return homes[key] }
		}
	}
	return nil
}
func (m *Manager) checkEnvironment(ctx context.Context, cwd, report string) (herdr.ServerID, error) {
	probeBin, err := os.Executable()
	if err != nil {
		return herdr.ServerID{}, err
	}
	if report == "" {
		report = probeBin
	}
	env := m.tabEnvironment("probe", cwd, report, nil)
	return m.environment.Check(ctx, m.h, func(ctx context.Context) (string, error) { ws, e := m.ensureWorkspace(ctx); return ws.WorkspaceID, e }, env, m.cfg.CredentialVariables(), probeBin, cwd)
}
func (m *Manager) holdEnvironment(uid string, index int, err error) error {
	_, e := m.store.Transition(uid, func(r *Run) error {
		r.Phase = Held
		r.Outcome = "unknown"
		r.Reason = "herdr_env_unclean"
		r.ActiveStep = r.Steps[index].Key
		r.Steps[index].Reason = err.Error()
		return nil
	})
	return e
}
