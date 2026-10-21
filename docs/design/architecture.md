---
title: Architecture
description: The process model, kept boundaries and reasons for an event-driven runtime.
last_edited: 2026-10-19
---

# Architecture

herdr-kata keeps scheduling and execution beside Kata rather than inside it.
It aims to leave Kata's TUI and Herdr responsive while agents do work. One
binary supplies the supervisor, worker, operator commands and TUI.
[Contributing](../development/contributing.md#repository-layout) maps the
packages.

## Events instead of frequent refreshes

One long-lived Kata event stream wakes definition, readiness and inbox work.
Definition events coalesce into a dirty flag; run-observation events do not
reload the catalog. Timers wait for the next scheduled occurrence. Slow
reconciliation repairs missed events without a frequent catalog poll.

The TUI reads local snapshots on its tick. Explicit edits and refreshes can
call Kata, but the display timer never does. Herdr calls use a native socket
client rather than spawning its CLI. Long agent waits have their own connections.
The [adapter contract](../reference/kata-adapter.md#transport-and-events)
owns the exact wake intervals and transport limits.

For scale, a recorded Linux smoke snapshot reported about 14 MiB RSS and
0.2% process-lifetime CPU for the worker, about 9 MiB for the supervisor and
34 MiB for the event-tail Kata process. Those are separate processes; worker
RSS alone is not the runtime's total memory cost. They are sample readings,
not an idle CPU or latency guarantee for another installation.

## Process model

```text
Herdr startup hook or manual up
              |
       session gate and stage
              |
     serve (serve.lock)
              |
     worker (worker.lock)
              |
  events + scheduler + inbox
  runners + ordered write queue
```

`up` checks the Herdr socket's session and stages its executable before
starting a detached `serve`. The staged path survives plugin updates that
replace the source checkout. Agents use it to write reports even after the
installed checkout has changed. [State](../reference/state.md) owns the build
names and retention rules.

`serve` restarts a crashed worker with backoff from one second to five
minutes. It waits for `worker.lock` before starting a replacement, so an
orphaned worker prevents another worker from starting. An upgrade asks the
old worker to stop, cancels and joins its runners and queued calls, then
replaces it. Command groups receive shutdown signals and may become unknown;
accepted prompt agents can continue across the change.

Stop cancels admission and joins local runners before releasing the worker
lock. A journal-read deadline that races shutdown preserves the caller's
cancellation, allowing a clean stop. Prompt agents remain running. The
supervisor leaves when Herdr has been unreachable for two minutes. If `serve`
itself dies, nothing restarts it until the next startup hook, up action or
manual `herdr-kata up`.

## Boundaries kept on purpose

Kata remains the only definition store. Local definition snapshots serve
display and recovery; they do not become an editable job database. Durable
run JSON also carries its own mutation intents, avoiding a separate outbox
database and cross-store commit problem.

The status command combines lane snapshots with control responses at the CLI
boundary. Lane inspection for startup recovery reads runtime and project state
without decoding control acknowledgements.

Route probes and completed route checks share typed pin and route snapshots.
Only completed checks publish fresh identity and timestamp evidence. Repeated
identical access-loss probes preserve the saved file.

The [trust model](trust-model.md) explains who may run work on the host and
what stays owner-local. [Configuration](../reference/configuration.md)
owns credential routing; [Run lifecycle](run-lifecycle.md) explains why
uncertain execution is held rather than relaunched.
