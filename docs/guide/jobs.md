---
title: Schedule jobs
description: Create prompt, workflow and notification jobs and choose their scheduling policy.
last_edited: 2026-10-20
---

# Schedule jobs

Store jobs in Kata and let herdr-kata execute supported definitions. These
steps assume a configured, running [runtime](../operations/runtime.md).
Run Kata commands against that same project and route. Replace `abc4` with
your issue's short ID; replace UID placeholders with full UIDs from Kata.

## Choose the work and issue

For a prompt with a new issue on each run, save this as `job.json`:

```json
{
  "name": "Inspect checkout",
  "definition": {
    "version": 1,
    "kind": "job",
    "enabled": true,
    "trigger": {"kind": "manual"},
    "action": {"kind": "execute", "prompt": "Inspect the checkout and report what needs attention."},
    "issue": {"kind": "per-run", "title": "Inspect checkout", "body": "Check the current checkout."},
    "checkout_key": "repo",
    "overlap": "forbid",
    "catchup": "skip",
    "options": {"agent": "default"}
  }
}
```

To run a [workflow](workflows.md), replace `action` with:

```json
{"kind": "execute", "workflow_uid": "<workflow-uid>"}
```

A run closes its issue only when the agent reports usable evidence;
[Write prompts that close their issue](prompts.md) shows how to ask for it.

Either execute shape can target an existing issue instead of creating one.
Read `kata show abc4 --json` and use its full issue UID:

```json
{"kind": "existing", "uid": "<issue-uid>"}
```

