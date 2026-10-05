# Building and testing

## A checkout you can work in

Link the checkout rather than installing it, which registers it where it stands
and leaves you editing the files that actually run:

```bash
git clone https://github.com/salmonumbrella/herdr-kata && cd herdr-kata
make build                       # link does not run build commands — install does
herdr plugin link "$PWD"
```

`herdr plugin unlink salmonumbrella.herdr-kata` undoes that and leaves your files alone.
Installing over a locally linked plugin is refused, so unlink before going back
to the released one.

```bash
make build      # stamps the version from `git describe`
make check      # formatting, build, vet and the full Go test suite
make ci         # formatting, build, vet, race suite and six cross-builds
make version    # show what a build would stamp
```

`make` is a convenience. Herdr already requires Git to install from GitHub;
Herdr Kata additionally needs Go. Herdr fetches a single commit without local tag
refs, so the manifest fetches shallow tag refs before running plain `go build`.
Go embeds an exact release tag automatically. Builds between releases show the
short source revision; modified builds remain marked as modified. The identity
comes from the binary, without a committed `VERSION` or manifest fallback.

For a release, bump the manifest version in a reviewed commit, merge it, and
run the reusable release procedure from a clean checkout at `origin/main`:

```bash
scripts/release.sh --check-only v0.1.0
scripts/release.sh --draft v0.1.0 /path/to/release-notes.md "v0.1.0 — fixes and clearer versions"
scripts/release.sh --publish v0.1.0 /path/to/release-notes.md "v0.1.0 — fixes and clearer versions"
herdr plugin install salmonumbrella/herdr-kata --ref v0.1.0 --yes
```

The script checks formatting, build, vet, the full test suite and the executable
version before tagging the exact validated commit. Tests use a temporary home
under the original home directory, outside system temporary directories,
clear any inherited `HERDR_KATA_HOME`, and retain the existing Go caches;
Git and GitHub CLI keep the original home and credentials. It publishes source-only
releases, refuses to move existing tags, and can resume a draft or a failed
publication safely. Go, Git and GitHub CLI must be on `PATH`.

Without `--ref`, Herdr installs the repository's default HEAD, which can be newer
than the latest release. A `go install …@<pseudo-version>` binary likewise reports
the source revision embedded in that module version.

## Running your checkout as the plugin

```bash
make install-plugin    # build, then unlink and relink this directory
```

`herdr plugin link` registers a directory where it stands and — unlike
`herdr plugin install` — **does not run the manifest's build commands**, so a
linked checkout runs whatever binary is in `./bin` right now. That is why the
make target builds first, and why a linked plugin can silently run last week's
code after a `git pull`.

The board notices anyway: it watches its own binary and re-execs when it
changes, so a board left open picks up a rebuild without being restarted.

## Testing against a store that is not yours

The state directory is the whole of Herdr Kata's state, so point it somewhere else
and nothing can touch the real database:

```bash
export HERDR_KATA_HOME=~/scratchpad/herdr-kata-test
```

`HERDR_KATA_HOME` is the supported state-directory override. Keep it set for
every command in a test run so the store and run directories stay isolated.

A store under `/tmp`, `/var/tmp` or the system temp directory is a **scratch
store**, and Herdr Kata refuses to start the daemon or the sentinel detached
against one. A detached pair outlives whatever started it, and when the temp
directory it serves is removed the pair is unreachable — `herdr-kata stop` writes
its flag into a directory that no longer exists, while each half revives the
other every five seconds. Foreground runs and every other command are
unaffected; a background pair in a temp directory is available on request:

```bash
export HERDR_KATA_ALLOW_SCRATCH_DAEMON=1
```

Prefer a scratch store somewhere that is not temporary, as above.

Copying a store means copying the **whole directory**. SQLite runs in WAL mode
and the daemon holds the database open, so recent rows are in `herdr-kata.db-wal`:
`herdr-kata.db` alone can restore as empty.

## End to end, as a stranger

`demo/e2e.Dockerfile` starts from a bare Ubuntu with Go, git and herdr on it and
nothing else, installs Herdr Kata **from GitHub the way the README says to**, and
then uses it: jobs, a flow that really runs, a failing step that parks, the
resource leases, the scheduler and its off switch, the board's refusal to
draw with no terminal, and an uninstall that leaves the store behind.

```bash
docker build -f demo/e2e.Dockerfile -t herdr-kata-e2e .
docker run --rm herdr-kata-e2e
```

It tests what the demo container cannot: the demo builds from the working tree,
which proves the code works and says nothing about whether anyone else can
install it. Every check here is a promise the README or the docs make. Two of
them were already wrong when the suite was first run — `herdr plugin list` does
not print the plugin directory, and asking herdr what actions it registered
needs a running server — and both were documentation bugs, not test bugs.

## The demo container

