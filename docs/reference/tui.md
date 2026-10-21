---
title: TUI reference
description: TUI keys, tabs, colors, navigation, screen fields and the embedded Kata tab.
last_edited: 2026-10-21
---

# TUI reference

Open with `herdr-kata tui`; [Use the TUI](../guide/tui.md) covers setup,
forms and the everyday tasks.

| Key | Action |
| --- | --- |
| `1` | Select the embedded Kata tab |
| `2`–`4` | Select Jobs, Runs or Workflows; ignored in details |
| `tab`, `shift+tab` | Cycle Kata, Jobs, Runs, Workflows forward or backward; ignored in local details |
| `L` | Toggle the list inspector on Jobs, Runs and Workflows (`layout`) |
| `j`, `k`, arrows, wheel | Move selection; scroll details without selectable rows |
| `[`, `]`, `ctrl+b`, `ctrl+f` | Page the list cursor; ignored in details |
| `pgup`, `pgdown`, `ctrl+u`, `ctrl+d` | Scroll the viewport by a full page without changing the action target |
| `l`, right, `enter` | Open read-only detail or follow its selected link |
| `h`, left, `esc`, backspace | Return one detail frame; list Escape clears the filter |
| `a` | Attach to the selected run's agent; a missing pane leaves the record open |
| `R` | Request an adapter job run through the worker; native jobs and inbox requests are refused |
| `u` | Request resume of the selected parked run; on Runs and linked run/job details, admission holds show their stop/abandon remedy without sending a request |
| `p` | Toggle the selected job's enabled state |
| `f`, `F` | On Jobs, favorite locally or show finished one-shots; ignored in job detail |
| `P`, `D` | On Jobs, prune matching finished one-shots or delete; job detail accepts `D`; `y` confirms |
| `space` | Expand current steps and archived attempts on runs, a workflow's steps on Workflows, or the selected workflow step's text |
| `/` | Edit the live filter; Enter or Escape keeps it; a later list Escape clears it |
| `e`, `n` | Edit or create a job; `e` edits a selected workflow in `$EDITOR` |
| `b` | Open a recurring job's shared parent binding |
| `r` | Refresh local snapshots; overlapping requests coalesce into one follow-up |
| `i`, `K` | Open a run's issue or the selected project in the [Kata tab](#embedded-kata) |
| `M`, `q` | Release/reclaim the mouse or quit |
| `ctrl+]` inside Kata | Restore the previous board screen and navigation; Tab and Shift+Tab cycle tabs; other keys belong to Kata |

