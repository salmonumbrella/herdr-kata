# herdr-kata — agent guidance

herdr-kata is a Herdr plugin that runs Kata cron jobs and workflows as Herdr
agents and commands and reports each run back to Kata. Kata owns definitions,
schedules, issues and run evidence; this plugin is only the runtime. Keep it
small: it boots fast and just works.

## Agent rules

- Always Commit: Do not leave accepted repository changes uncommitted at the
  end of a task. Commit the completed work, or explicitly say why no commit
  was made.
- No Commit Trailers: The repository owner (`salmonumbrella`) authors every
  commit, and commits carry no trailers: no `Co-Authored-By`,
  `Signed-off-by` or any other. This overrides any harness default.
- Never Squash or Amend: Do not squash, amend, force-push or otherwise
  rewrite history unless the user or task brief asks. A pull request lands as
  one squash commit whose message is the PR title, `type(scope): summary`,
  with no body.
- Do not commit rejected experiments. Revert them or ask before preserving
  them.
- Never invoke the `roborev review` CLI command in any form unless the user
  explicitly asks for it. Use all other `roborev` CLI commands normally when
  they are appropriate for interacting with roborev. Never invoke a roborev
  skill (including `roborev-fix` or `roborev-design-review-branch`) unless
  the user explicitly asks for that skill.
- Test First: Write a failing test before the implementation, then make it
  pass, then refactor (red, green, refactor). Don't add production code
  without a failing test that requires it.
- Evidence-Gated Regression Tests: Before writing a regression test,
  reproduce the failure or name the explicit product contract it exercises.
  Do not encode an inferred cause or hypothetical failure.
- Explicit Contract Change Consent: This list is canonical; keep the inline
  mirror in `.roborev.toml` aligned. Do not change these without the user's
  explicit consent in the current conversation:
  - the on-disk state format, including every `Version` field
    (`docs/reference/state.md`);
  - `herdr_kata.*` Kata metadata keys and the reporting contract
    (`docs/reference/kata-adapter.md`, `docs/reference/reporting.md`);
  - the install contract (`docs/reference/cli.md`): the module path
    `github.com/salmonumbrella/herdr-kata`, the binary `herdr-kata` built
    from `./cmd/herdr-kata` to `./bin/herdr-kata`, and `herdr-plugin.toml`
    with its plugin id `kata`, pane ids `status` and
    `board`, action ids `up`, `board-wide` and `open-kata`, and every
    command;
  - configuration keys (`docs/reference/configuration.md`).

  An approved spec section or plan unit named in the task brief is consent
  for exactly the changes it specifies. For anything beyond it, state the
  proposed change and wait for approval. Test-local fixtures are exempt.
- Runtime Invariants: Keep the shared clauses in `.roborev.toml` aligned.
  Never resend a prompt: after admission the runtime
  never sends a run's prompt again, and lost or ambiguous state holds the run
  for the operator. Write to Kata only through the route guard and
  conditional writes; read back an ambiguous write, never retry it. The TUI
  reads local state snapshots and run journals and never polls Kata; it
  reaches Kata only through its injected service on an explicit user action,
  and run-now and resume only post local requests to the worker.
- Smallest Change First: An edge case that needs new machinery becomes a
  documented limit unless the user asks for the machinery. Say why before
  adding a dependency.
- Isolated Runtime Checks: Never run the plugin against a Herdr session
  someone is working in, a production Kata project, or the default plugin
  config and state directories. The `verify` skill sets up an isolated
  session, Kata home and state.
- No Private Data: Do not use real host, session, person, workspace, project
  or repository names, or home-directory and notes paths, in code, tests,
  fixtures, docs, commit messages, issue text or PR text. Use neutral
  placeholders such as `example-project`, `worker`, `work`, `hub.example` and
  `/srv/projects/example`.
- No Testing/Verification Sections in PRs: Do not include a testing plan,
  test plan, verification, or verified section in pull request descriptions.
