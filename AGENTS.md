# herdr-kata — agent guidance

herdr-kata executes Kata jobs headless on herdr-core (`hc`). Kata owns job
definitions, issues and run evidence. Keep the adapter small and fast.

## Working rules

- Commit completed changes. The repository owner authors commits, with no
  trailers. Never squash, amend, force-push or rewrite history without consent.
- Test first: name the product contract or reproduce the failure, write the
  failing test, observe it fail, then implement. Read
  `.claude/skills/testing-without-tautologies/SKILL.md` before changing tests.
- Never invoke `roborev review` or a roborev skill unless explicitly requested.
- No personal data, real deployment names or home paths in code, fixtures,
  documentation, commits or PRs. Use `example-project`, `worker` and
  `/srv/projects/example`.
- No shell content-assertion tests. Exercise behavior or use tool validators.
- No unrequested GitHub comments. PR bodies state a user or developer benefit
  and contain no testing or verification section.
- Specs, plans and durable task briefs belong in the external project vault.
- Kata is the work ledger. Search before creating, claim assigned work, keep
  `work.attention` and `work.attention_msg` current, and close only verified
  work with commit evidence. Never `kata delete` or `kata purge` without consent.

## Contracts requiring explicit consent

- On-disk state and every Version field.
- Kata reporting: `cron-worker:<runUID>:issue`, `kata.*` terminal labels,
  `KATA_PROJECT` and `KATA_CRON_*` environment variables, `run_parent`, observation and notification semantics.
- Configuration keys, documented in README and `docs/install.md`.
- Install contract: module `github.com/salmonumbrella/herdr-kata`, binary
  `herdr-kata` built from `./cmd/herdr-kata` to `./bin/herdr-kata`, and CLI
  `up serve stop run release logs report exec-step doctor service version`.

An explicitly approved spec or plan unit authorizes exactly its changes.
The v2 rewrite drops the old Herdr plugin manifest, panes and TUI. Keep
this list and the runtime invariants mirrored in `.roborev.toml`.

## Runtime invariants

- Single writer: `serve` holds a lifetime lock. Prepare a durable journal
  before acknowledging an occurrence or request. Admission must be confirmed
  before launch. Frozen observation bytes and stable keys make retries safe.
- Save nonce and core identity before launch. Recover by all labels and argv,
  never by name alone. Lost or ambiguous execution holds for the operator;
  never resend a prompt to a live terminal.
- Before interrupt, kill or remove, recheck core id, creation time and nonce.
  Leave mismatched occupants alone. Held terminals remain inspectable.
- Every Kata call pins project, daemon and actor. Retry only keyed or
  replay-safe writes; read conflicts back. Save notification intent first;
  never retry an uncertain notification. Existing issues receive no writes.
- hc calls explicitly set HC_SESSION and remove HC_SOCKET. Every launched
  child gets an explicit environment prefix, with credentials out of argv.
- A command succeeds only with its receipt and ordinary `exited` reason.
  A termination signal forbids a receipt even when the child traps it.
- Non-test Go must remain at or below 6,700 lines. The budget covers
  immutable alert identity checks, fleet support, approved cron run sessions
  and persistent-agent work. Stop before exceeding the budget; do not remove
  specified behavior to fit.

Fleet adoption: known capabilities and a fresh successful unpaused job-list
read gate prepared admission. Request TTL ends at durable preparation.
Persist cancellation before cleanup or workflow completion; retain cancelled
terminals and preserve loss/ambiguity holds. Only a structured unsupported
feature conflict may replace frozen v2 bytes with a saved v1 projection before
keyed retry. Runtime beats are keyed, per-project and rate-limited.

## Checks and isolation

Read `.claude/skills/verify/SKILL.md` before real runtime checks. Never
touch an operator's core, running v1 lane, plugin directories or production
Kata project. Never update or restart installed hc cores.

Use the pinned toolchain: `mise install`, `make install-hooks`, then
`mise exec -- <command>`. Iterate with focused package tests under
`GOMAXPROCS=2 GOFLAGS=-p=2`. Run a broad gate at most once before push:

```sh
flock -w 120 /tmp/local-gate.lock env GOMAXPROCS=2 GOFLAGS=-p=2 mise exec -- make check
```

If busy, push and use PR CI. Never rerun an unchanged gate. CI is the final
proof; do not wait for runs that do not exist. Keep `omitzero` disabled in
the modernize fixer: persisted JSON tags are a consent contract.

Update the owning documentation and `docs/changelog.md` when behavior
changes. Lead with the operator's outcome, give each fact one owning page,
use neutral examples, and run `make docs-check`.
