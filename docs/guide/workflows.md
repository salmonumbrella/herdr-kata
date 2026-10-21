---
title: Build workflows
description: Combine prompt and command steps, pass input and notes and choose fresh agents.
last_edited: 2026-10-19
---

# Build workflows

Create a workflow in Kata, then attach it to an execute job. herdr-kata runs
its steps serially and reports the run back to Kata.

## Define the steps

Save this three-step request as `workflow.json`:

```json
{
  "name": "Inspect and check",
  "definition": {
    "version": 1,
    "about": "Inspect the requested change, check it and summarize the result.",
    "input": "Describe the change to inspect.",
    "steps": [
      {
        "key": "inspect",
        "kind": "prompt",
        "prompt": "Inspect {{input}}. Publish a short note for the check step."
      },
      {
        "key": "check",
        "kind": "command",
        "after": ["inspect"],
        "command": "printf '%s\n' \"$HERDR_KATA_PREVIOUS\"; git diff --check && printf '%s' 'Diff whitespace check passed.' > \"$HERDR_KATA_NOTE_FILE\"",
        "retries": 1
      },
      {
        "key": "summarize",
        "kind": "prompt",
        "after": ["check"],
        "prompt": "Summarize the inspection of {{input}}. Check result: {{previous}}. Read the checkout for any detail you need.",
        "options": {"agent_mode": "fresh"}
      }
    ]
  }
}
```

`after` names steps that must succeed first. Steps run in topological order;
ties follow definition order. Even independent steps run one at a time.
By default, a failed step skips its dependents and fails the run. Retries
and loops below can change that path. A `needs-human` result hands the run
to a human.

This example expects a job with a Git checkout. Replace `git diff --check`
with the check your task needs. Place side effects such as push or deploy
after the check step so a failed check prevents them.

## Create the workflow and attach a job

```sh
kata cron workflow create --file workflow.json --json
```

Keep the returned workflow UID. In an execute job, use:

```json
"action": {"kind": "execute", "workflow_uid": "<workflow-uid>"},
"options": {"agent": "default", "input": "Review the current diff."}
```

These are fields inside the job's `definition`, not a complete request.
Follow [Schedule jobs](jobs.md) to select the trigger, checkout and issue.

## Supply input and publish notes

A workflow's `input` field describes what the caller must supply. A nonempty
field requires nonempty effective input. Set the job's `options.input`, or
override it for one manual run:

```sh
herdr-kata run <job-uid> --input "Review the current diff." --wait
```

A native recurring child gets input from its issue body. Input is fixed at
admission and limited to 4 KiB of UTF-8 without NUL. Keep it non-secret.

Prompt text expands `{{input}}` and `{{previous}}` once. `previous` is the
previous executed step's published note, not its summary or transcript.
Without a note it is empty. It cannot appear in the first step. An expanded
prompt is limited to 256 KiB.

Prompt agents publish a note by adding `--note "one line"` to the
`report` command provided in their generated prompt. Command steps write
that line to `$HERDR_KATA_NOTE_FILE`, as the example does. Commands receive
`$HERDR_KATA_INPUT` and `$HERDR_KATA_PREVIOUS` in their environment; the runtime
does not substitute placeholders in shell commands.

Notes must be one UTF-8 line without NUL, at most 280 bytes. A command
note file may end with one LF or CRLF; the runtime strips that line ending.
The runtime reads a command note only after a certain command receipt,
from the current attempt's regular file. A bad note
holds the run. Do not publish credentials, logs or environment dumps as notes.

## Choose retries and agents

`retries` is the number of additional attempts, from 0 to 100. A prompt
retries only after the agent reports `failed` and has settled. A command
retries after a recorded nonzero exit and proof that its process group has
stopped. The receipt must match the current run, step and attempt, with
nonzero start and end times and an end no earlier than its start. An invalid
receipt, uncertain command outcome, missing result or timeout holds the run;
it does not spend retries by executing again.

Prompt steps normally share the run's agent. Set a prompt step's
`options.agent_mode` to `"fresh"` for a new agent and tab on each attempt.
The previous agent must settle before the workflow advances.

## Run a step on another profile

A prompt step can run on a different installation profile than its job, for
example a checker on another model. Name the profile in the step's options:

