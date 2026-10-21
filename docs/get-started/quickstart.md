---
title: Quickstart
description: Run a prompt job in Herdr, inspect its evidence in Kata and put it on an interval.
last_edited: 2026-10-20
---

# Quickstart

Run one prompt job on this machine and see its issue close with evidence in
Kata. Then put the same job on an hourly interval.

You need the [installation requirements](install.md#check-the-requirements),
an authenticated Codex installation, Git and `jq`. Start a Herdr session named
`work` and open a shell there. Use a checkout containing `README.md`.
Replace the example paths, session, project and actor with your own throughout.
The Kata binary must be on `PATH`.

## Install and select the project

Install the plugin and put its built executable on this shell's `PATH`:

```sh
herdr plugin install salmonumbrella/herdr-kata --yes
plugin_root=$(herdr plugin list --plugin kata --json | jq -r '.result.plugins[0].plugin_root')
export PATH="$plugin_root/bin:$PATH"
```

Use a local Kata home and initialize the checkout's project:

```sh
cd /srv/projects/example
export KATA_HOME=/srv/kata
kata init --project example-project --as worker --agent
```

`/srv/kata` must be writable by your user. Keep this `KATA_HOME` in the
shells you use below so Kata and the runtime reach the same project.

## Configure one project

The first `herdr-kata up` can discover the current route instead of requiring
this file to be written by hand. From the live `work` session, with `kata` on
`PATH`, it probes the active compatible projects and writes a private
config containing their route, actors, profiles and generated executor. It
adds only projects that already have a job or workflow, so name this new
project explicitly with `herdr-kata up --project example-project`. It skips
inaccessible or incompatible projects and leaves the file absent when the
route or credentials cannot be proven. Add the checkout mapping below to the
generated file before running this tutorial's job.

Find the plugin's configuration directory:

```sh
herdr plugin config-dir kata
```

Write `config.toml` in that directory, or run
`herdr-kata up --project example-project` first and edit the generated file. Use the absolute path to your installed Kata binary and
the same Kata home and checkout as above:

```toml
session = "work"
executor = "work-runtime"

[kata]
bin = "/usr/local/bin/kata"
env = { KATA_HOME = "/srv/kata" }

[[projects]]
name = "example-project"
actor = "worker"

[checkouts]
repo = "/srv/projects/example"

[profiles.default]
kind = "codex"
args = []
```

The `default` profile uses your installed Codex and its normal settings.
Use an executor label unique to this installation.

## Start and check

```sh
herdr-kata up
herdr-kata status
herdr-kata doctor
```

Once startup completes, `status` includes:

```text
runtime running: true; worker running: true
project example-project
route allowed: true
```

Every doctor check should say `PASS`. A profile marked `not probed` has not
yet launched an agent. Before starting, `up` rejects a missing or non-executable
Kata binary and a missing checkout or a checkout path that is not a directory.
It prints the same resource error as doctor. If `config.toml` is missing,
`up` discovers it only when run from a live named Herdr session. The concurrent
`tui --startup` hook quietly skips a missing config so discovery can finish.
`doctor` stays read-only; it prints the config path, this guide's path and a minimal
configuration example when the file is absent.
If `up` produces no output, check that this shell's
Herdr session matches `session` in the config. For a failed check, follow
[Run the runtime](../operations/runtime.md) before continuing.

If doctor reports a launch environment leak, remove the named exports from
shell startup files such as `~/.zshrc`, or [guard them in agent panes](../reference/configuration.md#secrets).
If Herdr inherited them, restart its server without them too; restarting alone
does not fix exports in rc files.

## Create a manual job

Save this as `job.json` in the checkout:

```json
{
  "name": "Read the project heading",
  "definition": {
    "version": 1,
    "kind": "job",
    "enabled": true,
    "trigger": {"kind": "manual"},
    "action": {
      "kind": "execute",
      "prompt": "Read README.md and report its first heading. Do not change any files. Report succeeded with a summary stating the heading you verified and evidence reviewed-paths:README.md."
    },
    "issue": {
      "kind": "per-run",
      "title": "Read the project heading",
      "body": "Verify the first heading in README.md without changing the checkout."
    },
    "checkout_key": "repo",
    "overlap": "forbid",
    "catchup": "skip",
    "timeout_seconds": 600,
    "options": {"agent": "default"}
  }
}
```

Create the definition in Kata and save its UID:

```sh
kata --project example-project --as worker cron job create --file job.json --json > created-job.json
job_uid=$(jq -r '.job.uid' created-job.json)
```

This job creates a new issue for each run. The optional `options.run_parent`
groups those issues under an open parent; see [Schedule jobs](../guide/jobs.md#group-run-history-under-a-parent).
You can also create the job with `n` on the [TUI's Jobs tab](../guide/tui.md#edit-or-create-a-definition).

## Run and watch

```sh
herdr-kata run "$job_uid" --wait
```

The command prints the run UID, waits, then prints that UID with `succeeded`
when the local run succeeds. Copy the run UID for the next step.

While it runs, watch the agent's tab in the **Kata** Herdr workspace. Complete
Codex's login or checkout trust prompt if one appears. The runtime supplies
the result-reporting instructions with the task.

In another shell in the same session, with the executable on `PATH`, open:

```sh
herdr-kata tui
```

The Jobs tab shows the job's latest outcome. Press `3` for Runs and inspect
the run. A successful tab closes only when the runtime can prove ownership;
an unproven tab stays open. Quit the TUI with `q`; the runtime keeps running.

## Inspect the result

Replace `<run-uid>` with the UID printed by `run`, then inspect run history
and the Kata record:

```sh
run_uid="<run-uid>"
herdr-kata runs --job "$job_uid" --limit 20
kata --project example-project --as worker cron run show "$run_uid" --json > recorded-run.json
issue_uid=$(jq -r '.run.issue_uid' recorded-run.json)
kata --project example-project --as worker show "$issue_uid" --agent
kata --project example-project --as worker events --limit 1000 --json |
  jq --arg issue "$issue_uid" '.events[] | select(.issue_uid == $issue and .type == "issue.closed") | .payload'
herdr-kata logs "$run_uid"
```

`runs` shows `succeeded` for the run and `main` attempt. The Kata issue is
closed with reason `done` and the agent's `reviewed-paths:README.md` evidence.
The `issue.closed` event shows that reason and evidence; `show --agent` shows
the issue's status and comments. Events are paginated. If no close appears,
read `events` without the `jq` filter and repeat with `--after <next_after_id>`
from its JSON output until the issue's close is shown.
If a successful agent omits usable close evidence, the issue stays open for
review; see [Reporting](../reference/reporting.md).

For this prompt-only job, `logs` prints just the attempt header:

```text
[main · attempt 1]
```

`logs` reads command-step output, so watch the Herdr tab for the agent's
conversation. [Workflows](../guide/workflows.md) can mix both kinds of step.

## Put it on an interval

Read the current event guard, replace the manual trigger with an hourly
interval, and update the job:

```sh
kata --project example-project --as worker cron job show "$job_uid" --json > saved-job.json
event_uid=$(jq -r '.job.definition_event_uid' saved-job.json)
jq '.definition.trigger = {kind: "interval", interval_seconds: 3600}' job.json > interval-job.json
kata --project example-project --as worker cron job update "$job_uid" --expected-event-uid "$event_uid" --file interval-job.json --json
```

The saved trigger is now `{"kind":"interval","interval_seconds":3600}`.
The interval is anchored to job creation, and the runtime must stay running
to execute it. Each successful occurrence creates and closes another run issue.
Use the TUI's `p` key to pause the job when you are finished.

Continue with [Jobs](../guide/jobs.md), [Workflows](../guide/workflows.md),
[Multiple projects](../guide/multiple-projects.md) or [Concepts](../guide/concepts.md).
