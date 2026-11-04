---
title: Operations
description: Inspect output, recover workers and release held work.
last_edited: 2026-11-04
---

# Operations

Use Kata's Jobs and Runs tabs for run history. `herdr-kata doctor` checks the
configured execution environment, unsupported definitions and pending/refused
unsupported-report counts with job UIDs and delivery errors. There
is no adapter status command or TUI.

`herdr-kata up` starts or adopts this installation's locked worker terminal,
named `herdr-kata-<state-path-hash>`. `stop` kills and removes only that
worker. Job terminals keep running. Start the worker again to recover them;
it never sends a prompt to an existing terminal. A foreground `serve` takes
the same lifetime lock. Definitions and scheduling are refreshed every
30 seconds. The 2-second local-request poll triggers a tick only for IDs this
process has not seen. Known pending requests, including releases waiting for
Kata, retry on the regular 30-second tick.

A manual run is rejected rather than queued when its existing issue is not
ready or a required host profile executable or checkout mapping is missing;
rerun it once the condition clears.

A worker badly stalled by hc or Kata timeouts across many runs can still drop
a `skip` occurrence once lateness exceeds grace plus 2 minutes.

## Runtime ownership

Operate one effective runtime per project UID. Use one executing installation,
or ensure every enrolled executor is on v0.3.0+ with distinct stable labels
and matching job pins. Project names alone do not establish shared identity.
Two workers enrolling the same project can both execute every unpinned job;
Kata does not prevent duplicate occurrences.

Pins are local filtering only. A v0.2.0 worker ignores `options.executor`, so
pins do not isolate mixed-version installations. Two installations sharing
one executor label cannot be distinguished. Inventory project UIDs and
executors before enrolling another runtime or changing ownership.

Doctor samples the newest 100 run observations per owned job, using server
`created_at` within the last 7 days. It excludes `reason: unsupported` evidence
and warns `warning: another executor reported job <uid>: <label>` for foreign
labels, showing absent/empty labels as `unknown`. A full page still inside
the window is marked truncated; a failed list read marks the check incomplete.
This warning detects evidence after the fact. It does not prevent duplicates
or prove that no other runtime is active.

## Unsupported report delivery

The `unsupported/` directory under the state root holds frozen observation
intents separately from execution journals. The frozen `Body` is readable JSON;
earlier Version 1 records with a base64 body remain readable and replay with
the same bytes. These records reserve no capacity
and never enter admission, release or launch. Delivery follows normal work,
with at most one pending intent per tick and one shared Kata call-timeout
budget, including conflict readback. Oldest attempts go first so a failing
record cannot monopolize delivery.

Accepted/replayed reports become `done` and stay for the life of this state
directory as deduplication barriers. Unavailable or uncertain results stay
`pending`; conflicts are read back and only identical evidence counts as
delivered. Different evidence or definitive validation refusals become
`refused` and are not retried automatically. Invalid or unreadable record files stay in
place for inspection and are named in doctor errors. Valid records still deliver
and contribute to the pending/refused counts. A pending report for an unenrolled
project stays local; it is never routed to another project. Doctor lists it
even when its project is no longer configured.

