---
name: testing-without-tautologies
description: Use when creating, editing, fixing, or reviewing herdr-kata tests, fakes, integration suites, TUI tests and golden frames, or when changing tests after a failure.
---

# Testing Without Tautologies

## Core Idea

Tests should fail when protected behavior breaks. A passing test helps only if it can catch a real problem.

Before writing or changing a test, ask: "What production change should make this test fail?" If you cannot answer, redesign the test.

This pairs with the repo's Test First rule: the failing test you write before the implementation must fail *because the behavior is missing*, not because of a typo or an assertion on scaffolding. Watch it fail for the right reason.

## Quality Gate

Before writing the test body, answer these:

- **Who uses this?** Prefer CLI output and exit codes, durable state files, Kata mutations sent through internal/kata, Herdr socket requests, rendered TUI output, or caller-visible results. Avoid private state.
- **What example proves it?** Use concrete inputs and literal expected outputs. Do not compute expected values with production logic.
- **What break would this catch?** Name the wrong branch, missing side effect, wrong argument, boundary case, or contract violation.
- **Do we own it?** Test our choices at framework, filesystem, process, and service boundaries. Do not re-test documented dependency mechanics.
- **Can you state it?** Given this setup, when the user/system does X, then Y observable behavior changes. If Y is not assertable, the test is not ready.

## Required Checks

Apply these checks to every new or modified test:

1. **Assert observable effects**
   - Check returned values, durable state files, Kata mutations, Herdr socket requests, CLI stdout/stderr/exit codes, rendered TUI output, errors, or caller-visible results.
   - A no-assertion test is acceptable only when the failure mode is the subject, such as "this constructor rejects invalid input." Prefer explicit assertions anyway.

2. **Prefer real collaborators over doubles**
   - Use real state on `t.TempDir()` through `state.OSFS`, and point
     `HERDR_PLUGIN_STATE_DIR` and `HERDR_PLUGIN_CONFIG_DIR` at it with
     `t.Setenv`.
   - Use `internal/herdr/herdrtest` for Herdr behavior, and script its
     replies and errors per branch.
   - A behavior that depends on the real Kata CLI contract belongs in the
     `kata` or `kata_children` suites against a real Kata binary with an
     isolated `KATA_HOME`. One that depends on real Herdr behavior belongs in the
     `herdr` suite.

3. **Make fakes specific**
   - When a fake stands in for a boundary, verify arguments, call counts, and which branch was taken when they are part of the contract.
   - Do not let a fake accept any input when the code must pass one value.

4. **Separate branch doubles**
   - Do not reuse one fake handler for success, error, incomplete, unauthorized, or other mutually exclusive paths.
   - Give each branch its own spy/fake so the wrong branch cannot satisfy the expectation.

5. **Do not mock the subject**
   - Fake dependencies, boundaries, and slow or nondeterministic collaborators.
   - Do not replace the handler, store method, command, model update function, or workflow under test.

6. **Investigate failures before changing expectations**
   - Do not flip expected values just to make a failing test pass.
   - First decide whether the production change is intended. Then update the test to describe the new contract.
   - Golden frames are the sharpest version of this: never run `go test ./<package> -update-goldens` to silence a failure without reading the golden diff and confirming every changed line is an intended rendering change.

7. **Avoid mirror assertions**
   - Do not compute expected values with the same logic under test.
   - Use literals, hand-checked fixtures, small examples, or invariant/property assertions.
   - Keep test logic simple enough to review by inspection.

8. **Do not test upstream functionality**
   - Do not re-test bubbletea, lipgloss, BurntSushi/toml, netresearch/go-cron or Go's
     `os/exec`. Test our use of them.
   - Test our boundary contract: argument handoff, error handling, state
     transitions, request shape, and caller-visible results.
   - For surprising upstream behavior, write a narrow characterization test
     around our integration point and name the upstream assumption.
   - For fake external services, test consumer behavior; if the fake itself
     is load-bearing, add a contract check that keeps it honest.

