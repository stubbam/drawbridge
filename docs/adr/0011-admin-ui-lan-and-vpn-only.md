# ADR 0011: Admin UI on the home network and VPN only

- **Status:** Accepted (2026-09-26)
- **Plan reference:** docs/PLAN.md §3, D11

## Context

Anyone who controls the admin UI can create a VPN client and reach the whole home network, so the
UI must never be reachable from the internet. The host usually has a public IPv6 address, and not
every router blocks unsolicited inbound IPv6.

## Decision

Enforce the requirement in two independent layers. The app accepts requests only from the detected
LAN prefixes (including the LAN's global IPv6 /64), link-local addresses, and the VPN subnets.
Drawbridge's nftables `input` chain drops the admin port from every other source. The UI is served
directly on TCP 51821, never behind a reverse proxy, and forwarded-for headers are ignored.

## Consequences

- Since M2, the daemon listens on every address (`:51821`) over HTTPS. Before M2 it listened on
  loopback only.
- The LAN is detected, not configured: the on-link subnets of the interfaces that carry the
  default routes. The firewall's sets are rebuilt with every reconcile (every 30 seconds), and
  the app rechecks every 10 seconds, so a new IPv6 prefix from the router is admitted without a
  restart. With no default route, only loopback, link-local, and the VPN are allowed.
- Loopback is allowed, so an SSH tunnel to the host works. That's safe only while no container
  can reach the host's loopback. Rootless Docker's setup script disables host loopback by default
  (`--disable-host-loopback`); docs/MANUAL_CHECKLIST.md §5 checks it on the reference platform.
- `tunnel down` deletes Drawbridge's whole nftables table, so while the tunnel is stopped only the
  app's allowlist protects the UI.
- TLS can't use ACME HTTP-01. It uses a self-signed certificate, an uploaded one, or ACME DNS-01
  (M6).
- A reverse proxy on the same host (including a container publishing ports 80/443) must never
  front the UI, because its requests would come from the host itself and pass both layers.
- **Amended 2026-09-26: extra sources.** Some admins also reach the host over a private overlay
  network such as Tailscale, whose 100.x addresses aren't on the LAN. The `admin_allowed` setting
  adds prefixes to both layers (`drawbridge server set --admin-allow 100.64.10.0/24`). Each must
  lie inside 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, or fc00::/7, so no setting
  can put the UI on the internet, and there are at most 16. This trusts the source address alone:
  a packet that arrived on the WAN interface with a forged source inside such a prefix would pass,
  so it relies on the router dropping private sources from the WAN. Scoping such prefixes to the
  `tailscale0` interface in the `input` chain would close that gap, and can follow if it matters.
