---
title: Kata adapter contract
description: Normative scheduling, admission, transport and native-child rules for the current adapter.
last_edited: 2026-10-20
---

# Kata adapter contract

herdr-kata implements Kata's
[cron adapter contract](https://github.com/kenn-io/kata/blob/main/docs/reference/cron.md).
Kata owns definitions, issues, readiness, native delivery and run evidence.
The runtime owns adapter scheduling, local execution and reporting. These
rules describe the current implementation on `main`.

## Ownership and dispatch

The adapter schedules enabled `execute` jobs and `notify` jobs on existing
issues. Kata owns per-run notify delivery; the adapter never schedules it.
Invalid or unsupported definitions are set aside individually. The
[definition reference](definitions.md) owns trigger grammar, field support,
catch-up policies and bounds.

Checkpoints atomically persist handled-through and pending occurrences.
History never advances a checkpoint. Definition changes supersede unadmitted
pending work; prepared journals retain frozen inputs. Admission refreshes
adapter definitions. Existing execute targets and native children must be
open and ready; readiness waits take no slot. Notify jobs have no readiness
requirement. All execution paths share the manager's capacity.

## Identity and admission

Occurrence keys are canonical UTC RFC3339Nano, or `manual:<run UID>`.
Scheduled run UIDs are ULIDs with occurrence milliseconds and the first ten
bytes of SHA-256 over `herdr-kata/run/v1\0` plus
`<job UID>\0<occurrence key>`. Native children use the child UID as the hash
source. Manual UIDs are allocated once per durable request.

Before preparing a UID without a valid local journal, the adapter checks
`cron run show` at the pinned authority. Existing evidence or a quarantine
entry vetoes replacement execution. In the current implementation, only a
`not_found` response permits preparation; other read failures leave work pending.

For a fresh execute/per-run preparation with `options.run_parent`, read the
parent after the authority check and before writing the journal. An open parent
must have the requested UID and pinned project UID. A closed parent skips
`parent_closed`. A parent in another project returns 404 through the pinned
Kata route, even when readable in its own project. The adapter reports
`parent_unavailable`, runs the access probe and leaves the occurrence pending;
a 404 never authorizes a skip. `parent_outside_project` is a skip only when
Kata returns the parent's project UID and it differs from the pinned project.
Other read failures also leave the occurrence pending.

The saved journal precedes all admission side effects. The order is:

1. Prepare frozen definitions, target, issue policy, mappings, input, expiry
   and a random per-journal `journal_id`.
2. Resolve an adapter per-run issue with its keyed create. This is the only
   write before admission. New preparations freeze `--label herdr-kata:run`
   and optional `--parent <UID>` into the create. Existing adapter issues and native children already
   have their issue UID; neither is mutated yet.
3. Send the frozen admission observation with expected revision `0`, status
   `running` and summary `{version:1, message:"admitted <journal_id>"}`.
   It has no start time and stays identical on every retry from this journal.
4. Save successful admission and append child claim/tracking and any configured
   deadline in the same transition. Finish that tail before launching work.
5. Execute.

The observation is the atomic existence fence. A created reply or this journal's
exact replay authorizes execution. A conflict is never read back as admission:
it holds `admission_rejected`, releases the execution slot and overlap gate, and leaves any
created issue for the operator. A `not_found` stays uncertain and prepared;
the identical observation retries without execution.

The queue fences every other issue mutation until durable admission is proven,
including before startup recovery and after a pre-admission hold. The
[state reference](state.md#durable-admission-and-signing-evidence) owns the
saved proof fields; [Recovery](../operations/recovery.md) owns local resolution.

A create uses `herdr-kata:<run UID>` and `herdr_kata.run=<run UID>`.
A look-alike conflict is read back: a matching run marker identifies the
issue; otherwise one separate frozen `--force-new` request is allowed.
Parent and labels remain identical on that retry. Recovery never rechecks the
parent or rewrites an older journal's parentless, unlabeled request. Kata resolves
the parent before its key lookup: a closed parent permits keyed replay, but an
invisible or unresolvable parent gives an uncertain 404 even if the child exists.
Such creates retry identically, with inspection after the seven-day window.
Signing refusals use the shared durable retry rules below. Fresh-create refusals
`link_target_archived`, `claim_denied` and federated cross-project boundaries
require inspection. No retry removes the parent or uses force-new for a 404.
A fatal pre-admission create holds `create_rejected` with its refusal detail,
returns its slot and gate, and never retries automatically. Release or offline
abandon resolves it without post-admission Kata writes.
Executor labels must be unique and stable.

The worker is the journal's single writer. Every transition serializes,
reads current state and persists execution changes with their ordered mutation
intents. [Run lifecycle](../design/run-lifecycle.md) owns submission and
occupant proof; [Reporting](reporting.md) owns outcomes and issue settlement.

## Lane ownership

Automatic adapter scheduling follows the job's original author, as described
in [Schedule jobs](../guide/jobs.md#choose-when-it-runs). Author provenance
decides only which jobs a lane schedules; the lane lease below decides which
installation runs the lane.
Status keeps these jobs supported for manual runs, shows an automatic-scheduling
warning when the author differs from the lane actor, and omits their next fire.

Each project has one lane lease in Kata: the `herdr_kata.lease` metadata of
the project's oldest issue labelled `herdr-kata:lease`, kept closed so it
stays out of ready lists. The closed issue and its lease metadata remain
visible to everyone who can see that project, including a shared project.
The lease is `{version:1, owner, generation,
heartbeat}`, where `owner` is the installation's `executor`. Every lease write
is conditional on the issue revision it read (`meta set --if-match`), so two
installations never both hold a lane. A project's first lane start creates
and closes the issue; if two installations create one at once, both use the
oldest.

When a lane is removed from config, worker startup retires it on its saved
pinned route. It waits for configured projects to resolve, then compares
project UIDs so a rename cannot retire a configured lane. It checks the
original route before changing journals or releasing the lease. It
conditionally clears only this installation's lease owner,
preserving the generation, without creating or claiming a lease. Its queue can
finish saved reports; it has no scheduler, definition refresh or event stream.
Unfinished admitted runs are handed off with their known outcome, otherwise
`unknown`. Unconfirmed admissions never execute. Journals and snapshots stay
on disk; status and doctor omit lanes absent from config. See
[Remove a project](../guide/multiple-projects.md#remove-a-project).

A lane reads its lease once, after its route check and before it lists
definitions, opens its event stream or admits anything:

- No lease, an expired lease, or this installation's own lease: the lane
  claims it and starts. An expired lease takes the next generation.
- Another installation's live lease: the lane refuses at once and shows
  `another installation (<executor>) owns this lane` in status. It re-reads
  the lease 3–5 minutes later, then at most every five minutes and a jittered
  minute, each time after a route check.
- An unreadable lease is unknown, not a refusal: the lane admits nothing and
  retries after a limit of 10 seconds that doubles to five minutes, each
  wait a random time between half and all of the limit (for example 6, 15
  and 34 seconds).

An owner renews its lease every three minutes and immediately before every
admission, scheduled, manual or inbox. A lease not renewed for ten minutes
has expired: its owner is presumed gone, and a normal start may take it. A
lease metadata change newer than this installation's last lease write
prompts an immediate read; its own writes, and a replay of the lease issue's
history to a new stream, do not.

Each run records the lease generation it was prepared under. Its issue
creation and admission observation go out only while the lane still holds
that generation; otherwise the run is held `admission_rejected` before Kata
sees it. Once admitted, a run belongs to the installation that admitted it.

`up --takeover` stops this installation's worker, then writes the next
generation of each lane's lease for this installation on a checked route that
matches any saved pin, then starts. The former owner sees the higher
generation at its next renewal, admission or lease event, stops admitting and
leaves the lane: it stops its scheduler, inbox scanner and event stream, and
waits as a refused lane. It keeps supervising the runs it already admitted
and settles each with its real result: report, evidence, comments and close.
The new owner leaves those runs alone. A run the former owner had prepared
but not yet admitted is held instead. After a restart, a prepared run waits
until the lane's first claim in the new process: it proceeds when the lane
holds the generation it was prepared under, and is held otherwise. Limit: a former runtime that restarts
before its runs settle recovers them only once it holds the lane again, so
let its runs finish, or resolve them, before restarting it.

Plain `up` reads each lease and refuses a lane another installation holds
live, naming the owner and the remedy `up --takeover`. Doctor reports the
same read as `lane lease`.

While a lane waits as a refused lane, its installation refuses `run`,
`resume` and `release` for it at once with `another installation (<executor>)
owns this lane: lease generation <n>`, the wording of `up`'s refusal, and
reason `lease_held_elsewhere` in the request result. A run request queued
before the takeover, waiting for a slot or an admission, settles the same way
and is never admitted there. `abandon` reads the lease before its Kata write
and refuses on another installation's live lease. The refusals end when the
lane claims its lease again.

Kata's exact admission receipt still decides each run. If Kata rejects an
admission, the run is held `admission_rejected` and never executes; when a
read names another executor, the inspection says `another installation
(<executor>) admitted this occurrence`. The lease, not that rejection, decides
which installation serves the lane.

## Transport and events

The runtime pins identity and resolved routing as specified in
[Configuration](configuration.md#pinned-route). Checks precede admission and
write batches, stream start/reset and slow reconciliation. The daemon must be
ready, capabilities must report the pinned project UID, and the instance UID
must match the pin. Mismatch refuses admission and queued mutations;
a configuration edit cannot retarget an existing journal.

The worker allows two project lane startups at once, through recovery and
the first definition refresh. Failed startup attempts release their slot
before waiting to retry and reacquire it before checking the route again.
A healthy lane start makes one route check and one job and workflow listing.
The route check that chose the lane also pins it. Recovery, the inbox
capability read and the scheduler's first start reuse that passing check
for at most five seconds; a refusal, an older check or any retry checks the
live route. The scheduler loads the rows of the latest definition refresh
instead of listing them again.
Route discovery, guard checks and access probes share a process-wide limit
of two concurrent probe attempts. Each attempt
has a 60-second deadline starting after it acquires a slot; an earlier
caller deadline still applies while queued and during the attempt.
Only deadline failures retry: at most three attempts, with one-second then
two-second backoff. Slots are released during probe backoff. A recovered
deadline does not publish access loss; exhausted retries fence the lane.
Other failures, including `not_found` and authentication refusal, fence
immediately. A retry must still pass every identity and pin check. This
policy retries read-only probes and never retries a mutation.

Periodic route checks wait a fresh random interval of 30–45 seconds after
startup and after each passing check. An allowed lane with no job definitions
has nothing to admit and waits 2.5–5 minutes instead; its event stream stays
open, and the first job definition it refreshes restores the 30–45 second
cadence at once. A refused lane, from access loss,
exhausted deadlines, signing or a pin mismatch, waits 30–60 seconds instead,
and the limit doubles after each refused check to a five-minute cap. Every
backoff in this section waits a random time between half and all of its
limit, so lanes refused together do not probe together. A lane whose lease
another installation holds does not run its route timer; see
[lane ownership](#lane-ownership). Only a refused lane's
timed check probes its route: stream resets and restarted lane work wait for
it. Failed startup attempts and restarted lane work back off from 1–2
seconds to the same cap. Queueing and probe work can delay the next check
further.

One runtime sends at most four one-shot Kata calls at once, across all lanes,
reads and writes alike. Event streams take no slot. A queued call has not
reached Kata: its one-minute deadline starts when it takes a slot, and a call
cancelled while queued is never sent. Waiting for a call slot counts against
a probe attempt's deadline. These limits and the probe deadline are internal
constants, with no configuration keys.

Every route must use Kata 0.19.0 or later.
Runtime checks require storage `schema_version >= 33`; `doctor` also prints
the daemon version. Schema alone cannot identify a build's contract, so CI
verifies the released test archive against its pinned SHA-256. The configured route is trusted:
remote diagnosis reporting `project_unverifiable` and spoke replicas are
accepted. A known federation role is informational in `doctor`. Conflict detection reads the configured authority; independently writable replicas
may hide a competing installation. See the
[trust model](../design/trust-model.md#routes-are-trusted-as-configured).

Kata one-shot calls use the configured CLI without a shell. On macOS, event
streams use a fixed wrapper that preserves the CLI arguments and stops them
when their worker exits. The watchdog closes its output descriptors so a lost
wrapper and exited tail still produce EOF and allow the stream to restart.
The [configuration reference](configuration.md#kata)
owns argv, environment and timeouts. Herdr calls use its native socket API;
no Herdr CLI is forked.

One `events --tail` stream per project starts before definition snapshots.
Reset and event lines, including their cursor updates, are handled in stream
order. Only
`cron.job.*` and `cron.workflow.*` dirty definitions. Issue events wake readiness
and inbox work, including `issue.scheduled_reached` from Kata builds that emit
it. Stream start/reset requests a full refresh and pending
recheck, and a route check when the lane is allowed. A new stream's start
refresh waits up to one second for the stream's first line, then until the
stream has been quiet for 250 ms, at most two seconds from its start or 1000
lines. Lines received before that refresh are covered by it: a reset, as
Kata sends for a stale cursor, or replayed `cron.job.*` and `cron.workflow.*`
events, as Kata 0.19.0 sends to a stream from event 0, add no refresh of their
own. Issue events among them follow the refresh, and the cursor advances
past them only then. Definition events after the refresh dirty definitions
as usual. A dropped stream
reconnects after 0.5–1 seconds, doubling to the five-minute cap; a stream
that delivered an event or stayed open five minutes resets that backoff. Dirty flags survive refreshes; the cursor advances only after
handling or marking work dirty and is never admission evidence.
Pending definition refreshes also survive route refusal and failed reads. The
next passing route check retries them. New scheduler admissions wait until the
scheduler has applied that refresh.
Runs already frozen in their journals keep their original definitions.

Definitions have a fifteen-to-eighteen-minute fallback. Route, readiness,
held capacity and inbox state have ten-to-twelve-minute fallback wakes. Each
lane picks its own jittered delay, so lanes started together do not check
together. Inbox scans run on inbox events, stream start/reset, freed capacity,
local control requests and the fallback; an issue event also scans while a
request waits on its child. Scans debounce for two seconds. The worker's
five-second control tick reads only local request files. The TUI tick reads
local snapshots and never polls Kata.

## Native children

`cron_v1` is the single complete Kata cron capability: portable definitions,
run history and native recurring children. Pickup requires
`cron_v1` and a configured `kata.inbox`; without either, scanning stays inactive.
The scanner reads inbox requests, the project ready set and requested issues.
A child must be
open, ready, in the configured project, linked to a parent, and carry version
1 `cron.occurrence` with a delivery job UID, historical definition-event UID
and valid scheduled instant. Inbox prose alone never authorizes execution.

The [parent binding](definitions.md#native-parent-bindings) is resolved and
frozen at admission. Invalid bindings and foreign owners are unsupported.
The child path skips adapter ownership classification, notification dispatch
and issue creation. It observes the historical delivery pair from the child
and the frozen bound workflow pair, rather than the delivery job's current definition.

After admission, an unowned child is claimed with `--if-unowned`; an actor-owned child is taken
over directly. Tracking writes use the saved revision. Later ownership changes
or child attention conflicts stop attention writes. The recurring parent is
never mutated.

After the final observation, acknowledgement unsets only the runtime's own
`notify.<base64url(inbox)>` key. It uses the saved notification JSON as
`--if-value`, `--json-value` and a freshly frozen `--if-match` revision. Retries
retain that guard; conflict read-back leaves a changed request alone. Delivery
is at least once: an identical replacement has no distinct request identity.

`inbox.json` is only a bounded read model. An `accepted` row is eligible for
admission, not proof that execution started. Failed scans retain previous
evidence. Journals remain the admission/execution record.

When upstream capability consolidation becomes available, upgrade Kata,
connected peers and adapters together. There is no compatibility alias.

## Explicit issue requests

An ordinary open child can opt into automatic execution with
`herdr_kata.request` JSON: `{version:1, kind:"execute", request_uid,
requested_at}`. The request UID is a canonical ULID; the timestamp is UTC
RFC3339Nano. The child also needs a notification in the configured runtime
inbox. Issue events and notifications wake discovery; their text never
authorizes execution.

For a request child that is not ready and has `scheduled_on`, the scanner
reads the instant with `kata show --planning-dates --json` once per child
revision and `scheduled_on` value, and remembers it while the child waits.
Later scans reuse it, so waiting children add no date reads. A future instant
shows as
`waiting-ready` with `scheduled for <instant>`, and the scanner wakes one
second after the earliest such instant; nothing is prepared and no slot is
held before then. Kata's ready set remains the gate, so the child never runs
before its date. A reschedule changes the child's revision and value, so the
next scan re-reads the date, replaces the wake, and the old date never fires. After admission the permanent receipt
below means a later reschedule never runs the child again. An unreadable
date is retried on the next scan and otherwise falls back to issue events and
the ten-minute wake.

Children carrying `cron.occurrence` cannot enter this explicit path. Native
pickup also rejects explicit request/admission markers, including after the
request is removed. Discovery, fresh native preparation and queued native
admission enforce that boundary; never remove provenance or admission markers
to change a child's execution source.

The scanner and admission path validate the pinned project, parent, open
status, readiness, owner and existing parent profile/checkout/workflow binding.
An unowned child is conditionally claimed after admission. Foreign owners and
unsupported bindings refuse execution. Preparation freezes the request,
parent, binding, input, notification, ownership and random journal ID locally.
Readiness, parent and child identity are rechecked before the admission CAS.
Readiness transport failures use the existing read backoff and leave an unsent
journal prepared; cancellation also leaves it recoverable. A successful
readiness response that excludes the child is an admission verdict, not a
transport failure.

Inbox classification uses the lane's established route evidence and the local
pin. Guards without cached route evidence perform a live classification check.
Admission and acknowledgement writes perform fresh route checks. A queued
write counts the lane's own passing route check as fresh for five seconds, so
the writes of one run share a check instead of probing before each one. Any
failed route check or access probe on the lane ends that reuse at once, and
the next write probes again. Starting a run always checks the route itself.
The window is an internal constant, with no configuration key.

`herdr_kata.admission` is the permanent existence fence:
`{version:1, request_uid, run_uid, journal_id, executor:{instance_uid,
project_uid, actor, executor_label}}`. The queue writes it as JSON using
`--if-absent` and the saved validated child revision. A successful CAS or exact
receipt read-back from this journal authorizes execution. A foreign receipt,
changed child or unconfirmed reply holds admission. Restart reads back a
possibly sent CAS instead of sending it again. If the first read finds no
receipt, later inbox scans and restarts may confirm the exact frozen receipt
and continue the original journal. An absent or foreign receipt keeps the hold;
the runtime never resends the ambiguous CAS. A definitive signing refusal
keeps the existing identical-write retry rule.

One child UID can have at most one automatic admission, even if its request is
edited. Receipts are never cleared, expired or taken over. A receipt without
its matching local journal vetoes reconstruction, including when its executor
label matches this installation. Such a request is shown as `ignored` with a
local-state-loss reason and an `abandon` command. Foreign or malformed receipts,
completed requests and replacement requests for an admitted child are
`unsupported`, with an admission-veto reason. Preparation-rejected holds also
refuse permanently, including readiness, parent and changed-request refusals;
see [Pre-admission holds](holds.md#hold-reasons). These permanent refusals
acknowledge only the saved matching runtime notification through the guarded
write queue; human and changed runtime notifications survive. Invalid
`herdr_kata.request` metadata is `ignored` by the scanner before dispatch,
without acknowledging the notification. A pending
acknowledgement without its original inbox notification needs
[report inspection](../operations/recovery.md#inspect-a-pending-request-acknowledgement).
A request awaiting
late admission confirmation remains `ignored` with its hold reason until exact
receipt proof arrives.

Request execution shares capacity, prompt
barriers, recovery and result handling with other runs, but never reads or
writes cron run history. [Reporting](reporting.md#explicit-request-outcomes)
owns its outcome metadata.

Competing workers must use the same authoritative Kata service. CAS on
independent writable replicas cannot provide global exclusion; existing route
guards trust the configured route and do not establish this topology.

## Write queue

All run mutations use one bounded queue with per-run order and at most two
CLI writes in flight. Issue writes serialize by project UID and issue reference. Exact payloads,
keys and guards are saved before sending; retries never regenerate them.

| Safety class | Operations | Lost-reply behavior |
|---|---|---|
| Replay-safe | Observations; keyed create, comment, close within seven days. | Retry identical intent; resolve conflicts by the supported read-back rules. |
| Guarded | Metadata set/unset and deadline. | Retain saved revision/value guards; read back conflicts, otherwise inspect. |
| Read-back | Claim. | Confirm the intended owner after uncertainty; otherwise inspect. |
| Unguarded | Notify and label add. | Confirm the effect by reading; never blind-retry. |

Final cron observations have their own revision order and may pass pending or
inspected issue mutations once execution has ended. They still use the pinned
route guard and the shared two-write bound. Issue mutations retain their order.
See [Reporting](reporting.md) for startup read-back recovery.

Each issue guard freezes only after preceding issue operations resolve.
Identical adjacent attention intents coalesce. Backoff starts at two seconds and doubles to five
minutes. While a lane's route refuses delivery, its queue re-checks the route
with the jittered lane backoff above, from 1–2 seconds to the cap, instead of
on every pass. Auth/validation refusals, unconfirmed conflicts and replay-safe
operations still unresolved after seven days require inspection. Close-validation
refusals have the [review fallback](reporting.md#close-or-review).

## Access loss and root signing

An access-loss `not_found` is indistinguishable from a missing resource. Dropping
pending jobs or marking a binding unsupported requires a complete, successful
job or workflow list that omits its UID. A passing capabilities probe cannot
prove an adjacent 404 is genuine. Without proof, work stays pending; a failed
probe, after the deadline retries above, marks the route `access_lost` and
blocks admission and report delivery
until a passing route check. The runtime and TUI binding probes use the same
guarded route.

A write's `not_found` stays uncertain and follows its saved safety class.
It is never a successful write or proof that an issue disappeared. An unresolved
404 can reach inspection through the seven-day horizon; access loss alone never
completes, drops or settles work. Queue shutdown cancels an in-flight access probe.

A `root_signing_key_unavailable` refusal retries the identical write without
read-back. The operation saves `signing_refused`, which survives later errors
and restarts until that exact operation succeeds or its whole run is quarantined.
This pauses new admission while delivery continues, including signing refusals
in lost-state abandonment reports. Retry `abandon` to finish those reports after
repair; their journals never become executable. An owner diagnosis listing
the project under `root_signing` also blocks delivery until repair; a later
passing diagnosis clears only that diagnosis's block. Only the affected project pauses; other lanes continue. The root owner repairs
signing with `kata federation root-key repin --project <name>`.

A later semantic conflict can retire an intent without delivering it. A
successful replacement cannot clear that original signing evidence; see the
[offline resolution](../operations/recovery.md#quarantine-an-admission-or-signing-hold).

## Maker/checker loop and findings

[Job and workflow fields](definitions.md#makerchecker-loop) owns the loop edge
and its rewind rules; [Finding comments](reporting.md#findings-comments) owns
the finding chain, typed replies and assertions.

## Limits

Recovery reconciles ordinary journals before dispatch, never repeats a possibly
accepted prompt, and never replaces execution whose local state was lost.
Shutdown leaves agents running. The supervisor restarts workers, but its own
death requires another `up`. Other current limits live in
[Definitions](definitions.md#limits), the [agent contract](agent-contract.md#limits),
[State](state.md#retention-and-limits) and
[Hold reasons and resume rules](holds.md).
