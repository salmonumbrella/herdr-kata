---
title: Run an agent on an issue's schedule
description: Run an agent on an issue when its Kata schedule arrives, from an explicit request or an execute job.
last_edited: 2026-10-19
---

# Run an agent on an issue's schedule

An explicitly requested child issue runs when its `scheduled_on` date
arrives, within seconds. An execute job targeting an existing issue also
works, with up to ten minutes' delay. Both assume a configured, running
[runtime](../operations/runtime.md).

## Request execution on the child

1. Schedule an open child of a bound parent before requesting it, so it is
   not ready yet:

   ```sh
   kata schedule <child-ref> 2030-01-01T09:00:00Z
   ```

2. Request execution on the child, as in
   [Request execution for an ordinary child](recurring-issues.md#request-execution-for-an-ordinary-child).

Until the date, `herdr-kata status` shows the request as
`waiting-ready`, `scheduled for 2030-01-01T09:00:00Z`. No run is prepared
and no slot is held. The runtime wakes one second after the instant Kata
resolves for the date, and Kata's ready set decides; blockers or `someday`
keep it waiting. Moving the date before it arrives moves the run; the old
date never fires. The child runs at most once: rescheduling it after the run
does not run it again. Use a new child for a new run.

## Request a job run for the scheduled issue

Use an execute job targeting an existing issue. The request waits for the
issue's schedule without reserving an execution slot. Replace `abc4` with
your issue's short ID and UID placeholders with full UIDs from Kata.

1. Set the issue's schedule and read its full UID:

   ```sh
   kata schedule abc4 2030-01-01T09:00:00Z
   kata show abc4 --json
   ```

2. Start from the prompt `job.json` in
   [Schedule jobs](jobs.md#choose-the-work-and-issue). Replace the whole
   `issue` object with `{"kind":"existing","uid":"<issue-uid>"}` and keep
   `trigger: {"kind":"manual"}`. Create it and submit one request:

   ```sh
   kata cron job create --file job.json --json
   herdr-kata run <job-uid>
   ```

The request stays pending, unslotted, until the issue is open and in
`kata ready`. The runtime rechecks on relevant issue events, stream resets
and its ten-minute fallback wake. Blockers or `someday` keep it pending
longer.

## Request it automatically

Instead of submitting `run`, use a `once` trigger with a future `at` instant
and `catchup: "latest"`. Create the job before that instant. The occurrence
waits for readiness in the same way. See
[catch-up limits](jobs.md#choose-when-it-runs).

## Limits

- For a job on an existing issue, a date maturing alone does not wake the
  scheduler, so execution can start up to ten minutes after the schedule
  arrives, plus any capacity wait. An explicit request wakes at the date.
- Changing the issue's schedule does not move a `once` job's fixed `at`
  instant or create another occurrence.
- The `issue-scheduled` and `issue-deadline` triggers are not supported; see
  [Job and workflow fields](../reference/definitions.md#limits).
