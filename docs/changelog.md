---
title: Changelog
description: Release history for herdr-kata.
last_edited: 2026-10-25
---

# Changelog

All notable changes to herdr-kata, grouped by release.

## Unreleased

- Cron run reports link known Codex and Claude sessions across prompt steps,
  retries, holds, handoffs and resumed runs. Reports retain up to 64 unique
  sessions and explicitly mark missing or excess sessions as incomplete;
  saved token usage is unchanged. Session links require a Kata CLI and daemon
  that both advertise `cron_run_sessions_v1`. Older combinations continue
  reporting ordinary outcomes. Lost-state abandonment preserves saved links.

## 0.1.0
<small>2026-10-21</small>

https://github.com/user-attachments/assets/38ec3cd3-1e22-42bb-b48e-59614ec4e41d

- `doctor` and `up` find Herdr 0.9.5's `default` session socket outside a
  Herdr pane. `HERDR_SOCKET_PATH` still takes precedence; named sessions keep
  their existing socket paths.
- With no configured projects, `up` now explains when retained lanes require
  a runtime for retirement and saved reports. With no retained lanes, it
  still prints `nothing started`.
- After `up --takeover` elsewhere, the stood-down installation refuses `run`,
  `resume`, `release` and `abandon` for that lane at once with up's wording,
  `another installation (<executor>) owns this lane: lease generation <n>`,
  instead of queuing them. A run queued before the takeover settles as
  refused, and `run --wait` returns within a fraction of a second rather than
  at its timeout. The TUI's `R` and `u` show the same refusal. The refusal's
  request result carries the new `reason` field, `lease_held_elsewhere`;
  older builds reject the unknown field, so after a rollback remove such
  results from `request-results/` if `status` reports them as corrupt.
- Lists sit as tight as Kata's issue list: one cell between columns, every
  column as wide as its content, and one flex column (`job` on Jobs, `detail`
  on Runs, `workflow` on Workflows) taking the rest. The issue column is
  headed `#`, as in Kata. At narrow widths the flex column gives way first,
  down to Kata's 20-cell title floor, then model; `#` and status never shrink.
- The project column is gone. An issue outside the default project shows as
  `project#abcd` in `#`; a row there with no issue, and its workflows, prefix
  `[project]` to their name.
- The list header is one span in Kata's table-header style, gaps and gutter
  included.
- Job detail's Runs list fits its outcome, trigger and age cells to their
  content and gives the note the rest; a clipped note scrolls on the selected
  row, as on the Runs tab.
- Run statuses use Kata's words and colors. Finished runs show `succeeded`
  or `cancelled` in Kata's closed gray instead of a green `done`; `running`
  and `queued` are green like Kata's `open`; `failed` is Kata's error red. A
  run handed to a person shows `needs-human`, Kata's attention value, instead
  of `handoff`, and a step that hasn't started shows `not started` instead of
  `pending`. One table drives the board, `runs` and `status`, which now prints
  the status word in place of the raw phase. The new
  [Run statuses](reference/statuses.md) page lists every word.
- A workflow with steps shows Kata's `▸` marker on Workflows; `space` expands
  it to `▾` with its numbered steps.

