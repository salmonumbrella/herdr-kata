---
title: Operations
description: Inspect output, recover workers and release held work.
last_edited: 2026-11-06
---

# Operations

Use Kata's Jobs and Runs tabs for run history. `herdr-kata doctor` checks the
configured execution environment, unsupported definitions and pending/refused
unsupported- and dropped-report counts with job UIDs and delivery errors. There
is no adapter status command or TUI.
Its optional `service` line reports login-service file presence; see
[Install](install.md#start-at-login) for setup, refresh and removal.

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

## Quarantined projects

An invalid `[[projects]]` entry is excluded from the loaded configuration.
Missing names or actors, negative limits, unknown defaults, unknown keys and
wrong value types quarantine that entry. Every entry sharing a repeated name
is quarantined, including an entry already invalid for another reason. Global
settings and TOML syntax stay strict. A file with no valid projects loads and
the worker idles.

Project keys are case-insensitive; agreeing name aliases remain accepted.
Conflicting `name` and `NAME` values quarantine that entry; every declared string name participates
in duplicate checks. Differently cased root containers such as `[[projects]]`
and `[[PROJECTS]]` in the same file are ambiguous and reject the whole file.

`serve` logs `warning: quarantined project entry N (name): reason` once per
entry at each worker start. A successful `up` prints the same warnings before
the worker address. `doctor` prints `FAIL config: quarantined ...` and exits 1
until all entries are fixed. Entry numbers follow file order, starting at 1;
a missing name is omitted from the diagnostic. `up` and `doctor` inspect the
file on disk. An adopted worker still uses its original loaded configuration.
If an entry has several wrong value types, `invalid value:` reports one of
them; which field appears first may vary between loads.

Quarantined projects have no definition reads, new launches or unsupported
report delivery. Active runs remain journaled and reserve global capacity;
automatic advancement, settlement, reporting and deadline enforcement wait.
Already launched terminals keep executing, and their results can still appear.
Frozen deadlines do not reset. Explicit release requests retain their existing
rules. A manual request for an excluded project's job is rejected as `job is
unknown` after configured-project reads succeed; read failures can leave it
pending instead.

Fix the entries, run `herdr-kata stop`, then `herdr-kata up` and
`herdr-kata doctor`. Processing resumes under the ordinary recovery rules:
saved launch intent without an address recovers by labels and argv, addressed
attempts resume settlement, and held runs stay held. Timeouts and lost or
ambiguous execution can require operator attention. Scheduled checkpoint
catchup remains bounded to 7 days and 100 occurrences under the job's catchup
policy; issue-scheduled jobs inspect the issue's current planning date.

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

## Fleet capabilities

Upgrade the Kata daemon before the worker. The worker reads each project's
advertised capabilities at most once every five minutes, retaining the last
good snapshot when refresh fails. Until discovery first succeeds it defers
new work and unsupported reports; already admitted runs keep advancing.
A successful response without fleet fields retains legacy behavior.

An advertised execution policy of `none` excludes every job; `owned` allows
only exact matches between job author and the daemon's member actor; `all`
allows normal local ownership. Unknown modes exclude all jobs. Excluded jobs
produce neither execution nor unsupported reports. Local manual requests get
the exclusion reason. Doctor lists each project's policy and feature flags,
and prints `excluded <job>: <reason>` before readiness or definition checks.

Policy changes can remain cached for five minutes. A job excluded after
preparation can reach Kata's admission backstop and hold as `report_refused`;
a per-run issue may already have been created. No terminal launches before
admission is confirmed.

When `cron_run_summary_v2` is advertised, newly frozen final and hold reports
include a reason and the latest attempt for each attempted step, in plan
order. Zero command exit codes are preserved. Step messages are bounded to
4 KiB; deterministic shortening keeps the fully encoded summary within
64 KiB. Usage and log links are omitted because the worker has no shared
usage or log evidence.

When the latest attempt has a command receipt with nonzero, ordered start
and end times, its step includes both timestamps in UTC RFC3339Nano. Both
UTC years must be within 1–9999; otherwise both timestamps are omitted while
the remaining step evidence is preserved. These times describe only that
command attempt's receipt interval. They do not include earlier retries or
cleanup, and prompt attempts have no receipt timing. A latest attempt without
a receipt never borrows times from an earlier attempt. Unattempted steps
remain omitted, so step coverage may be partial.

When both the Kata CLI and daemon advertise `cron_run_sessions_v1`, final and
held reports include recorded prompt sessions, independently of summary v2.
Earlier attempts appear in journal order, followed by the current attempt for
each step in plan order. Exact agent and session pairs are deduplicated, with
the first step retained. Reports include at most 64 distinct sessions.
`sessions_incomplete` marks a dropped identity or a recorded prompt launch
without a valid identity, including an ambiguous launch. Unlaunched prompts and
command steps add no session evidence. Doctor shows `sessions=true` only when
both capabilities are present.

The report freezes this choice and the recorded identities before sending;
retries reuse those bytes even if capabilities or the journal change. A
structured `unsupported_event_features` conflict first saves a projection
without sessions, then, if necessary, one without reason and steps, before
retrying with the same run identity and revision. A structured
`federated_read_only` conflict projects only session-bearing reports, removing
sessions and the incomplete marker. Other conflicts and uncertain writes retain
the original bytes. Removing steps also removes their receipt timing.

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

## Dropped report delivery

The `dropped/` state directory holds the frozen observations described in
[Jobs](jobs.md#dropped-occurrences). Each Version 1 record also saves the exact
post-drop checkpoint before the job checkpoint advances. Recovery applies that
decision before scheduling new work, even if its observation is already `done`
or `refused`. Checkpoint files and unsupported records keep their existing
formats and Version values.

Delivery tries one pending drop per tick, separately from the unsupported
outbox, with the same frozen-byte retries and conflict readback. Records reserve
no execution capacity. Keep pending, done and refused records: they preserve
recovery and deduplication evidence. There is no automatic pruning.

Doctor prints `dropped reports: pending=<n> refused=<n>` and each refusal's
error. Refused drops need operator investigation; deleting a record does not
resend it because the checkpoint may already have passed those occurrences.
Keep the record while fixing the cause. Unsupported-report deletion guidance
above applies only to unsupported records.

## Upgrade and rollback

v0.5.0 retains the v0.4.0 configuration and state formats. Keep the existing
state and executor identity when upgrading; held runs remain held. Fleet
requests, runtime heartbeats and structured summaries require advertised daemon
support. Daily alert replay across priority-default changes also requires a
Kata CLI supporting `create --priority none`; older CLIs retain their previous
defaults and conflict behavior. Previously consumed alert intents are not
backfilled. See [Failure alerts](#failure-alerts) for carrier safeguards.

Fix quarantined project entries before rolling back: v0.3.0 rejects the
whole file when any project entry is invalid, including an unknown key.

Before enabling per-job models, inventory raw job and workflow-step
`options.model` values, or stage the new binary and run its doctor with the
intended config before starting the upgraded worker. The v0.3.0 doctor ignores
these options and cannot identify affected jobs. Previously ignored models
now pass through a profile's `model_flag`, or make the job unsupported when
the profile lacks it. Owned unsupported jobs receive a durable failed
observation on first successful discovery, even without a due run.

v0.3.0 rejects `model_flag`. Roll back the binary and its configuration together;
retain current state, journals, checkpoints and unsupported-report records.


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

A run whose terminal the worker confirms stopped at its deadline settles
`failed` with `reason: timeout`. A new prompt attempt with no observed startup
activity at the two-minute cutoff is stopped and settles `failed` with
`reason: agent_not_started`. These known failures end the whole run, bypass
step retries and later steps, and free capacity and overlap for the next
occurrence or manual request. Local settlement frees the slot even while a
transient Kata error delays delivery of the frozen final observation. See
[Jobs](jobs.md#startup-and-timeouts) for startup evidence and timing.

Doctor names held run UIDs when either the project or global capacity limit
blocks new work, including holds in another project consuming global slots.

Unknown or ambiguous execution holds a run, reserves its capacity and keeps
its terminal for inspection. The hold is immediately reported to Kata as
`unknown`, or `failed` when a step certainly failed, with `reason: <code>`.
The reason code stands alone on the first line; detail and step summaries
follow on later lines.

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
| `no_activity` | A grandfathered or previously started reported agent stayed idle at turn zero for 5 minutes |
| `timeout` | Deadline expiry could not establish a stopped terminal without an ambiguous completion artifact, or no launch was proven |
| `agent_not_started` | Startup termination could not establish a stopped terminal without an ambiguous completion artifact |
| `command_outcome_unknown` | A command lacks a valid ordinary-exit receipt |
| `invalid_prompt` | Prompt validation failed before launch |
| `invalid_workflow` | Workflow dependencies cannot be resolved |
| `invalid_note` | Legacy hold from a malformed command note; new attempts normalize notes |
| `agent_error` | The agent explicitly reported an error state |
| `report_refused` | Kata definitively refused a write or read-back differs |
| `admission_expired` | Issue creation could outlive its keyed replay window |

Inspect `herdr-kata logs <run-uid>`. Commands read their local `output.log`;
prompt startup and timeout stops save the last 200 screen lines there before
the initial interrupt, so dialogs remain readable after terminal removal.
An incomplete snapshot is marked. Other live prompts use a guarded `hc read`.
Until stop intent is saved, prompt logs keep reading the live terminal even
if an interrupted startup check left a provisional snapshot.
Capture failure defers stopping. A resumed stop preserves its snapshot;
an older journal's already-pending timeout captures the remaining screen
before further destructive work. Use the configured hc session when
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

## Failure alerts

Failed and unknown runs request the configured project `actor`'s attention in
Kata's inbox. The alert names the frozen status, first reason line and run UID,
for example `run failed: reason: failed; <runUID>`. Details and step output stay
in the run observation.

| Run state | Attention |
| --- | --- |
| Settled `failed` or `unknown`, including a needs-human handoff | One settle alert intent |
| Held | One held alert intent with its reason and hc address |
| Succeeded, gate skip or released | No settle alert |
| Blocked agent | Existing per-spell notification on a per-run issue |
| Unsupported definition | No alert; inspect doctor and Runs |

Per-run jobs notify their run's issue. Existing-policy jobs create or reuse a
separate issue titled `cron alerts: <jobUID> <YYYY-MM-DD>`, grouped by the
run's UTC **start date**. Each failing run replaces the actor's current
attention request on that carrier. It has no parent or links; the configured
existing issue receives no adapter writes. A run starting before midnight
and failing afterward still uses its start-date group. Closing the carrier
suppresses requests for that group; reopening can expose an uncleared request.

With a Kata CLI supporting `create --priority none`, explicit unset priority
keeps historical unprioritized carriers replayable across client default
changes. Per-run issue creation retains its configured defaults. A prior
numeric-priority carrier still conflicts and requires investigation.

For an older numeric-only CLI, only its exact pre-dispatch parser refusal of
`none` permits a retry using the prior create defaults. This preserves existing
behavior and still verifies creation evidence, but changed defaults can still
conflict. Upgrade Kata to enable the default-drift repair. Other usage errors
and create conflicts never trigger adoption or a fresh key.

Before notification, the adapter verifies the immutable creation event and key,
project, author, teammate and original content. It also reads the current issue
and refuses closed carriers or changes to content, ownership, priority, labels,
relationships or non-notification metadata. Missing evidence and unrelated
conflicts fail closed. These checks do not replay previously consumed alert
intents; existing notification retry rules below still apply.

Held and settled intents exclude each other, including across worker restarts.
The worker saves intent before sending and retries only known unavailability.
An uncertain send is never resent. A crash after intent is saved but before
sending, or an uncertain or refused send, can leave no delivered request.
Notification refusal does not turn a settled run into a hold.

Carrier creation stops six days after midnight on the frozen UTC start date,
using an advancing clock at the create boundary. This leaves a full day's
margin before Kata's seven-day create-key replay window expires. A journal
without a start time or job identity also skips the carrier. These skipped
alerts log one diagnostic and allow final observation reporting to continue;
inspect Runs and held-run
diagnostics for old or undated failures. There is no alert configuration key.

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

The job-list `project_pause` field stops new preparation and admission. An
absent or null value allows work; every other value pauses it. A failed
job-list read also keeps prepared journals waiting. Admitted runs keep
settling and reporting. Resume applies each job's existing catch-up policy.

When advertised, pending Kata run requests use `request:<uid>` occurrence
keys and the same ownership, readiness, capacity and overlap checks as other
work. New preparation must happen before expiry, checked after preparation
reads; a durable preparation can recover after expiry. Definition revisions
must match. A request is answered by normal admission, with no separate
acknowledgment.

Advertised cancel requests save intent before acting, even while paused or
job reads fail. Prompts receive an interrupt and ten seconds of grace before
a guarded kill; commands are killed directly. Cancellation completes only after
a matching stopped terminal is observed. Unproven stops keep capacity held,
and saved removal intent still recovers after a lost reply. Terminals remain
inspectable.
Lost or ambiguous execution still holds for the operator. No later step or
retry starts after cancellation intent is saved.

Workers advertise active runtime presence only when `cron_runtimes_v1` is
advertised. Beats include the adapter version and compiled adoptions, at most
once per minute per project, measured from each attempt. Failures use the same
interval and a three-second call budget. The derived identity survives restart
without another state file. Older daemons retain legacy behavior.
