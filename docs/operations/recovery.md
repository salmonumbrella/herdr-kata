---
title: Recover runs
description: Inspect held runs, resume or hand them off and record runs whose local state was lost.
last_edited: 2026-10-20
---

# Recover runs

Inspect uncertain work before freeing it for a human to handle. herdr-kata
keeps held runs and never sends an already possibly delivered prompt again
on recovery. Kata history alone cannot authorize another execution.

On Linux and macOS, a killed worker's Kata event streams stop even when their
projects are idle. A replacement worker starts fresh streams from the saved
cursors.

## Recover a final run observation

1. Run `herdr-kata doctor --json`. A `final run <uid>` failure names a local
   terminal outcome that differs from Kata, including a run still `running`.
2. Restore the pinned route if it is unavailable.
3. Restart with `herdr-kata stop` then `herdr-kata up`. Worker startup
   recovers the saved observation through the guarded write queue.
4. Run doctor again. Inspect any remaining issue-reporting refusal with
   `herdr-kata logs <run-uid>`; final observation recovery does not clear an
   unrelated ambiguous issue write.

Recovery preserves an operator handoff and its known outcome, otherwise
`unknown`. It never invents success, changes a saved observation to overwrite
newer Kata evidence or resends a prompt. [Reporting](../reference/reporting.md)
owns the exact read-back and retry rules.

## Find the run and read its reason

```sh
herdr-kata status --json
herdr-kata runs --job <job-uid> --limit 20
herdr-kata logs <run-uid>
herdr-kata doctor --json
```

In the [TUI](../guide/tui.md), select Runs (`3`), open the detail with `l`,
and use `a` to focus its saved agent. Use `i` to open the run's Kata issue.
Command-only runs have no agent tab.

State reads retry concurrent atomic replacements. After three unsuccessful
attempts, `file changed while opening` reports that the file kept changing;
retry the command.

