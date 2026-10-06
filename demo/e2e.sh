#!/usr/bin/env bash
# Install herdr-kata the way the README says to, then use it.
#
# Every check is a thing the README or the docs promise. A promise that cannot
# be checked from outside the repository is not in here.
set -uo pipefail

REPO=${REPO:-salmonumbrella/herdr-kata}
PLUGIN_ID=salmonumbrella.herdr-kata
pass=0
fail=0

ok()   { printf '  \033[32mok\033[0m   %s\n' "$1"; pass=$((pass + 1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '       %s\n' "$2"; fail=$((fail + 1)); }
step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

# plugin_root finds the directory herdr keeps the plugin in.
#
# `herdr plugin list` prints `[github:owner/repo@sha]` for an installed plugin
# and `[local:/path]` for a linked one, so only the linked form carries a path.
# The managed checkout lives under the plugin data directory, named for the
# plugin id and a hash of its source.
plugin_root() {
    local linked
    linked=$(herdr plugin list 2>/dev/null | sed -n 's/.*\[local:\([^]]*\)\].*/\1/p' | head -1)
    if [ -n "$linked" ]; then
        printf '%s\n' "$linked"
        return
    fi
    ls -d "$HOME/.config/herdr/plugins/github/${PLUGIN_ID}-"* 2>/dev/null | head -1
}

# check <name> <expected-substring> <command...>
check() {
    local name=$1 want=$2; shift 2
    local out status
    out=$("$@" 2>&1); status=$?
    if [ -n "$want" ] && ! grep -qF -- "$want" <<<"$out"; then
        bad "$name" "wanted \"$want\", got: $(head -3 <<<"$out" | tr '\n' ' ')"
        return 1
    fi
    if [ -z "$want" ] && [ $status -ne 0 ]; then
        bad "$name" "exit $status: $(head -3 <<<"$out" | tr '\n' ' ')"
        return 1
    fi
    ok "$name"
}

step "install from GitHub, as a stranger would"
if [ -n "${GH_TOKEN:-}" ]; then
    # Private repository: stand in for the anonymous clone a public one gets.
    git config --global url."https://${GH_TOKEN}@github.com/".insteadOf "https://github.com/"
    echo "  (using GH_TOKEN — the repository is not public yet)"
fi

if ! out=$(herdr plugin install "$REPO" --yes 2>&1); then
    bad "herdr plugin install $REPO" "$(tail -3 <<<"$out")"
    echo; echo "install failed, nothing else can run"; exit 1
fi
ok "herdr plugin install $REPO"

check "plugin is registered and enabled" "$PLUGIN_ID" herdr plugin list

ROOT=$(plugin_root)
[ -d "$ROOT" ] && ok "plugin root exists: $ROOT" || bad "plugin root" "not found in plugin list output"
BIN="$ROOT/bin/herdr-kata"
[ -x "$BIN" ] && ok "build command produced $BIN" || bad "the manifest build did not produce a binary"
export PATH="$ROOT/bin:$PATH"

# The manifest's own contents, since asking herdr what it registered needs a
# running server and this container has none.
MANIFEST="$ROOT/herdr-plugin.toml"
grep -q 'id = "board"' "$MANIFEST" 2>/dev/null && ok "manifest declares the board pane" || bad "board pane missing from the manifest"
grep -q 'id = "run-now"' "$MANIFEST" 2>/dev/null && ok "manifest declares its actions" || bad "actions missing from the manifest"
# Every subcommand the manifest invokes has to exist. Two of them did not, once,
# and each failed only on a user's machine. `unknown command` is what main prints
# for a name it does not dispatch — any other outcome means the command is real,
# whatever it then goes on to complain about.
for cmd in $(grep -o '"\./bin/herdr-kata", "[a-z-]*"' "$MANIFEST" 2>/dev/null | sed 's/.*, "//;s/"//' | sort -u); do
    if "$BIN" "$cmd" --help 2>&1 | grep -q "unknown command"; then
        bad "manifest invokes '$cmd', which the binary does not have"
    else
        ok "manifest command '$cmd' exists in the binary"
    fi
done

step "the binary the manifest built"
check "herdr-kata --version reports a build"  "revision" herdr-kata --version
check "usage lists the documented commands" "herdr-kata stop" herdr-kata --help

step "go install, the README's other way in"
# The only path here that asks the module proxy anything. Everything else builds
# from a checkout, which is why `module github.com/salmonumbrella/herdr-kata` survived five
# v2 releases: `go install …@latest` could not see any of the v2 tags, quietly
# resolved the newest v1 one, and installed a herdr-kata from before workflows and
# resource leases existed — reporting itself as v1.1.1 and complaining about nothing.
MODULE=$(sed -n 's/^module //p' "$ROOT/go.mod" | head -1)
MAJOR=$(sed -n 's/^version = "\([0-9]*\)\..*/\1/p' "$MANIFEST" | head -1)
if [ "${MAJOR:-0}" -ge 2 ] 2>/dev/null; then
    case "$MODULE" in
        */v"$MAJOR") ok "the module path carries the released major ($MODULE)" ;;
        *) bad "go.mod says $MODULE, but this is a v$MAJOR release" \
               "go install would resolve the newest v1 tag and say nothing" ;;
    esac