```json
{"key": "check", "kind": "prompt", "prompt": "Check the change.", "after": ["inspect"], "options": {"agent": "checker"}}
```

The profile must be configured under `[profiles.checker]` and allowed for the
project; otherwise the job is unsupported with
`unmapped profile: steps.check.options.agent checker`. A step on another
profile always gets a fresh agent and tab per attempt. Admission freezes the
step's profile with the run, so a later config change does not alter it. The
run's prompt cap is the stricter of the job profile's and each step profile's
`max_run_prompts`. Workflow-scope `options.agent` and a command step's
`options.agent` must still equal the run's profile.

## Add a bounded maker/checker loop

To return to inspection after a failed check in the example, set the `check`
step's `retries` to `0` and add:

```json
"options": {"on_fail": {"goto": "inspect", "max_loops": 2}}
```

This permits two additional visits from `inspect` through `check`. Every
step in that span must have `retries: 0`, and the span must be linear: each
step after the maker depends only on the step immediately before it.
`max_loops` defaults to 1 and accepts 1–8. Put push, deploy and publish steps
after the checker; every step inside the span must be safe to run again.

Only an agent-reported failure or a certain nonzero command exit takes the
loop. Each repeated prompt gets a fresh agent. The maker receives the
checker's note and saved finding references; a first-step maker receives
its rejection explicitly and still cannot use `{{previous}}` in its template.
Input stays fixed through the loop. Have the checker publish a short failure
note so the maker has a concrete diagnosis. Missing notes stay empty.

The first checker failure starts a comment chain on the run issue. Maker
results and later checker verdicts continue that chain. Pending comment
writes must finish before dependent work proceeds; do not post replacement
findings to bypass them. To steer the next maker or checker attempt, reply
to a finding comment as the configured actor: later attempts see your reply
as an operator note. A note is context only; it does not resume or rerun
anything. See
[Finding comments](../reference/reporting.md#findings-comments) for the
finding and retry rules.

A spent budget holds the run as `loop_exhausted`. Repeated nonempty rejection
notes can hold it as `loop_stuck`; a saved verdict without its committed rewind
becomes `loop_pending` on recovery. Inspect these through
[Recover runs](../operations/recovery.md#resume-a-held-run), then resume the
held run or hand it off with `release`.

## Edit and inspect

Select a workflow in the [TUI](tui.md) and press `e` to edit its definition JSON
in `$EDITOR` (default `vi`). Closing with identical draft bytes returns to the
board with `workflow unchanged; nothing saved` and sends no Kata write.
After an edit, CHECK shows local definition problems.
Warnings and requirements that depend on the using job allow automatic save.
Other problems pause the save. Press `e` to edit again, `ctrl+s` to submit the
exact draft with its original event guard, or `esc` to cancel. Ctrl+S can
override CHECK problems, including valid JSON the local decoder cannot read.
Invalid JSON and drafts over 256 KiB are always refused. Kata validates the save.
Use Page Down/Up or Ctrl+D/U to scroll long CHECK results; help stays pinned.

A conflict keeps the draft and original guard. The editor writes a private
`<draft>.newer.json` copy beside the draft and shows its name and event summary.
Diff the files outside the board. A pre-existing or replaced file is preserved;
if the copy cannot be written, the editor shows the error. Cancel and reopen to
start from the newer definition. Closing removes only the editor's own files.
A terminal hangup stops the external editor and cleans those files too.
An uncertain save freezes the submitted bytes: `ctrl+s` retries those bytes with
the original guard, and `e` stays disabled until cancellation or success.

Saved changes appear when the worker refreshes its definition snapshot. They
affect future admissions; prepared runs keep their saved definition.

```sh
herdr-kata runs --job <job-uid> --limit 20
herdr-kata logs <run-uid> --follow
```

See [Recover runs](../operations/recovery.md) when a run is held.

## Limits

- Only one backward loop is supported. To hold ordinary failures, set workflow
  `options.on_failure: "park"`; see the [loop rules](../reference/definitions.md#makerchecker-loop).
- There is no conversation resume or parallel step execution.
- Input and notes are shared text. The runtime does not detect secrets in them.
