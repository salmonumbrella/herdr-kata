# Security

Herdr Kata launches agents with your credentials, on your machine, on a clock.
That is the whole product, and it is also the whole threat model: the risk is
not that a stranger reaches herdr-kata, it is that herdr-kata is the thing holding
the record of everything your agents were told and everything they said back.

## What herdr-kata is

- **No daemon you install, no privileged component.** The scheduler is the same
  binary, running as you. Nothing is setuid, nothing wants root, nothing
  installs a system service.
- **No network listener.** The board and CLI use the local execution store.
- **No telemetry, no phone-home.** Nothing in this repo makes an outbound
  request. `~/.herdr-kata` is the entire footprint (`$HERDR_KATA_HOME`
  overrides), and uninstalling leaves it alone.
- **Everything it writes is owner-only.** Prompts, transcripts, result files
  and the SQLite execution and lease database are created
  `0600`, inside `0700` directories — see `internal/statefs`. On a machine with
  a second login, the `0644` these used to carry handed all of it over for the
  cost of a `cat`. A store created by an older herdr-kata is tightened the next
  time that store is opened, so an existing install is fixed by upgrading
  rather than by a chmod you have to remember. Two tests in
  `internal/store/perm_test.go` hold the claim; a filesystem with no Unix modes
  at all — a mounted share — is opened anyway rather than refused.

## What it is not

Herdr Kata runs the commands you give it. Flow steps and job prompts execute what you wrote, with your
permissions, and there is no sandbox between them and your machine. Treat a
flow file the way you treat a shell script: something whose author you trust.

Agent results and artifacts are observations produced by agents; the harness
stores them but does not validate their factual claims.

## Scans

Two run on every push and pull request, and again weekly on a schedule, because
a vulnerability is published without this repo changing:

- **[govulncheck](https://go.dev/blog/vuln)** against the Go vulnerability
  database, with reachability analysis — a CVE in a package herdr-kata links but
  never calls is not counted as an exposure.
- **[gosec](https://github.com/securego/gosec)** static analysis. The rules it
  runs with, and the justification for each rule left out, are written in
  `.github/workflows/security.yml` next to the command.

Both are green on `main`. `make check` — gofmt, build, vet, and the tests under
`-race` — is the gate that runs beside them.

## Reporting

Open a [private security advisory](https://github.com/salmonumbrella/herdr-kata/security/advisories/new)
rather than a public issue. There is no bounty and no SLA; there is someone who
reads them.
