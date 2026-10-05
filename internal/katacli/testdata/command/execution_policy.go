package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Independent public CLI boundary: definitions remain available while the
// ordinary run logger is unavailable and retired execution commands are absent.
// It intentionally grants no permission, reserves no occurrence and imports no
// plugin packages.
func fixtureExecutionPolicy(dir string, body []byte) {
	f, err := os.OpenFile(filepath.Join(dir, "policy-calls.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	if err = json.NewEncoder(f).Encode(struct {
		Args []string `json:"args"`
		Body string   `json:"body"`
	}{os.Args[1:], string(body)}); err != nil {
		panic(err)
	}
	f.Close()
	for i, a := range os.Args[1:] {
		if a == "projects" && i+2 < len(os.Args) && os.Args[i+2] == "show" {
			if raw, err := os.ReadFile(filepath.Join(dir, "project.json")); err == nil {
				os.Stdout.Write(raw)
			} else {
				fmt.Print(`{"project":{"id":73,"uid":"01ARZ3NDEKTSV4RRFFQ69G5FAV"}}`)
			}
			return
		}
	}
	for _, a := range os.Args[1:] {
		if a == "--planning-dates" {
			if raw, err := os.ReadFile(filepath.Join(dir, "planning-dates.json")); err == nil {
				os.Stdout.Write(raw)
				return
			}
			fmt.Fprint(os.Stderr, "planning-date fixture missing")
			os.Exit(2)
		}
	}
	args := os.Args[1:]
	for i, arg := range args {
		if arg == "cron" {
			args = args[i+1:]
			break
		}
	}
	if len(args) == 1 && args[0] == "capabilities" {
		fmt.Print(`{"project_uid":"01ARZ3NDEKTSV4RRFFQ69G5FAV","event_features":["cron_v1"]}`)
		return
	}
	if len(args) > 0 && (args[0] == "job" || args[0] == "flow") {
		fixtureStore(dir, body)
		return
	}
	if len(args) > 1 && args[0] == "run" && args[1] == "list" {
		if raw, err := os.ReadFile(filepath.Join(dir, "history.json")); err == nil {
			os.Stdout.Write(raw)
		} else {
			fmt.Print(`{"runs":[]}`)
		}
		return
	}
	if len(args) > 1 && args[0] == "run" && args[1] == "observe" {
		if mode, _ := os.ReadFile(filepath.Join(dir, "observe-mode")); string(mode) == "ack-loss" {
			fixtureOrdinaryObservation(dir, args[2], body)
			return
		}
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			if _, err := os.Stat(filepath.Join(dir, "observe-paused")); os.IsNotExist(err) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Fprint(os.Stderr, "ordinary run logger unavailable")
		os.Exit(7)
	}
	for _, arg := range args {
		if arg == "create" {
			fixtureRunIssue(dir, body)
			return
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "attention-enabled")); err == nil {
		for _, a := range os.Args {
			if a == "inbox" {
				recipient := ""
				for i, arg := range os.Args {
					if arg == "--for" && i+1 < len(os.Args) {
						recipient = os.Args[i+1]
					}
				}
				json.NewEncoder(os.Stdout).Encode(map[string]any{"recipient": recipient, "requests": []any{}})
				return
			}
			if a == "notify" {
				fmt.Print(`{"ok":true}`)
				return
			}
		}
	}
	for _, a := range os.Args {
		if a == "ready" {
			if raw, err := os.ReadFile(filepath.Join(dir, "ready.json")); err == nil {
				os.Stdout.Write(raw)
			} else {
				issues := []map[string]any{{"uid": "01ARZ3NDEKTSV4RRFFQ69G5FAW", "project_uid": "01ARZ3NDEKTSV4RRFFQ69G5FAV"}}
				var rows map[string]fixtureCreatedIssue
				raw, _ := os.ReadFile(filepath.Join(dir, "created-issues.json"))
				json.Unmarshal(raw, &rows)
				for _, row := range rows {
					issues = append(issues, map[string]any{"uid": row.UID, "project_uid": "01ARZ3NDEKTSV4RRFFQ69G5FAV"})
				}
				json.NewEncoder(os.Stdout).Encode(map[string]any{"issues": issues})
			}
			return
		}
	}
	for i, a := range os.Args {
		if a == "show" && i+1 < len(os.Args) {
			if raw, err := os.ReadFile(filepath.Join(dir, "issue-error.json")); err == nil {
				os.Stderr.Write(raw)
				os.Exit(4)
			}
			ref := os.Args[i+1]
			if ref == "--" && i+2 < len(os.Args) {
				ref = os.Args[i+2]
			}
			var issues map[string]json.RawMessage
			if raw, e := os.ReadFile(filepath.Join(dir, "issues.json")); e == nil {
				json.Unmarshal(raw, &issues)
			}
			if issue, ok := issues[ref]; ok {
				json.NewEncoder(os.Stdout).Encode(map[string]any{"issue": issue})
			} else {
				json.NewEncoder(os.Stdout).Encode(map[string]any{"issue": map[string]any{"uid": ref, "project_id": 73, "author": "worker", "status": "open", "metadata": map[string]any{}}})
			}
			return
		}
	}
	fmt.Fprint(os.Stderr, "retired execution command unavailable: ")
	json.NewEncoder(os.Stderr).Encode(args)
	os.Exit(2)
}