fi
out=$(GOBIN=/tmp/gobin go install "$MODULE/cmd/herdr-kata@latest" 2>&1); status=$?
if [ $status -ne 0 ] && grep -q "no matching versions" <<<"$out"; then
    # The first release at a new major has no published tag under the new path
    # until it is tagged. Not a failure, but never silent either.
    echo "  --   $MODULE has no published tag yet, so go install @latest has nothing to fetch"
elif [ $status -ne 0 ]; then
    bad "go install $MODULE/cmd/herdr-kata@latest" "$(tail -3 <<<"$out")"
else
    ok "go install $MODULE/cmd/herdr-kata@latest"
    got=$(/tmp/gobin/herdr-kata --version 2>&1 | head -1)
    case "$got" in
        *"v$MAJOR."*) ok "the installed CLI is a v$MAJOR build ($got)" ;;
        *) bad "go install produced \"$got\", which is not a v$MAJOR build" ;;
    esac
fi

step "the skill ships with it"
SKILL="$ROOT/skills/herdr-kata/SKILL.md"
[ -f "$SKILL" ] && ok "skills/herdr-kata/SKILL.md is present" || bad "the published skill is missing"
head -1 "$SKILL" 2>/dev/null | grep -q -- --- && ok "skill has frontmatter" || bad "skill frontmatter missing"
grep -q "^name: herdr-kata" "$SKILL" 2>/dev/null && ok "skill name matches its directory" || bad "skill name does not match"
[ -L "$ROOT/.claude/skills/herdr-kata" ] && ok ".claude/skills symlink survives the clone" || bad ".claude/skills symlink missing"

step "jobs and workflows really run"
# `workflow new` has to produce something that parses. It is the first workflow anybody
# sees, and a broken template turns "write a workflow" into "debug herdr-kata".
herdr-kata workflow new scratch --about 'the shipped template' >/dev/null 2>&1
check "workflow new writes a template" "scratch" herdr-kata workflow list

WORKFLOWS="${HERDR_KATA_HOME:-$HOME/.herdr-kata}/workflows"
mkdir -p "$WORKFLOWS"
cat > "$WORKFLOWS/greenfield.yml" <<'YAML'
about: prove the chain
input: the thing to act on
steps:
  - id: one
    run: 'echo "one saw [$HERDR_KATA_INPUT]"'
  - id: two
    run: 'echo "two saw [$HERDR_KATA_PREVIOUS]"'
YAML
out=$(herdr-kata workflow run greenfield --input xyzzy 2>&1)
grep -q '"outcome": "done"' <<<"$out" && ok "workflow run completes" || bad "workflow run did not finish" "$out"
# Without a Herdr server, deterministic steps still complete and persist.
check "run is recorded"          "greenfield" herdr-kata run list

# latest_run picks the newest run of one workflow.
#
# By job, never by row position: `run list` prints oldest-first, so taking the
# first data row silently inspected an *earlier* workflow's run and then asserted
# against its steps. That is a check that passes for the wrong reason, which is
# worse than one that fails.
latest_run() { herdr-kata run list 2>/dev/null | awk -v j="$1" '$2==j{id=$1} END{print id}'; }

# The feature itself: the caller's x reaches the first step, and the first
# step's published result reaches the second. A workflow whose steps cannot see
# each other is just two jobs.
run_id=$(latest_run greenfield)
out=$(herdr-kata workflow status "$run_id" 2>&1)
grep -q "one saw \[xyzzy\]" <<<"$out" && ok "the input reaches the first step" || bad "input did not reach step one" "$out"
grep -q "two saw \[one saw \[xyzzy\]\]" <<<"$out" && ok "a step's result reaches the next" || bad "the chain did not carry" "$out"

# A workflow that declares an input must not run with a blank one: every {{input}}
# would become a hole an agent then invents something to fill.
out=$(herdr-kata workflow run greenfield 2>&1); status=$?
[ $status -ne 0 ] && ok "a workflow that needs an input refuses a blank one" || bad "a workflow ran with no input" "$out"

