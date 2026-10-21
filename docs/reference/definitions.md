---
title: Job and workflow fields
description: How the runtime interprets Kata definitions, parent bindings and unsupported fields.
last_edited: 2026-10-19
---

# Job and workflow fields

Kata owns the [definition schema](https://github.com/kenn-io/kata/blob/main/docs/reference/cron.md).
This page describes the fields herdr-kata consumes. Definitions are refreshed
before adapter admission and then frozen for the run. For creation and editing,
see [Schedule jobs](../guide/jobs.md) and [Build workflows](../guide/workflows.md).

## Dispatch and issue policy

Jobs use `version: 1`, `kind: "job"` and `enabled: true` for dispatch.
Disabled jobs are ignored. Invalid definitions are set aside individually,
with reasons in `status`, `doctor` and the TUI.

`doctor` reads each lane's saved definition snapshot, including when Kata is
unavailable. It reports the project, job name, UID and saved support reasons.
Inspect the cache with `herdr-kata status --json`, or the current definition
with `kata --project <name> cron job show <job-uid> --json`.

Snapshots last written more than 30 minutes ago, or from the older summary-only
format, are marked stale. The write time describes the cache, not proof of a
current Kata read. Missing or unreadable snapshots fail the check for an
initialized lane; an uninitialized project reports its missing snapshot as
information. Doctor never refreshes definitions or starts a worker.

| Shape | Runtime behavior |
|---|---|
| `action.kind: "execute"`, `action.prompt` | One prompt step named `main`. |
| `action.kind: "execute"`, `action.workflow_uid` | Execute the referenced workflow. Exactly one of prompt or workflow is required. |
| `action.kind: "notify"`, `issue.kind: "existing"` | Deliver a notification; do not launch an agent or command. |
| `action.kind: "notify"`, `issue.kind: "per-run"` | Kata owns native delivery. The runtime may pick up its child through the inbox. |

| Field | Interpretation |
|---|---|
| `issue.kind: "per-run"` | Create an unowned issue from nonblank `title` and optional `body`; append the UTC occurrence to its title. Execute jobs cannot set `parent_uid`. |
| `issue.kind: "existing"` | Use `uid`; no title, body, parent or deadline offset. Execute requires an open, ready issue; notify has no readiness requirement. |
| `issue.deadline_offset_seconds` | For a per-run issue, set a deadline at occurrence plus offset. Negative offsets are allowed. A deadline does not gate readiness. |
| `checkout_key` | Select a configured checkout; absent means the run directory. Not allowed on notify jobs. |
| `options.agent` | Select a configured profile; absent means `default`. |
| `options.tags`, `options.description` | Optional job metadata; see [Job metadata](#job-metadata). |
| `options.run_parent` | Full issue UID, only on execute jobs with `issue.kind: "per-run"`. Groups new run issues under an open parent in the pinned project. Non-string values, short refs and other job shapes are unsupported. |
| `action.recipient` | Notify recipient; `current-owner-or-author` resolves from a fresh issue read. |
| `action.message` | Notify text. If omitted for `current-owner-or-author`, use `Kata cron: <job name> is due`. Other adapter recipients require nonblank text. |

## Job metadata

Execute and existing-issue notify jobs can carry `options.tags` and
`options.description`. Tags are an array of at most 16 strings, each matching
Kata’s label rules: 1–64 characters from `[a-z0-9._:-]`. Description is a
string of at most 200 Unicode characters. The editor trims and deduplicates
typed tags in their original order; blank input removes the metadata key.

Malformed metadata written by another client produces a warning and omits
the invalid field from display. It does not make an otherwise supported job
unsupported. These keys are recognized only at job scope. Native recurring
jobs still forbid options, including metadata, whether enabled or paused.

## Triggers and catch-up

| `trigger.kind` | Fields and meaning |
|---|---|
| `cron` | `cron` uses five fields or a descriptor; `timezone` defaults to UTC. An embedded `TZ=` or `CRON_TZ=` must agree. `@every` is anchored at job creation. |
| `interval` | Positive `interval_seconds`; occurrences are anchored at `created_at`. |
| `once` | `at` is an RFC3339Nano instant. |
| `manual` | Only an operator request; no automatic fire or checkpoint change. |

Trigger fields for another kind are refused. Durations must fit Go's duration
range. `grace_seconds` and `timeout_seconds` cannot be negative.

### Cron expressions

Cron uses five fields: minute, hour, day of month, month and day of week.
Descriptors such as `@hourly`, `@daily` and `@every 90s` are also supported.
Seconds, years and extended `L`, `W`, `#` and `H` forms are unsupported.

- Sunday accepts `0` or `7`. `0 9 * * 5-7` runs Friday through Sunday;
  `5-7/2` and `5/2` in the weekday field run Friday and Sunday.
- Ranges may wrap around: `0 22-2 * * *` runs at 22:00, 23:00, 00:00,
  01:00 and 02:00.
- Steps must be smaller than their span. `0 */24 * * *` and `*/60 * * * *`
  are rejected; use `@daily` or `@hourly` instead.
- When both day fields are restricted, either may match. `0 9 1 * MON`
  runs on the first of the month and every Monday.

Cron follows the trigger timezone, defaulting to UTC. An embedded `TZ=` or
`CRON_TZ=` must match it; empty or invalid timezone directives are rejected.
Daylight-saving transitions affect occurrences:

- In an hour-long spring-forward gap, the skipped time runs one hour later
  that day. In `America/New_York`, `30 2 * * *` runs at 03:30 EDT
  (07:30 UTC) on 2026-03-08.
- In a repeated fall-back hour, the time runs on both passes. In
  `America/New_York`, `30 1 * * *` runs at 05:30 and 06:30 UTC on
  2026-11-01.
- Upstream transition-day limitations remain: daily schedules can miss an
  occurrence in zones with 30-minute shifts, such as `Australia/Lord_Howe`,
  or skipped local midnight, such as `America/Santiago`.

This matches Kata's netresearch/go-cron v0.16.1 parser. Older Kata releases
and adapters may reject these expressions or schedule them differently.
Upgrade every peer that parses the job before using the changed forms.

### Catch-up and overlap

| Field | Values and meaning |
|---|---|
| `catchup` | `skip`: newest missed occurrence only within grace; `latest`: newest; `all`: every missed occurrence in order. |
| `grace_seconds` | Grace window for `skip`; zero when absent. |
| `overlap` | `forbid`: pending occurrences wait for the job gate; `allow`: admit as capacity frees. |
| `timeout_seconds` | Positive value overrides `limits.default_timeout`; zero/absent uses that default. The run expiry is set at preparation. |

Catch-up is bounded to seven days and 100 pending occurrences per job,
including normal fires. Oldest overflow is dropped with a logged policy
decision. A new installation starts at the later of installation creation
and job creation. Definition changes supersede unadmitted pending work;
prepared runs keep their frozen definitions.

Occurrences accumulated during a route outage use the same catch-up policy
when access returns, including those already in the pending checkpoint.
Prepared journals retain their frozen admission and are not replaced.

## Workflows

Workflows use `version: 1` and a nonempty `steps` array. Steps run serially
in topological order, with definition order breaking ties.

| Field | Interpretation |
|---|---|
| `about` | Context included in prompts. Supports input substitution. |
| `input` | Input description, not the input value. A nonempty declaration requires nonempty effective input at admission; at most 4 KiB. |
| `steps[].key` | Unique key, at most 255 bytes, using letters, digits, `-_.`; `.` and `..` are refused. |
| `steps[].kind` | `prompt` or `command`, with exactly its corresponding nonblank field. |
| `steps[].command` | Command text must be UTF-8 without NUL. Invalid text is unsupported before admission with `command must be UTF-8 without NUL`. Newlines, tabs and shell syntax remain allowed. |
| `steps[].after` | Keys that must all succeed before this step runs. Missing keys and cycles are refused. |
| `steps[].retries` | Integer from `0` to `100`; additional attempts after a certain failure. Uncertainty never retries. |
| `options.agent` | At workflow scope or on a command step, must equal the run's selected profile. |
| `steps[].options.agent` | Prompt steps only: run this step on another configured, project-allowed profile, with a fresh agent and tab per attempt. Unmapped names are unsupported with `unmapped profile: steps.<key>.options.agent <name>`. The run's prompt cap is the stricter of the job profile's and each step profile's cap. |
| `options.on_failure: "park"` | Workflow scope only: hold exhausted step failures; omission settles as failed. See [Resume rules](holds.md#resume-rules). |
| `steps[].options.agent_mode: "fresh"` | Prompt steps only: new agent and tab per attempt. Omission uses the shared run agent. |
| `steps[].options.on_fail` | One maker/checker backward edge; see the loop rules below. |

`on_failure: "park"` holds the run for `resume`; this differs from Kata's
`kata schedule` sense of parking an issue until a planning date.

By default, a failed step outside a loop skips its dependents; independent
steps may still run. With `on_failure: "park"`, the run holds at that step
instead, with outcome `failed` and reason `step_failed`. Its overlap gate
stays held and later steps do not run. Single-prompt jobs keep their
failure behavior. A checker failure can rewind its declared span. A `needs-human`
result hands off the run. The [agent contract](agent-contract.md) defines
settlement, notes and command receipts.

## Maker/checker loop

One prompt or command step may declare a backward edge in its options:

```json
{"on_fail":{"goto":"make","max_loops":2}}
```

| Field inside `on_fail` | Meaning |
|---|---|
| `goto` | Required key of an earlier step in the serial topological order. That step is the maker; the declaring step is the checker. |
| `max_loops` | Additional span visits, default `1`, integer from `1` to `8`. The initial visit does not consume this budget. |

The inclusive span from maker to checker must be linear: after the maker,
each step has exactly one `after`, naming the immediately preceding step in
the span. Every span step must have `retries: 0`. Only one edge is supported;
unknown edge fields, null values, a missing/forward target or an invalid budget
make the workflow unsupported. Job- or workflow-level `on_fail` is unsupported.

Only a reported, settled prompt failure or a certain nonzero command receipt
takes the edge. The rewind consumes the verdict and budget and allocates new,
monotonic attempts in one durable transition. Re-run prompts always use fresh
agents. Keep the span safe to run again; put push, deploy and publish steps
after the checker. The runtime cannot identify those side effects.

Budget exhaustion, repeated nonempty rejection notes and an undecided saved
verdict produce [loop holds](holds.md#hold-reasons). Their findings are saved
on the run issue as described in [Reporting](reporting.md#findings-comments).
Holding workflow failures is opt-in; [Resume rules](holds.md#resume-rules)
own continuation.

## Effective input

Adapter job `options.input` supplies a string, overridden by manual `run
--input`. A native child's body supplies its input. Effective input is frozen
at admission, limited to 4 KiB of UTF-8 without NUL, and checked after the
override against any workflow requirement.

`{{input}}` and `{{previous}}` are the only prompt placeholders. The first
executed step and workflow `about` cannot use `{{previous}}`. Unknown or
unclosed placeholders are refused. Command text is not substituted. The
[agent contract](agent-contract.md#input-and-notes) owns the handoff bounds.

## Native parent bindings

These metadata values are read from the parent and frozen at child admission:

| Key | Absent | Meaning |
|---|---|---|
| `herdr_kata.workflow` | Single prompt | Workflow UID or a uniquely resolving workflow name. |
| `herdr_kata.profile` | `default` | Configured profile. |
| `herdr_kata.checkout` | Run directory | Configured checkout key. |

Invalid bindings make the child unsupported. The child must be open, ready,
in the configured project, linked to a parent and carry valid version 1
`cron.occurrence`. It must be unowned or owned by the configured actor.
Inbox prose alone never authorizes execution. See the
[adapter contract](kata-adapter.md#native-children).

The runtime creates `herdr_kata.run` on per-run adapter issues. It manages
`work.attention` and `work.attention_msg` only on those issues and children it
takes over. [Reporting](reporting.md#attention-and-ownership) owns those values.

## Job secrets

`secret_refs` maps child environment variables to names in the local
[`[secrets]` table](configuration.md#secrets), for example
`{"GITHUB_TOKEN": "github-review"}`. Unmapped secret names are unsupported.
Variables must match `^[A-Za-z_][A-Za-z0-9_]*$`. Every `KATA_*`,
`HERDR_KATA_*`, and every configured `credential_env` name across all projects
are reserved.
Secrets resolve at each step. Secret-bearing prompt steps always use fresh
agents and tabs so the next step sees a rotated source.

## Limits

- Issue titles and bodies must be UTF-8 without NUL. Prompt jobs and workflow
  prompt steps have a 256 KiB assembled limit. Static validation includes
  issue context, workflow context and saved input, reserving 4 KiB for runtime
  instructions. Expanded findings, manual input and local paths are checked
  again before an agent starts; exceeding the full limit holds `invalid_prompt`.

- Unsupported triggers: `issue-scheduled`, `issue-deadline`. Kata already
  notifies the owner when a planning date is reached. To run an agent when
  an issue's schedule arrives, [request it on the issue](../guide/issue-schedules.md).
- Unsupported issue policy field: `scheduled_offset_seconds`.
  `deadline_offset_seconds` is supported and never gates readiness.
- Unmapped secrets/checkouts/profiles and a differing workflow or command
  step profile are unsupported.
- `on_failure` supports only `"park"` at workflow scope. `on_fail` is supported
  only on one workflow step under the loop rules above.
- `options.input` is job-only; `agent_mode` accepts only `fresh` on prompt steps.
- Option keys are case-sensitive. Unknown keys, including case variants of
  supported keys, produce warnings and are ignored. Definitions larger
  than 256 KiB or with invalid field types cannot be decoded. Raw job and
  workflow bytes must be valid UTF-8; invalid bytes make the definition
  unsupported with `invalid UTF-8 in definition` before JSON decoding.
- There are no persistent/resumed conversations, per-run Git worktrees,
  Windows execution or automatic installation failover.
