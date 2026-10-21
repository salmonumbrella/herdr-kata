// Package doctor checks installation health without starting agents.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/agentlimit"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/lease"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type CheckResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}
type Report struct {
	Checks []CheckResult `json:"checks"`
}

func (r Report) OK() bool {
	for _, c := range r.Checks {
		if !c.Passed {
			return false
		}
	}
	return true
}

// Help probing uses the same bounded CLI transport, routing and environment as
// finite calls; no daemon is started and no profile is launched.
func Check(ctx context.Context, c config.Config, k *kata.CLI, h *herdr.Client, l state.Layout) Report {
	dir := l.Root
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		dir = os.TempDir()
	}
	return checkProjects(ctx, c, func(view config.Config) *kata.CLI {
		if k != nil {
			return k.WithConfig(view.Kata, dir)
		}
		return kata.New(view.Kata, dir, nil, nil)
	}, h, l)
}
func checkProjects(ctx context.Context, c config.Config, cli func(config.Config) *kata.CLI, h *herdr.Client, l state.Layout) Report {
	r := Report{}
	add := func(name, detail string, err error) {
		if err != nil {
			detail = err.Error()
		}
		r.Checks = append(r.Checks, CheckResult{name, err == nil, detail})
	}
	add("config", config.DefaultPath()+": configuration loaded", nil)
	if entries := c.ProjectEntries(); len(entries) == 0 {
		add("projects", config.NoProjectsError().Error(), nil)
	} else {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name)
		}
		add("projects", "configured: "+strings.Join(names, ", "), nil)
	}
	resources := ResourceChecks(c)
	r.Checks = append(r.Checks, resources...)
	kataAvailable := true
	for _, check := range resources {
		if check.Name == "kata.bin" {
			kataAvailable = check.Passed
		}
	}
	calling := herdr.SessionFromSocket(herdr.SocketPath(c.Session))
	var sessionErr error
	if calling != c.Session {
		sessionErr = fmt.Errorf("session = %q differs from calling Herdr session %q; run herdr-kata doctor in session %q or fix session in %s", c.Session, calling, c.Session, config.DefaultPath())
	}
	add("session", "matches calling Herdr session", sessionErr)
	local, localErr := LocalChecks(state.OSFS{}, l, c)
	for i := range local {
		if c.Kata.Project != "" {
			local[i].Detail = strings.ReplaceAll(local[i].Detail, "<name>", c.Kata.Project)
		}
	}
	r.Checks = append(r.Checks, local...)
	if localErr != nil {
		add("local runs", "", localErr)
	}
	layouts, layoutErr := state.ProjectLayouts(state.OSFS{}, l)
	add("state layout", "ready", layoutErr)
	if layoutErr == nil {
		r.Checks = append(r.Checks, definitionChecks(state.OSFS{}, l, layouts, c, time.Now())...)
	}
	if layoutErr == nil && kataAvailable {
		seen := map[string]string{}
		for _, entry := range c.ProjectEntries() {
			view := c.View(entry)
			report, uid := checkKata(ctx, view, cli(view), l)
			for _, check := range report.Checks {
				if len(c.ProjectEntries()) > 1 {
					check.Name = entry.Name + ": " + check.Name
				}
				r.Checks = append(r.Checks, check)
			}
			if uid != "" {
				if previous, ok := seen[uid]; ok {
					add("duplicate project UID", "", fmt.Errorf("entries %s and %s resolve to project %s", previous, entry.Name, uid))
				} else {
					seen[uid] = entry.Name
				}
			}
		}
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	pong, err := h.Ping(pctx)
	cancel()
	if err != nil {
		err = fmt.Errorf("%w; fix session in %s or start the configured server with herdr --session %q server", err, config.DefaultPath(), c.Session)
	}
	add("herdr", fmt.Sprintf("%s protocol %d", pong.Version, pong.Protocol), err)
	profiles := make([]string, 0, len(c.Profiles))
	for name, p := range c.Profiles {
		profiles = append(profiles, name+" "+p.Kind+": not probed")
	}
	sort.Strings(profiles)
	add("profiles", strings.Join(profiles, "; "), nil)
	for _, name := range slices.Sorted(maps.Keys(c.Profiles)) {
		if p := c.Profiles[name]; p.Kind == "codex" {
			if off := codexHooksOff(p.Args); off != "" {
				r.Checks = append(r.Checks, CheckResult{Name: "profile " + name + " session", Passed: true, Detail: fmt.Sprintf("warning: profile %s turns off Codex hooks (%s), so its agent never reports a Herdr session; a later prompt step or agent switch then holds agent_switch_unproven or submission_unproven; remove %s from profiles.%s.args", name, off, off, name)})
			}
		}
	}
	kinds := map[string]bool{}
	for _, p := range c.Profiles {
		kinds[p.Kind] = true
	}
	orderedKinds := make([]string, 0, len(kinds))
	for kind := range kinds {
		orderedKinds = append(orderedKinds, kind)
	}
	sort.Strings(orderedKinds)
	for _, kind := range orderedKinds {
		add("agent limits "+kind, agentlimit.Probe(kind), nil)
	}
	names := make([]string, 0, len(c.Secrets))
	for name := range c.Secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ref := c.Secrets[name]
		_, err := ref.Resolve()
		detail := config.SecretSource(ref)
		if err != nil {
			err = fmt.Errorf("%s: %w", detail, err)
		}
		add("secret "+name, detail, err)
	}
	for _, entry := range c.ProjectEntries() {
		name := "Herdr launch environment"
		if len(c.ProjectEntries()) > 1 {
			name = entry.Name + ": " + name
		}
		add(name, "clean", checkLaunchEnvironment(ctx, c.View(entry), h, l))
	}
	return r
}

