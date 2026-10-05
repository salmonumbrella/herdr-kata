package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A public Herdr CLI fixture recording live conversation behavior. It never
// controls operating-system processes.
type herdrFixture struct {
	Panes                              map[string]int
	Session                            string
	Calls                              [][]string
	Env                                map[string]string
	Name, Kind                         string
	Started                            bool
	PromptStatus                       string
	AgentStatus, WaitTabID, WaitPaneID string
	PromptResults                      []string
	Workspaces                         map[string]string
	Tabs                               map[string]map[string]string
	NextWorkspace                      int
	NextTab                            int
	PaneID, TabID, WorkspaceID         string
}

func fixtureHerdr(dir string) {
	path := filepath.Join(dir, "herdr.json")
	raw, e := os.ReadFile(path)
	if e != nil {
		panic(e)
	}
	var state herdrFixture
	if e = json.Unmarshal(raw, &state); e != nil {
		panic(e)
	}
	if state.Workspaces == nil {
		state.Workspaces = map[string]string{"w1": "Herdr Kata"}
	}
	if state.Tabs == nil {
		state.Tabs = map[string]map[string]string{}
	}
	if state.PaneID == "" {
		state.PaneID, state.TabID, state.WorkspaceID = "w1:p9", "w1:t9", "w1"
	}
	args := os.Args[1:]
	state.Calls = append(state.Calls, args)
	defer func() {
		raw, _ := json.Marshal(state)
		if e := os.WriteFile(path, raw, 0600); e != nil {
			panic(e)
		}
	}()
	arg := func(flag string) string {
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	reply := func(v any) { json.NewEncoder(os.Stdout).Encode(map[string]any{"result": v}) }
	fail := func(code string) {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"error": map[string]string{"code": code, "message": "fixture rejection"}})
	}
	if len(args) < 2 {
		fail("invalid_arguments")
		return
	}
	agent := func() map[string]any {
		row := map[string]any{"name": state.Name, "agent": state.Kind, "agent_status": "idle", "pane_id": state.PaneID, "tab_id": state.TabID, "workspace_id": state.WorkspaceID, "interactive_ready": true}
		if state.AgentStatus != "" {
			row["agent_status"] = state.AgentStatus
		}
		if state.Session != "" {
			row["agent_session"] = map[string]string{"agent": state.Kind, "kind": "id", "value": state.Session}
		}
		return row
	}
	switch args[0] + " " + args[1] {
	case "workspace get":
		id := args[2]
		label, ok := state.Workspaces[id]
		if !ok {
			fail("workspace_not_found")
			return
		}
		reply(map[string]any{"workspace": map[string]string{"workspace_id": id, "label": label}})
	case "workspace create":
		state.NextWorkspace++
		id := fmt.Sprintf("w%d", state.NextWorkspace+1)
		state.Workspaces[id] = arg("--label")
		tab := map[string]string{"pane_id": id + ":p0", "tab_id": id + ":t0", "workspace_id": id, "cwd": arg("--cwd")}
		state.Tabs[tab["tab_id"]] = tab
		reply(map[string]any{"workspace": map[string]string{"workspace_id": id, "label": arg("--label")}, "root_pane": tab})
	case "workspace rename":
		state.Workspaces[args[2]] = args[3]
		reply(map[string]any{})
	case "workspace close":
		id := args[2]
		delete(state.Workspaces, id)
		for tab, pane := range state.Tabs {
			if pane["workspace_id"] == id {
				delete(state.Tabs, tab)
			}
		}
		reply(map[string]any{})
	case "pane list":
		panes := []map[string]string{}
		for _, pane := range state.Tabs {
			if pane["workspace_id"] == arg("--workspace") {
				panes = append(panes, pane)
			}
		}
		reply(map[string]any{"panes": panes})
	case "tab create":
		state.Env = map[string]string{}
		// Public shell environment observation, restricted to harmless runtime keys.
		for _, key := range []string{"HTTP_PROXY", "NO_PROXY", "PORT"} {
			state.Env[key] = os.Getenv(key)
		}
		for i, a := range args {
			if a == "--env" && i+1 < len(args) {
				k, v, _ := strings.Cut(args[i+1], "=")
				state.Env[k] = v
			}
		}
		ws := arg("--workspace")
		pane := map[string]string{"pane_id": "w1:p9", "tab_id": "w1:t9", "workspace_id": ws, "cwd": arg("--cwd")}
		if ws != "w1" || state.NextTab > 0 {
			state.NextTab++
			pane["pane_id"] = fmt.Sprintf("%s:p%d", ws, state.NextTab)
			pane["tab_id"] = fmt.Sprintf("%s:t%d", ws, state.NextTab)
		}
		state.Tabs[pane["tab_id"]] = pane
		state.Panes[pane["pane_id"]] = 123
		state.PaneID, state.TabID, state.WorkspaceID = pane["pane_id"], pane["tab_id"], ws
		reply(map[string]any{"root_pane": pane})
	case "pane process-info":
		pid := state.Panes[arg("--pane")]
		if pid == 0 {
			fail("pane_not_found")
			return
		}
		reply(map[string]any{"process_info": map[string]any{"pane_id": arg("--pane"), "shell_pid": pid, "foreground_process_group_id": pid}})
	case "agent start":
		state.Name, state.Kind, state.Started = args[2], arg("--kind"), true
		reply(map[string]any{"agent": agent()})
	case "agent get":
		if !state.Started || args[2] != state.Name {
			fail("agent_not_found")
			return
		}
		reply(map[string]any{"agent": agent()})
	case "agent list":
		agents := []map[string]any{}
		if state.Started {
			agents = append(agents, agent())
		}
		reply(map[string]any{"agents": agents})
	case "agent prompt":
		if !state.Started || args[2] != state.Name {
			fail("agent_not_found")
			return
		}
		if len(args) > 3 && args[3] == "/clear" {
			reply(map[string]any{})
			return
		}
		// Parse the public runner result contract, so a reused conversation writes
		// into this prompt's run directory rather than its original tab env.
		path := filepath.Join(state.Env["HERDR_KATA_RUN_DIR"], "result.json")
		if len(args) > 3 {
			prompt := args[3]
			if pointer := regexp.MustCompile(`^Read the file (.+) and do exactly what it says\.$`).FindStringSubmatch(prompt); len(pointer) == 2 {
				body, e := os.ReadFile(pointer[1])
				if e != nil {
					panic(e)
				}
				prompt = string(body)
			}
			match := regexp.MustCompile(`to write (.+result\.json) with this exact shape:`).FindStringSubmatch(prompt)
			if len(match) == 2 {
				path = match[1]
			}
		}
		status := state.PromptStatus
		if status == "" {
			status = "ok"
		}
		if status != "none" {
			body, _ := json.Marshal(map[string]string{"status": status, "note": "Inspection complete"})
			if e := os.WriteFile(path, body, 0600); e != nil {
				panic(e)
			}
		}
		state.PromptResults = append(state.PromptResults, path)
		reply(map[string]any{})
	case "agent wait":
		if state.WaitTabID != "" {
			state.TabID, state.PaneID = state.WaitTabID, state.WaitPaneID
		}
		state.AgentStatus = "idle"
		reply(map[string]any{})
	case "agent read":
		reply(map[string]any{})
	case "pane close", "tab close":
		if args[2] == state.TabID || args[2] == state.PaneID {
			state.Started = false
		}
		delete(state.Tabs, args[2])
		reply(map[string]any{})
	default:
		reply(map[string]any{})
	}
}