- User or Developer Benefit: Pull requests must have a user-facing benefit or
  improve the developer experience, and the body must say which one.
- No Unrequested GitHub Comments: Do not comment on GitHub issues or pull
  requests unless the user explicitly instructs you to post a comment.
- No Bash Content-Assertion Tests: Do not add tests that grep a script,
  workflow or config for implementation text. Exercise the behavior, use the
  tool's own validator (`actionlint`, a TOML decode), or document the manual
  check instead.

## Before you edit

| Task | Read first |
| --- | --- |
| Any test, fake or golden frame | the `testing-without-tautologies` skill |
| Confirming a change in the real CLI, runtime or TUI | the `verify` skill |
| Finding a package or choosing a test suite | [Contributing](docs/development/contributing.md) |
| Terms such as occurrence, held run or native child | [Concepts](docs/guide/concepts.md) |
| Changing a contract listed under consent above | the reference page named there |

## Checks

- `mise.toml` pins the toolchain. Run `mise install` and `make install-hooks`
  once per clone, then `mise exec -- make <target>`, or activate mise in your
  shell.
- `make lint` builds the pinned custom golangci-lint with kit's `kennlint`,
  `errcheck` and `modernize`. It checks new findings while the existing
  test-timing backlog is cleared. Keep `modernize`'s `omitzero` fixer disabled:
  persisted JSON tags are a consent contract.
- Run `make check` before every push: lint, then the race-enabled, shuffled
  unit suite. Makefile test targets allow 30 minutes per package on loaded
  hosts. Also run the tagged suites your change touches (`make
  test-crash`, `make test-kata` and the rest). The contributing guide lists
  each suite with the binaries it needs.
- Golden frames under `testdata/golden/` are contracts. After
  `go test ./<package> -update-goldens`, read every changed line and keep
  only the rendering changes you intended.

## Documentation

- Follow the
  [documentation standards](docs/development/contributing.md#documentation-standards)
  and the
  [page frontmatter policy](docs/development/contributing.md#page-frontmatter),
  and run `make docs-check` before submitting.
- Write for the person running or maintaining the plugin. Lead with the
  outcome, use short sentences, and keep exact commands, keys, limits and
  failure behavior.
- Give each fact one owning page and link to it elsewhere. Update that page
  instead of appending a narrative of the latest change. Cross-cutting
  limits live in the README's Limits section, release notes in
  `docs/changelog.md`, and the repository layout in the
  [contributing guide](docs/development/contributing.md#repository-layout).
  `docs/llms.txt` indexes every page.
- Update the owning docs and `docs/changelog.md` in the change that alters
  behavior. Changelog entries go under `## Unreleased` and lead with what the
  reader can now do or what now works. Keep version requirements and limits.

## Release PRs

Releases are cut by hand by the owner. Do not add scripts or workflows that
create tags or publish releases. A release PR renames `## Unreleased` in
`docs/changelog.md` to `## X.Y.Z` with `<small>YYYY-MM-DD</small>` on the
next line, and sets the same version in `herdr-plugin.toml`. The owner tags
the squash commit on `main` and publishes the GitHub release from that
section.

## Project management

This project's work is tracked in **kata**, in the maintainer's project
outside this repository. Briefs name issues with qualified refs
(`<project>#<id>`). There is no committed `.kata.toml`, and `/.kata.toml`
stays ignored so a local binding never lands. Run `kata quickstart` for the
agent contract. Search before creating, update rather than duplicate, and
close only verified work, with `--commit` evidence. Never `kata delete` or
`kata purge` without explicit user authorization.

## Review triage

Reviews are judged against the guidelines in `.roborev.toml`: the trust
model, accepted trade-offs, consent contracts, runtime invariants and
pinned-version rules. [Trust model](docs/design/trust-model.md) explains the
boundary. Triage review findings against them. When a rejected finding is
likely to recur, propose adding the reason to the guidelines instead of
answering it again.