func checkKata(ctx context.Context, c config.Config, k *kata.CLI, l state.Layout) (Report, string) {
	r := Report{}
	add := func(name, detail string, err error) {
		if err != nil {
			detail = err.Error()
		}
		r.Checks = append(r.Checks, CheckResult{name, err == nil, detail})
	}
	var pinErr error
	pinned := false
	var pinLayout state.Layout
	if _, err := c.Kata.Credential(); err != nil {
		source := "credential_env"
		if c.Kata.CredentialFile != "" {
			source = "credential_file"
		}
		add("Kata credential", "", fmt.Errorf("%w; fix %s for project %q in %s; %s", err, source, c.Kata.Project, config.DefaultPath(), credentialRemedy(c.Kata, err)))
		return r, ""
	}
	health, healthErr := k.Health(ctx)
	incompatibleSchema := healthErr == nil && health.SchemaVersion < 33
	if incompatibleSchema {
		healthErr = fmt.Errorf("kata %s schema %d: schema >= 33 and a build containing #493 required", health.Version, health.SchemaVersion)
	}
	if healthErr != nil {
		_, malformedJSON := errors.AsType[*json.SyntaxError](healthErr)
		_, wrongJSONType := errors.AsType[*json.UnmarshalTypeError](healthErr)
		kataErr, isKataErr := errors.AsType[*kata.Error](healthErr)
		switch {
		case isKataErr && (kataErr.Code == "token_invalid" || kataErr.Code == "unauthorized" || kataErr.Code == "authentication_required"):
			source := "credential_env"
			if c.Kata.CredentialFile != "" {
				source = "credential_file"
			}
			healthErr = fmt.Errorf("kata rejected the credential; replace %s for project %q in %s with a valid credential for its actor", source, c.Kata.Project, config.DefaultPath())
		case isKataErr && strings.Contains(kataErr.Message, "named daemon not found"):
			healthErr = fmt.Errorf("%w; fix daemon for project %q in %s to select an existing named daemon", healthErr, c.Kata.Project, config.DefaultPath())
		case malformedJSON || wrongJSONType || incompatibleSchema:
			healthErr = fmt.Errorf("%w; set kata.bin in %s to Kata 0.19.0 (schema >= 33)", healthErr, config.DefaultPath())
		default:
			healthErr = fmt.Errorf("%w; check kata.bin and kata.env in %s, and daemon %q for project %q; restore the configured daemon or tunnel, then run kata daemon diagnose --json on that route before herdr-kata up", healthErr, config.DefaultPath(), c.Kata.Daemon, c.Kata.Project)
		}
	}
	add("kata version", fmt.Sprintf("%s schema %d", health.Version, health.SchemaVersion), healthErr)
	if healthErr != nil {
		return r, ""
	}
	err := k.CheckCommands(ctx)
	if err != nil {
		err = fmt.Errorf("%w; set kata.bin in %s to Kata 0.19.0 with cron capabilities and events --tail", err, config.DefaultPath())
	}
	add("kata commands", "cron and events --tail available", err)
	if err != nil {
		return r, ""
	}
	i, route, err := kata.ResolveIdentity(ctx, k)
	if err != nil {
		// Initial resolution cannot prove this project was ever accessible.
		if kataErr, ok := errors.AsType[*kata.Error](err); ok {
			err = kataErr
		}
		err = fmt.Errorf("project %q: %w; check project name and actor in %s and restore the configured Kata route", c.Kata.Project, err, config.DefaultPath())
	}
	add("capabilities", strings.Join(i.Features, ", "), err)
	if role := k.DiagnosedRole(); role != "" {
		add("federation role", role, nil)
	}
	if err == nil {
		pinLayout = l.Project(i.ProjectUID)
		add("route", route.DiagnoseState, nil)
		var saved kata.Installation
		pinErr = state.ReadJSON(state.OSFS{}, pinLayout.StateFile(), 64<<10, &saved)
		pinErr = state.RepairError(pinErr, l)
		if errors.Is(pinErr, fs.ErrNotExist) {
			pinErr = nil
			add("identity and route", "not pinned yet; "+i.InstanceUID+" / "+i.ProjectUID+" / "+i.Actor, nil)
		} else {
			if pinErr == nil {
				pinErr = kata.ComparePin(saved, i, route, c.Executor)
				pinned = pinErr == nil
			}
			add("identity and route", "matches pinned installation", pinErr)
		}
		if i.Actor != c.Kata.Actor {
			add("configured actor", "", fmt.Errorf("authenticated actor differs; fix actor for project %q in %s", c.Kata.Project, config.DefaultPath()))
		}
	} else {
		add("route", "", errors.New(strings.ReplaceAll(err.Error(), "<name>", c.Kata.Project)))
		add("identity and route", "", err)
	}
	if err == nil && pinErr == nil {
		add(laneLeaseCheck(ctx, k, c.Executor))
		if pinned {
			r.Checks = append(r.Checks, finalRunChecks(ctx, k, state.OSFS{}, l, runner.Target{Identity: i, Route: route, Executor: c.Executor})...)
			lost, scanErr := lostRunsAt(ctx, k, state.OSFS{}, l, pinLayout, c.Executor, c.Kata.Actor)
			requests, requestErr := runner.LostIssueRequests(ctx, k, state.OSFS{}, l, runner.Target{Identity: i, Route: route, Executor: c.Executor}, c.Kata.Inbox)
			scanErr = errors.Join(scanErr, requestErr)
			for _, request := range requests {
				uid, e := ident.IssueRequestUID(request.UID)
				if e != nil {
					scanErr = errors.Join(scanErr, e)
					continue
				}
				lost = append(lost, uid)
			}
			detail := "no lost runs in scanned pages"
			if len(lost) > 0 {
				commands := make([]string, 0, len(lost))
				for _, uid := range lost {
					commands = append(commands, "herdr-kata abandon "+uid+" --project "+c.Kata.Project)
				}
				scanErr = errors.Join(scanErr, fmt.Errorf("local state lost: %s", strings.Join(commands, "; ")))
			}
			add("lost-runs", detail, scanErr)
		} else {
			add("lost-runs", "installation not pinned yet", nil)
		}
	}

	return r, i.ProjectUID
}

