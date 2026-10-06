# Jobs

A saved job is a native Kata definition with an immutable ULID and a winning
event UID. Configure the native target and local checkout mapping before saving;
new jobs start disabled. Definition edits and deletion use the selected winning
event as a CAS guard. Enabling and pausing change only local activation. Failed saves leave the displayed draft intact.
Private SQLite job rows are not a fallback definition store. See
[native setup](native.md).

Explicit `job run` executes a freshly read native definition through the local
runner. Local scheduling requires `native activate <job-uid>` and a mapped
checkout. Offline cache reads cannot start new work; saving a definition alone
creates no local timer or process.

## How a run behaves

A headless run is invisible until it finishes, and a run that stops to ask a
question is a lost run. Herdr Kata runs each job as an interactive Herdr agent
instead, so `herdr agent attach` drops you into one while it is happening. Three
things follow from that, and they are most of the design:

- **Park, never drop.** A timeout, a `blocked` agent, and "the agent exited
  without writing `result.json`" all park the run for a human rather than
  discarding it. The tab is left open, so the work is still there to look at.
- **One result channel.** Each run gets `HERDR_KATA_RUN_DIR`, and the `result.json`
  the agent writes there is the *only* authority on the outcome. Terminal output
  is archived as `transcript.txt` for humans and is never parsed — an agent's
  narration of its own success is the least reliable artifact in the system.
- **A workspace Herdr Kata owns.** Runs live in a workspace Herdr Kata created,
  identified by the id recorded in `~/.herdr-kata/workspace.json` — never by being
  called Herdr Kata, which may be a name you already used. A space Herdr Kata did not
  make is never adopted, so your own session is never touched.

Every job has an immutable normalized **ULID**, used for native references, and
an editable **name** for display. Use the retained ULID when retrying a create;
existing edits use the winner shown by the last successful read. Checkout paths
must already have an explicit local mapping. Additional absolute directories are
refused until a portable mapping exists.

```bash
herdr-kata native checkout primary /path/to/checkout
herdr-kata job add --id 01J00000000000000000000003 --name "Daily brief" \
  --prompt 'Summarize the day.' --cron '0 7 * * *' \
  --cwd /path/to/checkout --model sonnet --permission-mode acceptEdits

herdr-kata job list
herdr-kata job list --tag daily
herdr-kata job show 01J00000000000000000000003
herdr-kata job edit 01J00000000000000000000003 --model opus
herdr-kata job edit 01J00000000000000000000003 --ref 'https://example.com/issues/42'
herdr-kata job edit 01J00000000000000000000003 --ref ''
herdr-kata job prune                  # list finished one-shots, delete nothing
herdr-kata job prune --yes            # tombstone selected winners; keep history
```

Each independently launched execution has its own run UID and local policy.
Another installation may run the same occurrence independently. Reported shared
history does not reserve the issue or permit a launch. See [native setup](native.md)
for scheduling, date readiness, buffered evidence and persistent recovery.

`--ref` attaches one external issue, ticket, or URL to a job. It is copied to
each run when that run starts, so editing the job later does not rewrite what
an earlier run was about. Unlike tags, it keeps case and spacing exactly as
typed. It must fit on one line within 512 bytes. `job show`, `run show`, and
the board detail show it. An agent receives it as `$HERDR_KATA_REF`; a reused
persistent agent also gets the current value in each run's prompt, since its
shell may still hold the value from an earlier run.

Every run records what it cost. Input, output, cache-read and cache-creation
tokens are stored separately because they are billed differently, alongside the
model the run actually used. The numbers come from the agent's own session
transcript, correlated by the run directory Herdr Kata names in its prompt — never
by picking the newest session, which would mis-charge concurrent agents and
persistent jobs. A transcript that cannot be read leaves the counts at zero: a
run keeps its real outcome regardless, since this is bookkeeping, not the work.

Jobs run with permission checks disabled by default. A scheduled job is
unattended, so a permission prompt has nobody to answer it: the run would park
until a human noticed, which is worse than useless for work meant to happen at
04:00. Pass `--skip-permissions=false` for a job you intend to supervise.

Every job names a model. Leaving `--model` unset resolves to `sonnet` rather
than to whatever the agent happens to default to, so a schedule's cost and
capability cannot change underneath it without the job changing.

`--autocompact` sets the agent's auto-compact window — `auto`, or a token count
from 100000 to 1000000:

```bash
herdr-kata job edit <job-uid> --autocompact 200000
```

Every model turn rereads the conversation so far, so on a long run the same
prefix is paid for again on every later turn. Measured on one job here: 119
turns per run, 63K–93K tokens of inherited context before any useful work, late
turns rereading 193K–251K tokens each — 15.6M cache-read tokens per run against
136 KiB of actual tool output. The context was not full of results; it was full
of a prefix, replayed.

A smaller window caps how large that prefix grows before it is summarised, so it
shrinks every later reread. It is not free: compaction discards detail, and a
window near the job's own baseline makes a run compact, immediately refill and
compact again — thrashing, which costs more and forgets more. Size it above the
inherited baseline plus the largest single tool result the job must hold.

The value is checked when the job is written rather than when it runs, because
the alternative is finding a typo at 04:00 as an agent that refuses to start.
It is emitted before `--extra-args`, so a job that spells the flag into its own
passthrough still wins.

Two things it is not: it does not shrink the baseline, which is the bigger prize
— trimming an inherited `CLAUDE.md` by 20K tokens saves that on every one of
those 119 turns — and it does nothing for a short job that never approaches the
window at all.

