---
title: State and run phases
description: State files, run phases, submission barriers, capacity rules and retention.
last_edited: 2026-10-21
---

# State and run phases

The worker owns durable local execution state. Kata owns definitions and
reported evidence. Attempt artifacts support the journal; they do not replace
it. [Hold reasons and resume rules](holds.md) explains held runs; use
[Recovery](../operations/recovery.md) to resolve held or lost runs.

## Plugin ID migration

The ID-only upgrade from `salmonumbrella.herdr-kata` to `kata` preserves every
existing state payload and version. `up` (and internal supervisor/worker starts)
adopts legacy sibling config/state directories before loading configuration.
It requires an idle legacy runtime and empty destination directories. The
migration takes the legacy `up.lock`, `serve.lock` and `worker.lock`, moves
the directories and leaves relative legacy aliases pointing to `kata`. Lock
inodes and saved absolute artifact paths survive the move.

`kata-id-migration.json` sits beside the `kata` state directory, with version 1,
source/target config and state paths, source-presence flags and a completion
flag. `kata-id-migration.lock` serializes its writers. The intent is synced
before moving anything; directory renames, aliases and completion are synced.
A retry finishes an interrupted transaction without overwriting or merging
files. Missing or changed aliases after completion block startup for repair.
Keep this marker and the aliases for saved artifact paths and future starts.

