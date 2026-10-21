---
title: Configuration
description: Configuration keys, defaults, environment overrides and pinned Kata routing.
last_edited: 2026-10-21
---

# Configuration

One `config.toml` maps one or more Kata projects to a Herdr session, local checkouts
and agent profiles. Kata owns definitions; this file contains installation
settings and no secret values.

When the exact config file is absent, the first `herdr-kata up` in a live named
Herdr session discovers the current Kata route, actor, accessible active
projects that advertise `cron_v1` and have at least one job or workflow, and
usable Herdr agent profiles. It writes one private config file atomically. An existing file is always left alone; use
the explicit form below when you need checkouts, profile arguments, limits or
other installation policy.

Herdr socket discovery prefers `HERDR_SOCKET_PATH`. Otherwise
`session = "default"` uses `$XDG_CONFIG_HOME/herdr/herdr.sock`, and named sessions use
`$XDG_CONFIG_HOME/herdr/sessions/<session>/herdr.sock`. The default config
home is `~/.config`, matching Herdr 0.9.5.

## Files and overrides

Config uses `HERDR_PLUGIN_CONFIG_DIR/config.toml` when set, otherwise
`$XDG_CONFIG_HOME/herdr/plugins/config/kata/config.toml`,
otherwise `$HOME/.config/herdr/plugins/config/kata/config.toml`.
This matches `herdr plugin config-dir kata`. State uses
`HERDR_PLUGIN_STATE_DIR`, otherwise
`$XDG_STATE_HOME/herdr/plugins/kata`, otherwise
`$HOME/.local/state/herdr/plugins/kata`.

