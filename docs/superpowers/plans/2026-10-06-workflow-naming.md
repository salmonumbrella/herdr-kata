# Plugin Workflow Naming Implementation Plan

**Goal:** Apply the requested vocabulary in this plugin repository: jobs, workflows, runs, and resource leases.

**Architecture:** Keep the existing execution, scheduling, retention, identity, expiry and renewal behavior. Rename the complete plugin surface, including Go packages, CLI, board, JSON/YAML, draft paths and documentation. Use Kata's `cron`, `cron_v1`, `workflow_uid` and workflow definition endpoints. Remove retired command aliases. No live-state writes or automatic local-state conversion.

**Tech stack:** Go, Bubble Tea, SQLite, YAML, Kata CLI.

**Specification:** The user requested a plan followed by a complete breaking rename and corrected the target repository to this plugin. Jobs and runs remain unchanged. The user confirmed leases remain unchanged and explicitly approved the plugin SQLite workflow-column and stored-field rename before schema edits.

## Constraints and review focus

- Preserve execution and resume snapshots, exact numeric payloads, holder matching, expiry and renewal.
- Keep file-system mutexes and external Kata issue/federation concepts distinct from resource leases.
- Keep `kata cron`; latest remote main already uses it.
- Preserve historical upstream fixtures where they describe actual imported legacy state.
- Never rewrite history or overwrite another worktree.
- Check saved workflow references, board actions, hook environment keys, CLI dispatch, native draft receipts, imports and recovery.

## Task 1: Pin public names

- [x] Add failing behavior tests for canonical workflow dispatch/draft paths, retired flow rejection, and Kata workflow command construction.
- [x] Keep lease/claim/renew/release terminology as explicitly requested.

## Task 2: Rename the plugin

- [x] Rename packages, identifiers, commands, board labels, flags, paths, JSON/YAML and native references.
- [x] Apply the explicitly approved local SQLite workflow-column and stored-field rename; preserve lease schema.
- [x] Update maintained docs, examples, hooks, demos and plugin skills; document breaking state compatibility.
- [x] Run targeted tests to green and audit retired spellings.

## Task 3: Deliver

- [x] Run repository checks and a real updated-Kata integration test where supported.
- [x] Obtain one independent whole-change review; address substantive findings.
- [x] Commit, push and open a PR in this repository (no existing PR was open at start).
- [x] Record completion and validation on the tracked issue.

## Validation record

Canonical workflow command/draft-path, retired flow-command rejection, native
workflow command construction, and legacy SQLite refusal tests were observed
failing before implementation and passing afterward. A regression test also
checks old upstream workflow columns retain their reference and input on import.

The independent review found no important correctness problems. Two remaining
job-show labels were corrected. Full race testing caught the sorted JSON fixture
position for the renamed workflow key; its correction passed targeted validation
and the complete race rerun.

Real integration passed against the workflow-enabled Kata build: CLI adapter,
product mappers, workflow edits, stopped-snapshot import, and isolated real Herdr
plugin packaging and shell-workflow execution. All runtime state was temporary.
The repository CI command also includes build, vet, and six target compilation
checks; target compilation is not a claim of native runtime testing on each OS.

Final `make ci` passed: formatting, build, vet, complete race suite, and Linux/macOS/Windows amd64/arm64 compilation.

Delivered in [PR #3](https://github.com/salmonumbrella/herdr-kata/pull/3). The owned validation runner was deleted after checks completed.
