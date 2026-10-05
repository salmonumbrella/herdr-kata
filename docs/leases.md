# Execution resource leases

Leases coordinate an exclusive execution resource, such as a browser or device.
They live in the plugin's execution store and are independent of Kata issue
reservations and scheduler occurrence claims.

Every write requires `--scope` and `--as`. A scope names the coordinator for a
resource: `local:example` for one local coordinator, or `shared:example` for
callers deliberately sharing the same execution store. Scope names have no
implicit discovery behavior. Different database files do not coordinate one
resource even if they use the same scope name.

```bash
herdr-kata lease claim browser --scope local:example --as worker --run run-a --ttl 20m --why 'inspection'
herdr-kata lease claim browser --scope local:example --as successor --run run-b --ttl 20m --wait 5m
herdr-kata lease renew browser --scope local:example --as worker --run run-a --ttl 20m
herdr-kata lease list --scope local:example --json
herdr-kata lease release browser --scope local:example --as worker --run run-a
```

A holder includes its name, optional `--job`, `--run`, and `--holder-id`.
All four must match exactly for renewal and release. Use a run identity for
execution and a stable holder discriminator for interactive sessions with the
same name. The CLI never guesses a holder or scope from an active Herdr pane.

Acquisition is transactional across independent connections. A resource held
by another identity returns the current lease. Reacquiring your own live lease
extends it, retaining its original start and reason unless a new reason is
supplied. Explicit renewal requires an unexpired hold; it cannot acquire an
unheld resource. Release by a different identity, release after expiry, and a
second release all fail. Refused renewal/release errors show the current live
holder's full identity and expiry, or state that no live holder exists. No live
holder may mean the resource was never acquired, released, or expired.

Expiry is checked on reads rather than by a background reaper. At the exact
expiry boundary a successor may acquire, even after the first process crashes
or the store restarts. `--ttl 0` deliberately holds indefinitely; prefer a
bounded TTL and renew before it expires. Always choose a renewal TTL explicitly,
as in the `renew --ttl 20m` example above. Omitting `--ttl` also means no expiry:
renewing a bounded lease without it converts that hold to an indefinite one;
it does not preserve the old expiry or duration. Use `renew --ttl 0` only when
you deliberately want an indefinite hold. Times use signed nanoseconds; a TTL
whose absolute expiry cannot be persisted without overflow is rejected.

`claim --wait` polls until acquisition, cancellation, or the wait deadline.
`lease list` shows all scopes unless filtered. JSON always contains a list,
including `[]` when no holds remain. The board's Leases tab reads the same
live projection and shows scope, resource, holder, run, expiry and reason.

The fresh fork store has no collaboration tables. Lease events record only
acquisition, renewal, and release; they do not carry discussion or checklists.