Before discovery, `up` can adopt the old plugin ID's directories. See
[upgrade](../get-started/install.md#upgrade-from-the-old-plugin-id).
Custom directory overrides keep their paths; automatic migration applies only
when both absolute directories end in `kata` and have legacy siblings.

TOML decoding rejects unknown keys. `kata.bin` and all checkout paths must
be absolute. Limits must be positive. `profiles.default` is required.

## First-start discovery

Automatic discovery runs only for `up`, and only when the exact path above is
missing. It first pings the socket selected by `HERDR_SOCKET_PATH` and derives
the named session from that socket. It then reads the current Kata route with
`kata whoami` and `kata projects list`, and probes each active project for
health, capabilities, authenticated identity and route diagnosis. Inaccessible
projects and projects without `cron_v1` are skipped. Each remaining project's
job list, then its workflow list, decides whether it becomes a lane: a project
with no definitions is skipped, because every lane holds an event stream and
route checks on its Kata route. A credential or authentication failure or a
missing usable agent profile leaves the config absent and reports the repair
needed.

`herdr-kata up --project <name>`, repeatable, also configures the named
projects when they have no definitions yet. A named project that is missing,
inactive, inaccessible or without `cron_v1` fails discovery and writes nothing.

When no project qualifies, discovery still writes a valid config with no
lanes, and `up` starts nothing. The generated file lists each compatible
project it skipped for having no definitions as a commented-out `[[projects]]`
block with its probed actor, inbox, daemon and credential source; see
[Add a project](#add-a-project). A project that became inaccessible during its
definition check is left out.

Discovery checks projects one at a time and stops after two minutes. On a slow
route with many active projects it can time out and write nothing; write
`config.toml` by hand in that case.

Existing Herdr agents supply profile kinds without starting another agent. If
the session has no agents, discovery accepts `codex` or `claude` when its
executable is on `PATH`, preferring `codex` for `default` when both exist.
The generated file contains only nonsecret route selectors, project actors and
daemon selectors, credential variable names or private `credential_file`
paths, profile mappings and a generated executor label. Credential values are
never written. Inline Kata credentials are rejected; an opaque workspace-local
or explicit daemon selector is rejected when it cannot be represented by the
generated config. First-start discovery also rejects an ambient
`KATA_AUTH_TOKEN_FILE` because Kata catalog precedence can make it differ from
the selected route; set `KATA_AUTH_TOKEN` or use explicit configuration with
`credential_file` for file-backed credentials. Add checkouts and any profile
arguments or limits that your jobs require, then restart with `up`.

## Top level

| Key | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `session` | String | None | Yes | Herdr session in which `up` may start the runtime. |
| `executor` | String | None | Yes | Unique, stable installation label, at most 256 bytes with no whitespace. |

## Projects

Each `[[projects]]` entry selects one Kata project. There is no fixed project
count limit; execution capacity still follows the configured limits. Entries
replace the [legacy project keys](#legacy-single-project-form) in `[kata]`.
With entries present, `[kata]` accepts only `bin` and `env`; mixing
`project`, `actor`, `daemon`, `inbox`, `credential_env` or `credential_file` there is an error.
See [Run multiple projects](../guide/multiple-projects.md) for a complete example.

A config with no `[[projects]]` entries and neither `kata.project` nor
`kata.actor` is valid and has no configured lanes. With no retained lanes in
local state, `up` prints `nothing started` with the config path. With retained
lanes, it starts a runtime for lane retirement and saved reports and explains
that in its output. This runtime stays running; use `stop` after the retained
work is resolved. Doctor's `projects` check says no projects are configured.

| Key in `[[projects]]` | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `name` | String | None | Yes | Unique project name passed with `--project`. |
| `actor` | String | None | Yes | Actor on this project's route, passed with `--as`. |
| `daemon` | String | Empty | No | Named catalog route passed with `--daemon`. |
| `inbox` | String | Empty | No | Recipient for this project's native recurring children. |
| `credential_env` | String | Empty | No | Credential variable for this project only. |
| `credential_file` | String | Empty | No | Absolute owner-private credential file; requires `credential_env`. |
| `checkouts` | String array | All configured | No | Allowed checkout keys; `[]` permits only the run directory. |
| `profiles` | String array | All configured | No | Allowed profile names; `[]` permits no prompt execution. |
| `commands` | Boolean | `true` | No | Whether command steps are supported. |
| `max_active_runs` | Integer | Global limit | No | Positive project slot cap, no greater than `limits.max_active_runs`. |

Project profile allowlists restrict only prompt steps. Command-only workflows
and notify jobs remain available with `profiles = []`. A command job refused
by `commands = false` names its project and the `commands` setting. Set that
project's `commands = true` to allow command steps.

Every listed profile and checkout must exist in the shared mappings. The
project cap and the global cap both apply; held occupants count against both.
An omitted allowlist differs from an empty one. Nothing inherits another
entry's actor, inbox, daemon or credential. Restart with `stop` then `up` to
apply changes; there is no hot reload.

## Kata

`[kata]` holds the settings every project shares.

| Key in `[kata]` | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `bin` | String | None | Yes | Absolute Kata executable path. |
| `env` | String map | Empty | No | Nonsecret routing variables; keys must match `KATA_[A-Z0-9_]+` or `XDG_[A-Z_]+`, excluding `KATA_AUTH_TOKEN`. |

Finite Kata calls run without a shell in the state directory. They pass
`--json`, the project and actor, and `--teammate herdr-kata`. Their process
deadline is 60 seconds and `KATA_HTTP_TIMEOUT` is forced to `45s`.

The child environment includes `HOME`, `PATH`, `USER`, `LANG`, `LC_ALL`,
`TZ`, inherited `XDG_*`, configured `kata.env`, and only the credential
variable named by `credential_env`. Arbitrary inherited `KATA_*` selectors
do not pass through. Set routing explicitly in `kata.env` or `daemon`.

`daemon` selects a named Kata catalog entry. A URL in that field is treated
as a catalog name. For a direct HTTP route, omit `daemon` and set the URL
in the shared environment instead:

```toml
[kata.env]
KATA_SERVER = "https://hub.example"
```

The `daemon` field in a `[[projects]]` entry also selects a catalog name.

For credential, daemon and executable failures, follow the
[doctor findings and fixes](../operations/doctor.md#findings-and-fixes).

For startup hooks, set `credential_file` to an absolute regular file owned by
the current user with mode `0600`, alongside the `credential_env` name that
Kata's catalog expects. The file takes precedence over that inherited variable.
The runtime resolves it for each Kata child, so a repaired or rotated file is
picked up without exporting credentials into Herdr or the supervisor.
Private-file or ACL validation failures are retried up to three total resolution
attempts per child, covering intermittent macOS ACL reads. Each attempt validates
the file again. A successful retry proceeds without recording a route refusal;
repeated failures refuse the lane. Failures are not retained between resolutions.
Missing, empty, nonprivate or symlink sources refuse the lane. Trailing newlines
are stripped. Values never enter route pins, run journals or command/agent panes.

```toml
[[projects]]
name = "example-project"
actor = "worker"
credential_env = "KATA_PROJECT_TOKEN"
credential_file = "/srv/credentials/kata-token"
```

`up` warns about unavailable credentials; `status` retains the startup cause.
If a hook started a supervisor without its inherited credential and every lane
is unavailable, a later credential-bearing `up` drains and restarts it. A file
source also recovers on the next route probe. Never export the credential from
shell startup files or Herdr's server environment; panes must not inherit it.

### Add a project

Edit `config.toml`, then restart the runtime:

1. Add a `[[projects]]` entry with the project's `name` and `actor`, or remove
   the leading `# ` from every line of a commented block that first-start
   discovery wrote. Copy `daemon`, `inbox` and `credential_env` from another
   entry on the same route.
2. Run `herdr-kata stop`, then `herdr-kata up`. Without a running runtime,
   `up` alone is enough.
3. Run `herdr-kata doctor`; the `projects` check lists the configured names.

`up --project` works only when the config is absent. With an existing file it
fails and names the file to edit; discovery never rewrites a present config.

### Legacy single-project form

Without `[[projects]]` entries, `[kata]` also selects one project with the
keys below. Setting only one of `project` and `actor` is an error. This legacy form still works; new installations use one
`[[projects]]` entry instead.

| Key in `[kata]` | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `project` | String | None | Yes | Explicit project selector for every Kata call. |
| `actor` | String | None | Yes | Actor passed with `--as`; must match the authenticated actor. |
| `daemon` | String | Empty | No | Catalog route passed with `--daemon`. |
| `inbox` | String | Empty | No | Exact recipient for native recurring children; pickup also requires `cron_v1`. |
| `credential_env` | String | Empty | No | Name of the variable carrying Kata credentials. |
| `credential_file` | String | Empty | No | Same file source and constraints as a project entry. |

## Limits

| Key in `[limits]` | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `max_active_runs` | Integer | `2` | No | Shared execution capacity; see [slots and overlap](state.md#slots-and-overlap). |
| `default_timeout` | Duration string | `"2h"` | No | Duration used for run expiry when `timeout_seconds` is zero or absent. Uses Go duration syntax. |
| `max_timeout` | Duration string | `"8h"` | No | Ceiling for job `timeout_seconds`; must be positive and at least `default_timeout`. |
| `max_run_prompts` | Integer | `12` | No | Prompt attempts per run generation, from 1 to 100. |

A job above `max_timeout` is unsupported. Its refusal names both fields and
values, for example `timeout_seconds 1900 exceeds limits.max_timeout 30m0s`.
Reduce the job's `timeout_seconds` or raise `limits.max_timeout`.
Steps, retries and loop visits share one run expiry.

The worst-case prompt count is the sum of `1 + retries` for prompt steps,
plus `max_loops` times the number of prompt steps in the loop span. Commands
count zero. Definitions over their effective cap are unsupported with
`prompt_budget <count> > <cap>`, including when re-read at admission.
The effective cap and timeout duration are frozen when the run is prepared; lowering
limits does not shorten a run in flight. See [Resume rules](holds.md#prompt-budget-and-loop-counts)
for the budget granted by an explicit resume.

## Checkouts

Every key in `[checkouts]` maps a string key to an absolute directory.
The table is optional and empty by default. A job's `checkout_key` or
parent's `herdr_kata.checkout` selects it. Without a selection, execution
uses the run directory. The runtime does not create Git worktrees.

## Secrets

`[secrets]` maps portable secret names to local sources. Jobs reference these
names through `secret_refs` (see [Job and workflow fields](definitions.md)).

```toml
[secrets]
github-review = { file = "~/.config/herdr-kata/github-review" }
service-token = { env = "EXAMPLE_SERVICE_TOKEN" }
```

Use one `env` or `file` source per name. Literal strings and `value` sources
are refused. Names match `[A-Za-z0-9._-]{1,256}`, excluding `.` and `..`.
File paths are absolute or begin with `~/`. Files must be regular, owned by
the runtime user and private (mode 0600); symlinks are refused. Missing,
empty or unreadable sources hold the run before execution. `doctor` checks
every configured source and reports its name and source without the value.

A secret cannot alias a Kata credential source, even under another secret name
or from another project. Configuration loading refuses `env = "KATA_AUTH_TOKEN"`,
any configured `credential_env`, and a `file` source matching any project's
`credential_file`. File matching expands `~/`, normalizes paths and compares
existing file identities, including hard links. Use a separate source for job
secrets.

Sources resolve at each step, so file rotation takes effect on the next
step. Every prompt step in a job with secrets uses a fresh agent and tab,
including when `agent_mode` is omitted. Values are supplied only to the
command or Herdr tab, never to the Kata CLI or the run journal. Inherited
environment variables used as secret sources are omitted from Kata children,
including names on the `XDG_*` allowlist. The selected Kata authentication
credential and explicit non-secret `kata.env` routing remain available.

Prompt steps require a clean Herdr launch environment. `doctor` and the
worker open a temporary probe tab without an agent or secrets and inspect
variable names after the interactive shell reads its startup files. The probe
also records the pane's absolute `HOME`, `CODEX_HOME` and `CLAUDE_CONFIG_DIR`,
which locate agent session logs for token usage and quota detection. Inherited
`KATA_*` names absent from the launch map, and any Kata credential name, prevent
prompt launches. This includes `KATA_AUTHOR`, `KATA_HTTP_TIMEOUT` and
`KATA_TELEMETRY_ENABLED`; the clean-environment rule covers every `KATA_*` name.

The reported names can come from shell startup files such as `~/.zshrc`, or
from the Herdr server environment. Remove or unset them in rc files, or guard
their exports so the runtime's agent and probe panes skip them:

```sh
if [ -z "${HERDR_KATA_RUN:-}" ]; then
    export KATA_AUTHOR=worker
fi
```

If the server itself inherited them, also restart Herdr without them. A server
restart alone cannot remove exports reintroduced by shell startup files.
Run `herdr-kata doctor` after repair, then explicitly resume held runs.

Each project lane keeps its own probe. The worker
checks the server identity before every launch and probes again after a
server or launch configuration change. Doctor checks each lane's launch map.
Command steps remain available while Herdr's environment needs repair.

Command environments remove inherited routing selectors and every project's
credential variable before adding shared `kata.env`, then job secrets, then
runtime variables. The run's project and its entry's daemon override shared
routing. Without an entry daemon, `KATA_DAEMON` is absent, even if shared
`kata.env` sets it. Tabs use the same overlays on Herdr's clean environment.
Every configured credential name and `KATA_AUTH_TOKEN` are removed again
after all overlays. Names from every project's `credential_env` are reserved
for secret mappings.

## Profiles

| Key in `[profiles.<name>]` | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `kind` | String | None | Yes | Herdr agent kind matching `[a-z][a-z0-9-]*`. |
| `args` | String array | Empty | No | Agent arguments; no entry may be an empty string. |
| `max_run_prompts` | Integer | Unset | No | Optional cap from 1 to 100; only lowers `limits.max_run_prompts`. |

The `default` profile must exist. Additional profiles are optional.
`options.agent` selects a profile for a job or a single prompt step;
`options.agent_mode` selects whether a prompt attempt gets a fresh agent. A
profile's `max_run_prompts` also caps every run with a step on that profile.
See [Job and workflow fields](definitions.md).

## Board

| Key in `[board]` | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `pin` | Boolean | `true` | No | Enable the pinned TUI opened by the startup hook. |

This remains `[board]` even though the public command is `tui`.

## Pinned route

The first worker start records the instance UID, project UID, authenticated
actor, executor, binary path, daemon selector, routing map, diagnosed endpoint
and diagnosis state. Credentials are not recorded. The configured route is
trusted, including remote hubs and spoke replicas. The daemon must be ready,
the project must advertise `cron_v1`, and its capabilities project UID and
instance UID must match the pin. Remote diagnosis can report
`project_unverifiable`; that is accepted. `doctor` displays a known federation
role as information only.

Changes to that identity or route stop admission and queued report delivery.
This includes changing `executor`, `kata.bin`, `kata.daemon` or `kata.env`,
or resolving a pinned project UID to another instance. An entry that resolves
to a new project UID creates a new lane; old runs wait for their original project. Changing
credentials does not itself change the pin, but a different authenticated
actor does. Every prepared run retains its target and execution mappings.

The worker loads config at startup; an `up` with the same build does not
reload it. See [Run the runtime](../operations/runtime.md) for restart, route
reconciliation and state-moving procedures.
