---
title: Agent and command contract
description: Step inputs, reports, notes, receipts and the evidence required before advancing.
last_edited: 2026-10-19
---

# Agent and command contract

Prompt steps report what they did through an attempt-bound result file.
Command steps report through exit receipts. The runtime owns the run issue's
`work.*` metadata and closure; an agent leaves those to the runtime.

## Working directory and environment

The working directory is the mapped checkout or the run directory. A prompt
tab receives these variables; command subprocesses receive them too:

| Variable | Value |
|---|---|
| `HERDR_KATA_RUN` | Run UID. |
| `HERDR_KATA_RUN_DIR` | Absolute run directory. |
| `HERDR_KATA_REPORT` | Absolute staged report executable. |
| `KATA_PROJECT` | Configured project selector. |

Commands additionally receive:

| Variable | Value |
|---|---|
| `HERDR_KATA_INPUT` | Frozen effective input. |
| `HERDR_KATA_PREVIOUS` | Previous executed step's note, or empty. |
| `HERDR_KATA_NOTE_FILE` | Current attempt's `note.txt` path. |

Commands inherit the worker environment after removing `KATA_AUTH_TOKEN`
and the configured credential variable and replacing run variables. The
named credential variable is also excluded if its name overlaps a run
variable. Prompt tabs are given the four nonsecret run variables above;
they are not given command input/note variables. Herdr controls the rest
of the agent launch environment.

## Prompt contents

Each attempt's `prompt.md` contains the issue ref and supplied title/body,
job name, run UID, occurrence, step and attempt. Workflow prompts add the
workflow name, `about`, input description and effective input. The task
text and result contract follow, including the exact report path and command.
Loop re-runs add the loop number, checker rejection and saved finding
references, plus any operator notes replying to the finding chain, labelled
as context only. The [reporting reference](reporting.md#findings-comments) owns the
comment context bounds and the maker-comment barrier before checker submission.
Assertion flags appear only when the selected staged report executable supports
them. Runs retain that executable across upgrades; older reporters keep the
ordinary result contract.

The runtime sends one line asking the agent to read that absolute path.
The agent writes the report before finishing. A report alone does not
advance execution: the matching occupant must also have settled. See
[Run lifecycle and recovery](../design/run-lifecycle.md).

## Input and notes

Prompt task text and workflow `about` substitute `{{input}}` and
`{{previous}}` once. Inserted text is not expanded again. `previous` means
the previous executed step's note, rather than all dependency results. On a
loop maker's re-run it is the saved checker rejection note. A maker that is
the first step still cannot use `{{previous}}` in its prompt; re-run context
includes the rejection explicitly.
Command strings receive no substitution; use their environment variables.

| Value | Bound |
|---|---|
| Effective input | At most 4 KiB; valid UTF-8 without NUL. |
| Note | One line, at most 280 bytes; valid UTF-8 without NUL or line separators. |
| Complete expanded prompt | At most 256 KiB, including context and the result contract. |

A prompt publishes its note with `report --note`. A command writes
`HERDR_KATA_NOTE_FILE`. The runtime reads only the current attempt's regular
note file after a certain receipt, permits one trailing LF or CRLF, and
freezes the validated note in `run.json`. Invalid command notes hold the run.
No note means empty text; summaries are never used as a fallback.

Input and notes are shared, nonsecret text. They are not extracted from
logs, transcripts or environment dumps. There is no automatic secret detection.
[Job and workflow fields](definitions.md) owns input selection and requirements.

## Prompt result

Use the report command supplied in `prompt.md`:

```sh
"$HERDR_KATA_REPORT" report \
  --result "$HERDR_KATA_RUN_DIR/steps/main/1/result.json" \
  --status succeeded \
  --summary "Checked the parser and verified its focused tests pass." \
  --evidence "test:go test ./parser" --close-reason done \
  --note "Parser checked"
```

The path above is for step `main`, attempt `1`; use your attempt's supplied
path. The command derives step and attempt from that path, takes the run
identity from the environment, and writes this shape atomically:

```json
{
  "version": 1,
  "run": "<run-uid>",
  "step": "main",
  "attempt": 1,
  "status": "succeeded",
  "summary": "Checked the parser and verified its focused tests pass.",
  "note": "Parser checked",
  "evidence": ["test:go test ./parser"],
  "close_reason": "done"
}
```

Optional `assert` (`confirm` or `refute`) and `target` fields travel together.
The report normalizes `target` to an uppercase full comment ULID. These fields
extend version 1, as `note` does. The worker decides whether an assertion is
eligible; [Finding comments](reporting.md#findings-comments) owns those rules.

Statuses are `succeeded`, `failed` and `needs-human`. `note` is optional;
`close_reason` may be empty, `done` or `audit-no-change`. Evidence types are
`commit`, `pr`, `test`, `reviewed-paths`, `external` and `no-change-audit`.
[Reporting](reporting.md#close-or-review) defines which combinations close an issue.

The serialized file, including JSON escaping and its newline, must fit
64 KiB. The worker accepts only a regular file with the matching version,
run, step and attempt. Symlinks, stale attempts and invalid files cannot
authorize success. `report` validates the result format; settlement applies
the stronger close-evidence checks.

## Command results

Commands run as `sh -c <command>` in a separate process group, without a
Herdr pane. The runtime saves `sending` before start. After `wait` returns,
it writes an attempt-bound `receipt.json` with exit code, start/end times,
process group and interruption/uncertainty flags, then copies that evidence
into `run.json`.

Exit `0` succeeds only with a certain receipt and a stopped process group.
A recorded nonzero exit may retry up to `retries` times after the group is
gone. Missing receipts, interruption at run expiry and uncertain descendants
produce `unknown`, even if a shell exited `0`. At run expiry the runtime
sends SIGTERM to the group and SIGKILL after ten seconds when needed.

Combined stdout/stderr is drained continuously into `output.log`. The file
keeps the first 1 MiB and last 256 KiB with `[output truncated]` between them.
Use `herdr-kata logs <run> [--follow]` to read it. Successful command receipts
supply `test:<command>` evidence at settlement.

## Limits

- A prompt possibly accepted by Herdr is never resent by recovery.
  Only a certain, settled `failed` report permits an automatic retry or
  declared loop rewind. A loop re-run is a new attempt with a fresh agent.
- A result written after a run is held does not settle it automatically.
  Inspect it through [Recovery](../operations/recovery.md).
- Commands must stay in their process group. A descendant that calls
  `setsid` and redirects its output can escape both group and pipe tracking,
  so its step may settle while it continues running.
