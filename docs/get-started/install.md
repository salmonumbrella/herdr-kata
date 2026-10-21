---
title: Install and upgrade
description: Install herdr-kata, check its requirements and upgrade or remove the runtime.
last_edited: 2026-10-21
---

# Install and upgrade

Install herdr-kata in the Herdr session that will run your Kata jobs. Kata
keeps the definitions and run evidence; the plugin runs the work on this machine.

## Check the requirements

- Linux or macOS.
- Herdr 0.9.5 or later.
- Kata 0.19.0 or later.
- Go 1.27 or later to build the plugin.
- An installed agent for each configured Herdr profile.

The runtime checks compatibility when it connects. The
[adapter reference](../reference/kata-adapter.md#transport-and-events)
describes the protocol checks.

## Install the plugin

From Herdr:

```sh
herdr plugin install salmonumbrella/herdr-kata
```

For a source checkout, build and link its root instead:

```sh
GOMAXPROCS=2 GOFLAGS=-p=2 go build -trimpath -o ./bin/herdr-kata ./cmd/herdr-kata
herdr plugin link "$PWD"
./bin/herdr-kata version
```

Commands on these pages use `herdr-kata` for the built executable. If it is
not on `PATH`, use its absolute path or `./bin/herdr-kata` from the checkout.

## Configure and start

Find the plugin's config directory:

```sh
herdr plugin config-dir kata
```

On the first start, you can let the plugin create `config.toml` for the
current installation. Run `up` from a live named Herdr session with the Kata
binary on `PATH` and the intended route already selected:

```sh
herdr-kata up
```

Discovery pings that Herdr session, lets Kata auto-start a stopped local daemon,
refreshes its route diagnosis, reads the authenticated Kata actor and
active project inventory, probes each compatible project for access and
scheduling support, and records each usable project that has a job or
workflow. Add `--project <name>`, repeatable, for a project whose definitions
you have not created yet. It also records profile
kinds from existing Herdr agents, or finds `codex` and `claude` on `PATH` when
the session has no agents.
It writes the missing file atomically with only route selectors, actors,
daemon selectors, credential sources and a generated executor label.
Credential values are never written. Discovery skips inaccessible or
incompatible projects and refuses to write when the route, credentials or
profiles cannot be proven. With no qualifying project it writes a config with
no lanes, and `up` starts nothing until you
[add a project](../reference/configuration.md#add-a-project). It does not replace an
existing file. Unused daemon and credential options are omitted.

If you need checkouts, profile arguments, limits or other policy at install
time, create `config.toml` in the directory above instead. `HERDR_PLUGIN_CONFIG_DIR`
overrides the directory. Use absolute paths, a stable unique executor label,
and your authenticated Kata actor. This example uses the Herdr session `work`:

```toml
session = "work"
executor = "work-runtime"

[kata]
bin = "/usr/local/bin/kata"

[[projects]]
name = "example-project"
actor = "worker"

[checkouts]
repo = "/srv/projects/example"

[profiles.default]
kind = "codex"
args = []
```

Change `kata.bin`, the checkout and profile to match this machine. Unknown
configuration keys are rejected. Keep credentials out of the file; see
[Configuration](../reference/configuration.md#kata) for credential routing.

In the configured session, start and inspect the runtime:

```sh
herdr-kata up
herdr-kata status --json
herdr-kata doctor --json
```

`up` can migrate the old plugin directories in any session, but starts the runtime
only in the configured session. See [Run the runtime](../operations/runtime.md)
for session selection, startup hooks and the state directory.

## Upgrade

Pre-release state layouts are not migrated. If `up` refuses an incompatible
layout, stop the old runtime and resolve its occupants first. Move the state
directory aside, then run `herdr-kata up` again with the new build. Keep the
old directory until you have saved the evidence you need. See
[Inspect the state layout](../operations/runtime.md#inspect-the-state-layout)
for the state location and recovery rules.

For an installed plugin, run the install command again and confirm Herdr's
replacement prompt. For a linked checkout, update the source and rebuild it.
Then run the new executable:

```sh
herdr-kata version
herdr-kata up
herdr-kata status --json
```

Run one `up` at a time. It stages the new binary and requests worker
replacement. Active command steps can be interrupted and become held; wait
for them to finish first if interruption is unacceptable. Existing agents
may keep working through the upgrade. If concurrent upgrades leave an older build running or stop the
supervisor, rerun `up` from the intended build.

## Upgrade from the old plugin ID

The plugin ID is now `kata`. The repository, Go module and executable keep
their names. Herdr refuses an ID change through `plugin update`; use a fresh
install for this transition, then use `herdr plugin update kata` normally.

1. In the configured session, find the old executable and config. Wait for
   command steps to finish, disable its startup hooks, then stop the runtime:

   ```sh
   old_root=$(herdr plugin list --plugin salmonumbrella.herdr-kata --json | jq -r '.result.plugins[0].plugin_root')
   old_config=$(herdr plugin config-dir salmonumbrella.herdr-kata)
   old_state="${XDG_STATE_HOME:-$HOME/.local/state}/herdr/plugins/salmonumbrella.herdr-kata"
   herdr plugin disable salmonumbrella.herdr-kata
   env HERDR_PLUGIN_CONFIG_DIR="$old_config" HERDR_PLUGIN_STATE_DIR="$old_state" "$old_root/bin/herdr-kata" stop
   cp -a "$old_config" "$old_config.before-kata"
   cp -a "$old_state" "$old_state.before-kata"
   ```

   Use the actual state directory if you previously set an override. Back up
   config and state together. Keep the executor label unchanged.

2. Install the new ID from the same source and run its executable:

   ```sh
   herdr plugin install salmonumbrella/herdr-kata
   plugin_root=$(herdr plugin list --plugin kata --json | jq -r '.result.plugins[0].plugin_root')
   "$plugin_root/bin/herdr-kata" up
   "$plugin_root/bin/herdr-kata" doctor --json
   "$plugin_root/bin/herdr-kata" status --json
   herdr plugin list --plugin kata
   herdr plugin update kata
   ```

   On its first start, the new binary moves the legacy config and state into
   the empty `kata` sibling directories, before first-start discovery. Herdr's
   empty directories from installation are accepted. The migration retains
   relative symlinks at the old paths, so saved artifact paths and process
   locks still address the same files. Pins, executor, lease generations,
   journals, checkpoints and definition snapshots carry over without editing
   their contents. A configured lane resumes its own lease without `--takeover`.
   [Removed lanes](../guide/multiple-projects.md#remove-a-project) remain removed:
   startup retires unfinished work and recovers saved final reports on their
   pinned routes, even when no lanes remain configured.

   A live old runtime or either nonempty destination blocks migration. Nothing
   is merged or overwritten. Stop both installations and resolve unexpected
   destination data manually. The marker and crash-recovery rules are in
   [State](../reference/state.md#plugin-id-migration). If a start is interrupted,
   finish it with the **new** binary before starting the old binary.

3. Check the original lane generation and run history, then remove the old
   installation by its ID:

   ```sh
   herdr plugin uninstall salmonumbrella.herdr-kata
   "$plugin_root/bin/herdr-kata" doctor --json
   ```

   Herdr removes the old checkout and registration, leaving user config/state
   intact. Keep the legacy aliases: run artifacts can still reference them.
   Only one ID should remain enabled. Both IDs share local locks after migration;
   an independent installation with a different executor is refused by the lane
   lease. Do not copy a pinned identity to independent state directories.
   If the installation was disabled and had no lanes, leave it disabled with
   `herdr plugin disable kata` after migration.

### Update a shell wrapper

Replace wrappers that glob the old `github-installations` directory with a
registry lookup. This also follows future Herdr installation-directory changes:

```sh
#!/bin/sh
plugin_root=$(herdr plugin list --plugin kata --json | jq -er '.result.plugins[0].plugin_root') || exit 1
exec "$plugin_root/bin/herdr-kata" "$@"
```

Run it in the intended Herdr session, or select that session through the
wrapper's `HERDR_SESSION`. An existing wrapper cannot find the new ID until
it is updated. Do not select a binary by file modification time.

## Uninstall

Stop the runtime before removing the plugin:

```sh
herdr-kata stop
herdr plugin uninstall kata
```

For a linked checkout, use `herdr plugin unlink kata`
instead. Agents already running remain in Herdr. Keep the state directory
until you have resolved those runs and saved any history you need; see
[Recover runs](../operations/recovery.md). Removing the plugin does not
remove Kata job definitions. Disable jobs in Kata if you want scheduling,
including native child delivery, to stop.

## Limits

- There is no Windows support or automatic failover to another installation.
- Upgrading does not retarget saved runs to another Kata project or actor.
- Automatic ID migration requires absolute config and state directories whose
  last component is `kata`, with legacy siblings. Custom directory pairs keep
  their paths; point both builds at the same pair and migrate them manually.
- `kata` is a bare plugin ID. An official plugin could claim it in the future;
  inspect Herdr's replacement preview and source before confirming an install.
