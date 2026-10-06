# Native Kata setup

Install Kata separately and select a daemon that advertises exact `cron_v1`
support. Herdr Kata invokes its public CLI with argv/stdin, resolves the executable
on every launch, and keeps explicit daemon/project/workspace/actor/teammate routing.
It bundles no Kata binary or private issue UI. A missing capability advertisement
requires upgrading Kata or selecting a compatible target.

Write a local mapping JSON file. Replace the example target and paths with your explicit local mapping:

```json
{
  "Client": {
    "Executable": "kata",
    "Target": {
      "Server": "http://127.0.0.1:7777",
      "Project": "spoke-project",
      "Workspace": "/path/to/checkout",
      "Actor": "worker",
      "Teammate": "adapter",
      "Home": "/path/to/kata-home"
    }
  },
  "Binding": {
    "ExecutorLabel": "example-executor"
  }
}
```

Use `Daemon` instead of `Server` for an explicit named Kata catalog target; set
exactly one. `Home` selects that local Kata configuration and credential routing.
For an explicit server bearer credential, `Token` is a local target field.
Ambient Kata targets, tokens, proxies and hosted `PORT` are not inherited.

```bash
herdr-kata native configure --file mapping.json
herdr-kata native checkout primary /path/to/checkout
herdr-kata native secret service-key /path/to/secret-value
herdr-kata native refresh
herdr-kata native activate <job-uid>
herdr-kata native tui
```

Configuration discovers support without creating cron definitions or run history and
binds the returned project UID. No executor registry or execution authority is
created. The local mapping is written with
owner-only permissions. Checkout paths and secret values stay local; portable
native definitions contain checkout keys and secret reference names.

For jobs, `secret_refs` maps child environment names to local secret names, for
example `{"SERVICE_TOKEN":"service-key"}`. Each fresh launch, including a
captured-session resume in a new process, resolves the saved definition's
references against the current local mapping, so rotating a value does not
change saved intent. Mapped secrets require a fresh process: live persistent
reuse refuses before clearing or prompting the agent, even for unchanged values.
Inspect the existing conversation before restarting it; the plugin leaves it
running. Secret-free persistent reuse continues normally. Missing references stop before launch.
Routing, launcher, proxy, shell and toolchain environment names are reserved;
secret references cannot override them. Values never enter shared run evidence
or the saved execution context.

