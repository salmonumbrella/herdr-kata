---
title: Use the TUI
description: Open the TUI, inspect runs and workflows, open Kata, edit definitions and bind recurring parents.
last_edited: 2026-10-21
---

# Use the TUI

Open the TUI to inspect the runtime's snapshots and edit definitions in Kata.
Start the [runtime](../operations/runtime.md) first so its snapshots are current.
The [TUI reference](../reference/tui.md) lists every key, display rule and
screen field.

## Open the TUI

```sh
herdr-kata tui
herdr-kata tui --wide
```

In a terminal, `tui` runs there. Without a terminal, it opens a Herdr pane.
`--wide` opens a horizontal split of the caller's pane, or a tab when there
is no caller pane.

The startup hook uses `herdr-kata tui --startup` to open one pinned TUI in
the configured session, take focus and select its Kata tab as
soon as the runtime's first route check lands; it keeps trying for 90 seconds
and stops at the first key press. Quitting Kata lands on the Jobs tab.
`herdr-kata tui --pin` reports an idle **kata** sidebar row with
running and parked counts.
With no active workspace, the startup hook quietly skips opening the TUI.
To disable startup pinning, add this to `config.toml`:

```toml
[board]
pin = false
```

## Find and inspect a run

Press `2` for Jobs, `3` for Runs or `4` for Workflows; [Tabs](../reference/tui.md#tabs)
describes each list. Like Kata's issue list, Jobs and Runs lead with the
`#` and `status` columns, and the time column comes last. With several
configured or retained projects, an issue outside the default project shows as
`project#abcd`, and `/` also filters by project name. `/` matches an issue reference such as
`#abcd`.

Native recurring-child pickup continues in the runtime. Use `herdr-kata status`
for inbox scan health and Runs for pickup results. `kata tui` has no attention-request view
yet; an upstream contribution is in progress. Use `kata inbox` in the CLI to
read attention requests.

Select Runs and press Enter, `l` or right to open any retained run, including
one without an agent pane. Press `a` to attach to its agent. Use `space` to
expand workflow steps and archived attempts. Parked runs show `waiting: <reason>`
in the `detail` column; expand the row to inspect step progress. `[ ]` changes the run page;
`ctrl+d/u` or Page Down/Up scrolls a long step block without moving selection.

Run detail explains the outcome, stopping result, budget, saved token counts
and existing local artifacts. Its help and status stay pinned while the body
scrolls. `l` opens the originating job; `h` returns one frame with the
previous selection, filter and scroll restored. See
[run evidence](../reference/tui.md#run-evidence) for timing, usage and
artifact limits.

Select a parked run and press `u`
to request continuation after inspecting its report and finding chain. See
[Recover runs](../operations/recovery.md#resume-a-held-run) for the procedure
and [resume rules](../reference/holds.md#resume-rules) for acceptance.

To run a job now, select it on Jobs and press `R`. `R` uses the same request
path as `herdr-kata run`, including readiness and capacity checks. It refuses
native delivery jobs and unsupported jobs. A workflow needs a job before it
can run.

## Inspect a workflow

On Workflows, Enter, `l` or right opens the definition's steps, loop, prompt
budget, using jobs and retained runs. Broken definitions stay visible with
their diagnoses.

Use `j/k` to select a step, using job or run. Space expands a step's full prompt
or command. Enter opens a selected job or run; `h` returns with selection,
expansions and scroll restored. Page Down/Up scrolls without changing the action
target. Status and help stay pinned.

`R` runs the selected Used by job with that job's configured input. Workflows
are created and deleted with `kata cron workflow …`. Press `u` to resume a
parked run of the workflow; [Workflows](../reference/tui.md#workflows) says
which run each screen targets and how long the `running` display lasts.

## Open Kata

The leading **Kata** tab runs your configured `kata tui` inside the board body,
beneath the navigation bar. Click it, press `1` from a list, press `K` from a
list or detail screen, or use the plugin's **Open Kata** action. The `K`
shortcut is unavailable inside modals or text fields. At startup the pinned
TUI selects Kata once the runtime's route check is ready.

Press `Ctrl+]` or quit Kata with its own `q` action to restore the previous
board screen, back stack, selection, filter and scroll. Clicking the same
local tab restores that screen; clicking a different tab selects its list.
Kata keeps running while you use the local tabs; selecting Kata again
restores its screen. Select Kata again to start a fresh process after a quit
or failure.

`K` uses the selected row's project, or the only configured project. With
several projects and no selected row, it uses the same project picker as `n`.
The global action opens a board with that picker when needed. Run `i` opens
the run's issue in the embedded Kata process.

Kata starts only when the selected project's latest route check is recent and
allowed. [Embedded Kata](../reference/tui.md#embedded-kata) lists the
environment it receives and the route checks it needs.

## Edit or create a definition

On Jobs, press `n` to create or `e` to edit. With several projects, `n`
first asks for a project, defaulting to the selected row's project. Use
j/k or arrows and Enter to choose; Escape cancels. The picker shows local job
counts and route refusals and keeps its selection visible. With one project,
the editor opens directly. A missing local project pin refuses opening;
creating a job also needs a locally accepted route. Profile, checkout and
workflow choices belong to the captured project. A new recurring job uses
that project's inbox and saves disabled.

Profile is the model picker. It shows `<profile> · <model> <effort>` from
the profile's configured arguments. Left and right cycle through the captured
project's allowed profiles. A model without an explicit argument shows
`agent default`; an unspecified effort is omitted. Saving stores the profile
key, so model and effort stay in the profile configuration.

Use j/k or Tab/Shift+Tab to select a field. `l`, right or Enter opens text,
cycles a choice or toggles a boolean; left cycles choices backward. Space
toggles a boolean. While typing, arrows move the caret and Enter adds a newline
in a textarea. Ctrl+S commits that field; press it again from navigation to
save the job. `S` also saves from navigation. Validation keeps the entered
buffer and names the field to fix.

Escape, q from navigation, or left on a non-choice field leaves the draft.
With changes, `discard changes? y/n` asks first. `n` or Escape restores the
same field, caret and scroll. Unknown definition fields and large integers
survive edits. An unchanged Run at keeps its original offset and fraction;
changing only Timezone changes its display. See the
[editor field reference](../reference/tui.md#job-editor) for accepted values.

Favorite is local. Saving only that field works while Kata is offline and
makes no definition write. A combined edit publishes the favorite only after
the definition save succeeds. If its local write fails, the TUI says
`favorite not saved: … · retrying` and retries that exact preference on ticks,
even after leaving the job. It never repeats the definition save for this.
The editor returns to its caller with the selected run and scroll intact.

On Workflows, `e` opens the definition JSON in `$EDITOR`, defaulting to `vi`.
Save and exit to run local CHECK before submitting to Kata. See
[Edit and inspect workflows](workflows.md#edit-and-inspect) for CHECK,
guarded saves and conflict copies.

Mutation acknowledgements and deletion confirmations name the target project.
Pause, update, delete and prune carry the selected definition's event guard.
On conflict, the TUI shows the newer definition and keeps your draft and
original guard. If the newer definition is unavailable, the conflict still
blocks retry and keeps the original guard. Cancel and reopen to deliberately start from that newer
version. An uncertain save keeps the original UID and payload for readback
and retry; do not open another draft to repeat a possibly successful create.

## Bind a recurring parent

Select a recurring job and press `b`. This editor changes the parent shared
by all its jobs and unadmitted children, one metadata key per save with a
fresh issue revision. Prepared runs keep their frozen bindings.

New native recurring jobs are saved disabled. Verify their parent binding,
then enable with `p`. See [Run recurring issues](recurring-issues.md) for the
complete setup.

## Limits

- The TUI reads changed local snapshots every two seconds. Idle ticks never
  poll Kata or scan the inbox. Read screens and draft opening use local data;
  explicit definition, binding and Kata actions can make Kata calls.
- Definition and binding writes check route and authority first. The TUI
  never edits worker journals or checkpoints.
- Quitting the TUI leaves the runtime and its agents running. Use
  [Recover runs](../operations/recovery.md) for held work.
