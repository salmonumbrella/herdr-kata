package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"github.com/salmonumbrella/herdr-kata/internal/workflow"
)

// Native definitions are authoritative. New/edit use local YAML drafts,
// and save submits the retained UID and winning event guard explicitly.

// workflowDir is the legacy adoption/journal fixture directory.
func workflowDir() string { return workflow.Dir(stateDir()) }

// workflowTemplate is what `workflow new` writes.
//
// It is a working workflow rather than an empty skeleton: the first thing anyone
// does with a new workflow is run it to see what happens, and a template that parks
// immediately teaches nothing. The comments are the documentation an agent
// editing this file will actually have in front of it.
const workflowTemplate = `# What this workflow is for. Shown in ` + "`herdr-kata workflow list`" + ` and on the board.
about: %s

# What the caller has to supply, described in a sentence rather than typed.
# The steps below reach it as {{input}}. Delete this line if the workflow takes none.
input: what this workflow should be run against

# Agent steps run with --dangerously-skip-permissions by default, because a
# workflow step has nobody in its pane to answer a prompt: it waits, then parks.
# Uncomment to make this workflow ask, or set skip_permissions on one step.
# skip_permissions: false

# Every workflow that runs agents is overseen: where a step would park, an agent
# holding the whole run decides what happens instead. It costs nothing while
# nothing goes wrong. Uncomment to configure it.
# overwatch:
#   model: opus            # the decision is usually harder than the steps
#   watch: every_step      # default on_trouble: only where the run would park
#   budget: 3              # decisions per run
#   timeout: 10m           # one consult
#   allow: [retry, goto, park, abort]   # add skip to let it wave a step through
#   brief: |
#     The deploy step is not safe to retry. Park instead.

steps:
  # Each step is an agent prompt or a shell command, never both.
  # A step that cannot show it finished parks the workflow and the rest never start.
  - id: assess
    agent: |
      Look at {{input}} and say in one line whether it is worth acting on.

  - id: act
    # {{previous}} is the note the step before published — not its context.
    # A step never inherits the previous agent's session.
    agent: |
      {{previous}}

      If that says it is worth acting on, do it. Otherwise stop and say why.

  - id: verify
    # A run step has no agent. It reads the same two values from the
    # environment, as $HERDR_KATA_INPUT and $HERDR_KATA_PREVIOUS.
    # Quoted because a bare YAML scalar cannot contain ": ".
    run: 'echo "verified: $HERDR_KATA_PREVIOUS"'
`

func workflowNew(argv []string) error {
	fs := flag.NewFlagSet("workflow new", flag.ExitOnError)
	about := fs.String("about", "", "what this workflow is for")
	if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
		return errors.New("usage: herdr-kata workflow new <id> [--about ...]")
	}
	id := argv[0]
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}
	parsed, err := workflow.ParseID(id)
	if err != nil {
		return err
	}
	dir := workflowDraftDir()
	if err := os.MkdirAll(dir, statefs.Dir); err != nil {
		return err
	}
	path := filepath.Join(dir, parsed+workflow.Ext)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("workflow %s already exists at %s", parsed, path)
	}
	text := *about
	if strings.TrimSpace(text) == "" {
		text = "what " + parsed + " does"
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(workflowTemplate, text)), statefs.File); err != nil {
		return err
	}
	fmt.Println("unsaved workflow draft: " + path)
	fmt.Printf("edit it, then: herdr-kata workflow save %s\n", parsed)
	return nil
}

func workflowList(argv []string) error {
	fs := flag.NewFlagSet("workflow list", flag.ExitOnError)
	if err := fs.Parse(argv); err != nil {
		return err
	}
	workflows, label, err := nativeWorkflows(context.Background())
	if err != nil {
		return err
	}
	fmt.Println(label)
	var bad []error
	// Broken workflows are named before the good ones are listed, because a workflow
	// that will not parse is invisible everywhere else — it simply does not
	// appear, which reads as "I never made that one".
	for _, err := range bad {
		fmt.Fprintln(os.Stderr, "herdr-kata:", err)
	}
	if len(workflows) == 0 {
		fmt.Println("no workflows yet — `herdr-kata workflow new <id>` writes one")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "WORKFLOW\tSTEPS\tWATCH\tINPUT\tABOUT")
	for _, f := range workflows {
		input := f.Input
		if input == "" {
			input = "-"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n", f.ID, len(f.Steps), watchColumn(f), input, f.About)
	}
	return w.Flush()
}

// watchColumn says how a workflow is overseen, in a column narrow enough to live
// in a listing.
//
// `skip` is called out because it is the one setting that changes what a workflow
// guarantees -- the steps after a failed one may run -- and a property like
// that belongs where a reader sees it without opening the file.
func watchColumn(f workflow.Workflow) string {
	if !f.Applies() {
		return "-"
	}
	w := f.Watcher()
	if w.Permits(workflow.DecideSkip) {
		return w.Watch + " +skip"
	}
	return w.Watch
}

func workflowShow(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: herdr-kata workflow show <id>")
	}
	def, label, err := nativeDefinitionSnapshot(context.Background(), "workflow", argv[0])
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, label)
	// The selected native document is canonical; retain opaque JSON exactly.
	fmt.Println(string(def.Definition))
	return nil
}

