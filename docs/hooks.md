# Run-settled hooks

A run can finish in the daemon, on the board, in `job run`, in `flow run`, or
while an old run is being reconciled. None of those callers is a good place to
wait for somebody else's program. They all write the same run row, so Herdr Kata
records a `run.settled` event beside that row in one SQLite transaction. The
daemon delivers it later. If the daemon is down, the event waits for it.

Put an executable at `$HERDR_KATA_HOME/hooks/run-settled` (normally
`~/.herdr-kata/hooks/run-settled`). On Windows, Herdr Kata looks for
`run-settled.exe`, then `.cmd`, then `.bat`; command scripts run through
`cmd.exe`. There is one hook for every job. It can inspect `run.ref` and ignore
events it does not care about.

The hook receives one versioned JSON object on stdin. Its working directory and
`HERDR_KATA_HOME` are the absolute state directory, including when the state
override is relative. Hook paths are resolved from that directory. The same
identifiers are available as environment
variables: `HERDR_KATA_EVENT_ID`, `HERDR_KATA_RUN_ID`, `HERDR_KATA_JOB_ID`,
`HERDR_KATA_RUN_OUTCOME`, `HERDR_KATA_PARK_REASON`, `HERDR_KATA_REF`, and
`HERDR_KATA_RUN_DIR`. The object includes the run, its settlement number, the
previous *settled* outcome, and the parsed root `result.json` when one was
readable. A flow normally has `result: null`: its authoritative results are in
step directories. Herdr Kata never judges an outcome from the transcript.

The optional root result snapshot is read before the SQLite write transaction.
Only regular files containing valid JSON of at most 1 MiB are included. Snapshot
work waits at most one second, or until the caller's context expires; unreadable,
nonregular, oversized, and timed-out results become `null`. At most four readers
can perform filesystem I/O at once, so a stalled filesystem cannot accumulate
unbounded background work. This does not change the run's outcome.

Each event payload is a snapshot. Changing the end time of an already settled
run without changing its outcome or whether it has an end time does not emit a
new event, and redelivery retains the original timestamp.

For a local log:

```sh
state_dir=${HERDR_KATA_HOME:-"$HOME/.herdr-kata"}
mkdir -p "$state_dir/hooks"
cat > "$state_dir/hooks/run-settled" <<'SH'
#!/bin/sh
cat >> "$HERDR_KATA_HOME/hooks/settlements.jsonl"
SH
chmod +x "$state_dir/hooks/run-settled"
```

For a webhook, put this body in the executable instead:

```sh
#!/bin/sh
curl --fail --silent --show-error \
  -H 'Content-Type: application/json' \
  --data-binary @- https://daemon.example/run-events
```

An exit code of zero acknowledges the event. A nonzero exit or a 30-second
timeout retries after 30 seconds, 2 minutes, 10 minutes, then 1 hour; five
failed calls leave the event dead. Any undelivered event, including a dead one,
holds later settlements of the same run until it is redelivered and delivered;
other runs keep moving. Hook output goes to
`$HERDR_KATA_HOME/hooks/run-settled.log`, headed by event ID. Missing or
non-executable hooks are recorded as `skipped` with `no hook`; they do not
quietly retry when a hook is installed later.

```sh
herdr-kata hook status                 # pending, retrying, dead, and skipped
herdr-kata hook redeliver 42           # requeue one event, including a skipped one
herdr-kata hook redeliver --dead       # requeue all dead events
```

Redelivery keeps the event ID and frozen payload. Delivery is at least once
when a hook is installed: if the daemon crashes after the hook succeeds but
before it records that success, it calls the hook again. A receiver can use
`event_id` to deduplicate. The hook reports an outcome to another system; its
exit status never changes the run. `result.json` remains the only authority on
what the run did, and a hook failure never stops scheduling.

---

[← back to the README](../README.md)