For a refused report, fix the cause, stop the worker, then delete only the
record file doctor names to report anew under a new run UID. Start the worker
again. Keep completed records and execution journals. See [Jobs](jobs.md#unsupported-definitions)
for reason codes, visibility and federation delays.

## Upgrade and rollback

Before adding project defaults, run the current binary's `herdr-kata doctor`
and inventory the enabled job definitions. Record each unsupported job and all its reasons,
its catchup policy, trigger, current checkpoint and resolved checkout/profile.
Decide which jobs should become runnable when the new defaults are added.
Disable any that must remain inactive before restarting the worker.

Defaults can wake previously unsupported jobs at the first supported tick.
A job with no checkpoint starts from the later of installation and job creation,
with catchup bounded to 7 days and 100 occurrences. `latest` can launch the newest
missed occurrence immediately; `all` queues the retained missed occurrences,
subject to capacity and overlap. `skip` retains only occurrences within its
startup grace and lateness bounds. Remaining owned definition defects produce
failed `reason: unsupported` observations. Review this inventory before the
upgrade so both catchup work and newly visible failures are expected.

Drain active work and choose a quiet interval before replacing the worker
binary or configuration. Keep the previous binary and its config together,
replace the binary atomically, then start the worker and run doctor. Preserve
executor labels, hc session and current state. v0.2.0 rejects the new project
default keys: rollback restores the old binary and its config as a pair,
while retaining current journals, checkpoints and unsupported-report records.
Never restore a stale state snapshot or restart the core for this upgrade.

## Held runs

Doctor names held run UIDs when either the project or global capacity limit
blocks new work, including holds in another project consuming global slots.

Unknown or ambiguous execution holds a run, reserves its capacity and keeps
its terminal for inspection. The hold is immediately reported to Kata as
`unknown`, or `failed` when a step certainly failed, with `reason: <code>`.
The reason code stands alone on the first line; detail and step summaries
follow on later lines.
Per-run issues receive one notification. A blocked agent gets one
notification per blocked spell, rather than an attention metadata write.
Existing-policy issues receive no adapter writes, including notifications.

A malformed optional note does not hold a known command or agent outcome.
It settles with a [normalized note](jobs.md#actions-and-issues), preserving
the command's exit decision or the otherwise valid agent result's status.
Once the run settles, capacity and overlap gates are available to later work.

| Reason | Meaning |
| --- | --- |
| `launch_ambiguous` | Saved intent cannot identify exactly one matching launch |
| `launch_unproven` | The core changed and absence cannot prove no launch happened |
| `occupant_changed` | Terminal identity no longer matches the saved execution |
| `agent_lost` | The agent disappeared without a provable outcome |
| `no_result` | A completed reported turn has no valid result after its grace period |
| `no_activity` | A reported agent stayed idle at turn zero for 5 minutes |
| `timeout` | The frozen run deadline expired |
| `command_outcome_unknown` | A command lacks a valid ordinary-exit receipt |
| `invalid_prompt` | Prompt validation failed before launch |
| `invalid_workflow` | Workflow dependencies cannot be resolved |
| `invalid_note` | Legacy hold from a malformed command note; new attempts normalize notes |
| `agent_error` | The agent explicitly reported an error state |
| `report_refused` | Kata definitively refused a write or read-back differs |
| `admission_expired` | Issue creation could outlive its keyed replay window |

Inspect `herdr-kata logs <run-uid>`. Commands read their local `output.log`;
live prompts use a guarded `hc read`. Use the configured hc session when
inspecting a terminal yourself. Do not edit a run journal to retry a prompt.
After investigating, `herdr-kata release <run-uid>` posts a local release
request. Once the hold has been observed or reporting has been refused,
release frees the slot and overlap gate. It preserves the outcome and does
not rerun the attempt, remove its terminal or prove that work succeeded.
Upgrading does not clear existing `invalid_note` holds. Investigate and release
those runs through this same procedure to free capacity for later work.

An agent reporting `needs-human` ends the run as `unknown` and keeps its
terminal. A lost or killed agent cannot be assumed successful from its exit
code. Never-reporting
agents can settle from an ordinary exit with a valid result, or a result
stable for 30 seconds. Reported idle/done agents require a completed turn;
a missing result has a 10-second grace period. Valid result files are regular
UTF-8 JSON files no larger than 64 KiB, with matching run/step/attempt
identity and status. Their notes are normalized at settlement; the prompt gives
the agent the exact `herdr-kata report` command, which still enforces the note
contract when writing a result.
Attempt-directory I/O errors are retried on later ticks rather than reported
as prompt validation failures.

## Recovery and isolation

Keep the state directory with its installation identity, checkpoints, run
journals and request files. Journals are durable before acknowledgement;
admission and final observation bodies are frozen before sending. Conflicts
are read back and compared. Uncertain notifications are never retried.
Accepted agent summaries are saved before terminal cleanup and included in
the final observation. Its message is bounded to 4,096 UTF-8 bytes. Run
journals publish as complete directories; interrupted unpublished staging
directories do not participate in recovery or deduplication.
Do not remove state to recover a run: doing so discards its duplicate-launch
barrier. v1 state cannot be migrated by copying it into v2.

Before interrupt, kill or remove, the worker checks core ID, terminal
creation time, PID and nonce. It leaves a changed occupant alone. A core
restart can therefore hold affected work instead of launching it again.

For runtime checks use a fresh `HC_SESSION=hk3-<random>`, scratch HOME and
XDG directories, isolated Kata home/catalog/daemon and `example-project`.
Remove inherited `HC_SOCKET`, `KATA_*` and native-agent configuration
selectors. Verify the scratch core state path and ID before terminal calls
and stopping it. Kill and remove only terminals the check created, then stop
both scratch services and remove their directories. Never use an operator's
session or real project. The gated `make test-live` automates these checks
on the installed hc and checksum-verified hc 0.1.16 and 0.1.15. The installed
binary still receives a fresh scratch session and state/config/runtime roots.
The suite checks that scratch core's state path and ID before terminal calls
and teardown. It deliberately does not snapshot or query the operator's default
core: runtime verification must stay inside the disposable session, including
when the default core is dormant. Add
`HERDR_KATA_LIVE=1 HERDR_KATA_LIVE_CODEX=1` to include the real native Codex
case; only its login file is copied into the disposable native home.
`HERDR_KATA_LIVE_CODEX_SANDBOX` selects the native sandbox mode and defaults
to `workspace-write`. Use `danger-full-access` only where the operator has
chosen the surrounding container as the security boundary. The test does
not add host capabilities or disable the container's seccomp policy.
