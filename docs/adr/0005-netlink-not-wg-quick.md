# ADR 0005: Drive WireGuard over netlink, not wg-quick

- **Status:** Accepted (2026-09-26)
- **Plan reference:** docs/PLAN.md §3, D5

## Context

`wg-quick` applies a config file by tearing the interface down and bringing it up again, which drops
every client's session, and it runs `PostUp`/`PostDown` hooks as root.

## Decision

Configure WireGuard directly over netlink with `wgctrl` (keys, port, peers) and
`vishvananda/netlink` (addresses, MTU, routes). Peers are diffed and updated in place with
`ReplaceAllowedIPs`.

## Consequences

- Adding, pausing, or removing a client, or changing the port, key, or MTU, applies live without
  disconnecting anyone else.
- There are no hook commands at all (ADR 0007).
- `wg show` still works for debugging, and `drawbridge export --format wg-quick` produces an
  equivalent config file for transparency.
