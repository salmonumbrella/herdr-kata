---
title: herdr-kata documentation
description: Find guides, reference and recovery instructions for herdr-kata.
last_edited: 2026-10-20
---

# herdr-kata documentation

herdr-kata runs Kata cron jobs and workflows as Herdr agents and commands, and
reports each run back to Kata. Kata owns definitions, issues and evidence;
herdr-kata owns execution.

> **Install:** `herdr plugin install salmonumbrella/herdr-kata --yes`
>
> Start with [Install and upgrade](get-started/install.md) for requirements,
> source builds and upgrades, or follow the [Quickstart](get-started/quickstart.md)
> to run your first prompt job.

## Quickstart

The full [tutorial](get-started/quickstart.md) supplies the `config.toml` and
`job.json` used here. Run these in your configured Herdr session:

```sh
herdr plugin install salmonumbrella/herdr-kata --yes
plugin_root=$(herdr plugin list --plugin kata --json | jq -r '.result.plugins[0].plugin_root')
export PATH="$plugin_root/bin:$PATH"
# Write config.toml as shown in the tutorial.
herdr-kata up
herdr-kata status
herdr-kata doctor
kata --project example-project --as worker cron job create --file job.json --json > created-job.json
herdr-kata run "$(jq -r '.job.uid' created-job.json)" --wait
herdr-kata tui
```

## How it works

`up` starts a supervisor and one worker. The worker reads definitions and
listens to Kata events, schedules supported jobs and uses the Herdr socket
to launch agents. Command steps run as worker subprocesses. Local journals
keep execution recoverable; ordered reports carry outcomes and evidence
back to Kata. See [Architecture](design/architecture.md).

## When to use

Use herdr-kata when scheduled prompts, command checks or workflows should run
on this machine, with agents visible in Herdr and history kept in Kata.
Use [jobs](guide/jobs.md) for a single task, [workflows](guide/workflows.md)
for dependent steps, and [recurring issues](guide/recurring-issues.md) when
Kata should create each occurrence as a child issue.

## Next steps

### Learn

| Page | Start here to |
|---|---|
| [Quickstart](get-started/quickstart.md) | Run one job and inspect its evidence. |
| [Install and upgrade](get-started/install.md) | Prepare the machine or replace the plugin build. |
| [Changelog](changelog.md) | Read release changes. |
| [Concepts](guide/concepts.md) | Understand jobs, runs, issues and execution slots. |

### Do

| Page | Start here to |
|---|---|
| [Schedule jobs](guide/jobs.md) | Choose a trigger, issue policy and execution profile. |
| [Write prompts that close their issue](guide/prompts.md) | Ask agents for the summary and evidence that close an issue. |
| [Build workflows](guide/workflows.md) | Combine prompt and command steps, notes and loops. |
| [Run recurring issues](guide/recurring-issues.md) | Bind Kata-created children to a workflow. |
| [Run an agent on an issue's schedule](guide/issue-schedules.md) | Start work when an existing issue becomes ready. |
| [Use the TUI](guide/tui.md) | Inspect runs and edit definitions. |
| [Run multiple projects](guide/multiple-projects.md) | Serve several Kata projects from one runtime. |

### Operate

| Page | Start here to |
|---|---|
| [Run the runtime](operations/runtime.md) | Start, stop, upgrade and inspect health. |
| [Diagnose setup](operations/doctor.md) | Read each doctor check and fix a failure. |
| [Recover runs](operations/recovery.md) | Inspect held work, resume or hand it off. |

### Look up

| Page | Find |
|---|---|
| [CLI and plugin surface](reference/cli.md) | Commands, flags, exit codes and plugin IDs. |
| [Configuration](reference/configuration.md) | Paths, profiles, checkouts and Kata routing. |
| [Job and workflow fields](reference/definitions.md) | Supported fields and limits. |
| [Agent and command contract](reference/agent-contract.md) | Inputs, result reports and evidence. |
| [What herdr-kata writes to Kata](reference/reporting.md) | Observations, outcomes, closure and attention. |
| [State and run phases](reference/state.md) | Journals, phases, outcomes and capacity. |
| [Hold reasons and resume rules](reference/holds.md) | What a hold means and what resume does. |
| [Kata adapter contract](reference/kata-adapter.md) | Scheduling, admission and delivery guarantees. |
| [TUI reference](reference/tui.md) | Keys, tabs, colors and screen fields. |
| [Run statuses](reference/statuses.md) | Every status word, what it means and its color. |

### Understand and contribute

| Page | Read about |
|---|---|
| [Architecture](design/architecture.md) | The process model and kept boundaries. |
| [Run lifecycle and recovery](design/run-lifecycle.md) | Admission, ownership and recovery decisions. |
| [Notes and maker/checker loops](design/loops.md) | How continuation, checking and parking work. |
| [Trust model](design/trust-model.md) | Who may run work on the host and how projects stay apart. |
| [Contributing](development/contributing.md) | Repository layout, checks and documentation rules. |
