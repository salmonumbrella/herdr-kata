---
name: herdr-kata-install
description: Install a short Herdr Kata section into the user's global CLAUDE.md so every future session knows the harness exists and when to reach for it — an index, not the manual. Use when asked to set up Herdr Kata for an agent, wire Herdr Kata into CLAUDE.md, or make agents on this machine herdr-kata-aware. The full instructions stay in the herdr-kata skill; this only plants the pointer.
---

# Installing Herdr Kata into an agent's standing instructions

A skill only helps an agent that thought to load it. This skill puts a short,
always-loaded section into the user's global `CLAUDE.md`, so every session
knows Herdr Kata exists, what each record is for, and to read the full skill
before writing anything. **The section is an index — the manual stays in the
[`herdr-kata` skill](../herdr-kata/SKILL.md).** Do not copy skill content into
CLAUDE.md: an always-loaded copy is paid for on every prompt and drifts from
the skill it copied.

## Steps

1. **Find the file.** `~/.claude/CLAUDE.md` is the global instruction file.
   If it is a symlink, follow it and edit the target — a versioned setup keeps
   the real file in a repo, and editing the link's path directly can replace
   the link with a dead copy. If the file does not exist, create it with just
   the block below.

2. **Check for the managed block.** The section lives between two markers:

   ```
   <!-- herdr-kata-skill:begin -->
   <!-- herdr-kata-skill:end -->
   ```

   Both markers present: replace everything between them with the current
   block, so a re-run is an update, not a duplicate. No markers: append the
   whole block at the end of the file. One marker without the other: stop and
   show the user — something edited the block by hand, and guessing eats
   their edit.

3. **Write the block**, exactly this, markers included:

   ```markdown
   <!-- herdr-kata-skill:begin -->
   ## Herdr Kata — the agent harness on this machine

   Scheduled jobs, declared workflows, and execution records that outlive any one
   agent. `herdr-kata --version` checks it is here. **Before writing to any of
   it, load the `herdr-kata` skill — it holds the traps `--help` cannot.**

   - **Workflow** — declared execution steps: `herdr-kata workflow run <id> --input '...'`.
   - **Run** — retained results and parked artifacts: `herdr-kata run list` and
     `herdr-kata workflow status <run>`; resume with `herdr-kata workflow resume <run>`.
   - **Lease** — exclusive execution resources: `herdr-kata lease claim <resource>
     --scope <scope> --as <holder> --run <run> --ttl 20m`. Renew before expiry;
     only that exact holder/run may release it. Use Kata for the work ledger.
   <!-- herdr-kata-skill:end -->
   ```

4. **Check the skill itself is installed** — the block tells agents to load
   it, so it has to be loadable: `~/.claude/skills/herdr-kata/` should exist. If
   not, `npx skills add salmonumbrella/herdr-kata`, or symlink a checkout's
   `skills/herdr-kata` there.

5. **Show the user the diff**, and where the block landed. If the target file
   is under version control, leave committing to the user's own conventions.

## Scope

This writes to one file the user already owns and touches nothing else — no
store, no scheduler, no jobs. Removing the block is deleting everything
between and including the markers; the skill under `~/.claude/skills/` is
removed the way it was added.
