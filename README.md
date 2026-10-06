# Herdr Kata

Run scheduled agent jobs and multi-step flows in [Herdr](https://herdr.dev), with shared definitions and run history in [Kata](https://github.com/kenn-io/kata).

You define the work once. Herdr Kata starts it, runs the steps in order, and keeps the terminals and artifacts available when something needs your attention.

- **Jobs** run prompts or flows on a schedule, or when you ask.
- **Flows** launch each step separately, record its result, and stop or retry according to the flow's rules.
- **Runs** keep the outcome, artifacts, and local agent session together. Parked runs can be inspected and resumed.
- **Leases** coordinate shared resources such as a browser, with an explicit holder and expiry.

Kata owns the saved definitions and shared evidence. Each installation owns its checkout mappings, activation, credentials, live sessions, and raw execution artifacts. Installing the plugin does not import your existing Kata issues into the board.

## v0.1.0 is a prerelease

The plugin requires a Kata daemon that advertises `cron_v1`. That backend is currently in [Kata PR #498](https://github.com/kenn-io/kata/pull/498) and has not merged. A normal Kata install may therefore be insufficient. Check the capability before setting up jobs.

Mac runtime testing used Herdr 0.9.3 and Kata commit `deda3451cf24d04e11af455a4f4368fe858ae348`. It covered native definitions, scheduling, shell flows, exact inboxes, and reuse of a live Claude conversation. Linux and Windows builds are checked; their native runtime behavior has not been verified for this release.

## Install

You need Herdr, Git, Go 1.26.6 or newer, and a compatible Kata CLI and daemon. Agent steps also need an installed agent harness. Claude Code has explicit support for model, effort, and permission flags; other Herdr agent kinds receive their passthrough arguments.

Install the tagged plugin:

```bash
herdr plugin install salmonumbrella/herdr-kata --ref v0.1.0 --yes
```

For a separate command on your `PATH`:

```bash
go install github.com/salmonumbrella/herdr-kata/cmd/herdr-kata@v0.1.0
export PATH="$PATH:$(go env GOPATH)/bin"
```

The plugin and command use the same local state under `~/.herdr-kata`. Set `HERDR_KATA_HOME` to select another installation.

## Connect Kata

Create a local `mapping.json` with your explicit daemon, project, checkout, and actor:

```json
{
  "Client": {
    "Executable": "kata",
    "Target": {
      "Server": "http://127.0.0.1:7777",
      "Project": "my-project",
      "Workspace": "/path/to/checkout",
      "Actor": "worker",
      "Home": "/path/to/kata-home"
    }
  },
  "Binding": {
    "ExecutorLabel": "my-machine"
  }
}
```

Then configure and inspect it:

```bash
herdr-kata native configure --file mapping.json
herdr-kata native checkout primary /path/to/checkout
herdr-kata native refresh
herdr-kata doctor
herdr-kata board
```

Configuration checks the daemon's capability and binds the selected project. It does not create jobs or start agent work. See [native setup](docs/native.md) for named daemons, authentication, teammate addresses, secrets, and snapshot adoption.

## Use it

The board has jobs, runs, flows, and leases. Select a job and press **R** to run it. Press **K** to open the configured Kata project TUI, or **i** on a linked run to open its issue. See [board controls](docs/board.md).

Create a job, inspect it, and try it manually before activating its schedule:

```bash
herdr-kata job add --id 01J00000000000000000000001 --name "Daily brief" \
  --prompt 'Summarize the current project state.' --cron '0 7 * * *' \
  --cwd /path/to/checkout
herdr-kata job show 01J00000000000000000000001
herdr-kata job run 01J00000000000000000000001
herdr-kata native activate 01J00000000000000000000001
```

New jobs start disabled. Activation enables scheduling on this installation. `native deactivate <job-uid>` pauses that job; `herdr-kata stop` stops the local scheduler pair. Explicit manual runs remain available.

Agent jobs and flow steps disable permission checks by default for unattended execution. Review the prompt and checkout before running them. Jobs support `--skip-permissions=false`; [flow options](docs/flows.md#steps) cover the equivalent step setting.

For a flow, create an unsaved draft, edit its steps, then save it to Kata:

```bash
herdr-kata flow new inspect --about "Inspect and verify a checkout"
herdr-kata flow edit inspect
herdr-kata flow save inspect
herdr-kata flow list
herdr-kata flow run <saved-flow-uid> --cwd /path/to/checkout
herdr-kata flow resume <parked-run-uid>
```

Flows support shell and agent steps, bounded failure loops, and an overwatch that can decide how to handle trouble. The harness controls step order; each step still needs a useful prompt and a meaningful check. See [flows](docs/flows.md).

Exact Kata inboxes can be connected to a local agent conversation. Generic Herdr delivery prints the quoted request for manual submission. It does not automatically wake an agent or clear the request. See [inboxes](docs/native.md#results-attention-and-exact-inboxes).

## For agents

Install the CLI skill so agents can inspect jobs, run flows, handle parked work, and use leases:

```bash
npx skills add salmonumbrella/herdr-kata
```

The full instructions are in [skills/herdr-kata](skills/herdr-kata/SKILL.md).

## Docs and development

| Guide | Covers |
|---|---|
| [Native setup](docs/native.md) | Kata routing, mappings, activation, recovery, and inboxes |
| [Jobs](docs/jobs.md) | Schedules, prompts, permissions, and persistent sessions |
| [Flows](docs/flows.md) | Steps, results, failure handling, overwatch, and resume |
| [Board](docs/board.md) | Navigation, inspection, and agent attachment |
| [Leases](docs/leases.md) | Resource ownership, renewal, expiry, and contention |
| [Scheduler](docs/scheduler.md) | Daemon, sentinel, catchup, and stopping |
| [Hooks](docs/hooks.md) | Run-settled events and delivery retries |
| [Development](docs/development.md) | Builds, tests, local linking, and releases |
| [Security](SECURITY.md) | State permissions, execution boundaries, and reporting |

```bash
make ci
KATA_NATIVE_TEST_BINARY=/path/to/compatible-kata \
HERDR_NATIVE_TEST_BINARY=/path/to/herdr make native-smoke
```

`make ci` runs formatting, build, vet, race tests, and six platform builds. The native smoke starts isolated Kata and Herdr servers, installs the actual manifest, runs a shell flow, and cleans up its own state. It does not use your personal Kata database.

MIT. See [LICENSE](LICENSE).
