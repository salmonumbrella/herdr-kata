---
title: What herdr-kata writes to Kata
description: Run observations, issue outcomes, closure evidence, attention and mutation counts.
last_edited: 2026-10-20
---

# What herdr-kata writes to Kata

Kata stores run evidence and issue state. The runtime reports the frozen
job/workflow identity and local outcome; a terminal observation in Kata
never advances local execution.

Final cron observations do not wait for issue closure, attention or other
issue mutations. They retain revision order, including pending resume and
handoff observations. An inspected issue write stays inspected after the
run observation lands.

Worker startup reconsiders a final observation inspected as `operation effect
not confirmed`. It checks the pinned route and reads the run back first. An
exact delivered result is acknowledged locally. A matching run still `running`
at the frozen expected revision permits an identical replay-safe retry. A
changed identity, different final result, advanced revision or signing refusal
stays inspected. No execution is repeated.

## Outcomes

| Local outcome | Final run observation | Runtime-owned per-run issue or child | Existing adapter issue |
|---|---|---|---|
| Success with usable closure evidence | `succeeded` | Close `done` or `audit-no-change`. | Comment; none for a command-only run (quiet success). |
| Success without usable closure evidence | `succeeded` | Comment, label `needs-review`, attention `needs-human`. | Comment. |
| Failure | `failed` | Comment, attention `stuck`. | Comment. |
| Held step failure or loop (`step_failed`, `loop_pending`, `loop_exhausted`, `loop_stuck`) | `failed` | Comment, attention `stuck`; keep the overlap gate. | Comment; keep the overlap gate. |
| Uncertainty | `unknown` | Comment, attention `needs-human`, notify. | Comment, notify. |
| Agent `needs-human` or operator release | `unknown`, or an already known outcome preserved by release | Comment, attention `needs-human`, notify; keep any existing tab. | Comment, notify. |

