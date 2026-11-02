# herdr-kata

A headless worker that runs [Kata](https://github.com/kenn-io/kata) jobs as
agents and commands on [herdr-core](https://github.com/herdrdev/herdr-core)
(`hc`), and reports every run back to Kata.

[![A Kata job runs as a Codex agent and closes its issue with evidence](docs/assets/demo.gif)](https://github.com/salmonumbrella/herdr-kata/releases/download/v0.1.0/demo.mp4)

## How it works

There is nothing to look at in herdr-kata itself. You work in Kata:

- Create, edit, enable and run jobs with `kata cron` or in `kata tui`'s Jobs
  tab.
- Watch runs and read their results in `kata tui`'s Runs tab, or on the
  issue each run reports to.

herdr-kata runs in the background as one worker terminal in an hc session.
It reads job definitions from Kata, launches each due run as a fresh agent
or command terminal in the same session, keeps a local journal so a restart
never launches the same work twice, and records the outcome in Kata. If the
Herdr app is connected to that session, job terminals show up there like
any other terminal and you can watch or attach to one while it works.

v2 replaces the old Herdr plugin. It has no plugin manifest, panes or board
of its own; `kata tui` is the interface. Its configuration and state start
fresh and are not compatible with v1.

## Requirements

- Go 1.27 to build: `make build` writes `bin/herdr-kata`.
- hc 0.1.15 or newer.
- Kata 0.20.0 or newer and an authenticated daemon catalog entry.
- Linux or macOS, an executable agent profile and a local checkout.

## Configuration

Save `$XDG_CONFIG_HOME/herdr-kata/config.toml` (default
`~/.config/herdr-kata/config.toml`). All filesystem paths are absolute.
Unknown keys are rejected. `--config` and `--state` select explicit paths.

```toml
executor = "work-runtime"
hc_session = "default"
hc_bin = "/usr/local/bin/hc"

[kata]
bin = "/usr/local/bin/kata"
daemon = "hub"

[kata.env]
KATA_HOME = "/srv/kata-home"

[[projects]]
name = "example-project"
actor = "worker"
commands = false
max_active_runs = 1

[checkouts]
example-project = "/srv/projects/example"

[profiles.default]
argv = ["codex", "--no-daemon"]

[limits]
default_timeout = "2h"
max_timeout = "24h"
max_active_runs = 4
unset_env = []
```

`hc_bin` and `kata.bin` default to PATH lookups, resolved once when loading
the config. `hc_session` defaults to `default`. Kata environment overrides
allow only `KATA_SERVER`, `KATA_HOME` and `XDG_*`; never put tokens in config.
State defaults to `$XDG_STATE_HOME/herdr-kata/`.

## Running work

Start the worker with `herdr-kata up` and check its execution environment with
`herdr-kata doctor`. `herdr-kata run <job-uid> --input <text>` queues a local
request and waits for the worker's decision. Use `herdr-kata logs <run-uid>` to read output and
`herdr-kata release <run-uid>` to free a held run's slot and overlap gate.
`herdr-kata stop` stops the worker; job terminals continue for recovery.

## Starting at login

Run `herdr-kata up` from your login environment. Pass a PATH that can find
your agent binaries. `up` passes the operator environment to the worker.

A systemd user unit can invoke it as a oneshot:

```ini
[Unit]
Description=Kata job worker
[Service]
Type=oneshot
Environment=PATH=/usr/local/bin:/usr/bin:/bin
ExecStart=/usr/local/bin/herdr-kata up
RemainAfterExit=yes
ExecStop=/usr/local/bin/herdr-kata stop
[Install]
WantedBy=default.target
```

A launchd user agent can run the same command at login:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>example.kata-worker</string>
  <key>ProgramArguments</key><array><string>/usr/local/bin/herdr-kata</string><string>up</string></array>
  <key>RunAtLoad</key><true/>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>/usr/local/bin:/usr/bin:/bin</string></dict>
</dict></plist>
```
