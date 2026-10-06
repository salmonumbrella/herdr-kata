#!/usr/bin/env bash
# Build a demo store and photograph the board from it.
#
# Every row in the screenshots is real: the jobs are added through the CLI, the
# runs are `run` steps that actually execute in this container, and the failing
# one actually fails. Nothing is inserted into the database by hand, because a
# screenshot of fabricated rows is a drawing, not a screenshot.
set -euo pipefail

OUT=${OUT:-/out}
mkdir -p "$OUT"
rm -rf "${HERDR_KATA_HOME:?}" && mkdir -p "$HERDR_KATA_HOME"

say() { printf '\n== %s\n' "$1"; }

say "workflows"
WORKFLOWS="${HERDR_KATA_HOME:-$HOME/.herdr-kata}/workflows"
mkdir -p "$WORKFLOWS"

cat > "$WORKFLOWS/nightly-build.yml" <<'YAML'
about: build and test on a schedule
steps:
  - id: build
    run: go build ./...
  - id: test
    run: go test ./internal/version/ -count=1
YAML

cat > "$WORKFLOWS/docs-sweep.yml" <<'YAML'
about: count the docs
steps:
  - id: count
    run: ls -1 /src/*.md | wc -l
YAML

cat > "$WORKFLOWS/release-check.yml" <<'YAML'
about: the pre-release gate
input: the version being cut, e.g. v2.1.0
steps:
  - id: version
    run: herdr-kata --version
  - id: vet
    run: go vet ./internal/store/
  - id: record
    run: 'echo "checked $HERDR_KATA_INPUT: $HERDR_KATA_PREVIOUS"'
YAML

# A workflow that takes an input and is never run here, because the screenshot has
# to show the INPUT column carrying something. Every other demo workflow is
# `run:`-only and input-less — there are no API credentials in this container,
# so an agent step would park — and a WORKFLOWS tab where that column is all dashes
# hides the one thing that makes a workflow callable.
cat > "$WORKFLOWS/triage.yml" <<'YAML'
about: triage an incoming report, then act on it
input: a report, a PR number, or a stack trace
steps:
  - id: assess
    agent: Look at {{input}} and say in one line whether it is real.
    model: opus
  - id: patch
    agent: "{{previous}} — if that says it is real, write the fix."
  - id: verify
    run: go test ./...
YAML

cat > "$WORKFLOWS/link-audit.yml" <<'YAML'
about: find broken links
steps:
  - id: broken
    run: test -f /src/README.md && false
YAML

say "jobs"
herdr-kata job add --id nightly-build --name "Nightly build" \
    --workflow nightly-build --cron '0 4 * * *' --model sonnet --tags ci,go --favorite
herdr-kata job add --id docs-sweep --name "Docs sweep" \
    --workflow docs-sweep --interval 6h --model sonnet --tags docs
herdr-kata job add --id release-check --name "Release check" \
    --workflow release-check --cron '30 9 * * 1' --model opus --tags release
herdr-kata job add --id link-audit --name "Link audit" \
    --workflow link-audit --cron '0 12 * * *' --model sonnet --tags docs

say "runs — these execute for real"
herdr-kata workflow run nightly-build || true
herdr-kata workflow run docs-sweep    || true
herdr-kata workflow run release-check --input v2.1.0 || true
herdr-kata workflow run link-audit    || true   # fails on purpose: a parked run

say "leases"
herdr-kata lease claim browser --scope local:example --as worker --run run-a --ttl 20m --why 'screenshotting the board'

say "screenshots"
cd "$OUT"
vhs /src/demo/board.tape
herdr-kata lease release browser --scope local:example --as worker --run run-a || true

ls -l "$OUT"
