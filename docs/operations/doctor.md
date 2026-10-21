---
title: Diagnose setup
description: Read each herdr-kata doctor check and follow the fix for a failure.
last_edited: 2026-10-20
---

# Diagnose setup

Run `herdr-kata doctor` first when the runtime will not start, a project
stops admitting work or a run is held. It reports what is wrong and how to
fix it. It does not apply fixes, start the runtime or launch a profile.

```sh
herdr-kata doctor
herdr-kata doctor --json
```

Each line reads `PASS <check>: <detail>` or `FAIL <check>: <detail>`; the
JSON report holds the same `name`, `passed` and `detail` fields. Doctor exits
`1` when any check fails. With several configured projects, per-project
checks are prefixed with the project name, for example
`example-project: route`.

Doctor reads Kata and the local state directory. It can save a lost-run scan
cursor, and its launch-environment check opens and closes a temporary Herdr
workspace with no agent and no secrets. Local holds, quarantined journals
and unreadable control results are listed even when Kata is offline. Doctor
leaves these files unchanged. If `config.toml` is missing or invalid,
doctor prints the error and exits `1` without running the other checks; with
`--json` it reports a failed `config` check. A missing file's error names
its path and shows a minimal example.

## Findings and fixes

| Check | What a failure means | Fix |
| --- | --- | --- |
| `config` | The configuration could not be loaded. | Correct `config.toml`; see [Configuration](../reference/configuration.md). |
| `projects` | Never fails; lists the configured project names, or says no projects are configured. | To serve a project, [add a project](../reference/configuration.md#add-a-project). |
| `kata.bin` | The Kata executable is missing or not executable. | Set `kata.bin` to the absolute path of an installed Kata binary. |
| `checkout <key>` | A configured checkout does not exist or is not a directory. | Create the directory or correct `[checkouts]`. |
| `local run <run-uid>` | A run is held in a way only the operator can resolve, or its journal is quarantined. The detail names the reason, any recorded issue, unresolved writes and the remedy. A hold without an issue says `no issue created`. | Follow the remedy in the detail; see [Recover runs](recovery.md). |
| `local runs` | The run directories could not be read. | Restore ownership and read and search permissions on the named directory for the runtime user; rerun doctor. |
| `abandon report <run-uid>` | Loss reporting is pending or requires inspection. | Retry the exact `abandon` command after the shown backoff. For an inspected report, stop the runtime, resolve the issue separately, and preserve the whole report directory at the named quarantine path; see [lost local state](recovery.md#record-lost-local-state). |
| `control result <request-uid>` | A saved control acknowledgement is unreadable or contains fields this build does not understand. | For a decode failure, use the current build for status and controls. For a permission failure, restore read and search permissions for the runtime user. Preserve the state directory and inspect the result before repair; see [upgrade compatibility](../reference/cli.md#operator-controls). |
| `state layout` | The named `state.json` is unreadable, is a legacy single-project pin, or has an unsupported version. | For a recognized legacy pin or unsupported layout version, move the state directory aside and run `herdr-kata up` again; see [Inspect the state layout](runtime.md#inspect-the-state-layout). For a corrupt file, stop the runtime, preserve original artifact paths, and restore damaged state from trusted evidence or explicitly resolve existing occupants before initializing a separate state directory. |
| `definitions <project>` | The project's definition snapshot is missing, unreadable, more than 30 minutes old or in the older summary-only format. | Run `herdr-kata up` and wait for its next refresh; inspect `herdr-kata status --json`. A project that has not started yet passes with `snapshot missing`. |
| `unsupported definition <project> <job-uid>` | An enabled job cannot run here. The detail names the support reason. | Fix the definition or the configuration it needs; see [Job and workflow fields](../reference/definitions.md). |
| `issue volume <project> <job-uid>` | Never fails; an enabled per-run job creates more than 24 issues a day. The detail gives the estimate. | Optional: target an existing issue, add a [run parent](../guide/jobs.md#group-run-history-under-a-parent), or use [recurring children](../guide/recurring-issues.md). |
| `Kata credential` | The project credential is missing, unreadable or not private. | Fix the named project's `credential_file` or `credential_env` in `config.toml`. Restore an owner-readable file with mode `0600`, or export the configured variable. When the file is already owner-only, the detail says the macOS ACL read failed after 3 attempts; run doctor again, or remove an extended ACL with `chmod -N` when the detail names one. Startup hooks need `credential_file`; see [Configuration](../reference/configuration.md). |
| `kata version` | The daemon selector is unknown, the executable returned invalid Kata JSON, Kata is older than storage schema 33, the route is unavailable, or Kata rejected the credential. | For an unknown named daemon, fix `daemon` in the project's entry. For invalid JSON or schema, set `kata.bin` to Kata 0.19.0 (schema >= 33). For transport failures, check `kata.env` and restore the configured daemon or tunnel. For a rejected credential, replace the named project's `credential_file` or `credential_env` with a valid credential for its actor. |
| `kata commands` | The Kata CLI lacks `cron` or `events --tail`. | Install a Kata build that meets the [adapter contract](../reference/kata-adapter.md#transport-and-events). |
| `capabilities` | Kata could not resolve the project: access was lost, cron support is unavailable, its diagnosis is not ready, or project signing is blocked. | Check the project name, `kata.env`, the project's `daemon` and its credential variable. For missing cron support, upgrade Kata on the configured route; see the [adapter reference](../reference/kata-adapter.md#transport-and-events). For `signing_blocked`, the root owner runs `kata federation root-key repin --project <name>`. |
| `federation role` | Never fails; reports the route's federation role. | None. |
| `route` | Fails with the same error as `capabilities`; a pass shows the diagnosed route state. | As for `capabilities`. |
| `identity and route` | The saved pin is corrupt, or the Kata instance, project, actor, executor or route differs from it. | For a corrupt pin, stop the runtime and preserve original artifact paths. Restore damaged state from trusted evidence, or explicitly resolve existing occupants before initializing a separate state directory. For a mismatch, follow [Resolve a route mismatch deliberately](runtime.md#resolve-a-route-mismatch-deliberately). |
| `configured actor` | The authenticated actor differs from the configured `actor`. | Use the credential for the configured actor, or correct `actor`. |
| `lane lease` | Another installation holds the lane's lease live, or the lease could not be read. Passes when the lease is free, expired or this installation's. | Stop the other installation, or move the lane here with `herdr-kata up --takeover`; see [lane ownership](../reference/kata-adapter.md#lane-ownership). An unreadable lease is retried by the lane. |
| `lost-runs` | Kata shows running observations or outstanding explicit-request receipts from this installation whose local state is missing. | Follow [Record lost local state](recovery.md#record-lost-local-state). |
| `duplicate project UID` | Two `[[projects]]` entries resolve to the same Kata project. | Remove one entry. |
| `session` | The calling Herdr session differs from `session` in the named config file. | Run doctor inside the configured session or correct `session`. |
| `herdr` | The Herdr socket for the configured session did not answer. | Run `herdr --session "<session>" server`, or fix `session` in `config.toml`. |
| `profiles` | Never fails; lists profiles as `not probed`. | None. Doctor never launches a profile. |
| `profile <name> session` | Never fails; warns when a Codex profile turns hooks off with `-c features.hooks=false` or `--disable hooks`. Its agent never reports a Herdr session, so a later prompt step or agent switch holds. | Remove the named argument from `profiles.<name>.args`. |
| `agent limits <kind>` | Never fails; says whether that agent kind's newest local session log has a recognized format. | None. Quota and login detection is unavailable without one; see [Agent quota and login](../reference/holds.md#agent-quota-and-login). |
| `secret <name>` | A `[secrets]` source is missing, empty, unreadable or not private. | Fix the source; see [Secrets](../reference/configuration.md#secrets). |
| `Herdr launch environment` | An agent pane would inherit `KATA_*` names or a credential, or the probe could not run. | Remove or guard the exports; see [Secrets](../reference/configuration.md#secrets). |

## Limits

- `profiles` and `agent limits` never launch an agent, so a broken agent
  install shows up only when a run starts.
- `lost-runs` scans outstanding runtime inbox requests and at most ten history pages per invocation; run doctor
  again to continue a larger scan.
- Doctor reads definitions from the saved snapshot and never refreshes them.