Replace the whole `issue` object with that policy. Existing issues must be open
and ready before execution; they wait without taking a slot. The runtime
comments on them but never closes, relabels or writes their `work.*` metadata.
A run whose steps are all commands stays quiet when it succeeds: it posts no
comment, and its result is in the run history. Failures, holds, handoffs,
resumed runs, runs that published findings and runs with a prompt step still
comment; see
[quiet success](../reference/reporting.md#outcomes).

For a reminder on an existing issue, use this `action` and the existing issue
policy above. Remove `checkout_key` and execution options:

```json
{"kind": "notify", "recipient": "current-owner-or-author", "message": "Review the standing task."}
```

An explicit recipient is also supported. Unlike execute jobs, these reminders
do not wait for issue readiness. For Kata-created children under a parent,
follow [Run recurring issues](recurring-issues.md) instead. To start work
when an existing issue's schedule arrives, follow
[Run an agent on an issue's schedule](issue-schedules.md).

### Group run history under a parent

| Work | Issue policy |
|---|---|
| Native cron or interval delivery within its supported constraints | [Recurring children](recurring-issues.md). |
| Execute jobs needing overlap control, a timeout, input, once/manual triggers or deadline offsets | `per-run` with `options.run_parent`. |
| Checks with a natural standing issue | `existing`. |
| Other executions | `per-run`, optionally without a parent. |

For parented execute runs, read the parent's full UID and add it to job options:

```json
{"agent":"default","run_parent":"<parent-issue-uid>"}
```

The parent must be open and in the configured project. Closing it skips fresh
occurrences with `parent_closed`; reopening does not backfill those skips.
A parent in another project returns 404 through the pinned Kata route and
leaves work pending as `parent_unavailable`, even if you can read it in its own
project. A 404 never authorizes a skip. `parent_outside_project` appears only
when Kata returns the parent's project UID and it differs from the pinned
project. Other unavailable parents also leave work pending, including when
access was lost. `status --json` and the TUI show the latest skip and pending
reason.
Prepared runs keep their saved parent through retries and recovery. A create
waiting for its parent remains pending, then requires inspection after seven
days if unresolved. The runtime never substitutes a top-level issue.

The parent belongs to you; the runtime never writes to it. To keep it out of
your ready queue without affecting children:

```sh
kata meta set <parent-issue-uid> someday true --json-value
```

A per-run job whose trigger fires more than 24 times a day gets the warning
`creates about N issues a day; consider an existing issue, a run parent, or a
native recurring job`; a job with a run parent is not told to add one.
Interval triggers count `86400 / interval_seconds`, cron triggers count
their busiest day, including weekend-only and day-of-month schedules, and `once` and `manual` triggers never warn. The
warning appears in `herdr-kata status`, the TUI job detail and
[doctor](../operations/doctor.md); it never stops the job. For frequent
workflows, `options.on_failure: "park"` with `overlap: forbid` also bounds
failure issues: one failure holds the gate until you resume it.

Every newly prepared per-run issue carries `herdr-kata:run`. Hide run issues with:

```sh
kata search --no-label herdr-kata:run "<query>"
kata ready --no-label herdr-kata:run
kata list --no-label herdr-kata:run
```

Existing issues and native children receive no run label. In the TUI editor,
**Run parent** accepts a ref and saves its full UID after checking that it is
open and in the pinned project. Old prepared runs keep their original create.

## Choose when it runs

Automatic adapter execution requires the job's original `author` to exactly
match the runtime lane's pinned authenticated actor. The comparison is
case-sensitive; an empty author or actor refuses automatic execution. This
applies to new occurrences, catch-up and recovered pending work. The runtime
checks the author again before dispatch. Editing a shared job does not change
its original author. Your own job can reuse a shared workflow.

Shared jobs remain visible and readable. An explicit `herdr-kata run` or TUI
`R` request can run another author's supported adapter job, with the usual
readiness, routing and capacity checks. Already admitted runs continue through
recovery and reporting.

[Native recurring children](recurring-issues.md) use their recipient, conditional
claim and child ownership checks; the delivery job's author does not decide
who executes the child. See [lane ownership](../reference/kata-adapter.md#lane-ownership)
for the limits when multiple installations share an actor.

Replace `trigger` with one of these objects:

| Trigger | JSON |
| --- | --- |
| Cron | `{"kind":"cron","cron":"0 9 * * 1-5","timezone":"UTC"}` |
| Interval | `{"kind":"interval","interval_seconds":3600}` |
| Once | `{"kind":"once","at":"2030-01-01T09:00:00Z"}` |
| Manual only | `{"kind":"manual"}` |

Cron follows the trigger's timezone, defaulting to UTC. Interval schedules
start from job creation. See [Cron expressions](../reference/definitions.md#cron-expressions)
for accepted syntax, daylight-saving behavior and compatibility with older
Kata releases and adapters. See [Kata's cron reference](https://github.com/kenn-io/kata/blob/main/docs/reference/cron.md)
for the definition format.

Set `catchup` to `skip` for only the newest missed occurrence within
`grace_seconds`, `latest` for the newest regardless of grace, or `all` for
every missed occurrence. Catch-up is bounded to seven days and 100 pending
occurrences per job; policy skips are logged.

Use `overlap: "forbid"` to queue occurrences behind an unfinished run, or
`"allow"` to admit them as capacity becomes available. Held runs can keep
both capacity and an overlap gate; see [Recover runs](../operations/recovery.md).
Set `timeout_seconds` for the run expiry, or omit it to use
`limits.default_timeout` (default `2h`). A timeout holds a prompt run without
killing its agent.

`checkout_key` selects a configured checkout. Omit it to use the run directory.
`options.agent` selects a configured profile, defaulting to `default`.

## Create and run it

```sh
kata cron job create --file job.json --json
herdr-kata run <job-uid> --wait
herdr-kata runs --job <job-uid> --limit 20
```

Save the job UID returned by Kata. For an uncertain create, reuse the generated
UID printed by Kata with `--uid`; do not create another job to retry. `run`
uses normal readiness and capacity rules. `--wait` succeeds only when the
local run succeeds.

You can also press `n` in the [TUI](tui.md) to create a job and `R` to request
an adapter job run.

## Pause, edit or remove a job

Read the current definition and its `definition_event_uid`:

```sh
kata cron job show <job-uid> --json
```

Save a request containing `name` and the full `definition` as `job.json`.
Set `definition.enabled` to `false` to pause, then update with the event UID
you just read:

```sh
kata cron job update <job-uid> --expected-event-uid <event-uid> --file job.json --json
```

Set it back to `true` to enable, using a fresh event UID. A conflict requires
you to inspect the newer definition before applying your changes. Changes
replace unadmitted pending occurrences; already prepared runs keep their
saved definitions.

Delete a definition with the same guard:

```sh
kata cron job delete <job-uid> --expected-event-uid <event-uid> --json
```

In the TUI, `p` toggles enabled, `D` deletes, and `P` prunes finished one-shot
jobs after listing their names and asking for `y`. Deleting a job does not
stop an already prepared run or remove its issue history.

Jobs can map `secret_refs` to [local secret sources](../reference/configuration.md#secrets).

## Limits

Unsupported jobs stay visible with a reason in the TUI and
`herdr-kata status --json`. They do not run. Current exclusions include:

- Unsupported triggers: `issue-scheduled`, `issue-deadline`. Kata already
  notifies the owner when a planning date is reached. To run an agent when
  an issue's schedule arrives, [request it on the issue](issue-schedules.md).
- Unsupported issue policy field: `scheduled_offset_seconds`.
  `deadline_offset_seconds` is supported and never gates readiness.
- Resumed conversations. A workflow prompt step may still use
  [another profile](workflows.md#run-a-step-on-another-profile).
- Job-level `options.on_failure`; [failure holds](workflows.md#limits) is a
  workflow option.
- Execute jobs with `issue.parent_uid`; use `options.run_parent` instead.

See the [adapter contract](../reference/kata-adapter.md) for the scheduling
and reporting rules.