An admission observation is `running` with summary version 1 and message
`admitted <journal_id>` as defined by [admission](kata-adapter.md#identity-and-admission).
Final observations include issue UID, actor, teammate
`herdr-kata`, executor label, occurrence key, frozen definition-event pairs,
start/end times and summary. Summary text is capped to a 60 KiB serialized
text budget to fit Kata's 64 KiB envelope. A failure or uncertainty reason
is retained at the beginning when needed.

Final and held observations also fill the summary's `input_tokens` and
`output_tokens` from the run's saved usage totals, the same counts run detail
shows ([TUI reference](tui.md#run-evidence)). `input_tokens` counts every prompt token:
uncached input, cache reads and cache writes. Both fields stay empty when no
attempt had usage data, when any collected attempt had none, or after a
handoff. Kata stores them as given; a later observation, such as a resumed
run's final one, carries the new run total. A held agent that keeps working
after its attempt was read is not read again, so its later tokens are not
counted.

Quiet success keeps existing issue threads for runs that need a reader. A
settled run on an existing adapter issue posts no comment when every step is
a command, every command exited 0, the run was not handed off, it is not a
resumed generation and it has not commented on the issue. Its final
observation still carries the command receipt message. Prompt and mixed
workflows still comment with the agent's summary. A resumed success, or a
looped run that published findings, still comments to close the story on the
thread. This is the default and has no option.

After a child's final observation, the runtime conditionally acknowledges
its saved inbox request. See the [adapter contract](kata-adapter.md#native-children).

## Explicit request outcomes

Issue-request runs use `herdr_kata.outcome` instead of cron observations:
`{version:1, request_uid, run_uid, journal_id, phase, outcome, reason, summary,
started_at?, ended_at?}`. Phase and outcome retain the existing local meanings;
summary retains the existing 60 KiB serialized text bound. Payloads are frozen
in the ordered queue, including explicit resume generations.

Before reporting, the runtime verifies the exact admission receipt, pinned
issue/project and expected owner. It freezes a fresh revision plus
`--if-absent` for the first outcome, or `--if-value` matching the preceding
delivered runtime outcome. A changed value requires inspection. Ambiguous
delivery reads back the exact payload and receipt; it never blindly replaces
metadata.

Settlement/finding comment keys, conditional tracking, closure evidence and
human handoff reuse the child rules above. `needs-human` leaves the child open
as `handed_off`/`unknown`; automatic execution never resumes. After outcome
reporting, acknowledgement unsets only the saved matching runtime notification.
Changed runtime notifications and notifications for human recipients survive.

Explicit abandonment of a lost request journal freezes a reporting-only batch.
The exact admission receipt supplies the request, run and journal identities;
execution state is never reconstructed. The outcome is `settled` / `unknown`
with reason and summary `local state lost`. An existing outcome must match the
same receipt and is replaced only with `--if-value`; a first outcome uses
`--if-absent`. The batch retains the loss comment, guarded human attention and
saved-notification acknowledgement. Each delivery checks receipt identity, and
outcome and attention writes also require the actor-owned open child. The
conditional revision comes from that same receipt and ownership check.

## Close or review

The runtime validates a close locally before sending it. It uses the
summary as the close message and never pads text or invents evidence.

| Close reason | Evidence |
|---|---|
| `done` | At least one `commit`, `pr`, `test`, `reviewed-paths` or `external`; only those types. |
| `audit-no-change` | Exactly one `no-change-audit`, with optional `reviewed-paths`. |

The normalized message must meet the 40-byte minimum enforced by the current
validator and must not be trivial. Normalization lowercases, collapses
whitespace and removes trailing `.?!`. Evidence payloads must be nonempty;
commit SHAs are 7–40 hexadecimal characters. Duplicate raw evidence items
are refused. See Kata's
[close contract](https://github.com/kenn-io/kata/blob/main/docs/reference/cli.md)
for the upstream schema.

Success with unusable evidence stays successful as run evidence but follows
the review row. A definitive Kata close-validation refusal also replaces the
unsent close tail with review reporting, including for command-only and mixed
workflows whose command receipts supply valid evidence. A guarded close conflict
instead requires inspection unless read-back confirms closure. Optional Kata close guards
`sibling_throttle` and `duplicate_message` retry the identical keyed close with
normal backoff. They never trigger review or inspection, and the close message
is never padded to bypass a guard. This also applies to native children.

Native-child journals retain the bound workflow name for their close message.
For all-command workflows, every frozen command needs a matching zero-exit,
certain receipt. The runtime builds the message (`1 command step` for a single step):
`Workflow <name>: <n> command steps exited 0 (run <uid>, occurrence <key>)`.
It supplies bounded, deduplicated `test:<command>` evidence. A mixed workflow
uses the last prompt report's summary/evidence and adds successful command
evidence for `done`; command failure cannot be erased by a later prompt success.

## Attention and ownership

The runtime writes only on issues it created or children it took over.
Per-run adapter issues start unowned with `herdr_kata.run=<run UID>` and
`work.attention=ok`. Newly prepared per-run creates also carry `herdr-kata:run`
and their optional run parent in that same request, adding no write. The runtime
never mutates a run parent. A runtime-created run issue may remain unowned or
be claimed by the pinned lane actor, including an agent using that actor with
a teammate. Accepting that claim requires the exact issue UID, project UID and
`herdr_kata.run` marker. A foreign owner still fences reporting. Worker startup
can reconsider an unsent ownership refusal after a fresh issue read; sent
ambiguous writes retain their original inspection and guard. Children are
tracked after conditional claim or takeover.

| Situation | `work.attention` | `work.attention_msg` |
|---|---|---|
| Created/tracked | `ok` | No initial message. |
| Restored after unblock | `ok` | `herdr-kata: running` |
| Resumed success closing its issue | `ok` | Unset before the guarded close. |
| Success needing review | `needs-human` | `herdr-kata: review run <uid>` |
| Failure | `stuck` | `herdr-kata: <reason>` |
| Unknown or handoff | `needs-human` | `herdr-kata: <reason>` |
| Lost-state abandonment of a recognizable open, unowned runtime-created issue | `needs-human` | `herdr-kata: local state lost (run <uid>)` |
| Agent blocked | `needs-human` | `herdr-kata: agent blocked` |

Each attention write reads the issue and freezes `--if-match`; the issue
must remain open with its expected owner. Closure, reassignment or unresolved
guard conflicts stop attention writes. Child attention conflicts require
inspection. Existing adapter issues receive only comments and notifications;
the runtime never closes, reopens or relabels them or their recurring parents.

Lost-state abandonment posts one keyed loss comment. Only an open, unowned
issue with the matching `herdr_kata.run` marker receives attention writes; other
issues receive the comment only. If a concurrent issue change prevents attention
after the comment lands, that delivered comment completes the report; the
retired attention intents retain their guards and inspection notes. Signing
refusals are never retired this way. Durable report receipts make retries idempotent.

Blocked runs notify immediately and set owned attention to `needs-human`.
Unblocking restores `ok`. Notification recipients are the current owner,
otherwise the author, resolved fresh and saved before delivery.

## Idempotency and write counts

| Operation | Stable key or guard |
|---|---|
| Per-run issue create | `herdr-kata:<run>` |
| Settlement comment | `herdr-kata:<run>:comment:<outcome>` |
| Finding comment | `herdr-kata:<run>:step:<step>:attempt:<n>:<maker or checker>` |
| Close | `herdr-kata:<run>:close`, with `--if-match` |
| Observation | Frozen body and expected revision. |
| Attention, deadline, child acknowledgement | Saved revision/value guards. |
| Notification, label | Serialize issue writes; read back an uncertain effect. |

Operator release uses distinct operation IDs and appends `-release` to its
comment key. Resumed settlement operation IDs and keys include
`:resume:<consumed-request-uid>` after the run UID; earlier operations keep
their original payloads and identities. The [adapter contract](kata-adapter.md#write-queue) owns replay
and queue limits.

| Base case | Kata mutations |
|---|---|
| Successful per-run adapter execution | Create, two observations, close: `4`. |
| Successful owned child | Track, two observations, close, acknowledgement: `5`. |
| Success requiring review on owned issue | Replace close with comment, label, two attention writes: `+3`. |
| Unknown owned child | Track, two observations, comment, two attention writes, notify, acknowledgement: `8`. |
| Notify job on existing issue | Notification and two observations: `3`. |
| Successful command-only run on existing issue | Two observations: `2`. |

Each accepted resume adds a running observation and, on owned issues, two
attention writes. Its settlement is a new reporting generation; a closing success also unsets
the owned attention message before close.

Counts exclude finding comments (`+1` per published comment), a claim
(`+1` for an unowned child), a deadline (`+1` when
configured), and a full owned blocked/unblocked cycle (`+5`: notify, two
attention fields on block, and both fields restored on unblock).
Reads and local recipient resolution are not mutations. A successful close
after unblock also clears the running message (`+1`). Lane ownership adds
one conditional lease write every three minutes and before each admission,
on the lane's lease issue, never on a run's issue; see
[lane ownership](kata-adapter.md#lane-ownership).

## Findings comments

The first checker failure starts one finding chain on the run's issue.
Once the chain exists, maker results and checker verdicts continue it through
comments. Each identifies run, step, attempt and status; continuations
include `Answers c:<saved comment UID>`. A note, when present, precedes the
summary or command failure reason. These comments are additional to ordinary
settlement reporting and never use the recurring parent.

Each comment has its own attempt-bound key from the table above. Its canonical
UID is saved with operation completion. The run retains the root, newest head
and latest maker comment UID. General continuations answer the newest saved
head, including checker-only continuations. At most one chain operation is
incomplete per run; an uncertain write remains incomplete until its exact
keyed replay recovers the UID. No later chain write is prepared before that.

The checker also waits for the maker's saved comment UID before its prompt
is written or submitted. Prompt context carries saved references and a
bounded read of the run's published chain: at most 50 comments and 16 KiB,
with missing or truncated context shown. The context is frozen per attempt;
a new attempt takes a fresh snapshot. It is not a complete read of all
comments on an existing issue.

The same read also freezes operator notes: replies to one of the run's
published chain comments whose author is the run's actor, whose teammate
is not `herdr-kata` and which the run did not write itself. Comments by other actors, top-level comments and replies
to other threads never become notes. The newest 10 notes within 4 KiB are
kept, oldest first; a partial read is marked. Like the rest of the context,
notes are frozen per attempt and saved with the run before its prompt is
written, so recovery never re-reads them; a new attempt takes a fresh
snapshot. The prompt labels them as context only: a note never authorizes
execution, a resume or anything beyond the step's task.

The retained reporter's assertion flags are probed when creating the prompt.
Submission reuses that saved choice even if a later probe would fail or recover.

Pending chain writes continue while a loop is held. Do not post a replacement
finding to bypass uncertainty; inspect the saved operation and use
[Recovery](../operations/recovery.md#inspect-a-loop-hold). Comments are
plain when either the CLI or daemon lacks typed replies.

Typed replies require Kata's `comment --reply`, `--confirm` and `--refute`
flags and daemon health `api_schema_version >= 0.28.0` (#523). The runtime
probes help and health without writing, before preparing new findings. The
contract is verified against released Kata 0.19.0.
Mode, target, kind, body and key are frozen per operation. An upgrade or downgrade
never changes a saved operation. The write response has no comment handle:
the runtime records `.comment.uid` and builds `c:<UID>` itself.

Settlement and release comments stay plain and do not advance the finding
head. Typed links apply only to maker/checker findings. Detection happens before
each new finding, so one chain can contain both plain and typed operations.

General typed replies target the newest head. A checker can explicitly
confirm or refute its saved maker claim with `report --assert confirm|refute
--target c:<full-comment-UID>`. Only the first assertion of each kind targets
that maker claim. The agent's summary must supply at least 40 Unicode characters
after collapsing whitespace; runtime headers and notes do not count. Exit
status never implies an assertion, and a failed checker may confirm a claim.

A wrong target, a non-checker assertion, short evidence or a repeated kind
becomes a general continuation answering the current head. Its body names the
requested claim and explains the downgrade. Plain-mode assertions name the
claim in their text and still record the published kind, so an upgrade does
not publish the same assertion again. The runtime never pads evidence.

Kata's duplicate guard is project-wide per actor, teammate, target and kind,
including `reply`. Different source issues do not bypass it. The runtime targets
only its own run's saved chain; it never publishes cross-run assertions.

A typed refusal requires inspection: `duplicate_reply` logs any supplied
`existing_uid` and `existing_handle`. The handle may be `<issue>:<suffix>` or
absent, and both diagnostics may be omitted for an unreadable duplicate.
Inspection never requires either field. `issue_closed`, `comment_not_found`,
`idempotency_mismatch` and a daemon downgrade also stop the saved operation.
The runtime neither forces a duplicate nor replaces a typed operation with plain
text. A successful response with conflicting
edge fields also requires inspection. Omitted edge fields are logged and the
canonical UID is accepted. Edges can be omitted when the target's issue or
project is hidden, deleted or archived, as well as for scoped readers. Only a
present edge that differs from the frozen operation requires inspection. The
runtime never sends `--force` or `--supersede`.

Confirm comment UIDs are not automatically added to close evidence: the close
payload is frozen before that UID exists. Agents still supply ordinary close
evidence under [Close or review](#close-or-review).
