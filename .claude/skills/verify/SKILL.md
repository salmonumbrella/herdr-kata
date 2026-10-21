---
name: verify
description: Build herdr-kata and drive it against an isolated Herdr session and Kata home to confirm a change at the real CLI, runtime, and TUI surfaces.
---

# Verifying herdr-kata changes

Never verify against a Herdr session someone is working in, a production Kata
hub, or the default plugin config and state directories.

## Isolated setup

    root=$(mktemp -d /tmp/hkv.XXXXXX)        # short: Unix socket paths are limited
    mkdir -p "$root/ws" "$root/config" "$root/state"
    GOMAXPROCS=2 GOFLAGS=-p=2 make build && cp bin/herdr-kata "$root/"
    session=hkv$$
    export HERDR_SOCKET_PATH="$HOME/.config/herdr/sessions/$session/herdr.sock"
    export HERDR_PLUGIN_CONFIG_DIR="$root/config" HERDR_PLUGIN_STATE_DIR="$root/state"
    herdr --session "$session" server >"$root/herdr.log" 2>&1 &
    k() { env -u KATA_SERVER -u KATA_AUTH_TOKEN KATA_HOME="$root/kata" "$KATA_BIN" --workspace "$root/ws" --project example-project --as worker "$@"; }

Set `HERDR_SOCKET_PATH` explicitly: inside Herdr it already points at your
own session. Set `KATA_BIN` to the absolute path of the released Kata 0.19.0
binary. Initialize the Kata project in `$root/ws`; see `kata init --help`. Write
`$HERDR_PLUGIN_CONFIG_DIR/config.toml` with `session = "$session"`, a unique
`executor`, `kata.bin = "$KATA_BIN"`, `kata.project = "example-project"`,
`kata.actor = "worker"`, `kata.env = { KATA_HOME = "$root/kata" }`, and a
`[profiles.default]` for an installed agent.

## Runtime surface

    "$root/herdr-kata" up
    "$root/herdr-kata" doctor --json      # every check passes
    k cron workflow create --file wf.json --json
    k cron job create --file job.json --json
    "$root/herdr-kata" run <job-uid> --wait
    "$root/herdr-kata" status --json
    "$root/herdr-kata" logs <run-uid>

A command workflow (`date -u >> "$HERDR_KATA_RUN_DIR/out.txt"`) proves
scheduling and reporting without an agent. Check the run's Kata issue and
`k cron run list --json`, not only the exit code.

## TUI surface

    tmux_socket="$root/tmux.sock"
    tmux -S "$tmux_socket" new-session -d -s v -x 140 -y 40
    tmux -S "$tmux_socket" send-keys -t v "env HERDR_SOCKET_PATH=$HERDR_SOCKET_PATH HERDR_PLUGIN_CONFIG_DIR=$HERDR_PLUGIN_CONFIG_DIR HERDR_PLUGIN_STATE_DIR=$HERDR_PLUGIN_STATE_DIR $root/herdr-kata tui" Enter
    tmux -S "$tmux_socket" capture-pane -t v -p      # plain frame
    tmux -S "$tmux_socket" capture-pane -t v -e -p   # with ANSI, for color checks

Drive `1` for Kata, `2`–`4` or `tab`/`shift+tab` for the local tabs, `j`/`k`,
`l`/`esc`, `/` filter, `R` (manual run), `q`. Resize with
`tmux -S "$tmux_socket" resize-window -t v -x 80 -y 24`.

## Teardown

    "$root/herdr-kata" stop &&
      k daemon stop &&
      herdr --session "$session" server stop &&
      tmux -S "$tmux_socket" kill-server &&
      rm -rf "$root"

If shutdown fails, keep the isolated directory and resolve the failure before
removing it.
