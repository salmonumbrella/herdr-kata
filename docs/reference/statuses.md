---
title: Run statuses
description: Every run and step status word the board and the CLI print, what it means, and its color.
last_edited: 2026-10-21
---

# Run statuses

The board and the CLI print the same status words from one table. Where Kata
has a word for a cron run (`running`, `succeeded`, `failed`, `cancelled`,
`unknown`), herdr-kata uses it. A run handed to a person shows `needs-human`,
the `work.attention` value herdr-kata sets on its Kata issue.

Colors follow Kata's issue list, where `open` is green and `closed` is gray:

| Role | Color | Means |
|---|---|---|
| active | green, like Kata's `open` | The run is moving on its own. |
| attention | pink | The run needs you to look or act. |
| failure | red, Kata's error color | The run failed. |
| finished | gray, like Kata's `closed` | Nothing more to do. |
| idle | dim | Nothing has happened yet. |

`none` and `NO_COLOR` drop the colors and keep the words.

<!-- status-legend -->
| Status | Applies to | Kata word | Means | Role |
|---|---|---|---|---|
| `queued` | run | no | Admitted and waiting to start. | active |
| `running` | run, step | yes | An agent or command is working. | active |
| `blocked` | run, step | no | The agent is waiting for input in its pane. | attention |
| `parked` | run, step | no | Stopped before finishing; resume it with `u` or `herdr-kata resume`. See [hold reasons](holds.md). | attention |
| `needs-human` | run, step | Kata's attention value | Handed to a person, by `release` or by the agent's `needs-human` report. The Kata issue stays open with `needs-human` attention. | attention |
| `unknown` | run, step | yes | Ended, but the result could not be confirmed; check the run. | attention |
| `failed` | run, step | yes | Ended with a failure. | failure |
| `succeeded` | run, step | yes | Finished; nothing more to do. | finished |
| `cancelled` | run | yes | Stopped on purpose; nothing more to do. | finished |
| `never` | job, workflow | no | No retained run. | idle |
| `skipped` | step | no | The workflow did not need this step. | finished |
| `not started` | step | no | The step has not started. | idle |
| `quota` | step | no | The step waits for the agent's usage limit to reset. | attention |
| `rejected` | step | no | Herdr refused the step's prompt. | attention |
| `stopped` | step | no | An earlier attempt of the step stopped without a result. | attention |
<!-- /status-legend -->

A `parked`, `failed`, `unknown`, `quota` or `stopped` step adds its reason in
parentheses, for example `parked (loop_exhausted)`. Archived attempts with no
recorded result show `sent, outcome unproven` or `not sent`.

`herdr-kata runs` prints Kata's recorded status, except that a matching local
journal shows `parked` or `needs-human`, which Kata records as `running` or
`unknown`. `herdr-kata status` prints the local status word before the
recorded outcome.
