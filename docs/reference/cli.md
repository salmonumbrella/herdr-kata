---
title: CLI and plugin surface
description: Commands, flags, exit codes and Herdr plugin entry points.
last_edited: 2026-10-21
---

# CLI and plugin surface

`herdr-kata` starts the runtime, inspects its work and submits operator
requests. Kata owns job and workflow definitions; edit them with Kata or the
[TUI](../guide/tui.md). These commands describe the current build on `main`.

## Arguments and exit codes

Use `herdr-kata --help` for the command list. Go flags accept either `-flag`
or `--flag`. Put flags before positional arguments unless the synopsis below
shows trailing flags. Replace `<job>` with a job UID or resolvable name, and
`<run>` with a full run UID.

| Exit | Meaning |
|---|---|
| `0` | Command completed; `run` was saved, or the worker accepted `resume`/`release`. |
| `1` | Operational error, control refusal or pending acceptance timeout, failed doctor check, or a nonsuccessful/interrupted `run --wait`. |
| `2` | Invalid command, flag or argument count. |

Public subcommands accept `--help` or `-h` and exit `0` with usage before
loading configuration or contacting Kata. `resume`, `release`, `logs` and
`abandon` require a full uppercase 26-character run ULID; malformed run IDs
exit `2` before reading state or submitting requests. Help includes required
operands and supported flags.

## Runtime

| Synopsis | Flags | Behavior |
|---|---|---|
| `herdr-kata up [--takeover] [--project <name>]...` | `--takeover` defaults to false. `--project` is repeatable and accepted only while `config.toml` is absent. | Start or upgrade in `config.session`; a different socket session is a successful no-op. With neither configured projects nor saved lanes, print `nothing started` and exit `0`; saved removed lanes start reporting-only retirement, and the output explains why a runtime is needed. |
| `herdr-kata stop` | None | Persist a stop request and wait for supervisor/worker locks to release. Agents remain running. |
| `herdr-kata status [--json] [--pause]` | Both default to false. | Read local runtime, route, definitions, schedules, inbox and run snapshots. `--pause` waits for Enter. |
| `herdr-kata doctor [--json]` | `--json` defaults to false. | Check config, Kata version/schema, capabilities and authority, Herdr, mappings, local holds, final run mismatches and quarantined evidence. Exit `1` if any check fails. |