The `u` key sends a durable control request to the worker. The status reports
that the request was sent; execution still needs the
[resume acceptance checks](holds.md#resume-rules).

## Tabs

| Tab | Contents |
| --- | --- |
| `1` kata カタ | Your configured `kata tui`, embedded beneath the navigation bar |
| `2` jobs | Definitions, models, schedules and latest local run status |
| `3` runs | Local run status, captured models, reasons, agents and steps |
| `4` workflows | Steps, input requirements, using jobs and retained runs |

The tabs occupy the top of the pane, without a brand or version line. Nonzero
running and parked counts align to the right of the tab row. Unhealthy runtime
status appears there in the attention color; healthy runtimes show no status.
At narrow widths, health takes priority, then compact totals, then project detail.
The active tab stays visible. The Kata tab uses the same health and counts; its
`ctrl+]` hint uses spare space and never displaces a tab. A truncated hint ends
with an ellipsis.

The active tab is bold and has an open bottom border, including in `none`
and `NO_COLOR` modes. In color modes it is also pink.
Jobs, Runs and Workflows fill the pane width. Each has one flex column that
takes the width the other columns leave: `job` on Jobs, `detail` on Runs and
`workflow` on Workflows. Press `L` on a list to hide or show its
inspector. The preference is shared across Jobs, Runs and Workflows for the
current TUI session. Small terminals
use the full-width list; a larger resize restores a selected inspector layout.

When an inspector is visible, it stays at the right edge and the table fills
the space to its left. The tab rule, row backgrounds and selection band reach the edge of their area.

Jobs, Runs and Workflows follow Kata's issue list: `#` (Jobs and Runs), then
`status`, then the record's own columns, with the time column last. As in
Kata's list, one cell separates adjacent columns, and every other column is
exactly as wide as its widest header or cell on the current page, so short
values leave no padding. Jobs and Workflows show the
latest local run’s outcome. Runs also shows the model and explicit reasoning
effort from each run’s saved profile. Run detail's `model` adds each prompt
step that ran on its own profile, as `<step> <model>`. Command-only and notify runs, and missing
model facts, show `—`.

| Tab | Columns | Flex |
|---|---|---|
| Jobs | `#`, `status`, favorite, `job`, `model`, `schedule`, `workflow` and `next` on wide panes, `updated` | `job` |
| Runs | `#`, `status`, `job`, `model`, `detail`, `started` | `detail` |
| Workflows | `status`, `workflow`, `steps`, `input`, `updated` | `workflow` |

When the pane is too narrow for every column, they give way in this order:

1. The flex column, down to Kata's 20-cell title floor.
2. `model`.
3. The secondary columns: `schedule`, `workflow` and `next` on Jobs; `input`,
   then `steps`, on Workflows.
4. `job` on Runs.
5. The time column.

`#` and `status` never shrink. A clipped cell scrolls instead of ending in an
ellipsis.

The `#` column shows the run's Kata issue as Kata's `#` column does: `#abcd`
for an issue in the default project, the qualified `project#abcd` for any
other project, and `—` when the run has no issue or its reference is not saved
locally. A job shows its newest run's issue, the one `i` opens.

There is no project column. The default project is the lane's own project, or
else the first configured project. A row outside it with no issue to qualify,
and every workflow outside it, prefixes its name with `[project] `, as Kata's
all-projects list prefixes titles. Retained rows and running or parked counts
keep their project labels after a project is removed from configuration. The
tab row groups counts by the projects recorded in runs. `/` filters by project
name as well as the record's text. With one project, every issue is a bare
`#abcd`.
When needed, project counts use `1r/1p` for one running and one parked run.
See [Run multiple projects](../guide/multiple-projects.md) for configuration.

Job and workflow headers use names without raw IDs. Issues and parents use
saved `project#id` references from retained runtime evidence, including
native parents with no retained child runs and the shared binding editor.
A run without a saved name uses a shortened run ID. The board reads local
snapshots only; [admission saves references](../operations/recovery.md#read-saved-issue-references).

## Colors and display

The board uses Kata's light and dark palettes. Its display environment matches
the embedded Kata TUI: a configured display value wins over the pane, including an
explicit empty value. Only terminal display variables reach the renderer.
Identical project display settings are shared. When projects differ, the board
uses the pane's display environment and shows
`projects set different terminal display settings; using this pane's`.

Any nonempty `NO_COLOR` wins over `KATA_COLOR_MODE`. The latter accepts `auto`,
`light`, `dark` and `none`, case-insensitively without trimming whitespace.
Unknown values select `auto`. Auto starts with the dark palette and adapts
when the terminal answers Bubble Tea's background-color query.
Explicit light or dark overrides that preference. None and `NO_COLOR`
remove colors while retaining bold, faint, underline and reverse selection.
Colored modes respect terminal capabilities: `CLICOLOR=0` can select ASCII and
remove attributes too. Use `KATA_COLOR_MODE=none` for colorless structure.

The footer uses Kata's reflowed help table. Narrow panes use multiple rows
so actions stay visible. The list page size accounts for the footer height.

The lists use Kata’s alternating row backgrounds. The header row is one span
in Kata's table-header style: bold, with Kata's dim foreground and header
band in color modes, and bold only in `none` and `NO_COLOR`. Every heading,
gap and the gutter share that style. Column headings stay lowercase. Detail and inspector
section headings are capitalized and bold in the terminal’s default foreground,
matching Kata’s issue detail. Selection keeps its highlight as you move and uses Kata’s
`▶` marker; `none` and `NO_COLOR` use reverse video. Expandable parent rows show
`▸` while collapsed and `▾` while expanded.
Runs uses one gutter glyph: an expandable parent keeps its `▸` or `▾`,
including when selected; selection adds highlighting. A selected row without
children uses `▶`. Project and job cells have no extra marker. Workflow detail
places disclosure beside the cursor.
Colorless modes use Kata’s `+` and `-` disclosure markers.

Every overflowing table cell and short inspector field scrolls on one shared
counter, including unselected rows and the first frame. Overflowing cells do
not use ellipses. Workflow detail’s `used by` and `runs` tables use the same
marquee, including unselected cells. Expanded Runs steps use the same counter
for their key, kind, outcome, duration and note cells. Each cell stays within
its column, including failed steps and archived attempts. Inspector prose wraps;
other nested detail rows stay static. Run artifact paths stay left-truncated to
keep the filename visible.
Rows follow Kata's issue list: every cell uses the default text color and
only the status cell carries color, just as Kata keeps closed issues at full
brightness and grays only their status. Status words and colors come from one
table shared with the CLI; [Run statuses](statuses.md) lists every word. Like
Kata's `open` and `closed`, a run that is moving (`running`, `queued`) is
green and one that needs nothing more (`succeeded`, `cancelled`) is gray. A
run that needs you (`parked`, `blocked`, `needs-human`, `unknown`) is pink,
and `failed` uses Kata's error red. A `—` placeholder is dim. Like Kata's context rows, a
paused or finished job and a workflow no enabled job uses dim their whole row.
Details and inspectors draw field labels dim and values in the default color,
including default values such as `run directory` and `agent default`, with
bold section headings, matching Kata's issue metadata.
Selection covers the whole row. Favorites are bold in the terminal's default
foreground.

Status notices clear when you change tabs or screens and expire after about
five seconds after the latest notice, including a repeated message. Each notice
sets its own information, success or error style. A completed `r` acknowledges
the reload on its originating screen; leaving that screen cancels the
acknowledgement. Persistent runtime, snapshot
and route problems stay visible until their underlying condition clears.

## Navigation

`j`/`k` and the wheel move selection. Brackets and Ctrl+B/F change the list
page. Page Up/Down and Ctrl+U/D scroll the viewport by a full page while keeping
the action target. The next selection move follows the cursor again. Details
have no tabs, and their status and help stay pinned at the bottom.

Each local tab keeps its list selection, filter, expansions and scroll position
when you switch away and return. Selection follows the record identity after
background updates.

Details use one back stack, capped at eight frames including the list. Following
a link already in that stack returns to its saved frame. Back restores the
filter, selected identity, expansions and scroll position, subject to viewport
clamping. If a caller was removed, the first Back shows that removed caller;
the next Back returns again. Digits and tab keys do nothing inside details.

Search owns keyboard and mouse input while editing. Enter or Escape keeps the
live filter; a later list Escape clears it. Ctrl+U clears the draft. Backspace
on an empty draft leaves search. Hidden prompt and command payloads do not match.

Click a row to select it and click it again to open detail. Clicking never
launches work. The wheel moves selection in lists and selectable details. It scrolls
details without selectable rows. A job-detail run click only selects it.
The inspector does not accept clicks. Press `M` for terminal text selection.

## Jobs

The columns are `#`, status, job, model, schedule and updated; job takes the
remaining width. Updated is the job's most recent
activity: the newest local run's start or end, or the definition's last change
in Kata when that is later. A job that has never run shows when its definition
was last changed. Wide panes add workflow and next before updated.

Runs uses the same columns, with detail in place of schedule and started in
place of updated; detail takes the remaining width there, so a long note stays
readable. Started is each execution’s start time; an execution that
has not started shows `—`, and its scheduled time is not shown as its start.

The `model` column uses the owning project's profile arguments, including
explicit reasoning effort. It is as wide as its longest value, up to 18
columns, which fits `gpt-6.1-sol xhigh`. Notify, recurring and command-only workflow jobs show
`—`. Search includes project, UID, name, description, tags, schedule, profile,
model, workflow, support reasons, the latest run's phase/outcome and its
issue reference. Raw JSON does not match. Runs search also matches each run's
issue reference, so `/` then `#abcd` finds the runs for that issue.

Jobs sorts favorites first, then oldest creation time, with UID as the tie
breaker. The favorite column appears only when a matching job is favorited; otherwise
job names start directly after the selection marker. Failed scheduled one-shots stay visible. Only a successful scheduled
occurrence for the current definition hides a one-shot. `F` shows those hidden
jobs, and `P` prunes only the finished jobs matching the current filter. Prune
and delete keep local run journals. Confirmations name the frozen targets;
only `y` deletes. A partial failure stops the remaining deletes.

At narrow widths, columns shrink in the order listed under
[Tabs](#tabs); `#` and status never shrink. The shared
marquee reveals clipped text. At 140 columns by 36 rows or larger, Jobs, Runs and
Workflows use bordered list and inspector panes by default. The list takes up to
110 columns, reserving 48 columns for the inspector. Smaller panes show the full-width list.
The inspector remains passive; row clicks stay inside the list border.

Job tags remain searchable and editable; the Jobs list omits them. Details
and inspectors use Kata’s dim, alphabetically sorted `[label]` chips. Tags
that do not fit collapse into `+N`, or `[N labels]` when no chip fits. Empty
tag sets show `(no labels)`.

The inspector summarizes the local definition and newest exact-project run.
NEXT comes from that project's installation pin and checkpoint. A missing pin
or invalid trigger shows `next unavailable locally`; a native recurring job
shows `Kata-managed; next delivery unavailable locally`. NEXT is a local
estimate and never advances a checkpoint. Status, issue and updated use the
newest local run, including one for an older definition.

Job prompts render as Markdown using Kata's renderer. Workflow prompt steps
use the same renderer when previewed or expanded. None mode strips Markdown
colors and attributes. Messages and commands keep their plain text.

Job detail shows structured fields, a six-line prompt/message preview, and up
to 20 local runs with outcome, trigger, age and reporting note. The trigger is
`manual`, `native` or `scheduled`. As on the Runs tab, the outcome, trigger and
age fit their content, the note takes the remaining width, and a clipped note
scrolls on the shared marquee. Secret references show configured names,
never values; an unmapped name is marked as a failure. Sandbox and approval
rows report only configured facts. Only explicit bypass settings use the
failure role. A command-only workflow has no agent-policy rows.

A run pushed beyond job detail's 20-run cap stays the action target until
deliberately changed. If its journal is removed, actions refuse it until
another run is selected. Opening detail, polling, rendering, animation and
editor typing never fetch Kata history. Use `K` for runs on other executors
or whose local journals have been removed.

On Jobs, `a` focuses the newest matching local run. In job detail, `a`, `u`,
`i` and `l` use the selected run, including a retained run beyond the visible
cap. Clicking a run only selects it, including a second click. `R`, `p`, `D`,
`e` and `b` use the open job's project and guard, regardless of the background
list. Tab, digits, search, list paging, `f`, `F`, `P` and `n` are ignored there.
A removed job remains visible as `deleted in Kata`; Back returns normally.

Conflict boxes retain the attempted guard and show field differences or
`Readback unavailable`. Escape discards the operation without retrying it.
Acknowledged job saves and pause/resume toggles retain the returned definition
and revision guard until the local snapshot catches up. A second toggle uses
that saved state. Other mutation keys and clicks cannot bypass the box. A
confirmed prune stops at its first failure and shows the deleted and pending
names.

## Job editor

| Field | Accepted value and behavior |
| --- | --- |
| Mode | Prompt, workflow, notify or recurring; hidden values survive switching back |
| Tags | Comma-separated names, trimmed and deduplicated; see [metadata limits](definitions.md#job-metadata) |
| Description | Optional job description; hidden for recurring jobs |
| Run at | RFC3339Nano with an offset, or `YYYY-MM-DD HH:MM[:SS]` in Timezone |
| Timezone | Display and wall-time interpretation; changing it alone preserves the instant |
| Durations | Whole seconds or Go durations such as `25h1m1s`; fractional seconds are refused; only deadline offsets accept negatives |
| Secret refs | `VAR=configured-name, …`; duplicate/reserved variables and unmapped names are refused |
| Favorite | Local preference; omitted from the Kata definition |
| Other options | Preserved opaque key names; read-only |

A new draft with only its default profile and checkout closes without a discard
prompt. An unselected Workflow field says `select a workflow`; an unavailable
saved reference stays unavailable.

An unchanged Run at preserves the exact stored offset and fractional seconds.
A changed explicit instant stores UTC with its fraction. Nonexistent or
ambiguous local times are refused; provide an explicit offset for a DST fold.
Timeout uses the project's configured maximum, or eight hours when absent;
the default shown in help is two hours when unconfigured.

Native recurring Action and Parent are read-only. Recurring Overlap is
read-only, and execution options and secret references do not enter new
recurring definitions. An unchanged existing native definition keeps its
untouched empty-equivalent fields. A missing workflow stays unavailable;
it is never treated as a command-only workflow.

A known save conflict cannot be retried from that draft. Help omits Save.
Escape asks to discard a changed draft; confirm with `y`, then reopen to start
from the newer definition. An uncertain save freezes UID, guard, bytes and resolved
parent; Ctrl+S reads back and retries that same request. The banner wraps its
recovery instructions before the error details. Polls do not retry it.
Ordinary save errors also wrap across lines so differing values and remedies
remain readable within the pane.
While a save is in flight, help shows only Ctrl+C to quit.

## Run evidence

Runs shows the current step position and loop usage, for example
`1/4 · verify · loop 1/2`. Succeeded and skipped steps count as complete.
Expanded steps are readable lines; selection moves across runs only.
Parked runs show `waiting: <reason>` in the `detail` column. When the active
step records a quota reset time, the note includes `resets in <duration>` in
Runs and the job and workflow inspectors and details. Successful runs
show `succeeded` in the outcome field. Agent-less command and notify runs omit agent,
model, token, prompt-budget and Herdr-pane fields from detail, and omit step-count
summaries. Command workflows retain their step receipts and outcomes. Settled
runs omit the deadline. Agent pane and tab IDs appear without the raw agent name.
Attaching to a gone pane explains that the run record remains available.

Enter, `l`, right and a second row click open every retained run. No agent pane
is required. Detail actions keep that run and its project selected across
refreshes and filters. Removed runs or unavailable projects refuse actions.
Digits, tab switching, list paging, search and job mutations are ignored in run
detail. Its body scrolls; status and help stay pinned. Artifacts are plain paths
and cannot be selected.

Result attributes evidence to the stopping step before considering a successful
step. Receipt uncertainty remains visible even with exit code zero. Text evidence
is displayed as text. Current and archived attempts use their own saved state.
Only valid command receipts provide step durations. Completed prompts show `—`;
a live prompt's `sent Nm` is time since submission. Step rows show no token
counts; the run's `tokens` line shows saved totals and `prompts` shows the
separate saved budget. A delayed occurrence remains
`scheduled`; delay alone does not prove catch-up.

The worker saves token counts once per completed prompt attempt, including
retries and fresh steps. Run detail reads those saved counts and shows an
estimated `cost` only when one was returned. It does not parse session logs
or make usage requests. The worker discovers an existing local agentsview
daemon from its configuration and runtime records, with a two-second request
limit. It uses the session usage breakdown for the attempt’s time window. If
no token data is returned, it reads the local Claude transcript or Codex
session log instead. Input, output, cache reads and cache writes stay
separate. Missing usage shows `0 in · 0 out · 0 cache read · 0 cache write`.
Historical journals without saved usage are not backfilled by opening the board.

Artifacts lists existing regular files in the run directory and
`steps/<key>/<positive-attempt>/`, followed by the runtime log when it exists.
It reads metadata only and skips symlinks. Entry, `r`, every fifth local poll,
and changed step/attempt/receipt sets refresh it. Late replies cannot overwrite
a newer scan or a new visit. A removed directory and partial read errors remain
visible. A scan visits at most 64 attempt directories and lists at most 512 files;
`+N more not listed` counts known omitted entries or unvisited directories.
Directory reads still allocate one directory's full entry list.

When a resumed run stops, the generic `reporting…` line disappears immediately.
Explicit reporting diagnoses and the run detail’s pending-write count remain
available while the worker finishes its ordered Kata writes.

## Workflows

The columns are status, workflow, steps, input and updated; workflow takes
the remaining width. Status is the latest retained run’s outcome,
or `never`. Updated is the workflow's most recent activity: the latest retained
run's start or end, or the definition's last change in Kata when that is later.
At narrow widths, workflow gives way first, then input, steps and updated;
status never shrinks.

A workflow with steps is a parent row, like a Kata issue with children: it
shows `▸`, and `space` expands it to `▾` with its numbered steps beneath.
The `steps` column keeps the count; Kata's `open/total` kids count has no
meaning for a definition, which has no open or closed steps.

Workflow rows are gray when no enabled job in the same project uses them.
Pausing or saving a job updates workflow activity and using-job facts immediately.
Saved names and workflow bindings stay visible while worker snapshots catch up.
Used by shows current job names. Runs keeps the job name recorded at admission,
so a rename does not rewrite history.
The workflow inspector previews ordered steps, usage and the latest retained
run. Blank lines separate its description, fields, steps and last run.
Step names stand apart from dim numbers and metadata. A prompt step on its
own profile shows that profile's model, or `profile <name>` when its arguments
name no model. It shows as fresh when the workflow declares a different
profile; otherwise it shares the agent of a job on that same profile. A
prompt budget over a step profile's cap is unsupported for every job. Workflow detail adds the
backward edge, maximum loops, shared or fresh prompt agents, per-generation
prompt budget and each using job's cap and timeout.
Unsupported definitions show a missing failure policy rather than a default.
Loop re-runs use fresh agents even when a step's configured mode is shared.

Broken definitions stay visible with their diagnoses. Search covers decoded
displayed fields and diagnoses. It does not search arbitrary JSON fields or
prompt bodies. The board has no workflow creation, deletion or input-override
form; `R` in detail runs a selected Used by job with that job's configured input.

On the list, `u` considers only the newest retained run of the workflow. In
detail, `u` uses an explicitly selected run; elsewhere it uses that newest run.
Resume requires a parked run. A newer completed run never causes an older parked run
to be resumed automatically. `a` attaches only to the selected run's agent pane.
After the request is acknowledged, Workflows, Runs and their inspectors show
`running` and the current step while the worker snapshot catches up. Once the
worker accepts the resume, that display stays until the run settles. If the
snapshot stays unchanged for ten seconds, the board shows its parked state
again; a queued request alone cannot hide a refusal or stopped worker.

The list and details read worker snapshots and retained local journals. `r`
reloads those files; it does not fetch Kata. The detail shows the newest 20 runs,
with `+N older · Runs lists every retained run` when more remain. A selected run
that moves below the cap stays the action target until you move selection. Runs
shows all retained runs. Pruned journals are unavailable here; open Kata for its
history.

For CHECK, Ctrl+S override and conflict-copy cleanup, see
[Edit and inspect workflows](../guide/workflows.md#edit-and-inspect).

## Embedded Kata

While Kata is selected, Tab and Shift+Tab cycle to Jobs and Workflows.
When Kata refuses to start, for example before a current route check, Tab
from Workflows and Shift+Tab from Jobs skip it and wrap, keeping the reason
on screen.
Other keys and body mouse events belong to Kata, including numbers, Escape
and pasted text. Exiting the board stops its Kata child
and any editor in that child's process group.

Kata receives the body's full width and follows terminal resizes. An
80-column board gives Kata 80 columns. The navigation bar uses three rows;
Kata's own minimum-height message applies if the remaining body is too short.
Its cursor appears as a steady inverse cell, without an idle animation timer.
Mouse forwarding follows Kata's own mouse setting; enable `[tui] mouse = true`
in its configuration or use its normal keyboard controls.

The child runs the selected project's Kata executable, actor, daemon/catalog
route and `kata.env`, with only that lane's configured credential plus the
pane's terminal display variables (`TERM`, `COLORTERM`, `TERM_PROGRAM`,
`TERM_PROGRAM_VERSION`, `COLORFGBG`, `NO_COLOR`, `FORCE_COLOR`, `CLICOLOR`,
`CLICOLOR_FORCE` and `KATA_COLOR_MODE`). Configured values take precedence,
including empty values. The VT emulator preserves Kata's ANSI colors and
answers foreground/background queries using the outer terminal's colors.
The board's color profile applies only to its own content; the child keeps its
own colors under `none`, `NO_COLOR` and `CLICOLOR=0` too. The navigation bar
shows `ctrl+] leave Kata` only while Kata is active.
Job secrets and other projects' credentials are excluded.

Opening Kata reads the selected project's saved pin and the worker's latest
route check without launching Kata probes. A missing, refused or mismatched
record, or one older than 60 seconds, refuses the start with a reason.
The worker checks a project with job definitions every 30–45 seconds; the
extra interval allows a slow check to finish without blocking the tab. A
project with no job definitions, or a refused route, is checked only every
few minutes, so its Kata tab can refuse until the next check. Start or upgrade the runtime and wait for its next route
check. Kata handles its own reads and edits; herdr-kata's definition and
binding writes still check the live route before saving.
