# ADR 0012: A resolver on the host, such as AdGuard Home, as the clients' DNS

- **Status:** Accepted (2026-09-26); amended 2026-09-28 and 2026-09-29
- **Plan reference:** docs/PLAN.md §3, D12

## Context

Many self-hosted setups already run a DNS resolver on the same host, such as AdGuard Home,
Pi-hole, Unbound, or dnsmasq, often listening on all addresses. The reference platform runs
AdGuard Home that way (DNS on port 53, its web UI on port 3000). VPN clients should get that
resolver's ad-blocking and logging without Drawbridge running a second one.

## Decision

Point new clients' DNS at the server's VPN addresses, where a resolver on the host can answer.
Don't install or manage a resolver. For AdGuard Home specifically, offer an optional integration
through its REST API (`/control`, HTTP basic auth, `http://127.0.0.1:3000/control` by default and
configurable) that syncs each client as a named persistent client and shows per-client DNS logs.

## Consequences

- A resolver that listens on all addresses needs no changes. Diagnostics still send test queries
  to both VPN addresses.
- AdGuard Home is optional at runtime: VPN management keeps working when it's down, and name sync
  retries.
- Drawbridge must never take ports 53 or 3000.
- **Amended 2026-09-28: hosts without a resolver.** The default only works if something answers
  DNS on the VPN addresses. On a host without a resolver, clients connect but can't look up names
  (systemd-resolved's stub listens on `127.0.0.53` only, so it doesn't help). Until the planned
  fix — a DNS step in the setup wizard, and a default that points at the host only when something
  answers there — docs/REQUIREMENTS.md tells admins to choose other servers in Settings before
  handing out configs.
- **Amended 2026-09-29: public resolvers by default, and a check.** New servers now hand out
  Cloudflare's public resolvers, because they work on every host. The server's VPN addresses
  become the clients' DNS only when the admin chooses *This server*, and the setup wizard and
  Settings first ask those addresses for a name (`GET /api/server/dns-check`, a UDP query for
  `example.com` to port 53, two-second timeout) and preselect the host only when it answers.
  Each address is checked on its own, and only the ones that answer are saved. The decision above
  still holds for hosts that run a resolver; it is no longer the default for those that don't.
  The CLI's `server set --dns` keyword for the server's addresses is now `server` (it was
  `default`), and it runs the same check: it saves only the addresses that answer, and refuses
  when none does unless `--force` is given.