cat > "$WORKFLOWS/breaks.yml" <<'YAML'
steps:
  - id: boom
    run: exit 3
  - id: never
    run: echo should not run
YAML
out=$(herdr-kata workflow run breaks 2>&1); status=$?
grep -q "parked" <<<"$out" && ok "a failing step parks the run" || bad "failing step did not park" "$out"
[ $status -ne 0 ] && ok "a parked workflow exits nonzero" || bad "parked workflow exited 0"
run_id=$(latest_run breaks)
out=$(herdr-kata workflow status "$run_id" 2>&1)
grep -q "never .*pending" <<<"$out" && ok "the step after a failure never starts" || bad "a step ran behind a failed one" "$out"

# A checker that hands the work back. The maker only gets it right on its
# second run, which is the shape the feature exists for: parking here would be
# correct and useless, because the step that can fix it is the one above.
cat > "$WORKFLOWS/heals.yml" <<'YAML'
steps:
  - id: implement
    run: 'if [ -f "$HOME/heal-tried" ]; then touch "$HOME/heal-fixed"; else touch "$HOME/heal-tried"; fi'
  - id: verify
    run: 'test -f "$HOME/heal-fixed"'
    on_fail:
      goto: implement
      max_loops: 2
YAML
out=$(herdr-kata workflow run heals 2>&1); status=$?
grep -q '"outcome": "done"' <<<"$out" && ok "a workflow heals itself and finishes" || bad "the loopback did not heal" "$out"
[ $status -eq 0 ] && ok "a healed workflow exits zero" || bad "a healed workflow exited nonzero"
run_id=$(latest_run heals)
out=$(herdr-kata workflow status "$run_id" 2>&1)
grep -q "attempt 2" <<<"$out" && ok "the retried step says which attempt it is on" \
    || bad "a healed run looks like one that worked first time" "$out"
grep -q "2/2 steps" <<<"$out" && ok "a step run twice is still counted once" || bad "the retry was counted as extra work" "$out"

# The two ways a loop stops on its own. Both are parks: the attempts are on
# record and a human can resume, rather than an unattended run rewriting the
# same code until somebody reads the token bill.
cat > "$WORKFLOWS/stuck.yml" <<'YAML'
steps:
  - id: implement
    run: echo nothing changes
  - id: verify
    run: exit 1
    on_fail:
      goto: implement
      max_loops: 5
YAML
herdr-kata workflow run stuck >/dev/null 2>&1
out=$(herdr-kata workflow status "$(latest_run stuck)" 2>&1)
grep -q "loop_stuck" <<<"$out" && ok "an unchanged verdict parks instead of looping again" \
    || bad "a loop that changed nothing kept going" "$out"

cat > "$WORKFLOWS/exhausts.yml" <<'YAML'
steps:
  - id: implement
    run: echo trying again
  - id: verify
    run: 'date +%s%N; exit 1'
    on_fail:
      goto: implement
      max_loops: 2
YAML
herdr-kata workflow run exhausts >/dev/null 2>&1
out=$(herdr-kata workflow status "$(latest_run exhausts)" 2>&1)
grep -q "loop_exhausted" <<<"$out" && ok "a loop that runs out of attempts parks" \
    || bad "a bounded loop did not stop where it said" "$out"
grep -q "resume with" <<<"$out" && ok "a parked loop says how to resume it" || bad "no resume line on a parked loop" "$out"
# The maker ran three times: the first attempt and the two loops the workflow
# declared. The number is the whole bound, so it is the thing to assert on.
grep -q "attempt 3" <<<"$out" && ok "a loop's attempts are counted on the record" \
    || bad "the run does not say how many attempts it made" "$out"

# The bound has to survive `workflow resume`, or it is a bound per attempt and
# anything that resumes on a schedule loops forever, a day at a time. The run's
# own note counts the retries this attempt took: a resume on a spent budget
# takes none, and says so by not mentioning any.
exhausted_run=$(latest_run exhausts)
out=$(herdr-kata workflow resume "$exhausted_run" 2>&1)
grep -q "loop_exhausted" <<<"$out" && ! grep -q "retries" <<<"$out" \
    && ok "a resume does not hand back loops already spent" \
    || bad "the resume refilled an exhausted loop" "$out"
# ...and the human who fixed the underlying problem can ask for it back.
out=$(herdr-kata workflow resume "$exhausted_run" --reset-loops 2>&1)
grep -q "2 retries" <<<"$out" && ok "--reset-loops hands the budget back" \
    || bad "--reset-loops did not restore the loop budget" "$out"