This executor supports portable `overlap: "forbid"`, zero/omitted
`grace_seconds` (the scheduler's two-minute first-fire grace), and zero/omitted
workflow-step `retries`. Other values remain in the raw native definition but cause a
named projection, activation or launch error. Overlap suppression is local to
this installation; independently started runs on other installations remain
independent. Workflow steps execute in dependency order. Herdr-specific step and
overwatch recovery options remain separate from portable retry counts.

An `issue.kind: "per-run"` job creates an ordinary attributed Kata issue before
launch, using its title, body and relative scheduled/deadline offsets. The local
run UID supplies the request idempotency key; the creation time and request are
frozen before sending, and the returned issue UID is bound immutably beside that
context. A lost reply or a future scheduled date leaves a parked local run.
Use `herdr-kata run list --state parked` and `herdr-kata run show <run-uid>` to
inspect it, then `herdr-kata run resume <run-uid>` to retry preparation for that
same prompt or workflow run. This command accepts only runs still awaiting issue
preparation; it does not restart completed work. Preparation-only rows stay local
until the child can start.
Older ordinary saved per-run contexts that lack frozen issue-creation intent
refuse preparation. Their recorded result, references, timestamps and retained
artifacts remain unchanged for inspection.

Kata's ordinary create idempotency has a seven-day lookback. If the creation
reply is still ambiguous after seven days, resume refuses another create. Inspect
the retained run's frozen context and the project's issues to reconcile whether
the original create succeeded. Keep those artifacts; a new `job run` is a separate
invocation that creates its own issue, including when its definition or occurrence
matches an earlier run, and must not be used as a blind retry. A raw local
reference never substitutes for the per-run policy. Future scheduled dates defer
the child; deadlines alone do not. Existing-issue jobs retain ordinary readiness.

`job add --id <retained-ulid>` saves a disabled native definition. Shared enabled
configuration never activates a newly joined installation. `native activate <job-uid>`
and `native deactivate <job-uid>` change only owner-only local `native-activation.json`
(v1, at most 262144 bytes); `job enable`/`job disable` and the board toggle use the
same local operation. Activation validates a fresh definition and mapped checkout.
The daemon runs locally active jobs only after a current ordinary refresh; offline
cache does not start new work. An explicit manual execution is opt-in for that run. Native raw definitions
can be saved using `native job save --uid <retained-ulid> --name <name> --file
portable-definition.json`; use `--expected-event-uid <winner>` to replace an
existing definition. The equivalent `native workflow` command accepts workflow documents.
All bodies retain exact JSON numbers and opaque options. A successful create retry
is recovered only by exact live name and whole-document readback at that same UID.
A stale update or tombstone requires refreshing and reviewing the winner.

`workflow new <draft-id>` and `workflow edit <native-uid>` create local unsaved YAML drafts.
`workflow save <draft-id>` retains its native UID and expected winner in a local
sidecar before the request; failures keep them for retry. `workflow show <native-uid>`
prints canonical JSON. Legacy SQLite/YAML remains adoption input rather than
another editable definition authority.

The cache is scoped to target, project UID, resource UID and winning event UID.
Refresh sees peer definitions. Offline cache reads are labeled, and cannot enable
activation. Saved writes always require the selected native daemon. This build
supports native read/save/TUI and explicit job execution through the ordinary
local runner. Two independent executions of one occurrence have separate run
UIDs. Persistent jobs reuse their live conversations; KeepContext=false clears
before the next prompt, while KeepContext=true preserves the session. No central
claim, issue reservation, per-process grant or forced persistent shutdown exists.

Every run saves an owner-only immutable local definition/input/routing snapshot.
Credentials are reloaded only for the recorded target. Initial/final bounded run
observations contain revision references and reported status, never local paths,
raw snapshots, logs, handles or credentials. Delivery failure keeps a local
pending write and unsent tail; it cannot block launching or the next prompt.
Authority-era journals remain unsupported for automatic recovery and are retained
for explicit operator inspection. Native direct-workflow execution and resume use the saved local snapshot, including
after shared edits or deletion. Resume keeps the original input/reference; start
a new run to change them. For a managed job, the actual linked issue UID is the
frozen runtime and run-row reference even when the original executor option was
empty or different. Saved job-workflow recovery follows that immutable issue UID;
it does not rewrite the snapshot or consult a later shared definition. A direct
workflow keeps its supplied frozen reference. Actual reference/input overrides still
require a new run. Ad-hoc prompt-only `run-once` writes a fresh canonical
local run/history/snapshot and has no shared cron observation without an
actual job/workflow revision pair. Local cron/interval/once/manual behavior follows
the existing scheduler. Cron/interval first runs use the server definition's
creation time, after any existing local-run anchor. A missing creation timestamp
holds the first scheduled run with a named diagnostic; malformed timestamps make
the refresh fail and retain the offline cache. Linked issue execution uses the
selected daemon's ordinary ready list, preserving scheduled_on, someday,
blocking, recurrence and daemon timezone behavior. Deadline alone never gates
readiness. Native issue-date execution uses the ordinary planning-date projection
described below. Custom notifications use the ordinary delivery bridge.

Older ordinary version1 job-workflow snapshots may retain the raw executor reference
in both context and run row. Resume verifies the supplied row against the stored
row and frozen input/reference, then records its already-frozen linked issue in
the local row without rewriting the context. Explicit overrides still require a
new run. If a job has no actual linked issue, its free-text reference stays in
the local row/runtime and never becomes a shared issue UID.

Every persistent native conversation requires compatible local run/tab provenance
and its immutable context, including when KeepContext is disabled or Herdr cannot
capture a harness session. Named reuse must match the recorded tab and agent;
a different tab is accepted only through verified restoration of that run's
captured harness session. An intentional harness resume may proceed with
unconfirmed optional session metadata after validating that capture; it retains
the ordinary “Herdr has not confirmed the session” note. Explicit mismatches and
adoption without matching session evidence still refuse. A refused fresh idle
resume closes only its newly created tab and clears rejected handles; existing
blocked/working conversations remain open. Its capturing run and immutable routing context remain
the session provenance even if a later attempt parks before capturing it again.
Reported workspace, harness and checkout must also
match. Refused replacements cannot become provenance. This uses existing local
handles, without pane or process-birth records; an indistinguishable replacement
reporting those same handles cannot be detected when session metadata is absent.
Missing provenance refuses live reuse for inspection.

When an editor saves a definition but its following local activation fails, it
reports “definition saved; local activation failed” and keeps the accepted revision.
After correcting the local configuration, Save retries activation without creating
another definition revision. The enabled intent stays in the form until satisfied.
Concurrent execution/resume of the same saved run UID returns already-running via
its local `execution.lock`; independently allocated run UIDs remain independent.
Native workflows retain the existing dedicated workspace, parked artifact tab and
saved-workspace resume/cleanup behavior. Any actual one-shot outcome deactivates
only local scheduling; explicit manual execution remains available.

Ordinary shared run observations replay after restart with bounded retry backoff.
A remote write never holds the local execution queue lock; pending data stays
frozen across reply loss. Local history recovery visits at most 100 run directories
per tick and rotates its position across restarts. It checks local run changes and
buffer metadata before rebuilding evidence, so unchanged recovered runs do not
reacquire result-comment locks. Directory catalogue enumeration still scales with
retained history. Owner-local `native-recovered.json` files record repair progress
and failures; unchanged corrupt inputs retry after a minute without repeating the
same diagnostic. Changed local inputs trigger repair immediately when selected.
Saved routing and identity failures remain failures on every selected drain.
Changing the configured executor display label invalidates cached repair progress;
the saved context is checked again and recovered evidence keeps that run's original
executor label. Target, project, actor and teammate changes still fail routing checks.

Accepted ordinary notifications have a separate bounded, rotating local settlement
drain. An unavailable earlier run cannot indefinitely hide a later accepted run.
Recovery updates local history and buffers evidence; it does not resubmit or clear
the recipient handler's request.

Unsent parked-run attention cancels when a scoped read confirms the linked issue
is closed or returns an explicit deletion timestamp. The current public `show`
command hides deleted issues and uses the same missing-issue error for a foreign
project UID. That ambiguous error, transport/authentication failures, and identity
mismatches keep attention pending with a diagnostic for retry or operator
inspection. An actually deleted issue can therefore retain pending attention;
this client cannot distinguish that case using the current scoped public read.

A native row with executor options this plugin cannot project is named by UID
with a diagnostic. Supported rows and the board's other tabs remain available;
the raw whole documents stay in the cache. Inspect an unprojectable job with
`native job show <uid>` or a workflow with `workflow show <uid>`.
The daemon also names local activation read failures and holds affected jobs;
healthy jobs continue scheduling. Repair the local activation file before
retrying an activation that reports a file or version error.

A remote refresh timeout can still display the labeled offline cache. Explicit
caller cancellation stops the request. The deadline fallback has a separate
250ms local-cache budget; board local tabs have a separate 1s budget after the
remote refresh's 5s budget. None of these cache reads allows activation.

The installed TUI inherits benign editor and terminal preferences, including
VISUAL and EDITOR, while ambient Kata targets, tokens and proxies remain
scrubbed. Windows environment-key casing is supported. Windows CLI arguments
must be valid UTF-8, including workspace and positional path arguments; malformed
or WTF-8 argument values are rejected. Byte-preserving non-NUL argument data
remains supported on Unix. Stdin bodies and exact JSON numbers are unchanged.

The board reads the newest100 ordinary shared run observations using the public
`cron run list` envelope and cursor. Peer rows are marked reported and show
actor, teammate, executor and bounded summary in run details. A matching local
run UID keeps its local outcome, artifacts and session handles; shared evidence
never creates a local run journal or imports handles. Failed reads retain only
the already-loaded, target-scoped bounded view, labelled offline. **i** opens the
selected run's real issue through the installed Kata TUI; **K** still opens the
project TUI. Agent attachment is available only through local run handles.

Portable cron expressions honor their configured trigger timezone, including
ordinary cron DST fold/gap behavior, without altering the saved expression or
native issue readiness. Scheduled cron/interval/once fires retain their exact
UTC fire time in the existing immutable local context and reduced observation.
Locally activated `issue-scheduled` jobs read Kata's ordinary planning-date
projection and fire from its resolved UTC instant. Native issue, recurrence,
daemon-default and UTC timezone rules remain intact; a trigger timezone does
not retime the issue. Saved local source identity suppresses unchanged tick
repeats without treating unrelated issue edits as another occurrence. Moving
or clearing a date invalidates queued work before launch. Ordinary readiness
still defers future schedules and someday issues; deadlines never gate execute
jobs.

Deadline lead times subtract a nonnegative number of seconds from the native
instant, rejecting duration or timestamp overflow. Deadline actions are
notification-only. Notify jobs need no process checkout. Custom offsets and exact
recipients use ordinary Kata notifications and wait behind an occupied recipient
slot. Source-date changes cancel unsent attention after a fresh scoped check.
The same-source, zero-lead current-owner-or-author alias belongs to Kata's existing
default-date sweeper; Herdr Kata does not send a second copy. Deadline notifications
do not defer execution readiness.


## Explicit snapshot adoption

First make a stopped, checkpointed backup of the source installation. Keep the
original SQLite database and `flows/*.yml` together in a separate snapshot
directory without renaming them. Multiple recognized databases in one snapshot
are rejected; select one complete stopped backup rather than merging them.
Workflow-only snapshots are supported when neither database is present and valid workflow
YAML exists; an empty directory or unrecognized source fails instead of reporting success.
A nonempty SQLite WAL is rejected: take a SQLite backup or checkpoint the stopped
source instead of dropping the WAL. The importer opens SQLite with read-only,
immutable options and never runs source migrations, backfills or permission changes.
Do not modify the snapshot while importing it.

```bash
herdr-kata native import --source /path/to/upstream-snapshot \
  --source-id example-installation --checkout-key primary \
  --cron-timezone Europe/London
```

Retain `source-id` on every retry and after moving the snapshot. It identifies the
installation, not its pathname or file contents. The namespace, resource kind
and original job/workflow ID produce stable native ULIDs. The importer validates the
whole snapshot before saving workflows followed by jobs through ordinary native CRUD.
If interrupted after one save, retrying creates the remaining definitions and
reads back already accepted identical definitions without updating their winners.
Changed content or tombstones conflict at the retained UID; inspect the current
native winner and use ordinary explicit CAS editing. A different source ID denotes
a different import and may create duplicates. Import completion is not atomic
across definitions; an error names the definition that stopped the batch.

Imported jobs carry `enabled: false` and no new local activation. Reimporting
never changes an activation you subsequently chose. All source checkouts are
replaced by the supplied portable checkout key. Review its local mapping before
activation; additional source directories are refused instead of exported.
Cron jobs require an explicit IANA timezone, because an upstream machine's implicit
local timezone is not portable. Review the resulting schedule before activating.
Prompts and shell commands remain literal data; recognized obsolete issue/forum/
memory command references receive review warnings, not automatic rewrites. Those
warnings are a convenience, not a complete prompt audit.

The importer reads only job definitions and workflow YAML. It does not read or adopt
obsolete issue/forum/memory records, run history, resource leases, PIDs, tabs,
sessions, credentials, or execution journals. Arbitrary paths or secrets embedded
inside prompt text are not scrubbed: review the source before sharing definitions.
Workflows become native documents with embedded portable steps and no source-file
pointer. No source file is used as an execution authority afterward.

## Results, attention and exact inboxes

Shared run evidence is bounded and attributed to the ordinary actor/teammate;
independent duplicate executions retain separate run UIDs. Local raw prompts,
transcripts, snapshots, process handles, credentials and buffers remain owner-local.
Logging before, during or after execution is ordinary buffered bookkeeping and
never a launch or persistent-session gate. Restart replay retains the frozen
pending observation, coalesces only its unsent tail, and uses ordinary revision
checks after acknowledgement. `doctor` shows pending and failed delivery.

Settled results also queue ordinary issue comments. Retries retain their exact
body and idempotency key for seven days. Older retries first reconcile the scoped
comment marker; outage or ambiguity retains the buffer for inspection. Parked
runs can separately request needs-human attention. Resuming cancels obsolete
unsent attention and preserves historical comments.

```bash
herdr-kata inbox list --for worker/adapter
herdr-kata inbox open --for worker/adapter --ref <issue-ref>
herdr-kata teammate connect --for worker/adapter \
  --workspace <workspace-id> --pane <pane-id> --conversation <session-id>
herdr-kata inbox deliver --for worker/adapter
kata inbox --for worker/adapter
# After handling the request, the recipient handler clears and reads it back.
kata notify <issue-ref> --to worker/adapter --clear
kata inbox --for worker/adapter
```

Each exact address has its own inbox. Reading `worker` never aggregates
`worker/*`, and reads or delivery do not clear requests. Native attention is a
replaceable slot per issue/recipient, so concurrent replacement/clear can race.
The local bridge retains busy or unavailable attention and validates the exact
runtime conversation, pane and workspace. Generic Herdr currently requires
manual wake for an idle or completed conversation with safe input; it never
automatically types a request into an agent. Working, blocked, or drafted input
keeps attention pending. Capability-
bearing transports must atomically guard idle state, conversation and draft at
submission. Disconnect stale registrations with `teammate disconnect --for ...`;
registration and wake coalescing survive a local restart.

Back up Kata's shared state with its ordinary backup workflow. Separately retain
this installation's mappings, activation, local journals, drafts and buffered
writes, using SQLite backup facilities or stopped writers for local databases.
A shared backup does not contain these local runtime artifacts or make another
installation active. Federation and backup retain ordinary project, actor,
credential-origin and role protections; cron evidence adds no authority.


## Workflow naming compatibility

The canonical command is `herdr-kata workflow`. Jobs use `--workflow`, draft
files live in `drafts/workflows`, native definitions use `kata cron workflow`,
and job/run references use `workflow_uid` and `workflow_definition_event_uid`.
The retired `flow` command and flag have no compatibility aliases.

Local SQLite job and run columns are `workflow_id` and `workflow_input`.
An installation with the older `flow_id` or `flow_input` columns is refused
before replacement columns are added. Retain that installation and its matching
binary; select a fresh `HERDR_KATA_HOME` for this version. Explicit upstream
snapshot import still reads historical `flows/*.yml` files and old database
columns, preserving deterministic imported identities. It imports definitions,
not execution history, lease state, or resumable runtime snapshots.

JSON/YAML workflow keys, hook payloads, runner snapshots, and generated workspace
labels use the new spelling too. Older runtime files and pending hook payloads
are not converted automatically. Resource leases keep their existing
`lease claim`, `renew`, `release`, and `list` commands and holder/expiry rules.
