---
title: Contributing
description: Find your way around the repository, run focused checks and update the documentation.
last_edited: 2026-10-19
---

# Contributing

herdr-kata is one Go binary that supplies the runtime, operator commands and
TUI of a Herdr plugin. Keep changes small, verified and documented. The
repository's [agent guide](https://github.com/salmonumbrella/herdr-kata/blob/main/AGENTS.md)
owns contribution rules, test discipline, commit identity and release policy.

## Repository layout

| Path | Owns |
| --- | --- |
| `.claude/skills` | Repository skills for test design and isolated runtime verification |
| `cmd/herdr-kata` | CLI commands, their output and the Herdr plugin entry points |
| `internal/daemon` | `up`, binary staging, the `serve` supervisor and the locked worker |
| `internal/runner` | Admission, execution, recovery, capacity and ordered Kata writes |
| `internal/sched` | Trigger math, pending occurrences, catch-up and checkpoints |
| `internal/kata` | The pinned Kata CLI contract, the route pin and guard, and the event loop |
| `internal/defs` | Decoding, classifying and snapshotting job and workflow definitions |
| `internal/inbox` | Native recurring children and parent bindings |
| `internal/herdr` | Herdr's socket protocol; `herdrtest` is the fake server |
| `internal/tui` | The TUI, its forms and its golden-frame harness; it reads local snapshots and never polls Kata |
| `internal/jobform` | Pure job form facts shared by the TUI screens |
| `internal/state` | Bounded atomic files, quarantine, process locks and the home directory |
| `internal/config`, `doctor` | Strict configuration and secret references; read-side health checks |
| `internal/agentlimit`, `usage` | Agent quota and login detection; best-effort token usage |
| `internal/ident`, `version` | Run identity, build identity |
| `internal/crashpoint`, `testenv` | Crash-build kill points; test isolation and integration-suite prerequisites |
| `docs` | The Zensical documentation source, its build scripts and `llms.txt` |

## Building

From the repository root:

```sh
mise install
mise exec -- make install-hooks
GOMAXPROCS=2 GOFLAGS=-p=2 mise exec -- make build
```

`mise.toml` pins Go, lint tools, the hook runner and `uv`. The Makefile owns
check commands. `make install-hooks` installs the repository's Git hooks.
`make build` writes `./bin/herdr-kata` with the same command as the build
step in `herdr-plugin.toml`, which stays the install contract. `make clean`
removes the binary, the lint cache and the built docs site.

## Local checks

Start with the package under change, for example:

```sh
GOMAXPROCS=2 GOFLAGS=-p=2 mise exec -- go test ./internal/defs
```

Before pushing, run the broad gate once, serialized on a shared host:

```sh
flock -w 120 /tmp/local-gate.lock env GOMAXPROCS=2 GOFLAGS=-p=2 mise exec -- make check
```

`make lint` builds the custom golangci-lint pinned in `.custom-gcl.yml`,
including kit's test-timing and error-text analyzers. It checks new findings
against `origin/main` (or the preceding commit in push CI) while the existing
test-timing backlog is cleared. Set `LINT_NEW_FLAGS=` for an uncapped audit of
the whole tree. `make fmt` uses the same custom build. The JSON `omitzero`
fixer stays disabled to preserve the persisted state format.

`make check` runs lint and the race-enabled, shuffled Go suite. All Makefile
test targets give each package 30 minutes, allowing durable-state and crash
tests to finish on loaded hosts without Go's default 10-minute cutoff.
This is a per-package limit, not a total suite budget. If another gate holds
the lock for two minutes, use PR CI rather than queueing longer. Do not rerun
an unchanged broad gate.

| Target | Coverage | Required environment |
| --- | --- | --- |
| `make test-crash` | Fault injection (`crashpoint`) | None |
| `make test-kata` | Real Kata contract and workflows | `HERDR_KATA_TEST_KATA_BIN` |
| `make test-kata-children` | Native recurring children | `HERDR_KATA_TEST_KATA_BIN` |
| `make test-kata-replies` | Typed reply detection, writes and checker resumes | `HERDR_KATA_TEST_KATA_BIN` |
| `make test-herdr` | Real Herdr socket contract | `HERDR_KATA_TEST_HERDR_BIN` (0.9.5) |

Set `HERDR_KATA_TEST_KATA_BIN` to an absolute path to the released Kata
0.19.0 binary. All three Kata suites and the real-write crash check use
this binary.
The Integration workflow downloads the release archive and checks its
pinned SHA-256 instead of building private Kata revisions. Every suite
creates a throwaway `KATA_HOME`; never point integration tests at a hub or
an operator's store.

Set `HERDR_KATA_TEST_HERDR_BIN` to an absolute path to Herdr 0.9.5. An
explicitly selected Kata or Herdr suite fails if its binary is unset, missing
or unusable, including when launched directly with `go test -tags`.

