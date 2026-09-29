# ADR 0008: Separate systemd units for the tunnel and the UI

- **Status:** Accepted (2026-09-26)
- **Plan reference:** docs/PLAN.md §3, D8

## Context

The VPN matters more than the admin UI. A UI crash, a failed upgrade, or a bad TLS setting shouldn't
take the VPN down, and the VPN should come up at boot even when the UI can't.

## Decision

Use two units. `drawbridge-tunnel.service` is a oneshot that runs `drawbridge tunnel up` at boot
from the database. `drawbridge.service` runs the web UI, the API, the reconciler, and the monitor.
Upgrades restart only the daemon.

## Consequences

- `systemctl stop drawbridge` stops the UI, and the VPN keeps running.
- Both units share the database and the reconciler code, and a lock serializes them.
- Only `drawbridge tunnel up` creates the interface. The daemon's reconcile leaves a missing
  interface alone, so stopping `drawbridge-tunnel.service` keeps the tunnel down even while the
  daemon runs (M1; the integration tests check it). Changes made while the tunnel is down are
  saved and apply when it starts.
- `systemctl reload drawbridge-tunnel` runs `tunnel up` again, which recreates a deleted
  interface.
