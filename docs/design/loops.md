---
title: Notes and maker/checker loops
description: Why workflows use explicit notes, one bounded rewind span and a saved finding chain.
last_edited: 2026-10-17
---

# Notes and maker/checker loops

A maker/checker loop lets a workflow respond to a certain failed check without
turning recovery into permission to repeat uncertain work. Notes pass the
diagnosis forward; Kata comments keep the history attached to the run issue.

## Explicit handoff

Input is fixed at admission. A step may publish a short note for the next
executed step, and a re-run maker receives the saved checker rejection.
Without a note, previous text is empty. Summaries stay reports rather than
becoming an implicit prompt input. The [agent contract](../reference/agent-contract.md#input-and-notes)
owns exact bounds and substitution rules.

## One span and two stop conditions

The workflow declares one backward edge over a linear span. It repeats that
span only after a settled prompt failure or certain nonzero command receipt.
Uncertainty holds the run instead. Loop-span retries are refused so an attempt
has one clear place in the visit accounting. Re-run prompts use fresh agents,
while retaining the old occupants' ownership evidence.

The additional-visit budget bounds correction work. Repeated nonempty rejection
notes can stop it sooner: equal text after whitespace normalization is evidence
that the next correction is not changing the diagnosis. Empty notes carry no
such evidence, so they rely on budget alone. [Definitions](../reference/definitions.md#makerchecker-loop)
owns edge fields, defaults and the re-runnable span rule. Put irreversible
push, deploy and publish work after the checker; the runtime cannot recognize
side effects in arbitrary commands or prompts.

## Durable rewind

The failed verdict, its consumed attempt, budget debit and reset of the whole
span commit in one journal transition. New attempts increase monotonically;
old attempt evidence remains. Recovery can continue a committed rewind without
charging it again. A saved failed verdict without its edge decision instead
holds as `loop_pending`, preserving the never-resend rule.

An exhausted budget or repeated diagnosis produces `loop_exhausted` or
`loop_stuck`. These deliberate stops keep a failed outcome and overlap gate.
Stopped proof may free their execution slot, but does not authorize another
maker visit. An operator can authorize continuation through a durable resume
request. [Resume rules](../reference/holds.md#resume-rules) own its stopped
proof, capacity, issue checks and target selection. Holding workflow failures
uses the same hold and resume path.

## Findings before dependent work

Maker/checker findings form a saved comment chain, with typed replies and
explicit confirm/refute assertions when Kata supports them, and plain comments
otherwise. Dependent work waits for the preceding comment's saved canonical
UID. [Reporting](../reference/reporting.md#findings-comments) owns reply targets,
fallback rules and bounded prompt context.

## Cost bounds

Prompt attempts are bounded per run generation, including retries and loop
visits. The cap is checked against definitions and enforced at the saved
submission barrier. An exhausted budget also prevents the next prompt launch;
it is not a launch counter. A lost agent before submission consumes no prompt.
[Configuration](../reference/configuration.md#limits) owns the limits and formula.

Steps, retries and loops share the run expiry, bounded by an installation
ceiling on the timeout duration. Resume is a person's authorization for a new
generation with a fresh expiry and prompt budget. Concurrency remains bounded
by `max_active_runs`, including held agents that are still working.

These limits do not bound tokens or money, the length of one agent turn, or
the number of runs per day. Herdr exposes no token usage; the worker records
best-effort usage from local sources but never enforces a budget with it.
Reaching a prompt budget or run expiry stops new work; it never interrupts a
working agent.