9. **Avoid blindingly obvious current-code assertions**
   - Do not test that the implementation is written the way it is written now.
   - Skip tests for plain constructor assignment, getters, trivial forwarding, constants, and data-only structs.
   - Test them only when they validate, normalize, default, derive, copy, enforce permissions, handle errors, cause side effects, or protect compatibility.
   - Prefer the first consumer-visible result that depends on the fields.

10. **No content-assertion shell tests**
    - Never assert shell script, workflow, or config behavior by grepping the file for implementation text (repo rule). Exercise the command or use the tool's own validation; if that's impractical, document the manual check instead.

11. **NEVER write negative-existence refactoring tests**
    - When cleaning up code, do not add tests asserting that a function, method, file, flag, route, or symbol *no longer exists* (grepping the source tree, reflecting on types, asserting a file is absent, asserting a package doesn't compile a name).
    - These are tautologies about the current shape of the codebase: they pin an implementation detail, break legitimate future reintroduction, and catch no behavior. The compiler already fails on references to removed Go symbols; deleted behavior is protected by the *positive* tests of what replaced it.
    - If a removal has a user-visible contract (a CLI flag now rejected, an endpoint now 404/410, a config key now erroring), test that observable behavior instead — the response the user gets, not the absence of code.

## Test Level

Use the narrowest test that can catch the break:

- Package unit tests: parsing, validation, scheduling math, state transitions.
- `-tags crashpoint` (`make test-crash`): process-kill boundaries and recovery
  after a crash at a named point.
- herdrtest fake: agent lifecycle, settlement, retries, socket errors.
- `-tags kata` / `kata_children`: claim, observe, report and inbox contracts
  against real Kata.
- `-tags herdr`: socket protocol compatibility with a pinned real Herdr.
- TUI: drive the model through `Update` and assert `View` output and the
  requests it emits.
- TUI golden frames (`testdata/golden/`, `-update-goldens`): the harness
  pins each frame's size and color mode; treat golden diffs as contract
  changes to review, not noise to regenerate.

Tests run with `-race -shuffle=on`; no test may depend on order.

## Repo hygiene that affects tests

- Time: use the injected clock (`clock.Now()`), not `time.Sleep`. Wait on a
  condition with a deadline when a real process is involved. New real-process
  deadlines must allow at least one second. The custom kit linter enforces
  sleeps, subsecond deadlines and redundant error prefixes on new findings;
  existing timing findings have a separate backlog.
- macOS is supported: keep Unix socket paths short (create them under a short
  `os.MkdirTemp("/tmp", "hk")` directory, not a nested `t.TempDir()`), and
  compare paths after `filepath.EvalSymlinks` (`/tmp` is `/private/tmp`).
- Fixtures use neutral names (`example-project`, `worker`, `work`).

## Mutation Check

Before finishing, mentally mutate the production code. At least one relevant test should fail for each realistic mutation.

- Wrong constant or argument.
- Wrong branch handler (success double satisfying the error path).
- Missing state change (success returned but journal not updated — necessary but not sufficient).
- Empty/default return.
- Missing side effect (no event emitted, no file written).
- Broken fake at a boundary our code should notice.
- Renamed or rearranged private fields with behavior preserved.
- Missing validation for zero, empty, nil, unauthorized, or malformed input.

If none fail, the test is probably tautological.

## Red Flags

- Reuses the same setup/assertion object, guaranteeing equality.
- Can fail only through panic, nil deref, or daemon crash.
- Still matters if only bubbletea/toml/cron remains and all our code is deleted.
- Translates a constructor, getter, setter, mapper, or wrapper line by line.
- Exists for coverage without checking side effects, boundaries, or outcomes.
- Hides expected values behind loops, formatters, builders, or helpers.
- Asserts that removed code stays removed (file absent, symbol gone, grep finds nothing) instead of testing the replacement's behavior.
- Asserts only a command's exit code without re-reading the state it claims to change.
- Regenerated goldens or flipped literals in the same commit that changed behavior, with no note on why the new contract is right.
