---
title: Operations
description: Inspect output, recover workers and release held work.
last_edited: 2026-11-02
---

# Operations

Use Kata's Jobs and Runs tabs for run history. `herdr-kata doctor` checks the
configured execution environment and lists unsupported definitions. There
is no adapter status command or TUI.

`herdr-kata up` starts or adopts this installation's locked worker terminal,
named `herdr-kata-<state-path-hash>`. `stop` kills and removes only that
worker. Job terminals keep running. Start the worker again to recover them;
it never sends a prompt to an existing terminal. A foreground `serve` takes
the same lifetime lock. Definitions and scheduling are refreshed every
30 seconds; local requests are checked every 2 seconds.

## Held runs

Unknown or ambiguous execution holds a run, reserves its capacity and keeps
its terminal for inspection. The hold is immediately reported to Kata as
`unknown`, or `failed` when a step certainly failed, with `reason: <code>`.
The reason code stands alone on the first line; detail and step summaries
follow on later lines.
Per-run issues receive one notification. A blocked agent gets one
notification per blocked spell, rather than an attention metadata write.
Existing-policy issues receive no adapter writes, including notifications.

| Reason | Meaning |
| --- | --- |
| `launch_ambiguous` | Saved intent cannot identify exactly one matching launch |
| `launch_unproven` | The core changed and absence cannot prove no launch happened |
| `occupant_changed` | Terminal identity no longer matches the saved execution |
| `agent_lost` | The agent disappeared without a provable outcome |
| `no_result` | A completed reported turn has no valid result after its grace period |
| `no_activity` | A reported agent stayed idle at turn zero for 5 minutes |
| `timeout` | The frozen run deadline expired |
| `command_outcome_unknown` | A command lacks a valid ordinary-exit receipt |
| `invalid_prompt` | Prompt validation failed before launch |
| `invalid_workflow` | Workflow dependencies cannot be resolved |
| `invalid_note` | A command note is too long, malformed or unreadable |
| `agent_error` | The agent explicitly reported an error state |
| `report_refused` | Kata definitively refused a write or read-back differs |
| `admission_expired` | Issue creation could outlive its keyed replay window |

Inspect `herdr-kata logs <run-uid>`. Commands read their local `output.log`;
live prompts use a guarded `hc read`. Use the configured hc session when
inspecting a terminal yourself. Do not edit a run journal to retry a prompt.
After investigating, `herdr-kata release <run-uid>` posts a local release
request. Once the hold has been observed or reporting has been refused,
release frees the slot and overlap gate. It preserves the outcome and does
not rerun the attempt, remove its terminal or prove that work succeeded.

An agent reporting `needs-human` ends the run as `unknown` and keeps its
terminal. A lost or killed agent cannot be assumed successful from its exit
code. Never-reporting
agents can settle from an ordinary exit with a valid result, or a result
stable for 30 seconds. Reported idle/done agents require a completed turn;
a missing result has a 10-second grace period. Valid result files are regular
UTF-8 JSON files no larger than 64 KiB, with matching run/step/attempt
identity, status and a one-line note of at most 280 bytes. The prompt gives
the agent the exact `herdr-kata report` command.
Attempt-directory I/O errors are retried on later ticks rather than reported
as prompt validation failures.

## Recovery and isolation

Keep the state directory with its installation identity, checkpoints, run
journals and request files. Journals are durable before acknowledgement;
admission and final observation bodies are frozen before sending. Conflicts
are read back and compared. Uncertain notifications are never retried.
Accepted agent summaries are saved before terminal cleanup and included in
the final observation. Its message is bounded to 4,096 UTF-8 bytes. Run
journals publish as complete directories; interrupted unpublished staging
directories do not participate in recovery or deduplication.
Do not remove state to recover a run: doing so discards its duplicate-launch
barrier. v1 state cannot be migrated by copying it into v2.

Before interrupt, kill or remove, the worker checks core ID, terminal
creation time, PID and nonce. It leaves a changed occupant alone. A core
restart can therefore hold affected work instead of launching it again.

For runtime checks use a fresh `HC_SESSION=hk2-<random>`, scratch HOME and
XDG directories, isolated Kata home/catalog/daemon and `example-project`.
Remove inherited `HC_SOCKET`, `KATA_*` and native-agent configuration
selectors. Verify the scratch core state path and ID before terminal calls
and stopping it. Kill and remove only terminals the check created, then stop
both scratch services and remove their directories. Never use an operator's
session or real project. The gated `make test-live` automates these checks
on checksum-verified hc 0.1.16 and 0.1.15. Add
`HERDR_KATA_LIVE=1 HERDR_KATA_LIVE_CODEX=1` to include the real native Codex
case; only its login file is copied into the disposable native home.
`HERDR_KATA_LIVE_CODEX_SANDBOX` selects the native sandbox mode and defaults
to `workspace-write`. Use `danger-full-access` only where the operator has
chosen the surrounding container as the security boundary. The test does
not add host capabilities or disable the container's seccomp policy.
