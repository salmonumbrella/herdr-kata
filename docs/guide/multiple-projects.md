---
title: Run multiple projects
description: Serve several Kata projects with one runtime and control their access to local resources.
last_edited: 2026-10-21
---

# Run multiple projects

One installation can run jobs from multiple Kata projects in the same
Herdr session. Each project has its own route, actor, inbox, snapshots and
schedules. Execution slots and the Herdr workspace are shared.

## Configure the projects

Keep the executable and nonsecret transport environment in `[kata]`. Move
project selectors into `[[projects]]` entries:

```toml
session = "work"
executor = "worker"

[kata]
bin = "/usr/local/bin/kata"

[limits]
max_active_runs = 3

[checkouts]
example = "/srv/projects/example"
team = "/srv/projects/team"

[profiles.default]
kind = "codex"

[profiles.team]
kind = "codex"

[[projects]]
name = "example-project"
actor = "worker"
daemon = "personal-hub"
credential_env = "KATA_TOKEN_PRIVATE"
checkouts = ["example"]
profiles = ["default"]

[[projects]]
name = "team-project"
actor = "worker"
daemon = "team-hub"
inbox = "worker/automation"
credential_env = "KATA_TOKEN_TEAM"
checkouts = ["team"]
profiles = ["team"]
max_active_runs = 1
commands = false
```

Set credentials in the named environment variables; never put their values
in config. Each entry supplies its own actor and optional credential, daemon
and inbox. Entries inherit none of those values from one another. Remove
`project`, `actor`, `daemon`, `inbox` and `credential_env` from `[kata]` when
using `[[projects]]`. The legacy single-project form still works.

Omitted checkout and profile lists allow all configured mappings. An empty
checkout list allows only the run directory; an empty profile list allows no
prompt execution. `commands` defaults to true. Allowlists narrow what a
project may run on this installation; a definition outside them stays visible
as unsupported. Jobs without `options.agent` use `default`, so a project
that excludes it must name an allowed profile. See the
[configuration reference](../reference/configuration.md#projects) for every key.

## Restart and inspect

```sh
herdr-kata stop
herdr-kata up
herdr-kata doctor --json
herdr-kata status --json
herdr-kata run <job-name> --project team-project
herdr-kata runs --project team-project
```

Config changes take effect at worker start. Each project starts independently;
a refused route pauses that project's admission and reporting while other
projects continue. A removed project's saved runs retain their reservations
and wait for that project. Restore its entry to continue; use
[recovery controls](../operations/recovery.md) for an explicit handoff.

A job name must be unique across projects unless `--project` selects one.
Run UIDs already identify their project for `logs`, `resume` and `release`.
Lost-state `abandon` needs `--project` with several configured projects.
The [TUI](tui.md) names other projects in `#` cells and row names, shows
project counts, searches project names with `/`, and asks for a project
before `n` opens a draft.

Kata controls each project's visibility; local status, logs and the TUI
combine all configured projects for the host owner. Read the
[trust model](../design/trust-model.md) before serving projects with
different audiences.

## Remove a project

1. Stop this runtime with `herdr-kata stop`.
2. Remove the project's `[[projects]]` entry from `config.toml`.
3. Run `herdr-kata up`, even if you removed the last project.
4. Check `herdr-kata status --json`. The removed lane, schedules and runs no
   longer appear. Retained run artifacts remain available through `logs`.

The worker hands unfinished work to the operator without killing its occupants
or sending another prompt. Completed results retain their saved outcome and
continue reporting through the original route. The worker also releases this
installation's lease. [Lane ownership](../reference/kata-adapter.md#lane-ownership)
owns the lease's visibility and conditional-write rules.

Retirement first resolves configured project UIDs, so renaming a project keeps
its lane and unfinished work. Retirement waits if a configured project's
identity is unavailable. It also needs the original route and credentials. If a removed entry held
its only credential reference, restore the entry and resolve its work before
removing it. An unavailable route leaves the journals preserved and retirement
waiting in the runtime log; it does not block configured projects. A lease
that cannot be released expires after its normal ten-minute TTL.

## Limits

- An older lane pin without a saved project name stays visible until a
  configured lane resolves and saves its name. A missing name alone cannot
  prove the project was removed.

- One installation serves its configured projects in one Herdr session, within shared execution limits.
- Use one herdr-kata installation per project across every route. A second
  installation serving the same project may execute an occurrence twice.
- Allowlists restrict resource selection; they are not a sandbox for
  untrusted code.
- Config changes, including added or removed projects, take effect only at
  `stop` then `up`; see [Add a project](../reference/configuration.md#add-a-project).
