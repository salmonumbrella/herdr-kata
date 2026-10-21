---
title: Run recurring issues
description: Bind a parent issue to a workflow and let Kata deliver recurring children to the runtime.
last_edited: 2026-10-19
---

# Run recurring issues

Let Kata create one child issue per occurrence and request work in the
runtime's inbox. herdr-kata picks up eligible children and executes their
parent's binding. Kata owns delivery; the runtime does not schedule these jobs.

## Set the inbox

In `config.toml`, set an explicit recipient in the project's `[[projects]]`
entry:

```toml
[[projects]]
name = "example-project"
actor = "worker"
inbox = "worker/runtime"
```

In the legacy single-project form, set `inbox` in `[kata]` instead.

Restart with `herdr-kata stop` followed by `herdr-kata up` after changing
configuration. Use the same project and route for the following Kata commands
as the [runtime](../operations/runtime.md).

## Choose a parent and bind it

Use an open parent issue. Replace `abc4` with its short ID. Children must be
unowned or owned by the project's configured `actor` to be taken over.

Set the execution binding on the parent:

```sh
kata meta set abc4 herdr_kata.workflow <workflow-uid>
kata meta set abc4 herdr_kata.profile default
kata meta set abc4 herdr_kata.checkout repo
```

The workflow can be a UID or a name that resolves uniquely. The profile and
checkout must exist in plugin configuration. Without a workflow binding,
the child gets a single prompt asking the agent to work its issue, including
its title and body. Without a profile binding it uses `default`; without a
checkout binding it uses the run directory.

A binding affects every job and unadmitted child sharing that parent.
Already prepared runs keep the binding they saved at admission. You can
also edit it with `b` on the recurring job in the [TUI](tui.md).

## Request execution for an ordinary child

An event producer can create a normal child under the same bound parent and
explicitly request execution without a recurring delivery job. Use a unique
canonical ULID for `request_uid`, an actual UTC request timestamp, and the
configured runtime inbox:

```sh
kata create "Inspect the event" --parent abc4 --body "Inspect the supplied event and report evidence."
kata meta set <child-ref> herdr_kata.request '{"version":1,"kind":"execute","request_uid":"01K00000000000000000000004","requested_at":"2026-01-02T03:04:05Z"}' --json-value --if-absent
kata notify <child-ref> --to worker/runtime --message "Execute the explicit request"
```

The child must be open, ready and unowned or owned by the configured actor.
To run it later, schedule it with `kata schedule <child-ref> <date>` before
the request; the runtime wakes when that date arrives and runs it then, never before. See
[Run an agent on an issue's schedule](issue-schedules.md#request-execution-on-the-child).
Notifications alone never opt in. The runtime freezes the parent binding and
issue input, admits through permanent `herdr_kata.admission` metadata, and
reports through `herdr_kata.outcome`. Editing the request cannot authorize a
second automatic run for the same child. Use a new child for a new execution.
Receipt loss or mismatched local state requires operator inspection; never
clear admission to retry. Competing workers must use one authoritative Kata
service, rather than separate writable replicas.

## Create the delivery job disabled

Save this as `recurring.json`:

```json
{
  "name": "Recurring inspection",
  "definition": {
    "version": 1,
    "kind": "job",
    "enabled": false,
    "trigger": {"kind": "interval", "interval_seconds": 3600},
    "action": {"kind": "notify", "recipient": "worker/runtime", "message": "Inspect this occurrence."},
    "issue": {"kind": "per-run", "title": "Recurring inspection", "body": "Inspect the checkout and report the result."},
    "overlap": "allow",
    "catchup": "skip",
    "grace_seconds": 300
  }
}
```

The recipient must match the project's `inbox`. Bind it to the parent on creation:

```sh
kata cron job create --parent abc4 --file recurring.json --json
```

Kata stores the parent's full UID in `definition.issue.parent_uid`. Save
the returned job UID. Delivery supports cron or intervals of at least
60 seconds, requires `overlap: "allow"`, and accepts no timeout, issue
offsets, checkout or options in the delivery definition. Execution settings
come from the parent binding and runtime configuration.

## Verify the binding and enable delivery

In the TUI, select the job, press `b` to inspect the parent binding, then
`p` to enable it. Enabling verifies the binding. New native jobs created
through its form are also saved disabled first.

From the CLI, read the current definition:

```sh
kata cron job show <job-uid> --json
```

Copy its full `name` and `definition` into `recurring.json`, preserving the
canonical `parent_uid`. Set `enabled` to `true` and apply the current
`definition_event_uid`:

```sh
kata cron job update <job-uid> --expected-event-uid <event-uid> --file recurring.json --json
```

Kata begins delivery. Pause with the same guarded update setting `enabled`
to `false`, or use `p`. The native parent cannot be changed on an existing job.

## Inspect pickup

Inspect the runtime's inbox snapshot and Kata's inbox:

```sh
herdr-kata status
kata inbox --for worker/runtime --json
```

`status` prints the last successful inbox scan and its error, then one line
per request: accepted, waiting for readiness, unsupported or ignored, with
its reason. `status --json` holds the same snapshot per project. Runs in the
[TUI](tui.md) shows the children the runtime picked up. Open, ready children
with a parent link and valid `cron.occurrence` metadata can run. Children
waiting for readiness consume no slot. Unowned children are claimed
conditionally; children already owned by the configured actor are taken over.
Other owners, invalid bindings and malformed occurrence metadata do not run.
Plain inbox prose cannot authorize execution.

The worker scans on relevant events, stream start/reset and a jittered
ten-to-twelve-minute fallback, with a two-second debounce. After reporting, it clears only its
saved request from its own inbox slot. A changed replacement remains.

## Limits

- Delivery is at least once. An identical replacement request cannot be
  distinguished from the original.
- Native jobs and inbox children cannot be run manually with `run` or `R`.
- Child input is its body. Delivery definitions cannot set `options.input`.
- A binding edit does not change a prepared run. See the
  [adapter contract](../reference/kata-adapter.md) for admission and reporting.
- `status` prints no inbox lines while the inbox snapshot is missing. Check
  runtime health before treating that as proof that nothing is waiting.
