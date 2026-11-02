---
title: Changelog
description: Changes to the headless Kata adapter.
last_edited: 2026-11-02
---

# Changelog

## Unreleased

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
