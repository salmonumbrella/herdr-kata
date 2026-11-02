# Kata CLI fixtures

Captured with Kata 0.20.0 in isolated temporary homes and an
`example-project` scratch project. Filesystem paths are normalized to
`/srv/projects/example`. The scratch daemons were stopped and homes removed.

`exit1.json` is real CLI output against a temporary HTTP server returning
500. The server was stopped after capture. `exit6.json` comes from a stale
conditional deadline update; no delete or purge command was used.
`exit8.json` comes from a timed-out wait on the scratch issue.

The wire structs in the parent package follow the corresponding generated
models in `go.kenn.io/kata` v0.20.0. Local copies avoid its additional
validator dependency, as permitted by the v2 dependency rule.