`demo/` builds a clean Ubuntu with herdr, Herdr Kata and a demo store in it, and
takes the screenshots in this documentation by driving a real terminal through
[VHS](https://github.com/charmbracelet/vhs):

```bash
docker build --build-arg VERSION=$(git describe --tags --always) \
  -f demo/Dockerfile -t herdr-kata-demo .
docker run --rm --cap-add SYS_ADMIN -v "$PWD/assets:/out" herdr-kata-demo
```

It doubles as a test of the install instructions above: the image starts from
`ubuntu:24.04` with nothing on it, so anything the README forgets to mention
fails the build.

Three details the container needs, each of which fails differently:

- `--cap-add SYS_ADMIN` — VHS screenshots through a headless Chromium, which
  cannot start in a default container. Without it: `Failed to launch the
  browser`, then a stack trace.
- **not root** — Chromium refuses to run as root without `--no-sandbox`, which
  VHS gives no way to pass. The image runs as Ubuntu's stock `ubuntu` user
  (uid 1000), which also means files written to a mounted `/out` belong to you.
- `--build-arg VERSION` — the build context has no `.git`, so an unaided build
  stamps every screenshot `dev`.

The demo store is built at run time rather than baked into the image, because
it carries timestamps: a screenshot that says "3 days ago" for something the
image built in March is worse than no screenshot. Every row in it is real —
the jobs are added through the CLI, the runs are `run` steps that execute in
the container, and the parked one actually failed.

## The skill

`skills/herdr-kata/` is an [Agent Skill](https://agentskills.io) — what an agent
should read before it takes a resource lease or calls a flow.

```bash
npx skills add salmonumbrella/herdr-kata
```

That is the whole installation. A skill is just a folder with a `SKILL.md` in
it, so if you would rather place it yourself, copying or symlinking into any of
these does the same job:

| where it goes | who reads it |
|---|---|
| `~/.claude/skills/herdr-kata/` | Claude Code, in every project |
| `<project>/.claude/skills/herdr-kata/` | Claude Code, that project only — commit it and your team has it too |
| `<project>/.agents/skills/herdr-kata/` | the cross-tool location other agent clients read |

```bash
git clone https://github.com/salmonumbrella/herdr-kata
ln -s "$PWD/herdr-kata/skills/herdr-kata" ~/.claude/skills/herdr-kata
```

**Symlink rather than copy** when you want it to follow the code: a linked skill
picks up the next `git pull`, and a copied one is a snapshot that will quietly
age past the commands it documents — which is worse than no skill, because an
agent trusts it either way.

Inside this repo it loads by itself through `.claude/skills/herdr-kata`, a symlink
to the same directory. If you installed the plugin rather than cloning, Herdr
already has a copy under
`~/.config/herdr/plugins/github/salmonumbrella.herdr-kata-<hash>/skills/herdr-kata`. (`herdr
plugin list` names the source, `github:salmonumbrella/herdr-kata@<commit>`, rather than that
path.)

## Nice to have

- **Logo.** A terminal bitmap (half-block cells, two pixels per row) can render
  a real image, but at any size that reads clearly it costs more vertical rows
  than a split pane can spare. Parked until there is a version that looks good
  small.

---

[← back to the README](../README.md)


## Native adapter checks

`make ci` is the command used by GitHub Actions. Its Linux/macOS/Windows amd64
and arm64 builds prove compilation, not runtime behavior on those platforms.
Ordinary unit tests do not depend on a user daemon or a bundled Kata fork.

For real native integration and the Herdr packaging smoke, build a compatible
Kata checkout separately, then supply absolute executable paths:

```bash
KATA_NATIVE_TEST_BINARY=/path/to/branch-kata \
HERDR_NATIVE_TEST_BINARY=/path/to/herdr make native-smoke
```

Kata must advertise `cron_v1` and support ordinary definitions, run
observations and planning dates. No version label can replace that capability
check. The real fixture creates an isolated Kata home, workspace and SQLite
database, starts its own loopback foreground daemon and retains credentials only
there. The Herdr smoke builds the current plugin binary, passes the actual
manifest to Herdr's TOML parser, links it disabled in a short-lived named test
session, runs a native shell-step flow through the real workspace lifecycle,
unlinks the plugin and stops only its owned session. Local mappings survive
unlink. Disabled linking does not invoke the detached scheduler startup hook.
Children inherit an explicit OS/toolchain allowlist; user daemon targets, proxies,
credentials, hosted ports, live Herdr sockets and caller panes are absent.
Temporary files and both owned server processes are cleaned by test teardown.

This smoke uses a linked local build, not a GitHub release download. It does not
exercise a provider-backed interactive agent or assert that generic Herdr can
safely wake one automatically. Generic inbox delivery remains manual wake.
Run the same explicit smoke on each target OS before claiming platform runtime
coverage. The manifest's minimum Herdr version remains its inherited requirement;
check your actual installed version when assessing available conversation APIs.

The historical `demo/e2e.sh` exercises the upstream standalone storage behavior.
It is not the acceptance check for native definitions or local activation and
must be reconciled before a release claims that older full-container workflow.