func checkLaunchEnvironment(ctx context.Context, c config.Config, h *herdr.Client, l state.Layout) error {
	reportBin, probeErr := os.Executable()
	if probeErr == nil {
		if probeErr = os.MkdirAll(l.Root, 0700); probeErr == nil {
			env := map[string]string{}
			maps.Copy(env, c.Kata.Env)
			env["HERDR_KATA_RUN"] = "probe"
			env["HERDR_KATA_RUN_DIR"] = l.Root
			env["HERDR_KATA_REPORT"] = reportBin
			env["KATA_PROJECT"] = c.Kata.Project
			if c.Kata.Daemon != "" {
				env["KATA_DAEMON"] = c.Kata.Daemon
			} else {
				delete(env, "KATA_DAEMON")
			}
			for _, name := range c.CredentialVariables() {
				delete(env, name)
			}
			delete(env, "KATA_AUTH_TOKEN")
			var probe herdr.EnvironmentProbe
			var probeWorkspace string
			id, _ := herdr.ReadServerID(h.Path)
			defer func() {
				closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if current, e := herdr.ReadServerID(h.Path); probeWorkspace != "" && e == nil && current == id {
					_ = h.Call(closeCtx, "workspace.close", map[string]string{"workspace_id": probeWorkspace}, nil)
				}
			}()
			_, probeErr = probe.Check(ctx, h, func(ctx context.Context) (string, error) {
				ws, e := h.WorkspaceCreate(ctx, "Environment check", filepath.Clean(l.Root))
				probeWorkspace = ws.WorkspaceID
				return ws.WorkspaceID, e
			}, env, c.CredentialVariables(), reportBin, l.Root)
		}
	}
	return probeErr
}

