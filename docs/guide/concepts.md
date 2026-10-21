---
title: Concepts
description: The terms and ownership boundaries used by Kata, the runtime and Herdr.
last_edited: 2026-10-18
---

# Concepts

Kata decides what work exists. herdr-kata runs it. Herdr supplies the agent
panes and lifecycle. This division keeps execution out of Kata and makes
the run's result visible where its issue already lives.

| Component | Owns |
|---|---|
| Kata | Definitions, issues, ownership, readiness, inbox requests, native recurring delivery and run evidence. |
| herdr-kata | Adapter schedules, catch-up, execution capacity, checkout/profile mapping, recovery and reporting. |
| Herdr | Workspaces, tabs, panes, agent identities and prompt submission. |

## Definitions and execution

| Term | Meaning |
|---|---|
| Job | A Kata definition combining a trigger, action, issue policy and execution policies. |
| Workflow | A Kata definition containing steps connected by `after` dependencies. |
| Step | One prompt or shell command. Dependencies determine order; execution is serial. |
| Attempt | One execution of a step, with its own artifact directory and result identity. |
| Maker/checker loop | One declared backward edge that repeats a linear step span after a certain checker failure. |
| Occurrence | One scheduled instant, or one manual request, that may produce a run. |
| Run | The durable local execution of one occurrence, reported to Kata under one UID. |
| Adapter job | A job herdr-kata schedules: execute, or notify on an existing issue. |
| Native recurring job | A notify job for which Kata creates a child and inbox request; herdr-kata picks up eligible children. |

A job is a definition, not a running process. A run freezes the definition
it admitted, so changing the job affects future work. The schedule's
checkpoint tracks which occurrences were handled; Kata history is supporting
evidence rather than the local scheduler's clock.

## Issues

| Issue mode | Meaning |
|---|---|
| Per-run | herdr-kata creates a new issue for an adapter execution. |
| Existing | An adapter job uses a standing issue; the runtime comments and notifies without taking its ownership. |
| Child | Kata created the issue under a recurring parent. The runtime may claim or take over that child, never the parent. |

Readiness is Kata's decision. An existing execute target or recurring child
waits until it is open and ready. That wait holds no execution capacity.
See Kata's [agent contract](https://github.com/kenn-io/kata/blob/main/docs/workflows/agents.md)
for readiness and attention, and [Reporting](../reference/reporting.md) for
what the runtime changes on each issue mode.

## Installation and mapping

| Term | Meaning |
|---|---|
| Installation | One config/state directory pair serving configured Kata projects in one Herdr session. |
| Project lane | One project's route, actor, inbox, schedules and snapshots inside the shared worker. |
| Executor | The installation's unique, stable label in Kata run observations. |
| Profile | A local mapping from a name to a Herdr agent kind and arguments. |
| Checkout | A local mapping from a definition's checkout key to an existing directory. |
| Occupant | The agent or command process that may still be doing an attempt's work. |
| Slot | One unit of shared capacity reserved for a possibly live execution. |
| Overlap gate | The job-level reservation that makes `forbid` occurrences wait for earlier work. |

A stopped occupant can release its slot while the run still holds its job's
gate. That lets unrelated work proceed without silently restarting unresolved
work. [State and run phases](../reference/state.md#slots-and-overlap) gives
the exact reservation rules.

Each run belongs to its original project for life. People who can write a
project's definitions or parent bindings are trusted to run its permitted
work on this host. [Trust model](../design/trust-model.md) explains that
boundary.

## Run outcomes

**Held** means automatic execution stopped for operator inspection.
[Hold reasons](../reference/holds.md#hold-reasons) defines each class and its
outcome, reservation and recovery rules.
**Handed off** means the agent or operator gave the work to a human and freed
capacity and overlap. **Settled** means local execution finished; reporting
can still be queued. These are local phases, separate from Kata's observed
`succeeded`, `failed` or `unknown` result.

A run starts with a journal and an admission observation, executes serial
steps, records attempt evidence and queues its final issue/run updates. A
restart reconciles the saved attempt. It never repeats a prompt that may
already have been delivered. [Run lifecycle and recovery](../design/run-lifecycle.md)
explains the safety decisions; [Recovery](../operations/recovery.md) gives
operator controls.

A loop's maker responds to a checker finding through new attempts. Its budget
limits additional visits, and repeated nonempty notes can stop it sooner.
The run's issue holds the finding chain. [Notes and maker/checker loops](../design/loops.md)
explains why a loop differs from an ordinary retry.

## Limits

Unsupported definitions stay visible and do not run. The exact field and
platform limits live in [Job and workflow fields](../reference/definitions.md#limits).
Lost local state requires explicit resolution and never launches a replacement.
The supervisor restarts workers, but if the supervisor itself dies, another
`up` is required; see [Architecture](../design/architecture.md#process-model).
