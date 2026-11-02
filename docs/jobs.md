---
title: Jobs
description: Supported Kata definitions and execution behavior.
last_edited: 2026-11-02
---

# Jobs

Create and edit portable definitions with Kata's job and workflow commands
or its Jobs tab. The adapter executes enabled `execute` jobs whose
`options.adapter` is absent or `herdr-kata`. It records independent run
observations; Kata's recorded run status does not schedule execution.

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

`options.agent` selects a configured profile at job scope or on an individual
prompt step. `options.input` supplies input; manual `--input` overrides it,
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

Doctor reports these reasons. Unsupported jobs are never launched.

| Feature | Reason | Use instead |
| --- | --- | --- |
| Notify jobs, including issue-deadline | `notify_unsupported` | Kata recurring children or date notifications |
| on_fail loops or findings | `loops_unsupported` | A separate workflow |
| on_failure park | `park_unsupported` | A failed step fails the run |
| secret_refs | `secrets_unsupported` | Operator-managed native agent authentication |
| agent_mode other than fresh | `agent_mode_unsupported` | Fresh attempts |
| scheduled_offset_seconds | `scheduled_offset_unsupported` | Native scheduling |
| Per-run issue on notify | `per-run notify belongs to the hub` | Native delivery |
| Another adapter | `adapter: <name>` | The selected adapter |

Missing profiles, checkouts or workflows and disabled commands also appear
in doctor. Triggers that cannot compile appear as `invalid_trigger`.
There is no resume, remote execution, issue-request executor,
token accounting, quota classification or custom issue metadata. See
[operations](operations.md) for held runs.
