---
title: Changelog
description: Changes to the headless Kata adapter.
last_edited: 2026-11-04
---

# Changelog

## Unreleased

- Keep jobs moving when an optional command note or an otherwise valid agent
  result note is malformed. Normalize notes within the existing 280-byte limit,
  preserve the known outcome and pass the normalized note to dependent steps.
  Existing `invalid_note` holds still require investigation and release.
<small>2026-11-04</small>

- Keep jobs moving after a startup dialog or a confirmed timeout stop: new
  prompt attempts without startup evidence begin stopping after two minutes;
  proven stops fail the whole run and release capacity and overlap. Unknown
  or ambiguous outcomes still hold for inspection. Older attempts retain
  their normal deadline.
- Preserve the startup/timeout prompt screen in run logs after cleanup, and
  report `reason: agent_not_started` or `reason: timeout` with replay-safe
  delivery through transient Kata errors.

## 0.3.0
<small>2026-11-03</small>

- Inventory previously unsupported jobs before enabling project defaults: they
  may launch missed occurrences through their existing catchup policy.
- Exclude `notify` and other-adapter jobs from doctor's unsupported-definition
  list. Manual `notify` requests are rejected with `job action is not execute`;
  foreign executor pins still have their own diagnostic line.

- Store frozen unsupported observation bodies as readable JSON, retaining support
  for earlier base64 records and byte-identical retries.

- Continue delivering and listing valid unsupported reports when another outbox
  record is invalid or unreadable; retain and identify the bad file.

- Preserve spaces and tabs in unsupported-run reasons while replacing embedded
  line breaks, so reported profile and checkout keys match the definition.

- Run enabled portable jobs with project `default_checkout` and `default_agent`,
  without per-job adapter fields. Explicit choices still win and prepared runs
  retain their frozen checkout and argv. Checkout mappings require selection
  by a job key or project default.
- A job without `options.agent` moves from implicit profile `default` to the
  project's `default_agent` when configured. Explicit `"default"` stays selected;
  present non-string agents now report `agent must be a string`.
- Interpret job `options.executor` as a local ownership pin. Other executors
  skip the job and reject its manual requests; malformed pins are unsupported.
- Record owned unsupported definitions once per distinct cause as failed Kata
  runs starting `reason: unsupported`. Include missing mappings, unavailable
  executables, invalid triggers and `once_in_past`. Persist replay-safe delivery
  separately from execution journals and expose pending/refused reports in doctor.
- Show foreign pins, existing-issue readiness gates and held capacity in doctor.
  Sample recent run evidence per owned job to warn about other executor labels.
  Document the single-runtime operating condition and mixed-version limits.
- Exercise scheduled defaults, unsupported reporting and pin filtering across
  ticks and restarts in isolated live runtimes, including the installed core.

## 0.2.0
<small>2026-11-02</small>

- Breaking: herdr-kata is now a headless worker on herdr-core (`hc`), not a
  Herdr plugin. Uninstall the v1 plugin, build `bin/herdr-kata`, and start
  with fresh configuration and state ([Install](install.md)).
- Read the project-wide `kata ready` list only when an enabled job uses an
  existing issue or an `issue-scheduled` trigger. Projects with only per-run
  jobs no longer stall a tick when that large read times out.
- Support job-level `options.run_parent`: each per-run issue is created as a
  child of that issue UID. Steps also receive `KATA_PROJECT`.
- Preserve committed progress and the original interval anchor across job edits
  and re-enables. Recompute unprepared work under the new definition's catch-up
  policy instead of resetting progress to the current time.
- Reject requests backed by unreadable run journals and release requests whose
  target cannot be found or read. Transient release lookup failures still wait
  for a later tick.
- Keep manual requests pending through capacity, overlap and transient read
  failures. Report the known blocker when the CLI wait ends. Known pending
  requests retry on regular ticks rather than triggering repeated fast ticks.
- Keep on-time `skip` work through brief Kata outages, bounded by grace plus
  2 minutes. A run published before a preparation error still acknowledges its
  request and recovers normally.
- Reject permanently unavailable manual run requests with a local reason and exit 3.
  Malformed request files and unreadable journals no longer block healthy work.
- Keep on-time `skip` occurrences between live ticks, accept embedded cron
  time zones, and report invalid triggers. Scheduled issue dates before a
  job's creation no longer fire.
- Distinguish invalid workflows and prompt validation from transient
  attempt-directory errors. Preserve observed hold reasons after notification
  refusal, and identify missing terminals at expiry as lost agents.
- Keep notification intent when Kata reports that a mutation may have
  committed. Set deadlines conditionally and read back an ambiguous result.
  Failure and hold observations put their reason code alone on the first line.
- Keep accepted agent summaries in Kata's final run evidence, including
  after a worker restart. Publish complete run journals atomically so an
  interrupted preparation does not block unrelated work.
- Keep command outcomes unknown when a receipt omits its exit code or records
  a value outside 0–255; malformed receipts cannot authorize a retry.
- Run and recover execute jobs through hc with durable admission, launch and
  settlement. Queue manual runs, read output and release held work through the
  headless CLI. Doctor checks Kata access from the core's execution environment.
- Configure the v2 headless adapter with strict TOML and explicit hc sessions,
  Kata daemon routing, project limits and plain argv profiles. The old Herdr
  plugin configuration, state and UI are replaced. Start with fresh v2
  configuration and state directories.