Schedules: `manual`, `--cron`, `--interval`, `--at` (one-shot, disables itself
after it runs). `--catchup` decides what happens to fires missed while nothing
was running:

| policy | after six missed hourly fires |
|--------|-------------------------------|
| `latest` (default) | one run, for the window as a whole |
| `all` | six runs, in series, one after another |
| `skip` | nothing — the window is gone |

A fire less than a minute old counts as current rather than missed, so `skip`
still runs the fire the scheduler is standing on. `all` replays in series under
one claim, so a backlog cannot stack up parallel agents, and replay is bounded
at 100 fires however long the downtime was.

A one-shot whose most recent run came out `done` is finished: it can never fire
again, so both the board and `job list` put it away, and both say how many they
withheld (`3 finished hidden`) rather than letting it vanish. `F` on the board
and `--all` on the command line bring them back, marked `(finished)`. Nothing
else is hidden: a parked one-shot still wants a human, a failed one must be
noticed, one that has never run is still pending, and a recurring job is never
finished whatever its last run says. `herdr-kata job prune` clears them, and lists
what it would delete without deleting anything until `--yes`. `P` on the board
does the same thing, and asks first: it names every job it would remove and
waits for `y`, since anything else cancels. Deleting a job keeps its runs, so
pruning loses the schedule, not the record of what happened.

`--persistent` reuses one long-lived agent per job instead of starting a fresh
one each run, skipping tab and startup cost. The agent's context is cleared
with `/clear` before every run, so runs stay independent.

Add `--keep-context` to carry one conversation across those runs. Herdr Kata
captures the harness session reported by Herdr and saves it per job. If the
agent disappears, Herdr Kata first adopts a live agent holding that session in
the job's workspace, then tries the harness's resume arguments. Codex and
Claude resume by ID; Pi/OMP resume by ID or session path.

```sh
herdr-kata job add --id <retained-job-ulid> --name Followups --prompt 'Check pending follow-ups' \
  --cwd /path/to/checkout --persistent --keep-context --on-context-loss park
herdr-kata job run <retained-job-ulid>
herdr-kata run show <run-id>
```

Every context-enabled run records `fresh`, `kept`, `adopted`, `resumed`,
or `lost`. The status, session and explanation appear in the run record,
run note and `context.json` artifact; the job inspector shows the status.
An explicit session mismatch refuses live reuse for inspection; a new unrelated
session in the same pane does not become trusted provenance.

`--on-context-loss fresh|park` is editable on the CLI and board. The default
is `fresh`: unsupported resumes or failed readiness start a new conversation
and record `lost`. `park` sends no job prompt and retains the saved session
for a human to recover. A conversation held outside the job's workspace
always parks, to avoid prompting somebody else's pane or duplicating it.
Failed resume tabs are closed before a fresh replacement is started.
Observation errors preserve the resumed pane and park for inspection rather
than discard a conversation that may still be healthy. Missing optional session
metadata after a successful resume is recorded as unconfirmed; it does not
force a fresh conversation.

Herdr must report `agent_session` for durable recovery. Older Herdr still
reuses live agents, but cannot save their conversations for later resume.
Enable the harness's Herdr integration where required. A first run after
enabling context starts fresh when there is no live agent or saved session;
if a later context-enabled run loses its agent without a captured session,
it reports `lost`. Non-context jobs and workflow steps do not capture or resume.

The jobs tab fills the space to the right of the table with an inspector for
the selected job: state, schedule, when it next fires, model, timeout, tags,
working directory, permissions, and how the last run went. It is a summary —
the full record, the prompt, and the run history are on the detail page. The
panel is dropped when the pane is too narrow to render it legibly.

Text too long for its column scrolls, the way a station board scrolls a
destination that does not fit, rather than being cut off. An ellipsis says
something was hidden; scrolling says what it was. Every overflowing cell shares
one frame counter, so the board scrolls in step instead of each cell drifting on
its own phase.

## Editing from the board

Select a job in JOBS and press `enter` to view it, then `e` to edit. You can
also press `e` directly on the jobs list. The detail page keeps its edit action
visible while you scroll through the run history.

| key | action |
|-----|--------|
| `n` | new job (id is derived from the name) |
| `e` | edit the selected job, from the list or its detail page |
| `D` | delete the job (run history is kept) |
| `l` `→` `enter` | descend: jobs → job → agent, or runs → run → its job |
| `h` `←` `esc` | ascend one level |
| `tab` | cycle the JOBS / RUNS / WORKFLOWS / LEASES tabs |
| `space` | toggle a yes/no field |
| `ctrl+s` | save the whole job, including the field being typed |
| `tab` / `shift+tab` in the form | commit the field and move forwards / backwards |
| `enter` in a text field | commit a single line; insert a newline in Prompt |
| `esc` in the form | abandon all unsaved edits, including the active field |
| `h` / `←` in the form, outside a text field | cycle a choice backwards; on other fields, abandon all unsaved edits |

Edits are made against a copy and only reach the store on save, so abandoning
an edit cannot leave a job half-changed, and a run scheduled mid-edit still
uses the old definition. Cross-field rules are checked on save: a job set to
`cron` with no expression is rejected rather than stored as something that can
never fire.

Invalid field input stays open with its error so you can correct it. The save
controls and errors stay visible in short panes. While a save is in progress,
the form shows `saving…` and waits for the result before accepting more edits
or cancellation. A failed save keeps the draft; a successful save returns to
the updated job detail or list with the job's ID and run history preserved.

---

[← back to the README](../README.md)
