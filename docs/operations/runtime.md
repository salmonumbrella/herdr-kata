---
title: Run the runtime
description: Start, inspect, upgrade and stop a runtime and handle its pinned Kata route.
last_edited: 2026-10-20
---

# Run the runtime

Run one herdr-kata installation for the configured Herdr session. The
supervisor keeps its worker running; Kata keeps definitions and evidence.
[Install and configure](../get-started/install.md) the plugin first.

## Start in the configured session

Use the plugin's **up** action or the built executable:

```sh
herdr-kata up
herdr-kata status --json
herdr-kata doctor --json
```

`up` is the public command that starts or upgrades the runtime. Before loading
configuration, it can finish the [plugin ID migration](../get-started/install.md#upgrade-from-the-old-plugin-id).
It then derives the calling session from `HERDR_SOCKET_PATH`; if that differs
from `config.session`, it exits successfully without starting the runtime.
Outside Herdr, without a socket override, it uses the configured session's
socket path.

Concurrent startup hooks share one runtime. `up` waits up to 500 ms for the
new supervisor to become ready; worker startup can finish later. Use
`status` or `doctor` to check readiness.

Project lanes start in bounded groups so a large installation does not
overload Kata with simultaneous startup calls. Later lanes can take longer
to become ready. A lane waiting to recover access lets other lanes start.

Herdr runs the `up` hook at startup. The separate TUI startup hook opens a
pinned view; see [Use the TUI](../guide/tui.md). The plugin's status pane shows
runtime status and waits for Enter before closing.

## Inspect health and runs

```sh
herdr-kata status --json
herdr-kata doctor --json
herdr-kata runs --job <job-uid> --limit 20
herdr-kata logs <run-uid> --follow
```

`status` reads local snapshots, including supervisor and worker liveness,
route refusal, definitions, pending occurrences and runs. `doctor` checks
plugin ID migration state, configuration, Kata capabilities and authority, Herdr connectivity, mappings
and missing local runs. Neither starts the runtime. `logs` reads the run's
command output; it is not an agent transcript.

Each project’s `running` and `worker_running` fields describe the shared
processes. Its `route.allowed` records the latest check of that project’s
route. The worker staggers periodic route checks and also checks an allowed
lane after an event-stream error. A refused lane backs off to one route check
every few minutes; the runtime never re-probes it in a tight loop. Probe deadlines retry with backoff before publishing
access loss. A confirmed access refusal still fences the lane immediately.
See [route probe limits](../reference/kata-adapter.md#transport-and-events)
for concurrency, deadlines and cadence.

The runtime log is `log/herdr-kata.log` under the state directory. It rotates
at 5 MiB, keeping five files including the current log. `HERDR_PLUGIN_STATE_DIR` selects that
directory; by default it is Herdr's user state directory for
`kata`.

## Stop or upgrade

```sh
herdr-kata stop
```

`stop` stops admission and waits for the supervisor and worker to exit.
An active command step is interrupted: its process group receives SIGTERM,
then SIGKILL after ten seconds if needed. Its outcome becomes unknown and
held. Agents already running stay in Herdr. At the next `up`, recovery
reconciles saved runs before admitting new work.

To upgrade, run `up` from the new binary as described in
[Install and upgrade](../get-started/install.md). Run one `up` at a time.
An upgrade can also interrupt active command steps when the worker is
replaced. Wait for commands to finish before stopping or upgrading if
interruption is unacceptable. The supervisor restarts crashed workers with backoff. If the supervisor
itself dies, use `up` or the next Herdr startup hook to restart it.

<a id="4-resolve-a-route-mismatch-deliberately"></a>

## Resolve a route mismatch deliberately

The first worker start pins the Kata instance, project, authenticated actor,
resolved route and executor. A later mismatch refuses admission and report
delivery. `doctor` reports the mismatch; `status --json` exposes route health.
Changing a config value cannot retarget a saved run.

The diagnostic names each differing field and shows pinned and current values
for identity, executor, binary and daemon settings. For `kata.env`, it names
only the changed keys. Environment values and resolved daemon endpoints stay
hidden because an endpoint can repeat an environment value. The diagnostic
gives the restoration steps below and points here for an intentional change.
Very long difference lists are abbreviated; compare the configuration and
saved pin for the remaining fields.

1. Stop the runtime and preserve the complete state directory.
2. Compare `config.toml`, the route environment and the project pin in
   `projects/<project-uid>/state.json` with
   the intended installation. Restore the original route, project, actor
   and executor when the change was accidental.
3. Run `doctor --json`, then `up` and inspect status again.

There is no repin command. For an intentional new target, resolve existing
work against its original target first. Use a separate state directory and
unique executor for the new installation. Do not delete or rewrite the pin
to make a mismatch disappear. See the
[adapter contract](../reference/kata-adapter.md) for the exact authority boundary.

## Move state safely

Stop the runtime before moving state or changing `HERDR_PLUGIN_STATE_DIR`.
Keep the whole directory together, including checkpoints, quarantine,
requests and staged binaries. Existing runs contain absolute artifact and
report-binary paths, so keep the old location available while any saved run
or live agent still refers to it. Changing the override alone does not
migrate those paths.

Point every invocation, startup hook and TUI at the intended directory.
Run `doctor --json` before restarting. If you cannot preserve saved paths,
finish or [hand off](recovery.md) existing work before moving. Never start
with an empty replacement directory as a recovery shortcut.

## Inspect the state layout

Fresh installations use one version-1 layout marker. Project pins, checkpoints
and snapshots live beneath `projects/<project-uid>/`; runs and attempt paths
stay at the root. There is no state migration. The
[state reference](../reference/state.md#directory-layout) lists every path.
If `up` or `doctor` recognizes a legacy single-project root pin or an unsupported
layout version (2 or later), move the state directory aside and run
`herdr-kata up` again. Legacy pins remain incompatible even with an empty
project UID. A corrupt current marker instead requires
stopping the runtime, preserving original artifact paths, and restoring trusted
state or resolving existing occupants before initializing a separate directory.
`up` checks the layout before staging, stopping or upgrading a runtime.
Pre-release layouts are not migrated; keep
the moved directory for inspection and stop its old runtime separately. Do not
rewrite the marker to make an incompatible directory appear supported.

Project additions, removals and allowlist edits also require `stop` then
`up`. Each lane starts independently. `status --json` includes a `projects`
array with route health, next fires, unsupported definitions and inbox data.
The top-level `route` is present only when exactly one project lane exists; use
each project's route otherwise. Job and run rows name their project. `doctor`
checks common resources once, then each project's route and evidence. See
[Run multiple projects](../guide/multiple-projects.md) for selectors and limits.

## Limits

- The supervisor has no external watchdog; a dead supervisor needs `up`.
- Shutdown and handoff leave agents alive.
- Local state loss never authorizes an automatic replacement execution.