type fixtureCreatedIssue struct {
	UID, Title, Body string
	Metadata         map[string]string
}

func fixtureRunIssue(dir string, body []byte) {
	key, title := "", ""
	metadata := map[string]string{}
	for i, arg := range os.Args {
		if i+1 < len(os.Args) {
			switch arg {
			case "--idempotency-key":
				key = os.Args[i+1]
			case "--meta":
				k, v, _ := strings.Cut(os.Args[i+1], "=")
				metadata[k] = v
			case "--":
				title = os.Args[i+1]
			}
		}
	}
	if key == "" || title == "" {
		os.Exit(2)
	}
	path := filepath.Join(dir, "created-issues.json")
	rows := map[string]fixtureCreatedIssue{}
	raw, _ := os.ReadFile(path)
	json.Unmarshal(raw, &rows)
	row, exists := rows[key]
	if !exists {
		sum := sha256.Sum256([]byte(key))
		uid := "0" + strings.ToUpper(fmt.Sprintf("%x", sum))[:25]
		row = fixtureCreatedIssue{UID: uid, Title: title, Body: string(body), Metadata: metadata}
		rows[key] = row
		raw, _ := json.Marshal(rows)
		if os.WriteFile(path, raw, 0600) != nil {
			os.Exit(2)
		}
		if _, err := os.Stat(filepath.Join(dir, "create-lose-reply")); err == nil {
			fmt.Fprint(os.Stderr, "ordinary create reply lost")
			os.Exit(7)
		}
	} else if row.Title != title || row.Body != string(body) || fmt.Sprint(row.Metadata) != fmt.Sprint(metadata) {
		os.Exit(5)
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"issue": map[string]any{"uid": row.UID, "project_id": 73}})
}

// Independent ordinary revision oracle: same evidence replays before CAS. It
// knows nothing about process permissions, claims, grants or plugin packages.
func fixtureOrdinaryObservation(dir, uid string, body []byte) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		os.Exit(2)
	}
	var expected int64
	if json.Unmarshal(fields["expected_revision"], &expected) != nil {
		os.Exit(2)
	}
	delete(fields, "expected_revision")
	canonical, _ := json.Marshal(fields)
	path := filepath.Join(dir, "observations-"+uid+".json")
	var state struct {
		UID      string
		Revision int64
		Body     string
		Lost     bool
	}
	raw, _ := os.ReadFile(path)
	json.Unmarshal(raw, &state)
	if state.UID != "" && state.UID != uid {
		os.Exit(5)
	}
	replay := state.Body == string(canonical) && state.Revision > 0
	if !replay {
		if expected != state.Revision {
			os.Exit(5)
		}
		state.UID = uid
		state.Revision++
		state.Body = string(canonical)
		lost := state.Lost
		state.Lost = true
		raw, _ = json.Marshal(state)
		if os.WriteFile(path, raw, 0600) != nil {
			os.Exit(2)
		}
		if !lost {
			os.Exit(7)
		} // accepted first write, reply lost
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"run": map[string]any{"uid": uid, "revision": state.Revision}, "replayed": replay})
}
