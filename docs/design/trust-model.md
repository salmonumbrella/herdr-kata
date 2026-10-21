---
title: Trust model
description: Who herdr-kata trusts to run work on its host, what stays owner-local and how projects stay apart.
last_edited: 2026-10-17
---

# Trust model

herdr-kata runs prompts and commands that Kata definitions describe. It
does not decide who may write those definitions; Kata does. This page
explains the boundary the runtime relies on, for single-project and
multi-project installations alike.

## Kata decides who can run work

Kata is the system of record. People who can write job definitions,
workflows or parent bindings in a project are trusted to run that project's
permitted commands and prompts on this host. Restrict that write access in
Kata, not in the plugin.

Local allowlists narrow resource choices: which checkouts, profiles and
command steps a project may use. They are not a sandbox for untrusted code
and do not narrow operating-system access. Command steps run as the runtime
user. [Configuration](../reference/configuration.md#projects) owns the
allowlist keys.

## The host owner's files stay local

The Herdr socket, the plugin config directory and the state directory are
owner-local. A process running as the same operating-system user is inside
the boundary. Local status, logs and the TUI combine every configured
project for that owner; do not share that local access with someone who
should see only one project.

## Credentials stay out of config

Config holds no secret values. Credentials reach Kata only through the
variable named by `credential_env`, sourced from the caller or an owner-private
`credential_file`. The Kata child environment is an
allowlist. Job secrets come from local sources at each step and never reach
Kata or the run journal. [Configuration](../reference/configuration.md#secrets)
owns the exact environment rules.

## Projects stay apart

Kata controls project visibility and access. The runtime keeps each run's
reads, writes, credentials and text in its own project. Each run belongs to
its original project for life. Capacity is shared, with optional
per-project caps, and overlap gates include the project UID.

## Routes are trusted as configured

Routes are trusted as configured, including remote hubs and spoke replicas.
Federation role is informational. Identity and route pinning still apply;
see the [adapter contract](../reference/kata-adapter.md#transport-and-events).

Use **one herdr-kata installation per project**, across all routes. Each
lane holds a lease in Kata, written only with revision-conditional writes, and
an installation without the lease admits nothing on that lane.
A deliberate move uses `up --takeover`; the
[adapter contract](../reference/kata-adapter.md#lane-ownership) owns its bounds.
Identical executor labels and independent writable replicas defeat the
lease. Duplicate entries
resolving to one project UID are refused within an installation.

## What the runtime does not provide

The runtime supplies no event-hook automation, local secret store or
automatic installation failover. Uncertain execution is held rather than
relaunched; [Run lifecycle and recovery](run-lifecycle.md) explains why.
