# ADR 0006: nftables with a table Drawbridge owns

- **Status:** Accepted (2026-09-26)
- **Plan reference:** docs/PLAN.md §3, D6

## Context

The VPN needs NAT and a few restrictive rules. The host may already have its own firewall rules, and
Debian's default `/etc/nftables.conf` starts with `flush ruleset`.

## Decision

Put every rule in one table, `inet drawbridge`, rendered to a file and applied atomically with `nft
-f`. Add only NAT and restrictive rules, never rules that try to open the host firewall, because an
`accept` in one table can't override a `drop` in another.

## Consequences

- Drawbridge never flushes the ruleset or touches another table.
- The ruleset is human-readable at `/var/lib/drawbridge/nftables.conf`.
- The drift check restores the table after `nftables.service` restarts.
- Diagnostics detect a host firewall or a rootful Docker `FORWARD DROP` that blocks VPN traffic, and
  explain the fix.
- The table's `comment` carries a hash of the ruleset (`drawbridge rev <hash>`). The drift check
  reads it with `nft -j list table inet drawbridge` and re-applies only when the table is missing
  or the hash differs (M1).
- NAT masquerades the VPN subnets with `oifname != "wg0"`, so it covers whichever interface is the
  uplink, and the LAN, without detecting the uplink (M1).
