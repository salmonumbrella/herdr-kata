---
title: Jobs
description: Supported Kata definitions and execution behavior.
last_edited: 2026-11-03
---

# Jobs

Create and edit portable definitions with Kata's job and workflow commands
or its Jobs tab. Enroll each project in `[[projects]]` once and configure
its defaults. Then the [minimal job](../README.md#create-a-job) runs with no
`checkout_key` or adapter options. Set `enabled: true`: the wire default is
false, although the TUI defaults to yes.

The worker owns enabled, non-deleted `execute` jobs in configured projects
whose `options.adapter` is absent or `herdr-kata` and whose executor pin is
absent or matches this worker. Ownership is filtered before readiness reads.
It records independent run observations; Kata's recorded run status does
not schedule execution.

Supported triggers are `cron`, `interval`, `once`, `manual` and
`issue-scheduled`. Time zones and daylight-saving transitions follow cron
semantics. Intervals are anchored to the job's creation time. Initial
installation does not backfill earlier occurrences. Catchup scans at most
7 days and retains at most 100 occurrences; `all`, `latest` and `skip` with
`grace_seconds` control selection. Changed definition events discard the pending
queue computed from the old definition. Prepared runs retain their frozen definitions.
For `skip`, an occurrence must be later than the later of the preceding
checkpoint and worker process start, minus grace. It must also be at most
grace plus 2 minutes old. This keeps on-time work through a brief Kata read
outage while bounding recovery after a longer outage or restart.
Edits and re-enables preserve committed progress and the original interval
anchor, then follow the job's catch-up policy, as Kata's native scheduler does.

An `issue-scheduled` job uses an existing issue's current raw schedule value
as its occurrence identity and requires the issue in `kata ready --limit 0`.
The worker reads that list only when an enabled job needs it: an
`issue-scheduled` trigger or an `existing` issue policy.
Clearing or changing the date changes unprepared work. `overlap: forbid`
blocks another occurrence while a prepared, running or held run exists.
`herdr-kata run <full-job-uid> [--input <text>]` posts a local manual request;
each invocation has its own request identity.
The command waits up to 15 seconds for the worker to prepare or reject it.
Permanent rejections print a one-line reason and exit 3: unknown, disabled,
deleted or unsupported jobs, and existing issues that are not ready. An unknown
UID is rejected only after every project's definitions were read successfully.
Overlap gates, capacity limits and transient Kata read failures leave the
request pending. At the end of the bounded wait, the command prints
`pending: <reason>` when the blocker is known and exits 1. The request remains
queued. Keep its printed request ID; submitting again creates another request.
Malformed request files move to `.rejected`; unreadable run journals are logged
and skipped so other work can continue. Their dedup barriers remain intact.

## Actions and issues

An execute action supplies either a prompt or `workflow_uid`. Workflows run
serially in dependency order, preserving definition order for ties. They
support prompt and command steps, `after` and `retries`. Only certain failed
attempts are retried. An unknown execution holds the run for an operator.
Failed steps skip their dependents while independent branches can continue.

Command steps require the project's `commands = true`. A command succeeds
only with an ordinary process exit and a matching durable receipt. A
termination signal forbids a receipt, including when a child traps it and
exits zero. `steps[].options.skip_exit` accepts 1–255: that exit closes the
gate and skips downstream steps without failing the run. Output is kept in
the attempt's `output.log`, with the first 1 MiB and last 256 KiB retained.

Checkout resolution uses a supplied job `checkout_key`, then the project's
`default_checkout` when the key is absent. A mapping requires a selected job key
or project default. A supplied unknown key never falls
back. With neither, the reason is `checkout not configured: set checkout_key
or the project's default_checkout`.

A prompt step resolves its profile from step `options.agent`, job
`options.agent`, project `default_agent`, then `default`. Empty strings count
as absent. A selected unknown profile never falls back. Explicit `"default"`
at job or step scope overrides a project default. A present non-string agent
is unsupported with `agent must be a string`. Resolution freezes the checkout
path and argv when work is prepared; config edits do not change prepared runs.

Job `options.executor` is an optional non-empty string equal to the worker's
stable `executor` label. A different pin excludes the job from this worker;
a manual request is rejected with `job is pinned to executor <x>`, and doctor
prints `pinned elsewhere <uid>: options.executor=<x>`. A malformed pin remains
owned and unsupported with `options.executor must be a non-empty string`.
Pins provide local filtering; see the [runtime operating condition](operations.md#runtime-ownership).

`options.input` supplies input; manual `--input` overrides it,
including an explicit empty string. Prompt text and workflow `about` expand
`{{input}}` and `{{previous}}` once. Inserted text is not expanded again.
`previous` is the preceding settled attempt's note. Every step's environment
carries `KATA_PROJECT` (the job's project) and `KATA_CRON_RUN`,
`KATA_CRON_RUN_DIR`, `KATA_CRON_STEP` and `KATA_CRON_ATTEMPT`. Unknown placeholders or
a prompt over 256 KiB hold `invalid_prompt`.

`per-run` creates one issue with a stable keyed `--force-new` request,
optionally setting `deadline_offset_seconds` from the frozen start time.
Job-level `options.run_parent`, a full issue UID, creates each per-run issue
as a child of that issue (`kata create --parent`). It requires a `per-run`
issue; a short ref, an `existing` issue policy or a workflow or step option
is unsupported.
The issue UID is saved before admission observation bytes are frozen.
A prepared run without a saved issue UID after 6 days holds
`admission_expired`. `existing` attaches the configured issue and does not
mutate it. Agents own issue closure. A successful run may leave its issue
open; the adapter never closes issues on the agent's behalf.

## Unsupported definitions

An owned definition that cannot run produces a failed Kata run with this
two-line message, even for a manual-trigger job without a run request:

```text
reason: unsupported
profile not configured: missing
```

Fix the job or worker configuration; `herdr-kata doctor` shows the same
reasons. Multiple reasons join with `, ` on line two, truncated to keep the
message within 4,096 UTF-8 bytes. No terminal or issue is created and no job
checkpoint advances. The worker records each distinct combination of job
definition event, workflow definition event and ordered reasons once for the
life of its state directory. A changed cause gets a new report; returning
to a completed cause does not. Missing or deleted workflows contribute no
workflow reference to the observation.

Delivery uses a separate durable outbox. It retries identical evidence after
uncertain results, including across a worker restart. An intent already
created is still delivered after a job is repaired, disabled, deleted or
repinned; those changes prevent new intents. Doctor shows pending/refused
reports and their errors. Manual unsupported-run requests retain their local
rejection path and create no additional report intent.

The worker writes to its configured daemon. With federation, the report
reaches the hub on the next push and other machines on their next pull,
usually about a minute. A quarantined spoke push hides every report from
those replicas; check `kata federation status`.

Doctor lists these owned-definition reasons:

| Feature | Reason | Use instead |
| --- | --- | --- |
| on_fail loops or findings | `loops_unsupported` | A separate workflow |
| on_failure park | `park_unsupported` | A failed step fails the run |
| secret_refs | `secrets_unsupported` | Operator-managed native agent authentication |
| agent_mode other than fresh | `agent_mode_unsupported` | Fresh attempts |
| scheduled_offset_seconds | `scheduled_offset_unsupported` | Native scheduling |
| Agent value is not a string | `agent must be a string` | A profile name |
| Malformed executor pin | `options.executor must be a non-empty string` | A stable executor label or no pin |
| Once time at or before job creation | `once_in_past` | A future instant |

Missing profiles, checkouts or workflows and disabled commands also appear
in doctor. Triggers that cannot compile appear as `invalid_trigger`.
There is no resume, remote execution, issue-request executor,
token accounting, quota classification or custom issue metadata. See
[operations](operations.md) for held runs.

## Why didn't my job run?

Unsupported reporting covers owned definition defects, not every scheduling
or transport problem. These cases do not create a new unsupported run:

| Case | Where to look |
| --- | --- |
| Project not enrolled, different project UID, or definition not replicated to this daemon | Project config and federation state |
| Disabled or deleted job, another adapter, `notify` action, or pin for another executor | Job definition; doctor shows foreign pins |
| Worker or core stopped, invalid startup config, daemon access or compatibility failure | Worker logs and doctor |
| Unreadable project jobs, workflows or required readiness | The tick skips the project; worker logs |
| Manual trigger without a request, future occurrence, no matching cron date, or no eligible issue schedule | Trigger and issue planning dates |
| Existing issue is not ready, including closed, blocked or deferred issues | Doctor: `gated <uid>: existing issue <issue> not ready` |
| Capacity exhausted or `overlap: forbid` with prepared, running or held work | Doctor: `project <p>: held runs hold capacity: <run uids>`; release investigated holds |
| Occurrence before installation/creation, outside catchup bounds, displaced by `latest`, beyond `skip` grace, or dropped with an old definition | Catchup policy and checkpoints |
| Local journal, checkpoint, admission or launch error | Worker logs; existing run holds where available |
| Spoke/hub/client transport delays or quarantine | `kata federation status` on the relevant replica |

A not-ready existing issue alone is a gate. An independently unsupported
definition still reports its defect after successful project reads. A
command's `skip_exit` is a successful skip, not an unsupported failure.