`up` prints its start/upgrade result, except for the session no-op. On first
start, `--project` adds the named projects to the
[discovered config](configuration.md#first-start-discovery) even when they have
no definitions yet. Plain `up` refuses a
lane whose lease another installation holds live. A deliberate
`up --takeover` stops this installation's worker and writes the next
generation of each lane's lease for this installation before it starts; the
former owner stands down at its next renewal, admission or lease event.
Inspect the former runtime's occupants first. See
[lane ownership](kata-adapter.md#lane-ownership). `stop`
waits for the runtime to drain and has no command timeout. `status` never calls Kata;
each run line shows its [status word](statuses.md), such as `parked` or
`needs-human`, before the recorded outcome. `doctor`
reads Kata and can save a lost-run scan cursor, but never launches work.
Configuration and state locations are in [Configuration](configuration.md).

A lane's signing refusal takes priority over its saved waiting route, so runs
retain their recorded reasons. Startup failures take priority in the lane's
route status; run waiting details still reflect the route before that override.

```sh
herdr-kata up
herdr-kata status --json
herdr-kata doctor --json
herdr-kata stop
```

## Run and inspect

| Synopsis | Flags | Behavior |
|---|---|---|
| `herdr-kata run <job> [--project <name>] [--wait] [--input text]` | `--wait` defaults to false; `--input` has no default override. | Allocate one manual run UID, print it and write a durable request. |
| `herdr-kata runs [--project <name>] [--job <job>] [--limit N]` | `--job` accepts a job UID or exact name and defaults to all jobs; `--limit` defaults to `100` and must be positive. | Read one page per selected project of Kata run history merged with local explicit-request journals, newest first within the limit; show local step attempts where available. |
| `herdr-kata logs <run> [--follow]` | `--follow` defaults to false. | Read labeled local attempts and command output. |

Before the first `up`, `run` reports `runtime not running`, including with
`--project`. Manual requests use normal readiness, overlap and capacity rules. They do
not change scheduled checkpoints. Native delivery jobs cannot be manually
fired. Known native, unsupported or deleted jobs are refused before a manual
request is queued, including without `--wait`. Unknown job selectors report
`unknown job "<job>"` in both `run` and `runs`. `--input` overrides the job input, including with an empty string;
see [Job and workflow fields](definitions.md).

`--wait` checks the local result every two seconds. It prints `held (<reason>)`
for resumable holds, or the outcome for a finished run,
and returns `0` only for `succeeded`. It does not wait for all Kata writes
to finish. Without `--wait`, exit `0` confirms the request was saved, not
that execution succeeded.

`runs` includes local explicit-request journals only for the selected project and
Kata instance. A `--job` filter selects cron history only. `logs` accepts both
version-1 cron journals and version-2 request journals. Unfiltered history skips
unreadable local journals and still lists available cron and request runs. It
omits unavailable local step details and prints a path-free notice directing you
to `herdr-kata doctor`. The damaged files stay in place for inspection.

Without `--job`, `runs` scans all retained local journals to find request runs;
`--limit` bounds displayed rows, not that scan. History reads grow with the local
journal count. Request-run IDs retain the child's creation timestamp, so stopping
at an older ID could omit a newly requested or started run. Use `--job` for cron
history without the full local scan.

Use `--project` to disambiguate job names or select history. `run`, `resume`
and `release` keep the run UID on the first output line and add a project
acknowledgement when local state identifies it.

`runs` prints tab-separated project, UID, status, job UID, occurrence key and summary.
Status words come from the table the TUI uses; [Run statuses](statuses.md)
lists each one. A matching local held journal displays `parked` and a handed-off
one `needs-human`; Kata retains its recorded outcome. It has no JSON or pagination flag. A local journal adds each step's latest
attempt and status, for example `verify · attempt 3 · failed`. A step that has
not started displays `not started`. Submitted active steps without an outcome
show `running`, `blocked`, `parked` or `needs-human` from the run phase; submitted
steps with no active phase show `unknown`. Recorded step outcomes take priority. `logs` requires an uppercase 26-character
ULID; it shows `output.log`, not an agent transcript. `--follow` checks local
files every 200 ms and ends when the run ends or is handed off. Every saved
attempt directory has a label, including prompts without command output:
`[verify · attempt 3]`. Steps that never created an attempt directory have no label. Output that switches attempts gets its label again;
earlier output remains available after resume.

```sh
herdr-kata run <job> --input "Check the parser" --wait
herdr-kata runs --job <job> --limit 20
herdr-kata logs <run> --follow
```

`herdr-kata open-kata` opens a board with its Kata tab selected.
With several projects, it first opens the TUI's project picker.
This is also the plugin's **Open Kata** action; see
[Open Kata](../guide/tui.md#open-kata).

`runs`, `logs` and `abandon --local` refuse an incompatible or corrupt state
layout before inspecting or changing run artifacts. `logs` and ordinary local
I/O failures from `abandon --local` direct you to doctor without raw filesystem
paths. A run absent from the selected Kata project is reported as unavailable
in that project, including when an inbox is configured. An unknown run leaves
local state and Kata unchanged.

## Operator controls

| Synopsis | Flags | Behavior |
|---|---|---|
| `herdr-kata resume <run> [--reset-loops]` | `--reset-loops` defaults to false. | Queue continuation of a held run and print its UID. |
| `herdr-kata release <run>` | None | Queue a human handoff, print the run UID, and keep its tab. |
| `herdr-kata abandon <run> [--project <name>]` | `--project` is required with several projects. | Record `unknown` in Kata for this installation's running observation whose local directory is missing. |
| `herdr-kata abandon <run> --local` | `--local` defaults to false. | Offline: quarantine a rejected or signing-refused run whole, or acknowledge existing quarantine; no Kata write. |

`resume` and `release` require a running worker and are processed on a wake
signal or its five-second request scan. They print the UID, then wait up to
70 seconds for worker acceptance. A refusal prints its reason and exits `1`;
acceptance prints `accepted` and exits `0`. A timeout leaves the saved request
pending and exits `1`. It does not wait for execution to finish. `status` and
`status --json` show worker refusal reasons in `control_results` until newer
local journal activity or an accepted control supersedes them. Completed
abandon reports and explicit local quarantine acknowledgements also clear
older refusals for resolved runs, even when their execution journal is missing.
Refusals recorded after resolution stay visible.
Pending, inspected or unreadable resolution evidence does not clear refusals.
Unreadable resolution files do not prevent status from rendering. Manual-run
refusals also disappear when the project's saved definitions show the job is
deleted or absent. New results carry [request identity](state.md#directory-layout).
Unreadable and invalid request files are set aside individually under
`quarantine/requests/`; valid controls continue in the same scan. Their result
keeps the refusal reason without the local request path. If the run identity
cannot be decoded, the refusal remains visible for operator inspection.
Legacy results without that identity stay visible unless
other local evidence resolves them. A later refusal is visible again.
Saved acknowledgements stay on disk for waiting callers;
local-state errors are summarized without exposing raw paths, including
`run --wait`, `resume` and `release` acknowledgements for unknown or missing
runs. Each waiter rejects a response addressed to a different run.

After another installation takes a lane over with `up --takeover`, the
stood-down installation refuses `run`, `resume`, `release` and `abandon` for
that lane at once, exiting `1` with `another installation (<executor>) owns
this lane: lease generation <n>`. The CLI refuses before saving a request when
the running worker's latest lease outcome for the lane names another owner;
the worker refuses the same requests at intake, with result reason
`lease_held_elsewhere`, and settles a `run` it had queued before the takeover.
`run --wait` reads that refusal within a fraction of a second. `abandon`
reads the lane lease in Kata first. The TUI's `R` and `u` show the same text.
See [lane ownership](kata-adapter.md#lane-ownership).

Use the current installed build for status and operator controls. Older builds
reject result files containing the new request-identity fields. A waiter started
before an upgrade can report a decode error; inspect `status` with the current
build to see the saved run state.

Current status explains when saved control results cannot be read and
directs you to doctor, preserving the original files.
If a downgrade makes status reject a control result as corrupt or unknown,
restore the newer executable and run `up` with the same config and state
directories. Plain `up` upgrades the running executable in place. Keep saved
acknowledgements: removing them is not a downgrade procedure. Doctor in the
current build also reports unreadable or unsupported control results, without
changing them. An older doctor's passing report does not establish that its
status decoder supports newer files. Source builds may share a release version;
retain the newer executable rather than selecting solely by `version` output.

Resume targets the held step, a checker-only recheck, or an authorized loop
rewind; `--reset-loops` clears loop counts and remembered rejection. See
[resume acceptance and target rules](holds.md#resume-rules).
Journals without a frozen admission timeout must be inspected and released
instead. Queued releases take priority over pending resume activation.

Mutation confirmations name the target project. Run-UID controls use the
journal's frozen project; `abandon --local` needs no selector.

`abandon` checks the pinned route and remote identity directly;
it refuses a run with local state. It also leaves a keyed issue comment and
guarded attention where runtime ownership can be proven. Release refuses
settled runs without changing their evidence. Release and abandon never authorize a
replacement execution.
`--local` refuses either held runtime lock, live or unproven occupants, and an
existing destination alongside the source. It preserves artifacts and unresolved
writes; retries finish directory syncs or become a no-op. `release` refuses an inspected admission tail and admission ownership holds,
which require `abandon --local`. A `create_rejected` hold can be released without
Kata writes. Acknowledging existing quarantine preserves its admission veto.
See [Recover runs](../operations/recovery.md).

## Agent reports

```sh
"$HERDR_KATA_REPORT" report --result <absolute-result-path> \
  --status succeeded --summary "Checked the parser and verified its focused tests pass." \
  --evidence "test:go test ./parser" --close-reason done --note "Parser checked"
```

| Flag | Required | Meaning |
|---|---|---|
| `--result` | Yes | Absolute `steps/<key>/<attempt>/result.json` path inside `HERDR_KATA_RUN_DIR`. |
| `--status` | Yes | `succeeded`, `failed` or `needs-human`. |
| `--summary` | Yes | Nonblank account of what happened and how it was verified. |
| `--evidence` | No | Repeatable `type:value` evidence. |
| `--close-reason` | No | `done` or `audit-no-change`; omission uses `done` at settlement. |
| `--note` | No | One line for the next step, at most 280 bytes. |
| `--assert` | With `--target` | `confirm` or `refute` a specific maker claim. |
| `--target` | With `--assert` | Bare full 26-character comment ULID or `c:<ULID>`, case-insensitive; short handles are refused. |

The checker prompt supplies the saved maker claim when one exists. See
[Finding comments](reporting.md#findings-comments) for assertion eligibility,
short-evidence downgrades and typed/plain detection. Invalid assertion flag
pairs or targets exit `2` without writing a result.

`report` accepts no positional arguments. It validates and atomically writes
the report, then prints its path. Exit `0` means the file was written, even
for `--status failed`. Invalid argument/path shape exits `2`; report validation
or write errors exit `1`. The [agent contract](agent-contract.md) owns the
result schema and environment; [Reporting](reporting.md) owns close validation.

## Herdr plugin surface

`herdr-kata tui [--wide] [--pin] [--startup]` accepts no positional arguments.
All flags default to false. In a terminal, plain `tui` runs inline. Without
a terminal it opens a Herdr pane. `--wide` opens below the caller's pane;
`--pin` adds a sidebar row. `--startup` opens one pinned pane, focused, and
selects its Kata tab once the runtime's route check lands, subject to the
configured session and `[board] pin`. See [Use the TUI](../guide/tui.md).

The hidden aliases `board` and `board-wide` remain for one release.
`board-wide` defaults `--wide` to true. Manifest IDs retain their names:

| Surface | ID | Command |
|---|---|---|
| Startup hook | — | `up` |
| Startup hook | — | `tui --startup` |
| Global action | `up` | `up` |
| Global action | `open-kata` | `open-kata` |
| Global action | `board-wide` | `tui --wide` |
| Popup pane | `status` | `status --pause` |
| Split pane | `board` | `tui` |

The manifest display name and plugin ID are `kata`. Herdr commands,
configuration/state paths, `plugin list` and update messages use that ID.
The install source and Go module remain `salmonumbrella/herdr-kata` and
`github.com/salmonumbrella/herdr-kata`. See the
[ID upgrade procedure](../get-started/install.md#upgrade-from-the-old-plugin-id)
for existing installations. `herdr-kata version` takes no
flags or arguments and prints the release version:

```text
herdr-kata <version>
```

## Internal commands

`serve` and `worker` take no flags or arguments and load the installation
config. The supervisor owns their lifecycle. `env-names <path>` writes the
sorted names of its environment variables as JSON to a new file at `<path>`;
the launch-environment probe runs it inside a Herdr pane. The only values it
writes are absolute `HOME`, `CODEX_HOME` and `CLAUDE_CONFIG_DIR` paths, as
`NAME=path` entries, so session logs are found where the agent writes them. Invoke the public commands above for normal use.