[ -f "$HERDR_KATA_HOME/runs/$exhausted_run/loops.json" ] \
    && ok "what the loops spent is on disk with the run" || bad "no loop ledger in the run directory"

# A parked run retains its ending on the durable run record.
out=$(herdr-kata run list 2>&1 | grep exhausts)
grep -q "loop_exhausted" <<<"$out" && ok "a parked loop's ending is on the run itself" \
    || bad "the run list does not say how the loop ended" "$out"

# An edge pointing forward is a branch, and a workflow is a series. Refused when the
# file is read, so the workflow that would loop into itself never starts.
cat > "$WORKFLOWS/branchy.yml" <<'YAML'
steps:
  - id: verify
    run: 'exit 1'
    on_fail:
      goto: ship
  - id: ship
    run: echo shipped
YAML
out=$(herdr-kata workflow run branchy 2>&1); status=$?
[ $status -ne 0 ] && ok "an on_fail pointing forward is refused" || bad "a forward edge ran" "$out"

# A job starts a workflow on a schedule; the job supplies the x.
herdr-kata job add --id breaks-job --name "Breaks" --workflow breaks --input none >/dev/null 2>&1
check "a job can start a workflow"   "breaks"    herdr-kata job list
out=$(herdr-kata job run breaks-job 2>&1); status=$?
[ $status -ne 0 ] && ok "job run exits nonzero on failure" || bad "job run exited 0 on a failed run"

# Removing a workflow a job depends on fails silently at 04:00 otherwise.
out=$(herdr-kata workflow rm breaks 2>&1); status=$?
[ $status -ne 0 ] && ok "a workflow in use cannot be removed" || bad "removed a workflow a job needs" "$out"

step "scoped resource leases"
check "claim is taken" "claim" herdr-kata lease claim browser --scope local:example --as worker --run run-a --ttl 5m --why e2e
check "list shows resource" "browser" herdr-kata lease list --scope local:example
out=$(herdr-kata lease claim browser --scope local:example --as successor --run run-b --ttl 5m 2>&1); status=$?
[ $status -ne 0 ] && ok "a contending lease is refused" || bad "contending claim succeeded" "$out"
check "renew" "renew" herdr-kata lease renew browser --scope local:example --as worker --run run-a --ttl 10m
check "release" "release" herdr-kata lease release browser --scope local:example --as worker --run run-a

step "the scheduler, and its off switch"
check "start brings the pair up" "running"   herdr-kata start
sleep 1
pgrep -f "herdr-kata daemon" >/dev/null && ok "daemon is alive" || bad "no daemon after start"
pgrep -f "herdr-kata sentinel" >/dev/null && ok "sentinel is alive" || bad "no sentinel after start"
check "stop reports stopping"    "stop"      herdr-kata stop
sleep 1
pgrep -f "herdr-kata daemon" >/dev/null && bad "daemon survived stop" || ok "daemon stopped"
herdr-kata ensure >/dev/null 2>&1
sleep 1
pgrep -f "herdr-kata daemon" >/dev/null && bad "ensure revived a stopped scheduler" || ok "stop survives the plugin startup hook"
check "start again"              "running"   herdr-kata start
herdr-kata stop >/dev/null 2>&1

step "the board, with no terminal to draw in"
out=$(herdr-kata board 2>&1); status=$?
grep -qi "no TTY" <<<"$out" && ok "board explains it needs a terminal" || bad "board error is unhelpful" "$out"
[ $status -ne 0 ] && ok "board exits nonzero with nowhere to draw" || bad "board exited 0 having drawn nothing"

step "state lives where the README says"
[ -f "$HERDR_KATA_HOME/herdr-kata.db" ] && ok "store is in \$HERDR_KATA_HOME" || bad "no database in $HERDR_KATA_HOME"
[ -f "$HERDR_KATA_HOME/herdr-kata.db-wal" ] && ok "WAL file exists (a backup must include it)" || echo "  --   no -wal right now, which is fine when nothing is open"

step "uninstall leaves the store alone"
check "herdr plugin uninstall" "" herdr plugin uninstall "$REPO"
herdr plugin list 2>&1 | grep -q "$PLUGIN_ID" && bad "plugin still registered after uninstall" || ok "plugin is gone"
[ -f "$HERDR_KATA_HOME/herdr-kata.db" ] && ok "the store survived the uninstall" || bad "uninstall deleted the store"

printf '\n\033[1m%d passed, %d failed\033[0m\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
