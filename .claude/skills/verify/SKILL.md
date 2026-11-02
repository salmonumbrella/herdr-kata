---
name: verify
description: Verify v2 with isolated herdr-core and Kata runtimes.
---

# Verifying herdr-kata

Build only inside the feature worktree. Never overwrite a production binary
or touch a running v1 lane, plugin directory, core or Kata project.

1. Create a short temporary root, fresh `HC_SESSION=hk2-<random>`, scratch
   `XDG_CONFIG_HOME`, `XDG_STATE_HOME` and `XDG_RUNTIME_DIR`. Remove inherited
   `HC_SOCKET`. Set the session explicitly on every hc command.
2. Use installed hc 0.1.15 only in that scratch session. Also download
   official hc 0.1.16 into the temp root, verify its archive SHA-256 against
   its release manifest, and run the same checks with it. Never `hc update`,
   upgrade or restart a preexisting core.
3. Start the scratch core, inspect its status and verify its state path and
   core id before any terminal operation. Recheck both before `server stop`.
   Never call `hc server stop` without the scratch session explicitly set.
4. Remove inherited `KATA_*` routing and credential selectors in test
   children. Use a scratch `KATA_HOME`, daemon and `example-project`. Give
   each job explicit scratch checkouts, config and state paths.
5. Run `env -u HC_SOCKET HERDR_KATA_LIVE=1 HERDR_KATA_LIVE_CODEX=1
   GOMAXPROCS=2 GOFLAGS=-p=2 make test-live`. Each test must inspect the
   actual run observation and issue, rather than only a CLI exit code.
6. Exercise the never-reporting scripted agent, gated command workflow,
   worker crash/recovery and one real Codex prompt. Record the real agent's
   settlement row and results for both hc versions.
7. Kill and remove every terminal the check started. Stop its scratch Kata
   daemon and verified scratch core, then remove temporary directories. If
   shutdown fails, resolve it before deleting the directories. Never act on
   terminals the check did not start.

Focused tests run with `GOMAXPROCS=2 GOFLAGS=-p=2`. Only one broad gate is
allowed before push, serialized with a bounded wait on `/tmp/local-gate.lock`.
