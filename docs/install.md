---
title: Install
description: Build and configure the headless worker.
last_edited: 2026-11-03
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
keys fail configuration loading.

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
in `[profiles]`. An unknown default fails startup with the project and key.
Both are optional. See [Jobs](jobs.md) for precedence and [Operations](operations.md)
for upgrade and rollback.

Profiles contain plain `argv` arrays. The first element is resolved to an
absolute executable when a run is prepared. The worker appends one argument:
`Read <prompt-file> and do exactly what it says.` Use `codex --no-daemon`
to avoid attaching a fresh attempt to a shared native daemon. Native agents
use their own authentication and configuration; the adapter does not manage
credentials.

The default timeout is 2 hours and the maximum is 24 hours. A job's timeout
is capped by `limits.max_timeout`. Both values are duration strings.
`limits.unset_env` removes named variables from launched children.

## Start

Run `herdr-kata up`, then `herdr-kata doctor`. Doctor checks the core,
profiles, checkouts, worker and Kata access from inside the core's execution
environment. The README has optional login-service examples. `serve` runs
in the foreground and holds the lifetime state lock. Only one worker may
write a state directory.

hc children inherit the core's environment. The adapter explicitly carries
PATH, HOME and XDG paths from the worker and supplies pinned Kata routing.
`kata.env` permits only `KATA_HOME`, `KATA_SERVER` and `XDG_*`. Never store
tokens there: they would enter process arguments. Every hc call sets the
configured session and removes `HC_SOCKET`; doctor warns about an inherited
override. See [operations](operations.md) for recovery and isolation.