After interruption, restart the **new** binary to complete the migration before
starting the old binary. Keep the old plugin disabled throughout migration.
`doctor` reads the marker and paths without migrating: it reports pending,
blocked, interrupted, complete, custom directories or no legacy directories.
Only absolute directory pairs ending in `kata` are auto-migrated; overrides
select their own legacy siblings and never fall back to default directories.
See [upgrade](../get-started/install.md#upgrade-from-the-old-plugin-id).

## Directory layout

`$STATE` means the directory selected in [Configuration](configuration.md#files-and-overrides).

| Path below `$STATE` | Purpose |
|---|---|
| `state.json` | Version-1 layout marker. Project state always lives below `projects/`. |
| `projects/<project-uid>/state.json` | Version-1 project pin: name, creation time, executor and Kata identity/route. |
| `projects/<project-uid>/lease.json` | The lane's latest lease outcome for status: held and generation, or the owner and reason it is not held. The lease itself lives in Kata; see [lane ownership](kata-adapter.md#lane-ownership). |
| `up.lock` | Serialize binary staging and supervisor startup. |
| `serve.lock`, `worker.lock` | Exclusive supervisor and worker process locks. |
| `serve.json` | Supervisor/worker PIDs, build path and startup information. |
| `stop`, `upgrade` | Durable shutdown or staged-build request. |
| `workspace.json` | Owned workspace ID and Herdr server identity. |
| `projects/<project-uid>/events.cursor` | Event progress hint; never proof of completed execution. |
| `requests/` | Manual-run, resume and release request files. |
| `request-results/` | Local responses to control requests, including request kind, project UID and job selector when available, and `reason: lease_held_elsewhere` when a stood-down lane refused the request; refusals are also exposed by status. |
| `project-startup.json` | Per-project startup failures before a route can resolve, cleared when its lane starts. |
| `quarantine/runs/<run-uid>/acknowledged.json` | Explicit offline acknowledgement; preserves the quarantine admission veto. |
| `bin/herdr-kata-<build-id>` | Stable staged executables, named by the first 16 hex characters of executable SHA-256. |
| `projects/<project-uid>/jobs/<job-uid>.json` | Definition event, handled-through instant and pending occurrences. |
| `runs/<run-uid>/run.json` | Frozen definitions, input, target, timeout, prompt cap/count, step/attempt, provenance, outcome, loop decision, resume acceptance, finding UIDs and frozen finding context with operator notes, optional usage/cost totals and ordered Kata intents. |
| `runs/<run-uid>/steps/<key>/<attempt>/` | `prompt.md`, `result.json`, `receipt.json`, `process.json`, `output.log` and optional `note.txt`. |
| `quarantine/` | Corrupt state or whole runs resolved offline; nonempty run paths retain their UID and veto execution. |
| `projects/<project-uid>/defs.json` | Definition snapshot for status and the TUI, including saved issue and parent references for bindings without local runs and each job's Kata `updated_at`. Version 2; builds older than the field reject it until the runtime's next definition refresh rewrites the file. |
| `projects/<project-uid>/inbox.json` | Bounded inbox read model with last successful scan and error; no admission authority. |
| `projects/<project-uid>/route.json` | Last route-check result, observed project/instance/actor and `checked_at` timestamp. The [Kata tab](tui.md#embedded-kata) checks these against the pin without CLI probes. |
| `projects/<project-uid>/doctor-lost.cursor` | Progress through the bounded lost-run history scan. |
| `abandoned/` | Frozen observations for explicit lost-state abandonment. |
| `abandoned/reports/runs/<run-uid>/run.json` | Reporting-only version-1 queue journal for a lost run or request-refusal acknowledgement; never scanned for execution. Completed reports stay as replay receipts. |
| `board-start.lock` | Serialize startup pin creation. |
| `log/herdr-kata.log` | Runtime log, rotated at 5 MiB with five files retained. |

Runs, requests, workspace ownership, quarantine, staged binaries and logs stay
shared at the root. A run's frozen project UID chooses its lane forever.
Removing a project leaves its runs waiting and its reservations intact.

Files are versioned, size-bounded JSON where applicable. Writes use a temp
file, file fsync, rename and directory fsync. `run.json` transitions are
serialized per run. [Architecture](../design/architecture.md) explains the
process and package boundaries.

The run expiry is stored under the JSON key `Deadline` in `run.json`. It is separate
from Kata's issue planning date, `deadline_on`.

Every new run records its resolved project UID, `ProjectName` and an
`issue_refs` map from issue UIDs to authoritative `project#id` references.
Admission refuses an unresolved project. These display fields are separate
from frozen mutation arguments; capturing a reference never changes a queued
write or prompt submission. Journal version remains 1.

Usage adds optional `Usage` objects to the version-1 run and prompt-attempt
snapshots. The worker records input/output/cache counts, whether a source had
data, and an optional integer cost estimate in microdollars. A non-nil attempt
record prevents duplicate collection during settlement or recovery. Retries
start a new attempt record and add its usage to the retained run total. The run
total sets `Partial` when one collected attempt had data and another had none;
a partial total is not reported to Kata. Usage never changes execution or slot
ownership. It adds token counts only to the final observation built at
settlement.

## Run phases and outcomes

| Phase | Meaning |
|---|---|
| `prepared` | Journal exists; saved issue/admission operations are pending. Capacity is reserved. |
| `admitted` | Admission or resume observation is proven; finish pending claim, tracking, deadline or ownership writes before execution. |
| `running` | Executing or waiting for a step. |
| `blocked` | Agent needs attention; this is not a terminal outcome. |
| `held` | Automatic execution stopped for operator inspection; see [Hold reasons](holds.md#hold-reasons) for classes and their effects. |
| `handed_off` | Agent or operator handed work to a human; automatic execution never resumes. |
| `settled` | Execution finished; ordered reporting may still be pending. |

Outcomes are empty until known, then `succeeded`, `failed`, `unknown` or
`cancelled`. `cancelled` is a retained model value; ordinary execution emits
the first three. An agent's `needs-human` maps to `handed_off`/`unknown`.
An `Inspect` message can block queued reporting independently of phase.
Loop holds and held step failures retain a `failed` outcome, rather than the `unknown` used for
uncertain execution. Loop budget, consumed checker attempt and rejection note
are saved with the rewind; completed attempts and finding references remain
in the journal.

## Durable admission and signing evidence

New explicit issue-request runs use journal `Version: 2`; adapter and native
cron journals remain version 1. No existing state is migrated. Version 2
requires the optional `issue_request` envelope and rejects cron provenance.
Older binaries reject/quarantine these journals; preserve the complete state
directory before rolling back.

The envelope contains `request`, `parent_uid`, `binding`, `ownership`,
`notify_key`, `notify_json` and `admission`. Request and admission identify the
explicit execution request and its permanent receipt.
Binding and ownership retain their native-child shapes. The run reuses its
existing frozen target, input, workflow, profile, steps, journal ID and ordered
intents. `Source` is `issue-request:<child UID>`; `Child` and cron admission
observations are absent. Its stable run UID hashes that source using the child
UID's timestamp, independently of request edits.

New journals save a random `journal_id` and explicit `admitted: false`.
`lease_generation` records the lane lease generation the run was prepared
under; its issue creation and admission observation go out only while the
lane holds that generation ([lane ownership](kata-adapter.md#lane-ownership)).
Successful admission records `admitted: true` with its post-admission tail.
Pre-admission holds persist explicit false, so a hold or restart
never supplies proof.

Operations retain the observation's `replayed` result. `signing_refused` is
sticky evidence; `retry_identical` records a definitive uncommitted refusal
so even an unguarded operation can retry safely. The sending transition clears
that retry permission before the next attempt. Later uncertain replies retain
signing evidence and follow the operation's safety class. Only successful
delivery of the exact operation or whole-run quarantine removes its block.
Retired signing-refused operations remain visible to doctor and retention.

## Submission states

| State | Meaning for a prompt attempt |
|---|---|
| `none` | No prompt sent. |
| `rejected` | Herdr returned `agent_blocked` or `agent_not_ready`; no input was sent. |
| `sending` | Submission intent saved; delivery may have happened. |
| `accepted` | Prompt possibly or definitely delivered; never resend this attempt. |

`PromptCap` freezes the effective prompt limit, the stricter of the run's and
each prompt step's profile cap. `step_profiles` freezes, by name, every prompt
step profile that differs from the run's; it is omitted when there is none. `PromptsSent` counts attempts
in the generation, incremented atomically with the first `none` → `sending`
transition. A rejected resend counts once; recovery never resends `sending`
or `accepted`.

Commands save `sending` before start, then copy a receipt into the journal.
A missing receipt after a possible start is uncertainty, never a retry cue.

## Slots and overlap

Adapter runs and native children share `limits.max_active_runs`. A reservation
is made before preparation; a durable prepared journal keeps capacity even
while admission or reporting needs inspection. `running`, `blocked` and
possibly occupied `held` runs keep a slot. Held-slot reconciliation may release
capacity after stopped proof while preserving the held outcome.

[Pre-admission holds](holds.md#hold-reasons) release their execution slot and
overlap gate while retaining the journal for operator resolution.

For other runs, the job gate is separate: `forbid` waits until the earlier
run is `settled` or `handed_off`, even when its held occupant has stopped and
released its slot.
`allow` bypasses that gate but still uses shared capacity. Release deliberately
frees both, even if the agent keeps running. See the proof rules in
[Run lifecycle and recovery](../design/run-lifecycle.md#held-capacity).

## Retention and limits

A startup/daily pass removes settled, fully reported, noninspectable run
directories once their end time is older than 14 days. Held, blocked,
handed-off, unknown and signing-refused runs stay. Staged report binaries stay while retained
run references require them.

Quarantined journals no longer reserve a slot or gate; inspect their Herdr
occupants before relying on the configured capacity bound. A quarantine entry
vetoes fresh preparation of its run UID. Combined definition snapshots above
16 MiB are unreadable to `status` and the TUI. Never edit a journal to advance
an attempt; use operator controls.

## Command start evidence

Immediately after starting a command, the runtime saves an attempt-bound
`process.json` using the receipt fields: run, step, attempt, start time and
process-group ID, with `Uncertain=true` and no end time. A final `receipt.json`
still owns execution outcomes. Recovery never interprets a start record as
success or retries the command automatically. Once the recorded group is
proven gone, capacity can return and an explicit resume can authorize a new
attempt even when the final receipt was lost.
