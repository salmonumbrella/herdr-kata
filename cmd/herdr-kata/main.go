// Command herdr-kata is an agent harness: it schedules work and runs each job as
// an interactive herdr agent that a human can inspect, interrupt, and answer.
//
// This first slice implements `herdr-kata run-once`, which executes a single
// ad-hoc job through the full runner lifecycle. The scheduler daemon, store,
// and TUI board build on the same runner.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdrcli"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"github.com/salmonumbrella/herdr-kata/internal/version"
)

// commands is every subcommand herdr-kata answers to.
//
// A function rather than a literal inside main, so a test can ask whether the
// commands the plugin manifest invokes actually exist. Two of them did not, and
// nothing noticed until they failed on a user's machine.
func commands() map[string]func([]string) error {
	return map[string]func([]string) error{
		"run-once":   runOnce,
		"board":      boardCmd,
		"job":        jobCmd,
		"native":     nativeCmd,
		"inbox":      inboxCmd,
		"teammate":   teammateCmd,
		"run":        runCmd,
		"hook":       hookCmd,
		"workflow":   workflowCmd,
		"lease":      leaseCmd,
		"usage":      usageCmd,
		"ensure":     ensureCmd,
		"daemon":     daemonCmd,
		"sentinel":   sentinelCmd,
		"board-wide": boardWideCmd,
		"doctor":     doctorCmd,
		"stop":       stopCmd,
		"start":      startCmd,
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmds := commands()
	switch os.Args[1] {
	case "-v", "--version", "version":
		fmt.Println(version.Full())
	case "-h", "--help", "help":
		usage()
	default:
		fn, ok := cmds[os.Args[1]]
		if !ok {
			fmt.Fprintf(os.Stderr, "herdr-kata: unknown command %q\n", os.Args[1])
			usage()
			os.Exit(2)
		}
		if err := fn(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "herdr-kata:", err)
			os.Exit(1)
		}
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `herdr-kata — agent harness

Usage:
  herdr-kata native configure --file mapping.json   Bind explicit Kata target and installation
  herdr-kata native import --source snapshot --source-id retained-name --checkout-key primary
  herdr-kata native refresh              Refresh native job/workflow cache
  herdr-kata native tui [issue-ref]       Open installed Kata TUI
  herdr-kata inbox list|open|deliver --for actor[/teammate] [--ref issue]
  herdr-kata teammate connect --for actor[/teammate] --workspace w1 --pane w1:p1 --conversation session-id
  herdr-kata teammate disconnect --for actor[/teammate]
  herdr-kata teammate list               List exact local runtime registrations
  herdr-kata board                       Open the TUI board
  herdr-kata board --pin                 Open it in herdr-kata's workspace, unfocused
  herdr-kata job list                    List jobs
  herdr-kata job add --id <id> --prompt <text> [--schedule <cron>]
  herdr-kata job edit <id> --autocompact 200000   Cap the agent's context window
  herdr-kata job remove <id>             Remove a job
  herdr-kata job run <id>                Run a stored job now
  herdr-kata run list [--state parked]   List runs
  herdr-kata workflow new <id> [--about]     Write an unsaved local workflow draft
  herdr-kata workflow save <draft-id>        Save draft to native Kata with retained UID/CAS
  herdr-kata workflow list                   Native workflows, labeled offline when cached
  herdr-kata workflow show|edit|rm <id>      Read, open, or delete one
  herdr-kata workflow run <id> [--input ...] Call a workflow with an x
  herdr-kata workflow status <run>           Per-step outcome and duration
  herdr-kata workflow resume <run>           Restart at the step that parked
                     [--reset-loops]  ...and give its on_fail edges a full budget again

  Native Kata owns jobs/workflows; local SQLite holds only derived cache, mappings,
  leases and execution journals. Explicit native configuration is required for
  saved writes. Workflow YAML lives under ~/.herdr-kata/drafts/workflows and stays unsaved
  until workflow save succeeds. Failed saves keep the draft and UID. Jobs start
  disabled; checkout paths and secret values stay in local installation mappings.
  The board's K key opens the installed Kata TUI in the existing pane.
  New jobs require current native definitions and local checkout mappings for
  execution. Saved runs retain their frozen context; logging failure buffers
  evidence without preventing execution or live conversation reuse.
  Generic Herdr inbox delivery requires manual wake and never automatically types.
  herdr-kata lease claim <resource> --scope <scope> --as <holder> --ttl 20m
  herdr-kata lease renew <resource> --scope <scope> --as <holder> --ttl 20m [--job <job>] [--run <run>] [--holder-id <id>]
  herdr-kata lease release <resource> --scope <scope> --as <holder> [--job <job>] [--run <run>] [--holder-id <id>]
  Renewal/release must match every holder field used in the claim.
  For claim/renew, omitted or zero TTL means no expiry; choose renewal TTL explicitly.
  herdr-kata lease list [--scope <scope>] [--json]
  herdr-kata usage [--since 7d]          Token totals per job
  herdr-kata hook status                 Pending, retrying, dead and skipped events
  herdr-kata hook redeliver <id>|--dead   Requeue an event or every dead event
  herdr-kata daemon [--tick 5s]          Run the scheduler loop
  herdr-kata daemon --detach             Start it in the background and return
  herdr-kata stop                        Stop the scheduler, and keep it stopped
  herdr-kata start                       Undo a stop and bring it back
  herdr-kata doctor                      Detached schedulers, pending delivery and exact runtime health

  A stop is remembered. The daemon and the sentinel revive each other, and the
  plugin hook and the board start them too, so a stop that was not written down
  would last about five seconds; only start undoes it.
  herdr-kata ensure                      Reconcile orphaned runs (plugin startup hook)

  herdr-kata run-once --prompt <text>    Run an ad-hoc job
  herdr-kata --version                   Print the running build

run-once options:
  --prompt <text>    Instruction for the agent (required)
  --id <name>        Job id, used to name the tab and agent (default "adhoc")
  --kind <kind>      herdr agent kind (default "claude")
  --cwd <path>       Working directory for the agent (default: current dir)
  --timeout <dur>    Deadline for the run, e.g. 15m (default 15m)
  --agent-args <s>   Space-separated args passed through to the agent binary
`)
}