Read the held reason and the current attempt's result or command receipt.
Common reasons include `timeout`, `no_result`, `no_activity`, `agent_lost`,
`agent_start_failed`, `launch_unproven` and `command_outcome_unknown`. These
distinguish missing evidence from a known failure; do not assume a held run's
agent has stopped. [Hold reasons](../reference/holds.md#hold-reasons) lists
what to inspect for each.

When an agent disappears without a result, the runtime holds `agent_lost`
as soon as it proves the saved pane exited or returned to its shell. It does
not wait for the run expiry or send another prompt. A missing agent name
alone is insufficient proof.

A blocked startup screen requests human attention and a notification on the
run issue. Dismiss the screen in the saved pane. The prompt waits until Herdr
reports the agent interactive-ready with no pending launch.

Codex's hook review also blocks submission, even if Herdr reports
interactive readiness. That covers the first-run `Hooks need review` modal
(`N hooks are new or changed`), its `Trusting hooks...` state, and the hooks
browser that `Review hooks` opens. Trust or skip the hooks in that pane. The worker checks the visible screen every 30 seconds
and submits the unsent task once the review clears. If the run expires first,
inspect the pane and resume the held run. An unreadable screen holds the run
as unproven; a possibly delivered prompt is never sent again.

On macOS, an exited command's empty process group can briefly remain visible
to a signal check. The worker confirms that the group has no process members
before treating it as stopped. A surviving member or failed membership check
still requires inspection.

A failed step's note appears in its issue comment and attention message.
Held comments lead with the hold reason. Command steps held before execution
say `not started`; a started command with missing evidence remains uncertain.
After a resumed run succeeds, one short comment records the recovery.

A held run keeps its overlap gate. It can also keep an execution slot until
the runtime proves its saved agent or command has stopped. This may block
later occurrences even when the UI looks idle. The
[adapter contract](../reference/kata-adapter.md) describes execution and reporting.

## Resume a held run

Resume continues a held run in a new attempt at the held step, or at the
loop target the hold names. It never replays a possibly delivered prompt.

1. Inspect the run, its agent and the held attempt's artifacts as above.
2. Confirm that every saved agent and command has stopped. Resume refuses a
   working agent; the runtime never kills one to make resume possible.
3. Request continuation:

   ```sh
   herdr-kata resume <run-uid>
   ```

   In the TUI, select the parked run and press `u`. To start a new loop
   visit instead of rechecking, add `--reset-loops` to the command.
4. Run `herdr-kata status --json` again to confirm the worker accepted the
   request.

The command writes a durable request; it needs no project selector. The running worker
handles it as described in [Operator controls](../reference/cli.md#operator-controls).
[Resume rules](../reference/holds.md#resume-rules) define acceptance, the
resume target for each hold reason and the prompt budget a resume grants.
[Pre-admission holds](../reference/holds.md#hold-reasons) cannot resume; see
[Quarantine an admission or signing hold](#quarantine-an-admission-or-signing-hold).

### Inspect a loop hold

A held [maker/checker loop](../reference/definitions.md#makerchecker-loop)
keeps its overlap gate. Inspect the current checker report, its note and the
run's saved finding references before handing it off. `loop_exhausted` and
`loop_stuck` name deliberate loop stops; `loop_pending` means a failed verdict
was saved in `run.json` without a committed edge decision. A restart never
authorizes that missing rewind.

A pending comment write continues through the saved write queue while the run
is held. Its exact keyed retry recovers a lost reply and the canonical comment
UID. Do not post a replacement finding to work around an unresolved write.

Use `herdr-kata release <run>` to hand the work to an operator. Release retains
the artifacts and releases the gate; it does not run the maker or checker again.
A result artifact still needs fresh occupant settlement validation before it
becomes a verdict.

### Resume after an agent limit or login failure

`agent_quota` means the agent's structured session log ended at a usage,
rate or billing limit. `agent_login` means it ended with a login failure.
[Agent quota and login](../reference/holds.md#agent-quota-and-login)
lists supported agents, codes and lookup bounds. `status` and the TUI show a
Codex reset time when one is available; times are UTC. Claude reset prose is
not parsed. `doctor` reports whether each profile kind's newest local main
session file has a recognized format. That diagnostic is advisory and never
launches a profile. No session log means detection is unavailable.

For quota, wait for the allowance to reset or resolve billing. For login:

- Claude: run `/login` in the agent pane.
- Codex: run `codex login` using the agent's `CODEX_HOME`, then close its tab.

Then request continuation:

```sh
herdr-kata resume <run-uid>
```

Resume keeps the held attempt's artifacts and allocates a new attempt at the
same step. Its prompt warns about partial work: a quota hit may follow real
edits. A proven idle shared agent keeps its context; a closed tab gets a new
agent. A missing agent whose pane returned to its saved shell also gets a
fresh agent. Fresh-agent steps stay fresh. The limit attempt consumes no retry or
loop. A login that still fails holds again. Resume never happens automatically.

## Hand work to a human

After inspecting the unfinished run and its agent, request release. Settled
runs are refused without changing their journal or writing to Kata:

```sh
herdr-kata release <run-uid>
```

Release hands off the run, frees its slot and overlap gate, and queues its
report. It keeps the tab and never kills the agent. Check whether that agent
is still working before allowing later jobs to overlap with it.

The command writes a durable request. See
[Operator controls](../reference/cli.md#operator-controls) for worker acceptance
and refusal. A request waiting for admission stays queued. Inspect status again
to confirm the handoff has happened.

A valid `result.json` that arrives after a hold does not settle the run
automatically. Inspect it, then resume the held run or use release. A handoff never authorizes the
same occurrence to execute again. Any manual run is a separate occurrence.

## Record lost local state

`doctor` lists running Kata observations attributed to this installation
whose local run directories are missing. It scans at most ten history pages
per invocation and saves its cursor in `doctor-lost.cursor`; run it again
to continue a larger scan. It also checks outstanding runtime inbox
notifications for explicit-request admission receipts from this installation.
An admitted request with no local run directory appears in doctor and the
inbox status with an `abandon` command. Requests with a matching terminal
outcome are already finished and are excluded.

First check for a still-running Herdr agent and recover the original state
from a backup if available. When the local directory is genuinely lost:

```sh
herdr-kata abandon <run-uid>
```

`abandon` checks installation and run identity, then records `unknown` with
summary `local state lost` and posts a keyed comment on its issue. Recognizable
runtime-created issues that are still open and unowned also receive guarded
`needs-human` attention and a loss message. Existing issues and closed or
reassigned issues receive the comment only. It refuses while the local run
directory exists. If the issue was deleted or is missing, `abandon` still records
the loss, exits successfully, and says the issue is gone. Existing queued issue
operations are retired with their frozen evidence preserved.
An uncertain observation is retried with its saved payload. A matching
confirmed observation is recognized on retry; a changed remote report is
refused. Issue reporting uses a separate durable queue beneath
`abandoned/reports/`; it cannot recreate execution state or relaunch the work.
Doctor and status list unfinished loss observations and issue reports, including
the exact project and retry command. A pending report returns an error; retry
`abandon` after the indicated backoff to finish the saved report. A fatal report
names its refusal and the complete source and quarantine directories: stop the
runtime, inspect the preserved report, resolve the issue separately, and move
the entire report directory into the named quarantine directory before
restarting. This acknowledges the report without retrying a rejected write.
If a retry automatically quarantines a corrupt report journal, doctor and status
keep showing the unfinished report. Inspect the quarantined journal and resolve
the issue separately. Preserve the complete remaining source directory in a new
directory beneath the existing quarantine path, then restart. Automatic journal
quarantine alone does not acknowledge the report.
For a cron loss report, if an issue changes after the loss comment lands,
the comment is sufficient: attention writes stop, their frozen intents remain
for inspection, and abandonment completes without overwriting the issue change.
Signing-refused reports block new admission on their project, even after route
diagnosis recovers. Repair root signing and retry `abandon` to deliver the saved
operation. If that report requires inspection, stop the runtime and preserve
its complete directory from `abandoned/reports/runs/<uid>/` under
`abandoned/reports/quarantine/runs/<uid>/` before restarting. This retires the
report without deleting its signing evidence; resolve its issue separately.
A quarantined report cannot be recreated by another `abandon`. Repeated
completed calls write nothing.

For an explicit request, `abandon` needs its outstanding runtime notification
and matching admission receipt instead of a cron run record. It records
`settled` / `unknown` with reason `local state lost` in `herdr_kata.outcome`,
posts the loss comment, marks the actor-owned open child `needs-human`, and
acknowledges only the saved runtime notification. It preserves the permanent
admission receipt. A changed receipt, outcome or owner requires inspection.
An inspected outcome or acknowledgement is not reset by another `abandon`,
even if the issue changes back. An unconfirmed write remains read-back-only.
Resolve the issue separately and preserve the complete report at its shown
quarantine destination, using the report-preservation procedure above. This
keeps the loss record and its frozen write evidence.
The separate reporting journal can finish pending, uninspected delivery after
restart without recreating execution state. Repeating a completed abandonment writes nothing, even after
the notification is gone.

For unreadable run directories, damaged layout markers or corrupt project pins,
follow the [doctor findings and fixes](doctor.md#findings-and-fixes).

## Inspect a pending request acknowledgement

A refused request keeps its acknowledgement in the reporting-only queue.
If the worker stops after Kata acknowledges the notification but before the
local completion is saved, the pending report remains visible in doctor and
status. A notification that is already gone or changed cannot drive another
inbox retry; the runtime does not reconstruct work from that report.

1. Inspect the reporting journal and its issue. The report UID shown by doctor
   identifies `abandoned/reports/runs/<report-uid>/run.json`; it is not a new
   execution run. Compare the saved notification guard with the current issue.
2. If the saved matching notification remains, let the next inbox scan check
   the saved acknowledgement. If it is gone or changed, resolve any outstanding
   notification on the issue separately.
3. For that manual resolution, run `herdr-kata stop`. Preserve the complete
   reporting directory at the quarantine destination shown by doctor or status,
   then run `herdr-kata up`. Keep the report's frozen intents as evidence.

## Inspect quarantined journals and leftover tabs

A corrupt journal is moved under `quarantine/runs/<run-uid>/`. That entry
blocks fresh preparation of the same UID. Preserve it for inspection rather
than deleting it to unblock a job. A quarantined journal no longer reserves
capacity or its overlap gate; inspect its agent before trusting the active
run count.

Retention continues after a successful quarantine, so other expired journals
can still be removed. A failed quarantine or directory sync stops the pass
and surfaces the error.

To acknowledge an already quarantined journal, first inspect its preserved
artifacts and stop any original occupants. Then run `herdr-kata stop` and
`herdr-kata abandon <run> --local`. If that call discovers a corrupt journal,
it preserves the journal and acknowledges its quarantine in the same call.
This records `acknowledged.json`; doctor
stops treating that quarantine as unresolved. It preserves the artifacts and
permanent admission veto, including when the journal cannot be decoded.
Resolve any remote issue or observation separately.

### Quarantine an admission or signing hold

A rejected admission holds `admission_rejected`. An explicit request whose
admission reply is still unconfirmed can continue when its exact saved receipt
appears; see the [admission rules](../reference/kata-adapter.md#explicit-issue-requests).
For a hold that remains unresolved, or a run
carrying `signing_refused`, resolve the whole local
run offline:

```sh
herdr-kata stop
herdr-kata abandon <run> --local
```

`--local` refuses while either runtime lock is held, and requires the run's
occupants to be proven stopped. A command with a valid interrupted or uncertain
receipt, or an attempt-bound `process.json` start record, can be quarantined once
its process group is proven gone; its outcome
and receipt stay unchanged, and command-only runs need no Herdr connection.
Occupant checks have a 30-second deadline; timeout keeps the journal and releases
both runtime locks. It makes no Kata write. One rename moves the
whole run, artifacts and unresolved operations to `quarantine/runs/<run>/`.
That directory permanently vetoes preparation, delivery and execution of the UID.
Retry the command after an interrupted rename or directory sync; a completed
retry is a no-op. If both source and quarantine directories exist, it refuses
and moves nothing. `release` refuses `admission_rejected`. Release does not
clear a pending operation's `signing_refused` flag; that evidence remains until
the exact operation succeeds or the whole run is quarantined. Release also
refuses an inspected post-admission tail, because the blocked write would
fence its handoff report.

`doctor` lists these local holds, orphaned issues and signing refusals even
when Kata is offline. It also lists quarantined journals and their unsent or
in-flight operations. The owner resolves any orphaned issue separately. Once
the local directory is gone, plain `abandon` retains its existing checks and
can mark this executor's still-running remote record unknown.

A fatal pre-admission create uses `create_rejected` and saves the original
refusal. Its capacity and overlap gate are already free. Repair the definition,
then use `release` to retire that local run without Kata writes, or stop and
use `abandon --local`. Neither action retries the failed create.

### Inspect leftover tabs

A crash between creating a tab and saving its identity can leave an empty
Herdr tab. The runtime cannot prove it owns that tab and leaves it alone.
Inspect the Herdr workspace and remove a confirmed empty tab manually.
Status cannot identify this gap reliably. Saved tabs close automatically only
when full server, pane, terminal and agent-session identity still matches.

After a resume, the proven successful attempt's tab closes. An original tab
whose agent exited before recording an `agent_session` remains unproven and
stays open, even after success. Inspect it and close it manually in Herdr.

## Keep the evidence you need

A daily retention pass removes settled, fully reported local run directories
older than 14 days. Held, blocked, handed-off, unknown, inspectable and
signing-refused runs remain. Staged report binaries remain while retained runs reference them.
Local retention does not remove Kata issues or their run evidence.

## Read saved issue references

Admission saves the resolved project and authoritative issue references with
the run. The board reads these local references without querying Kata.

Definition refresh also resolves missing issue and parent references on saved
job bindings, including native parents with no local child runs. Each refresh
allows at most 32 reads in 15 seconds; saved references are reused across
refreshes and definition edits. Unresolved bindings retry on a later refresh.

## Limits

- Resume requires a journal with a frozen timeout. Inspect and release older
  held runs; do not edit their journals to continue them.
- A command that may have started without a valid receipt becomes unknown;
  recovery does not run it again automatically.
- Lost state, a quarantine entry or a terminal Kata observation is never
  permission to resend a prompt.
