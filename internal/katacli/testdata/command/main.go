package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var buildGeneration = "first"

func main() {
	exe, _ := os.Executable()
	mode, _ := os.ReadFile(filepath.Join(filepath.Dir(exe), "mode"))
	switch string(mode) {
	case "sleep":
		time.Sleep(10 * time.Second)
	case "oversized":
		fmt.Print(string(make([]byte, 8192)))
		return
	case "oversizedstderr":
		os.Stderr.Write(make([]byte, 8192))
		return
	case "malformed":
		fmt.Print(`{broken`)
		return
	case "nonzero":
		fmt.Print(`{"error":"conflict","counter":9007199254740993}`)
		os.Exit(5)
	}
	body, _ := io.ReadAll(os.Stdin)
	if string(mode) == "execution-policy" {
		fixtureExecutionPolicy(filepath.Dir(exe), body)
		return
	}
	if string(mode) == "herdr" {
		fixtureHerdr(filepath.Dir(exe))
		return
	}
	if string(mode) == "forward" {
		dir := filepath.Dir(exe)
		calls, err := os.OpenFile(filepath.Join(dir, "policy-calls.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		if err := json.NewEncoder(calls).Encode(struct {
			Args []string `json:"args"`
			Body string   `json:"body"`
		}{os.Args[1:], string(body)}); err != nil {
			panic(err)
		}
		calls.Close()
		bin, _ := os.ReadFile(filepath.Join(dir, "forward-binary"))
		command := exec.Command(string(bin), os.Args[1:]...)
		command.Env = os.Environ()
		command.Stdin = bytes.NewReader(body)
		command.Stderr = os.Stderr
		output, err := command.Output()
		action := ""
		for i, arg := range os.Args {
			if arg == "cron" && i+2 < len(os.Args) {
				action = os.Args[i+1] + " " + os.Args[i+2]
				break
			}
		}
		loss, _ := os.ReadFile(filepath.Join(dir, "lose-reply"))
		loseAction, _ := os.ReadFile(filepath.Join(dir, "lose-action"))
		if len(loseAction) == 0 {
			loseAction = []byte("job create")
		}
		if action == "job show" && string(loss) == "lost" {
			os.Exit(5)
		}
		if action == string(loseAction) && err == nil && string(loss) == "armed" {
			os.WriteFile(filepath.Join(dir, "lose-reply"), []byte("lost"), 0600)
			os.Exit(5)
		}
		os.Stdout.Write(output)
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				os.Exit(exit.ExitCode())
			}
			os.Exit(1)
		}
		return
	}
	if string(mode) == "store" {
		fixtureStore(filepath.Dir(exe), body)
		return
	}
	if string(mode) == "responses" {
		raw, _ := os.ReadFile(filepath.Join(filepath.Dir(exe), "responses.json"))
		var responses map[string]struct {
			Exit int             `json:"exit"`
			Body json.RawMessage `json:"body"`
		}
		json.Unmarshal(raw, &responses)
		key := ""
		for i, arg := range os.Args {
			if arg == "projects" && i+2 < len(os.Args) && os.Args[i+1] == "show" {
				key = "projects show"
				break
			}
			if arg == "cron" && i+1 < len(os.Args) {
				if os.Args[i+1] == "capabilities" && i+2 == len(os.Args) {
					key = os.Args[i+1] + " show"
					break
				}
				if i+2 >= len(os.Args) {
					break
				}
				key = os.Args[i+1] + " " + os.Args[i+2]
				break
			}
		}
		response, ok := responses[key]
		if !ok {
			fmt.Print(`{"error":"unknown operation"}`)
			os.Exit(2)
		}
		fmt.Print(string(response.Body))
		os.Exit(response.Exit)
	}

	for i, arg := range os.Args {
		if arg == "cron" && i+1 < len(os.Args) && os.Args[i+1] == "capabilities" && i+2 == len(os.Args) {
			fmt.Print(`{"project_uid":"01ARZ3NDEKTSV4RRFFQ69G5FAD","event_features":["cron_v1"]}`)
			return
		}
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"argv": os.Args[1:], "stdin": string(body), "env": os.Environ(), "generation": buildGeneration, "last_bytes": []byte(os.Args[len(os.Args)-1])})
}

