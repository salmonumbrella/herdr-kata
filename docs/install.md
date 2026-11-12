---
title: Install
description: Build and configure the headless worker.
last_edited: 2026-11-11
---

# Install

Download the archive for your platform (macOS or Linux, arm64 or amd64)
from the [latest release](https://github.com/salmonumbrella/herdr-kata/releases/latest)
and check it against `checksums.txt`, or build `bin/herdr-kata` with
`mise exec -- make build`. Copy the binary to an operator-owned directory on
PATH. The module remains
`github.com/salmonumbrella/herdr-kata`. Use hc 0.1.15 or newer and Kata
0.20.0 or newer. The live suite exercises the installed hc and checksum-verified 0.1.15 and 0.1.16. Linux and
macOS are supported; Windows is not.

v2 replaces the old plugin. Its configuration and state are incompatible
with v1. Start with a fresh configuration and state directory. There is no
`herdr-plugin.toml`, plugin pane or adapter TUI. Inspect jobs and runs with
`kata tui` or the Kata CLI.

## Configure

The [README example](../README.md#configuration) lists every configuration
key. The default path is `$XDG_CONFIG_HOME/herdr-kata/config.toml`, falling
back to `~/.config/herdr-kata/config.toml`. State defaults to
`$XDG_STATE_HOME/herdr-kata`, falling back to `~/.local/state/herdr-kata`.
Place `--config <path>` and `--state <directory>` before the command to
override them. All configured filesystem paths must be absolute. Unknown
keys outside `[[projects]]` fail configuration loading. Invalid project entries
are quarantined; the remaining valid projects can run.

`executor` identifies this installation in Kata observations and terminal
labels. Keep it stable with the state directory. `hc_session` defaults to
`default`. Choose an explicit session to isolate the worker. `hc_bin` and
`kata.bin` default to PATH lookups, resolved when configuration is loaded.
`kata.daemon` selects an authenticated entry in the operator's Kata catalog.

Each project supplies `name`, `actor`, an optional `commands` opt-in
(default false), and `max_active_runs` (default 1). The global limit defaults
to 4. Held runs reserve capacity until released.

Optional project defaults let portable jobs run without adapter fields:

```toml
[[projects]]
name = "example-project"
actor = "worker"
commands = false
max_active_runs = 1
default_checkout = "example-project"
default_agent = "default"
```

`default_checkout` must name a key in `[checkouts]`, and `default_agent` a key
in `[profiles]`. An unknown default quarantines that project with its name and key.
Both are optional. See [Jobs](jobs.md) for precedence and [Operations](operations.md)
for upgrade and rollback.

Profiles contain plain `argv` arrays. The first element is resolved to an
absolute executable when a run is prepared. The worker appends one argument:
`Read <prompt-file> and do exactly what it says.` Use `codex --no-daemon`
to avoid attaching a fresh attempt to a shared native daemon. Native agents
use their own authentication and configuration; the adapter does not manage
credentials.

Optional `profiles.<name>.model_flag` declares a model-capable profile, for
example `model_flag = "--model"`. Absent or empty means no model support.
A non-empty flag starts with `-`, contains no whitespace or `=`, and is neither
`-` nor `--`. Invalid flags fail configuration loading. The CLI must accept
this flag and a separate value at the end of profile argv, before the prompt.
Configuration loading rejects `model_flag` when profile argv ends in the CLI
option terminator `--`.
With a selected model the argv order is `<profile argv...> <model_flag> <model>
<prompt-line>`. Duplicate-flag handling belongs to the agent CLI. Fixed models
can remain in profile argv. See [Jobs](jobs.md#models) for model selection.

The default timeout is 2 hours and the maximum is 24 hours. A job's timeout
is capped by `limits.max_timeout`. Both values are duration strings.
`limits.unset_env` removes named variables from launched children.

## Start

Run `herdr-kata up`, then `herdr-kata doctor`. Doctor checks the core,
profiles, checkouts, worker and Kata access from inside the core's execution
environment. `serve` runs
in the foreground and holds the lifetime state lock. Only one worker may
write a state directory.

hc children inherit the core's environment. The adapter explicitly carries
PATH, HOME and XDG paths from the worker and supplies pinned Kata routing.
`kata.env` permits only `KATA_HOME`, `KATA_SERVER` and `XDG_*`. Never store
tokens there: they would enter process arguments. Every hc call sets the
configured session and removes `HC_SOCKET`; doctor warns about an inherited
override. See [operations](operations.md) for recovery and isolation.

### Start at login

From your account's normal login shell, where `herdr-kata up` already works:

```sh
herdr-kata service install
herdr-kata doctor
```

On macOS this installs and registers a launchd user agent. On Linux it enables
and restarts a systemd user unit. Both invoke `up` at login and retry failed
startup after 30 seconds. They do not supervise a worker that later dies;
use `up` to start it again. A successful launchd install confirms registration;
doctor's worker check and the logs show whether startup succeeded. Remove
any previous hand-written unit or plist before installing.

The launcher records the executable path returned by the operating system,
absolute config and canonical state paths, and the current PATH with empty
and relative entries removed. It also records `XDG_CONFIG_HOME`,
`XDG_STATE_HOME`, `XDG_DATA_HOME` and `XDG_CACHE_HOME` when set. Directory
selectors must be absolute for install and uninstall; paths and captured values
must be printable UTF-8 without control characters or Unicode noncharacters.
On Linux the executable path must also contain no quotes or backslashes;
move the binary and reinstall if its path contains them. The installer serializes no other
environment variables. The service manager supplies the account's HOME and
may supply other inherited environment values. Use the account's normal HOME
when installing so the manager discovers the file at the next login.

Rerun install after changing PATH, the config path or binary location, or
after an upgrade removes the recorded executable path. A package-manager
symlink may resolve to its version-specific target. Reinstalling the same
state root refreshes its file and starts `up` again; a live worker is adopted.
Configuration must load for both install and uninstall. Put explicit paths
before the command:

```sh
herdr-kata --config /srv/projects/example/config.toml --state /srv/projects/example/state service install --print
herdr-kata --config /srv/projects/example/config.toml --state /srv/projects/example/state service install
herdr-kata --config /srv/projects/example/config.toml --state /srv/projects/example/state service uninstall
```

`install --print` writes only the rendered definition to stdout, creates no
directories and calls no service manager. On unsupported operating systems,
run `herdr-kata up` from your login environment instead.

Service identity is `herdr-kata-<state-path-hash>`, the worker's existing name.
Different state roots create separate installations. Before changing the state
root, uninstall with the old `--state`, then install with the new one. Uninstall
removes autostart and leaves the worker running; `herdr-kata stop` stops it.

The user-owned service file has mode `0600`. On macOS it lives at
`~/Library/LaunchAgents/<name>.plist`, with stdout/stderr in
`~/Library/Logs/<name>.log`. On Linux it lives at
`$XDG_CONFIG_HOME/systemd/user/<name>.service` (fallback
`~/.config/systemd/user/`); read its logs with `journalctl --user -u <name>`.
Doctor's optional `service` line reports file presence, or an informational
`unknown` for an inaccessible path. It does not probe loaded/enabled state.
If activation fails, the file stays in place and the command reports failure;
correct the error and rerun install, or uninstall it.

## Session capture

`report` records the invoking agent's session on its attempt when available;
`herdr-kata logs <run-uid>` shows recorded attempt identities. After normal
prompt cleanup it reports when terminal output is unavailable. If a result is
near the 64 KiB limit, optional session metadata is omitted to preserve it.
Prompt launches unset the built-in, override and mapped session variables and
inherit the rest of the core environment. Claude Code uses
`CLAUDE_CODE_SESSION_ID`; Codex uses `CODEX_THREAD_ID` then `CODEX_SESSION_ID`.
For other agents, map an agent name to its shell variable in `[sessions.env]`,
for example `pi = "PI_SESSION_ID"`. Names are lowercase letters, digits and
hyphens, starting with a letter or digit, up to 32 bytes. Variable names use
letters, digits and underscores and cannot start with a digit. Targets must be
unique and cannot be `PATH`, `HOME`, a built-in or override session variable,
or begin with `XDG_`, `KATA_`, `HC_` or `HERDR_`. Only mapping names are
passed to the prompt; inherited session values are cleared.

Set both `KATA_TRANSCRIPT_AGENT` and `KATA_TRANSCRIPT_SESSION_ID` in the
agent shell to override discovery. An incomplete or invalid override records
nothing. Different agents or conflicting IDs for one agent are ambiguous;
IDs for the same agent are compared case-insensitively. Claude and Codex IDs
must be UUIDs; other IDs are up to 128 ASCII letters, digits, dots, underscores
or hyphens, starting with a letter or digit. Missing, invalid and ambiguous
identities never change the report outcome. No transcript files are scanned.
Session IDs are local journal evidence and can identify a transcript to someone
with access to it; they are not yet sent to Kata.
