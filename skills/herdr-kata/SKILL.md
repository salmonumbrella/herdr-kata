---
name: herdr-kata
description: Use when operating Herdr Kata native cron jobs, flows, local activation, parked runs, persistent recovery, buffered results, exact inboxes, hooks, or scoped resource leases.
---

# Herdr Kata execution

Kata owns intent, portable native definitions and bounded attributed run evidence.
Herdr Kata owns local scheduling, activation and process recovery. Install Kata
separately; `native tui` and the board's **K** key resolve its installed TUI on each
launch. Use ordinary Kata issue, comment, notify and inbox commands for work.

Check `herdr-kata --version` and configure an explicit native target advertising
`cron_v1`; follow [native setup](../../docs/native.md). Daemon, project,
workspace, accountable actor and teammate are explicit. Checkout paths and secret
values stay in local mappings. Never run experiments against user state: use an
owned temporary home, workspace, database and named Herdr test session, with a
scrubbed OS/toolchain environment.

## Save and activate deliberately

`job add --id <retained-ulid>` saves a dormant native definition. Shared `enabled`
configuration does not activate your installation. Use `native activate <job-uid>`
after reviewing a fresh definition and checkout mapping; `native deactivate`
changes only local activation. A scheduler starts new work only after a current
native refresh. Labeled offline cache is for reads. Explicit `job run <uid>` is
local opt-in for that execution. Other installations may independently execute
the same occurrence, with their own run UIDs and local policy.

```bash
herdr-kata native checkout primary /path/to/checkout
herdr-kata job add --id <retained-ulid> --name Inspect --prompt 'Inspect workspace' --cwd /path/to/checkout
herdr-kata native activate <retained-ulid>
herdr-kata job run <retained-ulid>
```

Retain create UIDs across failures. Definition edits/deletion use the displayed
winning event UID; conflict requires refreshing and reviewing that winner.
`native job|flow save --uid ... --file ... --expected-event-uid ...` accepts portable
JSON. Native issue readiness defers future `scheduled_on`, `someday` and blocked
work; deadlines never gate execute jobs. Cron honors its trigger timezone; native
issue dates use Kata's timezone/DST projection. Default reached-date notifications
belong to Kata's sweeper. Custom offsets/recipients use ordinary notifications.

## Flows and recovery

Use a flow for a sequence the harness must enforce. `flow new <draft-id>` writes
unsaved YAML under `~/.herdr-kata/drafts/flows/`; editing alone does not save it.

```yaml
steps:
  - id: inspect
    agent: Read {{input}} and describe the change.
  - id: verify
    run: go test ./...
```

```bash
herdr-kata flow new inspect --about 'Inspect and verify'
# Edit the unsaved YAML, then save explicitly.
herdr-kata flow save inspect
herdr-kata flow list
herdr-kata flow run <native-flow-uid> --input 'example-workspace'
herdr-kata flow status <run-uid>
herdr-kata flow resume <run-uid>
```

Each step has exactly one `agent` or `run`; unknown YAML keys, malformed steps and
placeholders are rejected. Each agent step has its own result file. Failed steps
park and stop later steps; completed steps remain complete on resume. Bounded
`on_fail` loops retain their budget unless `--reset-loops` is explicit. Resume
retains the original input, reference, snapshot and workspace, including after
shared definition edits/deletion. To change input or routing, start another run.

Persistent jobs reuse a proven live conversation. KeepContext=false clears before
the next prompt; true preserves it. Recovery verifies the saved run/tab/context
and available harness-session provenance. Missing or mismatched provenance needs
inspection. Do not restart a user's foreground session to repair it. See
[native recovery](../../docs/native.md) for precise limits and parked artifacts.

## Evidence and attention

Shared observations are bounded, attributed status/summary evidence, never raw
logs, snapshots, credentials or runtime handles. Local buffer failure does not
permit or prevent execution. Restart retries a frozen pending observation and
coalesces only its unsent tail. `doctor` exposes pending/failed delivery. Settled
result comments retain their body/key; after seven days they reconcile scoped
markers before retrying. Parked needs-human attention is separate from history.

```bash
herdr-kata inbox list --for worker/adapter
herdr-kata teammate connect --for worker/adapter --workspace <id> --pane <id> --conversation <session>
herdr-kata inbox deliver --for worker/adapter
kata inbox --for worker/adapter
# The recipient handler clears after handling, then reads back.
kata notify <issue-ref> --to worker/adapter --clear
kata inbox --for worker/adapter
```

`worker` never aggregates `worker/*`. Reads and bridge delivery never clear a
request. Attention is replaceable, so concurrent replacement/clear can race.
Occupied/busy/unavailable attention stays local. Generic Herdr delivery requires
manual wake and never automatically types into an agent. Disconnect stale local
registrations with `teammate disconnect --for ...`.

## Upstream adoption and resources

Use `native import --source <offline-snapshot> --source-id <retained-namespace>
--checkout-key primary [--cron-timezone <IANA-zone>]`. The source must be separate,
stopped and checkpointed. Retain its namespace after interruption or moving it.
Definitions stay dormant; no obsolete collaboration data, history, leases, PIDs
or sessions are adopted. Review obsolete prompt warnings and local mappings before
activation. Changed source content conflicts; use ordinary explicit CAS editing.

Local resource leases coordinate only callers sharing the same store/scope; they
do not reserve native issues or occurrences. Claim a bounded TTL, renew before it
expires, and match all holder/job/run/discriminator fields on renew/release.
`lease claim ... --wait 5m` names the current holder on contention. Never release
another run's lease.

Back up native shared data with Kata's ordinary workflow and local mappings,
activation, buffers, drafts and journals separately. Use SQLite backup facilities
or stopped writers, not a main-file copy during WAL writes. `stop` records the
local daemon/sentinel stop; `start` reverses it. Report actual operational changes
in Kata. See [hooks](../../docs/hooks.md) for optional ordinary result hooks.
