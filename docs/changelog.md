---
title: Changelog
description: Changes to the headless Kata adapter.
last_edited: 2026-11-02
---

# Changelog

## Unreleased

- Keep command outcomes unknown when a receipt omits its exit code or records
  a value outside 0–255; malformed receipts cannot authorize a retry.
- Run and recover execute jobs through hc with durable admission, launch and
  settlement. Queue manual runs, read output and release held work through the
  headless CLI. Doctor checks Kata access from the core's execution environment.
- Configure the v2 headless adapter with strict TOML and explicit hc sessions,
  Kata daemon routing, project limits and plain argv profiles. The old Herdr
  plugin configuration, state and UI are replaced; v2 is under construction.
