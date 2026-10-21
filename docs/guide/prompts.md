---
title: Write prompts that close their issue
description: Write job and workflow prompts whose runs report enough evidence to close their Kata issue.
last_edited: 2026-10-17
---

# Write prompts that close their issue

A successful run closes its per-run issue or recurring child only when the
agent's report carries usable close evidence. A success without it stays
open with the `needs-review` label and `needs-human` attention; see
[Close or review](../reference/reporting.md#close-or-review). Write each
prompt so the agent knows what to verify and which evidence proves it.

Existing issues are never closed by the runtime; their runs only comment.
The rules below still decide what the comment reports.

## Know what the runtime adds

Every attempt's prompt already contains the issue reference, title and body,
the job, run, step and attempt, any workflow context and input, and the
result contract: the exact report command, the evidence types, the summary
rule and the close reasons. [Prompt contents](../reference/agent-contract.md#prompt-contents)
lists it in full. Your prompt does not need to explain how to report.

It does need to say what counts as done. Name the check the agent must run
or the file it must read, and say which evidence to report. Agents choose
evidence more reliably when the prompt names it, as the quickstart does:

```text
Read README.md and report its first heading. Do not change any files.
Report succeeded with a summary stating the heading you verified and
evidence reviewed-paths:README.md.
```

## Ask for a summary that passes

The summary becomes the close message. After the runtime lowercases it,
collapses whitespace and removes trailing `.`, `?` or `!`, it must be at
least 40 bytes and not a bare word such as `done`, `fixed` or `ok`. Ask for
a sentence that says what was done and how it was verified, for example
"Fixed the date parser and verified the focused parser tests pass." The
runtime never pads a short summary.

## Ask for the right evidence

Choose the close reason by outcome:

| The work | Close reason | Evidence the prompt should ask for |
| --- | --- | --- |
| Changed something or verified a fact | `done` | At least one `commit:<sha>`, `pr:<url>`, `test:<command>`, `reviewed-paths:<path>` or `external:<account>`, and no other type |
| Checked and found nothing to change | `audit-no-change` | Exactly one `no-change-audit:<rationale>`, optionally with `reviewed-paths:<path>` |

`commit` takes a 7–40 character hexadecimal SHA. Every payload must be
nonempty, and the same raw item may appear only once. An all-command
workflow needs no prompt evidence: the runtime closes with `test:` evidence
from the successful commands. A mixed workflow uses the last prompt's report
and adds the successful command evidence.

For a recurring audit, say both paths:

```text
Check go.mod for dependencies with published security advisories.
If you update one, commit the change, run the tests and report succeeded
with evidence commit:<sha> and test:go test ./...
If none needs updating, report succeeded with close reason audit-no-change
and evidence no-change-audit:<what you checked> and reviewed-paths:go.mod.
```

## Say when to hand off

Tell the agent when to report `needs-human` instead of guessing: missing
access, a decision only a person can make, or an instruction that conflicts
with the checkout. That report hands the run to a human. The issue gets
`needs-human` attention and a notification, and the agent's tab stays open.
Report `failed` when the task itself fails; a workflow can retry a failed
step or loop back to its maker.

## Leave a note for the next step

In a workflow, a prompt step can pass one line to the next executed step
with `--note`. The next prompt reads it as `{{previous}}`, and a command
reads `$HERDR_KATA_PREVIOUS`. Ask for a note only when the next step needs
it, and say what it should hold, for example "Publish a note naming the
files you changed." [Build workflows](workflows.md#supply-input-and-publish-notes)
shows a complete chain.

## Limits

- A note is one line of at most 280 bytes. Without one, `{{previous}}` is
  empty; the summary is never used instead.
- Input and notes are shared text. Do not ask an agent to put credentials,
  logs or environment dumps in them.
- The runtime validates evidence shape, not truth. A report claiming a test
  it did not run still closes the issue.
