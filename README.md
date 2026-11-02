# herdr-kata v2

Run Kata execute jobs headless on herdr-core. Kata owns definitions,
schedules, issues and run evidence. Use Kata's Jobs and Runs tabs to inspect
work. This adapter launches fresh agents and command steps, keeps a local
recovery journal, and records observations in Kata.

Status: v2 headless worker and operator CLI; 4,160 lines of non-test Go.
The live suite covers hc 0.1.15 and 0.1.16, including a real Codex prompt.

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

## Limits

This is a clean-slate v2; old plugin configuration and state are incompatible.
There is no plugin manifest or TUI. No loops, secrets, resume, remote machines,
session links or boot-unit generator. Notify jobs belong to native Kata.
Unsupported definitions are listed by `doctor` with their reason.
