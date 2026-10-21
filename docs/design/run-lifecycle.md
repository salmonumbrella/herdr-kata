---
title: Run lifecycle and recovery
description: Why journals, submission barriers and occupant proof govern recovery and cleanup.
last_edited: 2026-10-17
---

# Run lifecycle and recovery

Recovery continues saved local execution. It does not infer a new attempt
from Kata's run status. The central rule is that a prompt possibly delivered
to Herdr is never sent again for that attempt.

## Identity and admission

Scheduled UIDs combine the occurrence's milliseconds with deterministic
SHA-256 entropy. Rebuilding a schedule therefore finds the same run rather
than making a replacement. Native child UIDs use the child as the entropy
source; manual requests allocate a random UID once and keep it through retries.
The [adapter contract](../reference/kata-adapter.md#identity-and-admission)
gives the exact identity and admission order.

Before a new journal is prepared, the runtime asks the pinned authority about
its UID. Existing evidence vetoes preparation. Admission then writes a
revision-zero observation carrying the frozen definition and executor identity.
A conflicting journal cannot authorize this journal's execution, even when
the immutable identity matches: the frozen journal ID distinguishes them. Unique executor labels matter; this is not automatic
failover or an exactly-once guarantee across installations.

## One journal and one writer

The worker serializes every run transition: lock, read current `run.json`,
apply, durably replace, unlock. Execution, control requests and queue results
all use this path. Queue completions find their saved operation by ID rather
than writing an earlier copy over newer execution state.

The journal freezes input, definitions, target, profile, checkout and expiry.
It records the active attempt and every pending Kata mutation before its
side effect. Result and receipt files are evidence to reconcile into that
journal. They are not a second execution state machine.

Kata can show a terminal status while local reporting or an occupant remains
unresolved. That status alone cannot advance steps, release gates or close
tabs. Likewise, a late result on a held run cannot automatically turn its
outcome into success.

## Submission barrier

Before `agent.prompt`, the worker saves `sending`, submission time and the
available completion sequence. `agent_blocked` and `agent_not_ready` save
`rejected`, because Herdr sent no input. Any other response or lost reply becomes `accepted`.
A recovered `sending` is already possibly delivered and goes straight to
waiting on the saved occupant.

Only `none` or `rejected` can submit. Already resolved attempts advance
without waiting again. A certain `failed` report may use the configured
retry count, with a new attempt directory and settlement proof. Ambiguous
submission, missing results and lost command receipts do not authorize retries
or loop rewinds. A declared checker edge is separate from retries: its saved
verdict, budget debit and span reset commit together. Recovery holds a verdict
without that decision as `loop_pending`. [Notes and maker/checker loops](loops.md)
explains the rewind and finding barriers.

## Ownership before cleanup

The server identity comes from the socket file's device, inode and
full-resolution change time. A replacement server makes old workspace and
pane IDs unproven. Closing a tab or resubmitting to a retained agent requires
the saved server, single root pane, terminal ID, agent name and full
`agent_session` object to match. Missing session identity is unproven.

Read-only waiting can use a partial match and capture a delayed session once.
A changed session never replaces the saved one. A successful run closes its
tab only when the matching agent is settled under full proof. Other tabs
stay for inspection. Fresh agents retain per-attempt provenance; moving to
the next agent still requires the previous occupant's settlement proof.

## Activity and settlement

Idle or done after submission can be a stale observation. If both saved and
current `completion_seq` exist, the current sequence must advance. Otherwise,
saved working/blocked activity or a valid attempt-bound result corroborates
the turn. A valid report never advances work while its occupant is working.

Until evidence appears, the worker uses activity waits of at most 30 seconds,
then checks the occupant and result. Five minutes of idle/done without
activity or a valid result becomes `no_activity`. Submission time and activity
are durable, so recovery keeps the original grace period. Older journals
without a timestamp start one saved grace period on reconciliation. Observed
working/blocked activity ends that grace; the run expiry still applies.
A valid result from a settled occupant takes precedence over expired activity
grace when evaluated.

Once settlement is proven, a missing report gets a ten-second file grace,
checked every two seconds, then `no_result`. A missing agent without a result
holds `agent_lost` as soon as the saved pane is proven exited or back at its
shell. [Recovery](../operations/recovery.md#find-the-run-and-read-its-reason)
owns the stopped proof; [Hold reasons](../reference/holds.md#hold-reasons)
maps hold reasons.

## Held capacity

Held occupants are rechecked every ten minutes and when admission finds
capacity full. Reconciliation releases only the slot; the held outcome and
overlap gate remain for the operator, and no prompt is sent.

Idle/done needs matching provenance and a recorded outcome, plus an advanced
sequence when both values exist. Without a sequence, a submitted prompt
needs saved activity or a valid result. Late activity is saved without
freeing capacity. `no_activity` is the special case: its grace is resolved,
so a matching idle/done occupant can release capacity even without sequence
advance, while the unknown outcome stays held.

An absent agent must be absent by both name and pane on the saved server.
Its original pane must have exited or returned to its foreground shell.
That proof also allows a failed startup to release capacity as
`agent_start_failed`; a foreign or unproven occupant keeps it. Retained
agents from earlier attempts must be proven stopped too. Command capacity
requires a matching certain receipt and proof that its process group is gone.

## Lost state and quarantine

A missing journal is not permission to run again. Authority evidence and
UID-preserving quarantine prevent fresh preparation. `doctor` finds this
installation's running observations without local directories; explicit
`abandon` records the loss without executing anything.

Quarantine preserves evidence but removes the journal from the live index,
so it does not preserve its capacity reservation. A crash between tab creation
and saving provenance can also leave an unidentifiable empty tab. Recovery
leaves it alone rather than guessing ownership. See
[Recover runs](../operations/recovery.md) for inspection,
handoff, abandonment and manual cleanup.