func workflowEdit(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: herdr-kata workflow edit <id>")
	}
	f, err := prepareWorkflowDraft(argv[0])
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "unsaved workflow draft; use workflow save after editing")

	editor := firstNonEmptyEnv("HERDR_KATA_EDITOR", "VISUAL", "EDITOR")
	if editor == "" {
		// No editor is not an error worth failing on: the path is the useful
		// half of this command, and an agent has no $EDITOR at all.
		fmt.Println(f.Path)
		return nil
	}
	cmd := exec.Command("sh", "-c", editor+" "+shellQuote(f.Path))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	// Re-read on the way out, so a syntax error is reported now rather than at
	// 04:00 by the scheduler.
	if _, err := workflow.Load(workflowDraftDir(), strings.ToLower(argv[0])); err != nil {
		return err
	}
	return nil
}

func workflowRemove(argv []string) error {
	fs := flag.NewFlagSet("workflow rm", flag.ExitOnError)
	if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
		return errors.New("usage: herdr-kata workflow rm <id>")
	}
	id := argv[0]
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}
	ctx := context.Background()
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	f, err := nativeWorkflow(ctx, id)
	if err != nil {
		return err
	}
	refs, err := jobsUsingWorkflow(f.ID)
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		fmt.Fprintf(os.Stderr, "warning: removing workflow %s still referenced by jobs: %s\n", f.ID, strings.Join(refs, ", "))
	}
	if err := s.Native.Delete(ctx, "workflow", katacli.Definition{UID: f.ID, DefinitionEventUID: f.NativeEventUID}); err != nil {
		return err
	}

	fmt.Printf("removed workflow %s\n", id)
	return nil
}

// jobsUsingWorkflow lists the ids of jobs that start this workflow.
func jobsUsingWorkflow(id string) ([]string, error) {
	s, err := openStore()
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if s.Native != nil {
		snapshot, err := s.Native.Refresh(context.Background())
		if err != nil {
			return nil, err
		}
		if snapshot.Offline {
			return nil, errors.New("refresh native definitions before inspecting workflow dependencies")
		}
		var refs []string
		for _, def := range snapshot.Jobs {
			var body struct {
				Action struct {
					Workflow string `json:"workflow_uid"`
				} `json:"action"`
			}
			if err := katacli.Decode(def.Definition, &body); err != nil {
				return nil, fmt.Errorf("inspect native job %s dependency: %w", def.UID, err)
			}
			if body.Action.Workflow != "" && strings.EqualFold(body.Action.Workflow, strings.TrimSpace(id)) {
				refs = append(refs, def.UID)
			}
		}
		return refs, nil
	}
	jobs, err := s.Jobs(context.Background())
	if err != nil {
		return nil, err
	}
	var out []string
	want := strings.TrimSpace(id)
	for _, j := range jobs {
		// A job that names no workflow starts no workflow. Without this, an id that
		// trims to empty -- `workflow rm "  "` -- matches every prompt job on the
		// machine, and the caller is told its workflow is in use by jobs that have
		// never heard of it.
		if workflow := strings.TrimSpace(j.Workflow); workflow != "" && strings.EqualFold(workflow, want) {
			out = append(out, j.ID)
		}
	}
	return out, nil
}

// firstNonEmptyEnv returns the first of these environment variables that is set.
func firstNonEmptyEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// shellQuote makes a path safe to interpolate into `sh -c`.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// workflowJob is the configuration a directly-called workflow runs under.
//
// A workflow has no schedule, no working directory and no model of its own: those
// belong to whoever calls it, and baking them into the file would make a workflow
// that only works in one repository. So a direct call builds a job on the spot
// from its flags, and a scheduled call uses the job that already exists.
// The defaults here are the ones `job add` applies, and they have to be applied
// here too because a directly-called workflow never goes through `job add` or
// PutJob. Two bugs came from that gap in one sitting: an empty Kind, which herdr
// refuses outright, and permission prompts left enabled, which park every agent
// step at `blocked` eight seconds in. A workflow of `run:` steps survives both, so
// neither showed up until a workflow with an agent step was actually run.
func workflowJob(f workflow.Workflow, cwd, model, kind string) store.Job {
	j := store.Job{
		ID:       f.ID,
		Name:     f.About,
		Workflow: f.ID,
		CWD:      cwd,
		Model:    model,
		Kind:     kind,
		Enabled:  true,
		// A workflow step has nobody sitting in its pane, so a permission prompt is
		// a step that waits forever and then parks. `job add` makes the same call
		// for the same reason: these runs are unattended by construction.
		SkipPermissions: true,
		PermissionMode:  defaultPermissionMode,
		Catchup:         store.CatchupLatest,
	}
	if strings.TrimSpace(j.Model) == "" {
		j.Model = store.DefaultModel
	}
	if strings.TrimSpace(j.Kind) == "" {
		j.Kind = store.DefaultKind
	}
	return j
}

// firstNonEmpty returns the first non-blank of its arguments.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// defaultPermissionMode is what an unattended agent runs under. It matches the
// `job add` default, so a workflow called by hand behaves like the same workflow called
// by a schedule.
const defaultPermissionMode = "acceptEdits"