func fixtureStore(dir string, body []byte) {
	args := os.Args[1:]
	for i, arg := range args {
		if arg == "cron" {
			args = args[i+1:]
			break
		}
	}
	if len(args) == 1 && args[0] == "capabilities" {
		fmt.Print(`{"project_uid":"01ARZ3NDEKTSV4RRFFQ69G5FAD","event_features":["cron_v1"]}`)
		return
	}
	if len(args) < 2 {
		os.Exit(2)
	}
	resource, action := args[0], args[1]
	var state struct {
		Counter   int
		Resources map[string]map[string]map[string]json.RawMessage
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	json.Unmarshal(raw, &state)
	if state.Resources == nil {
		state.Resources = map[string]map[string]map[string]json.RawMessage{}
	}
	if state.Resources[resource] == nil {
		state.Resources[resource] = map[string]map[string]json.RawMessage{}
	}
	rows := state.Resources[resource]
	if action == "list" {
		list := []map[string]json.RawMessage{}
		ids := []string{}
		for id := range rows {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		includeDeleted := false
		for _, a := range args {
			if a == "--include-deleted" {
				includeDeleted = true
			}
		}
		for _, id := range ids {
			if rows[id]["deleted_at"] == nil || includeDeleted {
				list = append(list, rows[id])
			}
		}
		json.NewEncoder(os.Stdout).Encode(map[string]any{resource + "s": list})
		return
	}
	id := ""
	if len(args) > 2 {
		id = args[2]
	}
	if action == "create" {
		for i, a := range args {
			if a == "--uid" && i+1 < len(args) {
				id = args[i+1]
			}
		}
	}
	id = strings.ToUpper(id)
	old := rows[id]
	if action == "show" {
		loss, _ := os.ReadFile(filepath.Join(dir, "lose-reply"))
		if string(loss) == "lost" {
			os.Exit(5)
		}
		if old == nil {
			fmt.Print(`{"error":"not found"}`)
			os.Exit(4)
		}
		json.NewEncoder(os.Stdout).Encode(map[string]any{resource: old})
		return
	}
	var request map[string]json.RawMessage
	json.Unmarshal(body, &request)
	os.WriteFile(filepath.Join(dir, "last-request.json"), body, 0600)
	var expected string
	json.Unmarshal(request["expected_event_uid"], &expected)
	for i, a := range args {
		if a == "--expected-event-uid" && i+1 < len(args) {
			expected = args[i+1]
		}
	}
	current := ""
	if old != nil {
		json.Unmarshal(old["definition_event_uid"], &current)
	}
	if action == "create" && old != nil || action != "create" && (old == nil || expected != current) {
		fmt.Print(`{"error":"conflict"}`)
		os.Exit(5)
	}
	if old == nil {
		old = map[string]json.RawMessage{}
	}
	put := func(k string, v any) { old[k], _ = json.Marshal(v) }
	if action == "create" || action == "update" {
		old["name"] = request["name"]
		old["definition"] = request["definition"]
	}
	if action == "delete" {
		put("deleted_at", "2026-10-04T00:00:00Z")
	}
	if action == "restore" {
		delete(old, "deleted_at")
	}
	state.Counter++
	put("uid", id)
	put("project_id", 7)
	if action == "create" {
		put("created_at", time.Now().UTC().Format(time.RFC3339Nano))
	}
	put("updated_at", time.Now().UTC().Format(time.RFC3339Nano))
	put("definition_event_uid", fmt.Sprintf("%026d", state.Counter))
	put("revision", state.Counter)
	rows[id] = old
	raw, _ = json.Marshal(state)
	os.WriteFile(filepath.Join(dir, "state.json"), raw, 0600)
	loss, _ := os.ReadFile(filepath.Join(dir, "lose-reply"))
	if action == "create" && string(loss) == "armed" {
		os.WriteFile(filepath.Join(dir, "lose-reply"), []byte("lost"), 0600)
		os.Exit(5)
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{resource: old})
}
