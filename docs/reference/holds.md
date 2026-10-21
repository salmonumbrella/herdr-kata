---
title: Hold reasons and resume rules
description: What each hold reason means, what to inspect and what an explicit resume does.
last_edited: 2026-10-19
---

# Hold reasons and resume rules

A held run has stopped automatic execution and waits for an operator. This
page answers "this run is held; what does resume do?" Follow
[Recover runs](../operations/recovery.md) for the procedures.

## Hold reasons

Every row requires inspection through [Recovery](../operations/recovery.md).
Releasable holds can be handed off. No row authorizes automatic replacement
execution.

Each hold has one class:

- **Pre-admission:** No outcome is set. The execution slot and overlap gate
  are free, and resume is refused. Release retires `create_rejected`; either
  pre-admission reason can be resolved offline with `abandon --local`.
  `admission_rejected` requires that offline resolution unless an
  [explicit request](kata-adapter.md#explicit-issue-requests) awaiting admission
  confirmation later proves the exact receipt for its original journal. That confirmation can continue the
  original admission; it never creates a replacement run.
- **Step-failure/loop:** Outcome is `failed`. The overlap gate stays held;
  the execution slot can be freed after the occupant is proven stopped.
  Inspect, then explicitly resume under the [resume rules](#resume-rules),
  or release to hand the work off without resuming.
- **Uncertainty:** Outcome is `unknown`. The overlap gate stays held;
  a possibly live occupant keeps its execution slot until it is proven stopped.
  Inspect, then explicitly resume under the [resume rules](#resume-rules),
  or release to hand the work off without resuming.

See [Slots and overlap](state.md#slots-and-overlap) for reservation rules.
`signing_refused` is an operation flag, not a hold reason; see
[Access loss and root signing](kata-adapter.md#access-loss-and-root-signing).

| Reason | Class | What to inspect |
|---|---|---|
| `admission_rejected` | pre-admission | Admission was refused or remains unconfirmed. Inspect the saved reason. Resolve offline; an unconfirmed [explicit request](kata-adapter.md#explicit-issue-requests) can instead confirm its exact saved receipt. |
| `create_rejected` | pre-admission | A fatal create refusal stopped admission. Fix the definition, then release or abandon locally. No automatic retry. |
| `secret_unavailable` | uncertainty | A step could not read a configured secret. Its detail names the source. Fix it and resume; the unsent step keeps its attempt. |
| `herdr_env_unclean` | uncertainty | Herdr launch environment is unproven or leaks unexpected `KATA_*` names or credentials. Fix shell startup exports or the server environment as described in [Configuration](configuration.md#secrets), run `doctor`, then resume. No agent was started. Even the first manual request creates a visible held run; repairing the environment does not release it without resume. |
| `timeout` | uncertainty | Run expiry reached; the prompt agent may still be running. |
| `prompt_budget` | uncertainty | The generation has spent its frozen prompt cap; no new attempt is submitted or launched. Inspect before authorizing resume. |
| `no_result` | uncertainty | Settled agent did not leave a valid report during the ten-second file grace. |
| `agent_lost` | uncertainty | Saved agent disappeared; inspect pane and attempt artifacts. |
| `no_activity` | uncertainty | Five minutes without activity or a valid result; inspect whether the prompt ran. |
| `agent_quota`, `agent_login` | uncertainty | Structured agent limit or login failure; see [Agent quota and login](#agent-quota-and-login). |
| `agent_start_failed` (optional Herdr error suffix) | uncertainty | Launch failed or the agent vanished during readiness. Inspect the pane. |
| `launch_timeout`, `pane_busy` | uncertainty | Startup readiness or foreground shell was not ready within its bound. |
| `launch_unproven` | uncertainty | Saved launch intent cannot prove the occupant. |
| `agent_unproven`, `submission_unproven`, `recovery_unproven` | uncertainty | Agent identity, resubmission proof or recovered state does not match. |
| `agent_switch_unproven` (optional `: agent reported no Herdr session`) | uncertainty | Previous attempt's agent has not been proven settled before switching. The suffix means the agent never reported a Herdr session, usually because its profile turns Codex hooks off; `doctor` names the argument to remove. Exit the agent in its pane, then resume. |
| `command_no_receipt` | uncertainty | Command may have started; no certain receipt exists. |
| `command_outcome_unknown`, `command_group_unproven` | uncertainty | Interrupted command, uncertain output/descendants or process group still present. |
| `invalid_note` | uncertainty | Current command note is not a valid bounded regular file. |
| `invalid_prompt` | uncertainty | Expanded prompt exceeds its bound or cannot be written. |
| `execution_error` | uncertainty | Execution returned an error; read runtime logs and journal. |
| `step_failed` | step-failure/loop | An exhausted step failure was held by the workflow policy; inspect its report or receipt before resuming. |
| `loop_pending` | step-failure/loop | Checker failure saved in `run.json` without a committed rewind decision. Inspect the saved verdict; restart never takes the edge implicitly. |
| `loop_exhausted` | step-failure/loop | The additional-visit budget is spent. Inspect the latest checker report and finding chain. |
| `loop_stuck` | step-failure/loop | Consecutive nonempty rejection notes match after whitespace normalization. Inspect the repeated diagnosis; empty notes rely on budget alone. |

If both stop conditions apply, budget exhaustion is checked first. All three
loop holds keep the job gate and their attempt evidence. Only a verdict
frozen in `run.json` counts as saved for `loop_pending`; a result artifact
still needs fresh occupant settlement validation before it becomes a
verdict. See [Inspect a loop hold](../operations/recovery.md#inspect-a-loop-hold)
for inspection.

## Resume rules

`herdr-kata resume <run> [--reset-loops]` asks the worker to continue a held run.
Use `u` on a selected held run in the TUI for a resume without a loop reset.
Both route through the project UID saved in the run; no project selector is
needed. If its lane is missing, the request waits until that lane returns.
The journal must be intact and belong to this installation. Quarantined,
handed-off and settled runs cannot resume. [Pre-admission holds](#hold-reasons)
cannot resume; resume cannot grant admission authority.

The worker waits for the run's report queue to drain successfully and for a
free slot. A run keeps its overlap gate; its own reservation does not block
resume under `overlap: forbid`. Every saved prompt occupant and command group
must be freshly proven stopped, even if an earlier check released the slot.
Working agents and unknown provenance refuse resume. The runtime never kills
an agent to make resume possible.

The pinned route must still be allowed. The same issue must be open and ready,
and a native child must still belong to the configured actor. Closed or
reassigned children are refused, never reopened or reclaimed. Resume does not
need another child or inbox notification.

### Resume targets

| Hold reason | Resume target |
| --- | --- |
| `loop_pending` | Finish the saved failed verdict's edge once, apply budget and stuck checks, and continue at `goto` |
| `loop_exhausted`, `loop_stuck` | Re-run only the checker; `--reset-loops` starts a new span visit at `goto` |
| [Uncertainty](#hold-reasons), `step_failed` | Re-run the held step |

### Prompt budget and loop counts

`prompt_budget` holds before a new prompt attempt can launch or submit.
Inspect its saved count and reports before authorizing another generation.
Every accepted resume resets the generation's prompt count to zero against
the original frozen cap. Both plain resume and `--reset-loops` do this; neither
refreshes spent step retries. Redelivering the same request grants no new budget.

`--reset-loops` also clears spent loop counts and the remembered rejection.
Plain resume preserves them.
A pending edge that hits its budget or stuck check remains held with that
reason; it does not rediscover the verdict by rerunning the checker.
If every step finished before a reporting timeout, resume settles the saved
results without another execution attempt. Exhausted failures under the default
policy remain part of that aggregate.
Steps completed outside an authorized loop rewind stay complete. Each new
attempt has a higher number, so the earlier prompt, receipt and result
artifacts remain available.

### Acceptance and reporting

Acceptance durably consumes the request UID and records its generation, target,
new attempt, slot reservation and one fresh expiry. That expiry uses the
timeout frozen at admission. Changes to live configuration do not affect it;
steps, retries and loops share it. Journals created before frozen timeouts
were supported cannot resume; use release instead.

A queued `running` observation keeps the original start time and clears the
end time. Execution stays held until that effect and owned attention writes
are confirmed. Conflicts or inspection requirements prevent activation and refuse release
while the pending resume tail requires inspection.
Recovery leaves pending reports in the write queue so startup and other
controls can continue. The control scan finishes the saved transition after
confirmation, even if its request file is absent. Redelivering a consumed request
never allocates another attempt, even after another hold. Earlier reports stay
unchanged; settlement operations after resume use the consumed request UID.
Unrelated notifications are left alone. Release cancels pending activation and
unsent resume writes; any sent write is resolved before the handoff report.
Queued releases take priority over pending resume activation, including after
a restart.

See the [CLI reference](cli.md) for request output and attempt
listing, and the [TUI reference](tui.md) for workflow displays.

## Agent quota and login

Herdr reports an agent's session identity, never why a turn stopped. For
Codex and Claude, herdr-kata reads that agent's main session file,
read-only and best-effort, to recognize a usage limit or a failed login.
Every other agent, and any file that cannot be read, keeps the usual hold
reasons. Nothing here adds a Kata write.

### Finding the session

The occupant's saved `agent_session` must have source `herdr:codex` and
agent `codex`, or source `herdr:claude` and agent `claude`, of kind `id`.

- Codex: `$CODEX_HOME/sessions/<local date>/rollout-*-<id>.jsonl` (else
  `$HOME/.codex`), for the saved launch date and the days on each side. The
  ID must be a UUID.
- Claude: `$CLAUDE_CONFIG_DIR/projects/<cwd slug>/<id>.jsonl` (else
  `$HOME/.claude`), then `projects/*/<id>.jsonl`.

These variables are the agent pane's, as the worker's last passing
launch-environment probe saw them, so a `CODEX_HOME` set only for the Herdr
server still counts. Before any probe has passed in this worker, the
worker's own environment is used. Token usage reads the same file. The slug replaces every
  character outside `[A-Za-z0-9]` with `-`.

Only one matching main-session file is accepted. Compressed, reverted,
ambiguous or unrecognized files are ignored. Runtime lookup does not crawl
directories or poll logs. The tail scan reads backward in 64 KiB chunks,
at most 8 MiB, with a 1 MiB line limit. An exceeded bound, malformed record
or invalid timestamp leaves the usual reason intact.

### When a stop is classified

Only when a prompt attempt ends without a result and its occupant is proven
stopped: the matching agent settled, or its pane exited or returned to its
saved shell after the agent disappeared. A hold over a possibly working
agent, such as `timeout`, is never classified. A valid result is never
reclassified. When an agent becomes blocked, a read-only pass may name quota
or login in the blocked notification and attention text; it saves no limit
or verdict. The same attempt continues after unblocking.

### The attempt's span

Only the last terminal record timestamped at or after the saved submission
counts. A new submission to an occupant happens only after it is idle or
proven stopped. A late record from an older attempt, even from a timed-out
agent that kept running, therefore cannot classify a newer attempt.
A normal terminal record after a recovered error prevents classification.

| Agent | Quota (`agent_quota`) | Login (`agent_login`) |
|---|---|---|
| Codex: `task_complete` / `turn_aborted` `error.codex_error_info` | `usage_limit_exceeded`, `rate_limit_exceeded` | `unauthorized`, or `http_connection_failed.http_status_code` 401 / 403 (saved as `codex:unauthorized`) |
| Claude: an `isApiErrorMessage` assistant line's `error` | `rate_limit`, `billing_error` | `authentication_failed`, `unauthenticated`, `invalid_grant` |

### The limit hold

A match replaces `no_result`, `no_activity` or `agent_lost` as the hold reason.

- The step records `Limit {Kind, Code, ResetsAt}`. For Codex quota, the reset
  comes from the newest preceding `token_count` with a full rate-limit
  window; if both windows are full, the later reset wins. Claude reset
  prose is not parsed.
- Outcome stays `unknown`, phase stays `held`, and the usual slot and
  overlap rules apply. A saved hold remains final across recovery.
- Reporting uses the existing unknown-outcome path, with text built from
  recognized codes and reset times. Transcript messages never enter state,
  comments or notifications.
- It consumes no retry or loop. The submitted attempt stays counted in its
  generation's prompt budget. Execution checks retry exhaustion with
  `Attempt - Uncharged > Retries`; recovery checks remaining retries with
  `Attempt - Uncharged <= Retries`, keeping its result and proof conditions.

### Resuming a limit hold

The run waits for an explicit resume under the [resume rules](#resume-rules).
There is no automatic resume. Acceptance and stopped-proof checks stay the same.

- Resume continues at the held step in a new attempt. The accepted held
  attempt is never sent again. `Uncharged` increments when that classified
  attempt is replaced.
- The new prompt names the previous attempt's usage limit or login failure
  and asks the agent to check for partial work before redoing it.
- A proven idle shared agent takes the new attempt. A closed tab, or a missing
  agent whose pane returned to its saved shell, gets a fresh agent. A step
  configured for a fresh agent keeps that requirement.