The optional `HERDR_KATA_TEST_LEGACY_KATA_BIN` enables the typed-reply
cross-version detection probes when a pre-reply cron-capable binary is already
available. Those two probes skip when it is unset. Frozen plain findings still
replay against 0.19.0, and unit tests cover capability refusal. No private
legacy build is maintained by CI. Run the suite's real-write crash check with:

```sh
GOMAXPROCS=2 GOFLAGS=-p=2 go test -race -tags kata_replies,crashpoint ./internal/runner -run '^TestIntegTypedChainCrash$'
```

Run only the suites relevant to the change during iteration;
cap Go concurrency and serialize broad runs as above.

Every test package starts through `testenv.Main` in its `TestMain`. It gives
the test binary a temporary `HOME` and drops inherited `HERDR_*`, `KATA_*`,
`XDG_*` and agent-home variables, keeping `HERDR_KATA_TEST_*`, so tests
resolve the defaults a clean CI host would and never the plugin config,
state, Kata home or Herdr socket of the machine running them. A test sets the
directories it needs with `t.Setenv`. A test package without that `TestMain`
fails the `testenv` suite, and `testenv.Main` refuses to run when a directory
variable still reaches the account's Herdr or Kata directories.

CI is split to keep Actions minutes low. Every pull request and push to `main`
runs `make check` and `make test-crash` on Linux; documentation changes run
the docs check instead. The real Kata and Herdr suites and the macOS check run
only on release tags and on demand (the Integration workflow), so run the
integration suites your change touches locally before you push.

Runtime smoke checks must use an isolated Kata home, plugin config and state
directories, and a Herdr session you created. Follow the repository's
[verify skill](https://github.com/salmonumbrella/herdr-kata/blob/main/.claude/skills/verify/SKILL.md).
Never test against an operator's running session or production Kata project.

## Documentation checks

Install the pinned docs toolchain and preview the site:

```sh
mise exec -- make docs-install
mise exec -- make docs-serve
```

Before submitting documentation changes:

```sh
mise exec -- make docs-check
git diff --check
```

`docs-check` runs the docs script tests, validates frontmatter and nav
coverage, checks that `docs/llms.txt` matches the nav, and runs a strict
Zensical build that rejects broken page and anchor links. It then checks
every `docs/<page>.md#<anchor>` that Go code prints against the built site.
After changing the nav or a page description, regenerate the index with
`cd docs && uv run python scripts/llms_txt.py --write`. Read pages in GitHub's
file view as well as the local site.

## Documentation standards

Write for the person running or maintaining the plugin:

- Lead with the outcome, use short sentences, and explain unfamiliar terms.
- Describe shipped behavior. Preserve exact commands, keys, limits and
  failure behavior.
- Give each fact one owning page and link to it elsewhere. Update that page
  instead of appending a history of the latest change.
- Put steps in numbered lists inside a task-named section. Headings carry no
  step numbers, so their anchors stay stable when sections move.
- Use neutral examples such as `example-project`, `worker` and
  `/srv/projects/example`. Keep proposals, private paths and real deployment
  identities out of the docs.
- Use relative `.md` links, plain fenced code and tables that also render on
  GitHub.

### Page types

| Directory | Type | Reader question | Page rule |
| --- | --- | --- | --- |
| `get-started/` | Tutorial and install how-to | "Get me running once." | Linear; every command is copy-pasteable. |
| `guide/` | How-to; `concepts.md` is explanation | "How do I do X with jobs, workflows or the TUI?" | One goal per page, task-named sections, ending with `## Limits`. |
| `operations/` | Operator how-to | "How do I run, diagnose or recover?" | As `guide/`. |
| `reference/` | Reference | "What exactly does this field, key or state mean?" | Tables and rules, no procedures. Owns every exact bound. |
| `design/` | Explanation | "Why does it work this way?" | Rationale only; links reference for the exact rules. |
| `development/` | Contributor how-to and reference | "How do I change this repository?" | Owns the repository layout and these rules. |

### Release notes

Update the owning docs and `docs/changelog.md` in the change that alters
behavior. Put entries under `## Unreleased`, leading with what the reader
can now do or what now works. Keep version requirements and limits. A
release renames `## Unreleased` to `## X.Y.Z` with `<small>YYYY-MM-DD</small>`
on the next line; the owner cuts releases according to the agent guide.

When simplifying an older entry, preserve its release date, behavior at that
version and limits. Do not describe a later feature as part of an older
release.

### Page frontmatter

Every Markdown page in the nav, including `index.md` and `changelog.md`,
starts with YAML frontmatter:

```yaml
---
title: Run the runtime
description: Start, inspect, upgrade and stop a runtime and handle its pinned Kata route.
last_edited: 2026-10-17
---
```

- Set `title` to the page heading and `description` to one sentence about
  what the reader can learn or do. Both must be nonempty strings.
  `docs/llms.txt` lists every page with its nav title and this description.
- Set `last_edited` to the date in `YYYY-MM-DD` format. Update it when the
  page content changes; do not bump dates on unchanged pages.
- Add a new page's nav entry to `docs/zensical.toml` in the same change.
  `make docs-check` refuses a page missing from the nav and a stale
  `docs/llms.txt`.
