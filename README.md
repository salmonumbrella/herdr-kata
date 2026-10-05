# Herdr Kata

A Herdr plugin for Kata-native cron jobs, flows and agent execution. Shared definitions and run history live in Kata; herdr-kata runs them locally.

![The Herdr Kata triangle, watching](assets/eye.jpg)

[![ci](https://github.com/salmonumbrella/herdr-kata/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/salmonumbrella/herdr-kata/actions/workflows/ci.yml)
[![security](https://github.com/salmonumbrella/herdr-kata/actions/workflows/security.yml/badge.svg?branch=main)](https://github.com/salmonumbrella/herdr-kata/actions/workflows/security.yml)

Ask one agent to do five things and it will do four and report success.
**Herdr Kata moves the sequence out of the agent's head and into the harness.**

- Agent skips step three and reports done — **flows** run the steps as separate
  agents, so skipping is not available to it.
- Step fails at 03:00 and the run sits there until you wake up — an **overwatch**
  reads the whole run and decides: retry, jump back, or stop.
- You are the cron — **jobs** run prompts and flows unattended, and park instead
  of dying when one needs an answer.
- Two agents grab the same browser — **leases** give it to one, and tell the
  other who has it and for how long.

Herdr Kata is not an agent. It is the layer beneath whatever agent you already run
on [herdr](https://herdr.dev): a scheduler, a sequencer, and an execution record. A
flow is a sequence the agent cannot skip. A job is a clock it never has to
remember. A scoped resource lease tells an execution coordinator who holds a resource. And every step stays a live terminal you can attach to, interrupt, and
answer. If you have not yet watched an agent skip step three and call the task
done, start with [why sequences belong in the harness](docs/why-the-harness.md)
— it is the failure this tool exists for.

## Install

**Prerequisite: a Go toolchain — and nothing else.** Every step below is a
plain `go build` under the hood: no make, no C compiler, no system packages. If
`go version` answers, you have everything Herdr Kata needs. Linux, macOS, and
Windows are all supported.

Three steps, each independent of the others:

**1. Register the plugin.** This compiles Herdr Kata, starts the scheduler, and
puts the board [one keystroke away](docs/board.md#in-herdrs-sidebar):

```bash
herdr plugin install salmonumbrella/herdr-kata
```

**2. Install the `herdr-kata` command.** A separate copy on your `PATH`, talking to
the same store, so you can drive it from any shell:

```bash
go install github.com/salmonumbrella/herdr-kata/cmd/herdr-kata@latest
```

`go install` drops the binary in `$(go env GOPATH)/bin` — `~/go/bin` by default,
`%USERPROFILE%\go\bin` on Windows. **That directory has to be on your `PATH`**
or the shell will not find `herdr-kata`. If it is not already, add it:

```bash
# Linux / macOS (bash, zsh) — add to ~/.bashrc, ~/.zshrc, or your shell profile
export PATH="$PATH:$(go env GOPATH)/bin"
```

```powershell
# Windows (PowerShell) — persist it for your user, then reload the shell
[Environment]::SetEnvironmentVariable('Path', "$env:Path;$(go env GOPATH)\bin", 'User')
```

```bat
:: Windows (cmd.exe) — persist it for your user, then open a new prompt
setx PATH "%PATH%;%USERPROFILE%\go\bin"
```

**3. Add the skill.** So your agents know how to drive Herdr Kata without being
told each time — see [for the agents](#for-the-agents):

```bash
npx skills add salmonumbrella/herdr-kata
```

Everything lives in `~/.herdr-kata` (`$HERDR_KATA_HOME` overrides). No config
file, no daemon to install. `herdr plugin uninstall salmonumbrella/herdr-kata` removes it
and leaves your store alone.

## Native configuration

This build reads and saves native Kata jobs and flows, refreshes a derived local
cache, and opens the installed Kata TUI. Configure an explicit daemon, project,
workspace and actor before saving. See [native setup](docs/native.md).
Explicit native job execution uses the ordinary local runner and live persistent
sessions. Each execution has its own run UID; shared history is bounded evidence,
and a logging outage buffers observations locally without withholding a process.
Local activation gates automatic scheduling. Direct native flows and saved resume
use local snapshots; ad-hoc prompt-only runs remain in local history. The reduced
native producer supports shared run evidence and ordinary issue-date sources.
Ordinary run observations replay from a bounded local outbox after restart. Failed
writes appear in `doctor` and the board without preventing execution or reuse of
a live persistent conversation. Pending evidence remains frozen through reply
loss; its latest unsent successor follows the acknowledged ordinary revision.
Independent runs keep separate UIDs. Historical result comments retain a stable
idempotency key; retries beyond Kata's seven-day window reconcile the comment
marker before posting. An outage or ambiguous marker leaves the result pending.

Notification-only jobs need no process checkout. Same-issue, zero-lead date jobs
addressed to `current-owner-or-author` delegate to Kata's existing default date
sweeper. Custom recipients, lead times and cross-issue targets use ordinary
`notify`, wait behind an occupied exact recipient slot, and retain local retry
intent. Notifications remain replaceable attention signals. Reading an inbox
never clears or reassigns its requests; the handler clears them after handling.
Restoring the same removed date reactivates attention that was never submitted;
an already completed request stays handled. Restart also repairs unfinished local
history after notification acceptance without submitting the request again.

Connect each runtime to its exact actor or actor/teammate address locally:

```sh
herdr-kata teammate connect --for worker/child --workspace w1 --pane w1:p1 --conversation session-id
herdr-kata teammate list
herdr-kata inbox list --for worker/child
herdr-kata inbox open --for worker/child --ref abcd
herdr-kata inbox deliver --for worker/child
herdr-kata teammate disconnect --for worker/child
herdr-kata doctor
```

Registrations belong to the configured target, project and actor. The parent
inbox does not aggregate teammate addresses. Delivery rechecks the registered
workspace, pane and conversation identity; missing or reused runtimes retain
pending attention and request human handling. Generic Herdr cannot atomically
check the expected conversation and safe input while submitting a prompt, so
`inbox deliver` prints the quoted request for **manual wake** and never types.
Working or blocked runtimes coalesce pending requests. This local bridge is
neither a central executor registry nor an exactly-once inbox.
Polling rotates batches of at most 100 registrations and retains progress across
restart, so larger registries continue to refresh every exact recipient.

```bash
herdr-kata native configure --file mapping.json
herdr-kata native checkout primary /path/to/checkout
herdr-kata native refresh
herdr-kata native tui
```

The board's **K** key opens the installed Kata TUI in the same pane. **i** opens
the selected run's linked issue. Shared history shows bounded reported evidence;
local execution rows retain their own session and artifact handles. The executable
is resolved on every launch, so upgrading Kata updates that UI immediately.

## Flows — a sequence it cannot skip

The failure at the top of this page — four of five, reported as success — is
what a flow removes. The sequence stops being the agent's job:

```yaml
# ~/.herdr-kata/drafts/flows/triage.yml
about: triage an incoming report
input: a report, a PR number, or a stack trace

steps:
  - id: assess
    agent: Look at {{input}} and say in one line whether it is real.
    model: opus
  - id: patch
    agent: "{{previous}} — if that says it is real, write the fix."
  - id: verify
    run: go test ./...
```

Each step is its own agent process, so **B is launched by Herdr Kata, not by A
remembering to hand off**. Call it with an `x` — the same command whoever is
asking, human or agent:

```bash
herdr-kata flow new triage --about "Triage a report"
# Edit the unsaved draft, then save explicitly.
herdr-kata flow save triage
```

A step that fails stops everything after it and keeps everything before it, so
`herdr-kata flow resume <run>` picks up where it stopped without paying twice. A
step that would rather hand the work back than wait for a human says so — a
reviewer's `on_fail: {goto: patch, max_loops: 2}` re-runs the step that caused
what it rejected, bounded and told why.

The run opens **a workspace of its own** for its step tabs. Results, progress,
retry history, and parked-run artifacts remain available when the workspace
closes; a resume reuses the workspace when it still exists.
→ [flows](docs/flows.md)

### The overwatch — one reader who sees the whole run

Every step is a fresh agent that has read nothing but its own prompt. That is
what stops step four inheriting step one's confusion, and it leaves nobody
holding the shape of the run — so when a step failed, the harness could only
take the decision available from outside: park, and wait for a person. Right
when nobody knows why the step failed. A wasted night when the answer is legible
from two steps up.

**Every flow that runs agents has an overwatch**, declared or not. It is handed
the flow as declared, every step's outcome and note, and the artifacts of
whatever just went wrong, and it answers one question — what should this run do
now — as `retry`, `goto`, `park`, `abort`, or `continue`.

```yaml
overwatch:
  model: opus          # the decision is usually harder than the steps it judges
  watch: on_trouble    # default: only where the run would park. every_step sees
                       # every result, at one agent call per step
  budget: 3            # decisions per run, and a resume does not hand it back
  timeout: 10m         # one consult
```

Three things keep it from becoming a way to wave work through. **A declared
`on_fail` edge wins** — it is explicit, free, and the author's; the overwatch is
asked where the flow would park, including when an edge has run out, which is
the moment it is worth most: *not converging* and *pointed at the wrong step*
look identical from inside a loop. **`skip` — accepting a failed step and
carrying on — is not in the default allow-list**, because it is the one decision
that ends what a flow is for; a flow that wants it says so in the file, and
`flow list` prints `+skip` so the choice is visible without opening anything.
And **ambiguity parks**: no decision, unreadable JSON, a disallowed verb, a
`goto` pointing forward — each stops the run with the reason recorded. Nothing
resolves towards carrying on.

A run where nothing goes wrong never starts one, which is most runs.
→ [the overwatch](docs/flows.md#the-overwatch)

## Jobs — a clock it never has to remember

```bash
herdr-kata job add --id 01J00000000000000000000001 --name "Daily brief" \
  --prompt 'Summarize today.' --cron '0 7 * * *' --cwd /path/to/checkout
# New jobs are disabled. The UID stays stable across retries.
herdr-kata job show 01J00000000000000000000001
```

A run that stops to ask a question is **parked, never dropped** — the tab stays
open for a human instead of the work being discarded.
→ [jobs](docs/jobs.md) · [the scheduler](docs/scheduler.md)

## Resource leases

Execution resources have an explicit coordinator scope, holder, run, and expiry.
Use the same scope for callers that share a resource; separate scopes describe
independent resources. A fresh store persists leases independently of issue
tracking. These leases do not reserve issues or scheduler occurrences.

```bash
herdr-kata lease claim browser --scope local:example --as worker --run run-a --ttl 20m --why 'inspection'
herdr-kata lease renew browser --scope local:example --as worker --run run-a --ttl 20m
herdr-kata lease list --json
herdr-kata lease release browser --scope local:example --as worker --run run-a
```

Choose renewal TTL explicitly and match the claim's full holder identity.
Omitted or zero TTL means no expiry, including when renewing a bounded hold.

Contending callers are refused with the current holder. `claim --wait 5m` waits
up to five minutes; expiry is evaluated on each read and survives restart.
→ [resource leases](docs/leases.md)

## Documentation

| | |
|---|---|
| [Why sequences belong in the harness](docs/why-the-harness.md) | the failure this tool exists for, with the transcript |
| [Jobs](docs/jobs.md) | what a job is, its fields, schedules, tags, parking, editing from the board |
| [Flows](docs/flows.md) | the YAML file, the input, what crosses between steps, the run's workspace and step artifacts, parking and resuming, the overwatch |
| [Native setup and adoption](docs/native.md) | local routing, explicit snapshot import, execution, recovery and exact inboxes |
| [Resource leases](docs/leases.md) | explicit coordinator scope, holder, run, expiry, renewal and wait |
| [Hooks](docs/hooks.md) | frozen run-settled events, retries, redelivery, and the executable hook contract |
| [The board](docs/board.md) | every key, the mouse, the tabs, the inspector, search |
| [The scheduler](docs/scheduler.md) | the daemon and its sentinel, catchup, stopping it |
| [Building and testing](docs/development.md) | make targets, version stamping, the demo container |
| [Security](SECURITY.md) | the threat model, what herdr-kata will and will not do to your machine, the scans, reporting |

## For the agents

Most of Herdr Kata's users are not people.
[`skills/herdr-kata/`](skills/herdr-kata/SKILL.md) is an
[Agent Skill](https://agentskills.io): what an agent should read before it writes
an execution resource or calls a flow — including the traps, which is the
half a command's `--help` cannot tell it.

```bash
npx skills add salmonumbrella/herdr-kata
```

A skill only helps an agent that thought to load it, so there is a second one:
[`skills/herdr-kata-install/`](skills/herdr-kata-install/SKILL.md) plants a short
index into the agent's global `CLAUDE.md` — what each record is for, and to
load the full skill before writing. A pointer, not a copy: ask your agent to
"set up Herdr Kata in my CLAUDE.md" and re-running it updates the section in
place instead of duplicating it.

→ [other places to put it, and when to symlink instead](docs/development.md#the-skill)

## Status, and what to trust

Herdr Kata starts at **0.1.0**. It is under development; CI and current-revision automated review determine readiness. See [LICENSE](LICENSE) for the MIT license and copyright notice.

**What a release is actually checked against.** Every part of Herdr Kata — jobs,
flows, resource leases, the scheduler and its off switch —
has Go unit and integration coverage, with opt-in real native-daemon and Herdr
packaging checks documented in [development](docs/development.md). The historical
container suite remains a separate compatibility reference:
[end to end, as a stranger](docs/development.md#end-to-end-as-a-stranger).
Every check in that suite is a promise this README or the docs make; when the
suite and the docs disagree, one of them is a bug.

**What Herdr Kata can and cannot do to your machine.** Herdr Kata schedules, launches,
and records — the shell belongs to the agents it launches, under whatever
permissions the harness gives them. Jobs and flow steps run Claude Code with
permission checks disabled *by default*, because an unattended run has nobody to
answer a prompt; that default and its off switch are documented where you set
them: [jobs](docs/jobs.md#how-a-run-behaves), [flows](docs/flows.md#steps).
Herdr Kata opens no network listener and has no telemetry. Everything it
writes under `~/.herdr-kata` — prompts, transcripts, leases, results — is
created owner-only, `0600` inside `0700`. The full posture, the scans that run
weekly against `main`, and where to report something: [SECURITY.md](SECURITY.md).

Kata owns canonical definitions and run history. Local state under
`~/.herdr-kata` contains the derived cache, installation mappings, leases,
execution journals and unsaved flow drafts. Offline reads are labeled explicitly;
saved writes require the configured native daemon. Legacy job rows and flow files
are not promoted automatically. Use the explicit read-only [snapshot importer](docs/native.md#explicit-snapshot-adoption)
with a retained source ID. It creates dormant native definitions, warns about
obsolete collaboration prompts and keeps runtime history and handles local.

The contract is the CLI. Everything else lives under `internal/`, so nothing here
is importable as a Go library — deliberately.

**The supported harness is [Claude Code](https://claude.com/claude-code).** Herdr
runs other agent kinds and Herdr Kata will launch them, but only Claude Code's flag
spellings are modelled — `--model`, `--effort`, `--agent`, the permission flags.
Another kind gets its passthrough args and none of that, rather than flags
invented on its behalf.

## License

MIT — see [LICENSE](LICENSE).