func runOnce(argv []string) error {
	fs := flag.NewFlagSet("run-once", flag.ExitOnError)
	prompt := fs.String("prompt", "", "instruction for the agent")
	id := fs.String("id", "adhoc", "job id")
	kind := fs.String("kind", "claude", "herdr agent kind")
	cwd := fs.String("cwd", "", "working directory")
	timeout := fs.Duration("timeout", 15*time.Minute, "run deadline")
	agentArgs := fs.String("agent-args", "", "args passed through to the agent")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *prompt == "" {
		return fmt.Errorf("--prompt is required")
	}
	if *cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		*cwd = wd
	}

	r := &runner.Runner{
		Herdr:    herdrcli.New(),
		StateDir: stateDir(),
	}
	job := runner.Job{
		ID:      *id,
		Prompt:  *prompt,
		CWD:     *cwd,
		Kind:    *kind,
		Timeout: *timeout,
	}
	if *agentArgs != "" {
		job.AgentArgs = strings.Fields(*agentArgs)
	}

	uid, err := katacli.NewUID()
	if err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	c := runner.NativeExecutionContext{Version: 1, RunUID: uid, Runtime: store.Job{ID: *id, Name: *id, Prompt: *prompt, Kind: *kind, CWD: *cwd, Timeout: *timeout, ExtraArgs: *agentArgs}}
	if s.Native != nil && s.Native.Client != nil {
		c.Target = s.Native.Client.Target
		c.Target.Token = ""
		c.ProjectUID = s.Native.Binding.ProjectUID
		job.Prompt += nativePromptInstructions(c.Target)
	}
	if err := c.Save(runDirFor(uid)); err != nil {
		return err
	}
	if s.Native != nil && s.Native.Client != nil {
		r.Env = nativeRunnerEnvironment(c, s.Native)
	}
	initial := store.Run{ID: uid, JobID: *id, Trigger: "manual", Outcome: "running", StartedAt: time.Now(), RunDir: runDirFor(uid)}
	if err := s.PutRun(context.Background(), initial); err != nil {
		return err
	}
	run, err := r.Execute(context.Background(), job, uid)
	if run != nil {
		record := store.Run{ID: uid, JobID: *id, Trigger: "manual", Outcome: string(run.Outcome), ParkReason: string(run.ParkReason), StartedAt: run.StartedAt, EndedAt: &run.EndedAt, RunDir: run.RunDir, TabID: run.TabID, AgentName: run.AgentName, Note: run.Note()}
		if e := s.PutRun(context.Background(), record); e != nil {
			return e
		}
	}

	if run != nil {
		printRun(run)
	}
	if err != nil {
		return err
	}
	if run.Outcome == runner.OutcomeFailed {
		os.Exit(1)
	}
	return nil
}

func printRun(run *runner.Run) {
	out := map[string]any{
		"run_id":  run.RunID,
		"job_id":  run.JobID,
		"outcome": run.Outcome,
		"status":  run.Status,
		"run_dir": run.RunDir,
		"tab_id":  run.TabID,
		"agent":   run.AgentName,
		"seconds": int(run.EndedAt.Sub(run.StartedAt).Seconds()),
	}
	if run.Context != "" {
		out["context"] = run.Context
		out["context_session"] = run.ContextSession
		out["context_note"] = run.ContextNote
	}
	if run.ParkReason != "" {
		out["park_reason"] = run.ParkReason
	}
	if run.Result != nil {
		out["result"] = run.Result
	}
	if run.Err != nil {
		out["error"] = run.Err.Error()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

// stateDir resolves where the store and run directories live: ~/.herdr-kata,
// holding herdr-kata.db, herdr-kata.log, the lock files and every run directory.
//
// One visible directory rather than the XDG split, for the same reason herdr-kata
// ships as one binary with no unit file: the whole of it should be somewhere a
// person can find by looking. A run directory is something you read — it is
// where an agent's prompt and result live — so burying it under
// ~/.local/state costs more than the tidiness is worth.
//
// It deliberately ignores HERDR_PLUGIN_STATE_DIR. That variable is only set
// for commands Herdr launches, so honouring it would give the board opened as
// a plugin pane a different database from the one the daemon writes — the
// scheduler must run with no Herdr server at all, so the store cannot live in
// Herdr-managed state. HERDR_KATA_HOME overrides, which is how the tests get
// a private directory.
func stateDir() string {
	if d := os.Getenv("HERDR_KATA_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".herdr-kata")
}

// newRunID builds a sortable, filesystem-safe run id.
func newRunID(jobID string) string {
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405Z"), jobID)
}