// codexHooksOff returns the profile arguments that turn off Codex's hooks,
// which carry the Herdr session report, or "" when hooks stay on.
func codexHooksOff(args []string) string {
	for i, arg := range args {
		next := ""
		if i+1 < len(args) {
			next = args[i+1]
		}
		switch {
		case (arg == "-c" || arg == "--config") && strings.ReplaceAll(strings.Join(strings.Fields(next), ""), `"`, "") == "features.hooks=false":
			return arg + " " + next
		case arg == "--disable" && next == "hooks":
			return arg + " " + next
		case arg == "--disable=hooks", strings.HasPrefix(arg, "--config=") && strings.ReplaceAll(strings.Join(strings.Fields(strings.TrimPrefix(arg, "--config=")), ""), `"`, "") == "features.hooks=false":
			return arg
		}
	}
	return ""
}

// credentialRemedy follows the credential error's cause. An owner-only file
// whose ACL read failed needs another attempt, not a mode change.
func credentialRemedy(k config.Kata, err error) string {
	switch {
	case k.CredentialFile == "":
		return "export the configured variable; configure credential_file for startup hooks"
	case errors.Is(err, config.ErrACLRead):
		return "run herdr-kata doctor again"
	case errors.Is(err, config.ErrExtendedACL):
		return "remove the extended ACL"
	}
	return "restore an owner-readable file with mode 0600"
}

// laneLeaseCheck reads the lane lease. Only another installation's live
// lease fails; an unreadable one fails as unknown, never as a refusal.
func laneLeaseCheck(ctx context.Context, k *kata.CLI, executor string) (string, string, error) {
	current, found, err := lease.Read(ctx, k)
	switch {
	case err != nil:
		return "lane lease", "", fmt.Errorf("lane lease unreadable: %w; the lane retries the read and admits nothing until it succeeds", err)
	case !found:
		return "lane lease", "free; this installation claims it when its lane starts", nil
	case current.Owner == executor:
		return "lane lease", fmt.Sprintf("held by this installation at generation %d", current.Generation), nil
	case current.Live(time.Now()):
		return "lane lease", "", &lease.HeldError{Lease: current}
	}
	return "lane lease", fmt.Sprintf("expired lease of %s; this installation claims it when its lane starts", current.Owner), nil
}
