# Requirements and known roadblocks

Drawbridge was developed and tested on one home network. This page lists what it needs, what
it has been tested on, and the places where other setups commonly differ. Read it before you
install: a few of these can leave VPN clients without DNS, or the host without IPv6.

## What Drawbridge needs

- **Linux with systemd, from the Debian family.** Drawbridge ships as a `.deb` for arm64 and
  amd64, and its units, sysusers, and sysctl files are for systemd.
- **Kernel WireGuard and nftables.** WireGuard has been in the mainline kernel since 5.6; the
  package depends on `nftables` and loads the `wireguard` module. Userspace WireGuard
  (`wireguard-go`) isn't supported.
- **Root to install.** The package's install scripts need root. The daemons themselves run as an
  unprivileged `drawbridge` user with only `CAP_NET_ADMIN`.
- **Two free ports:** UDP 51820 for WireGuard (changeable with `drawbridge server set --port`)
  and TCP 51821 for the admin UI.

## Tested on

- A Raspberry Pi 5 running Debian 13 (arm64), with NetworkManager managing the network. Every
  item in [MANUAL_CHECKLIST.md](MANUAL_CHECKLIST.md) marked verified ran there.
- Ubuntu 24.04 (amd64) in CI, where the kernel integration tests run on every change.

Other Debian-family distributions, such as Raspberry Pi OS and Ubuntu Server, should work but
aren't tested.

## What your network needs

- **A way in from the internet.** Either a public IPv4 address with UDP 51820 forwarded to the
  host by your router, or an IPv6 endpoint (below). A connection behind carrier-grade NAT
  (CGNAT) or DS-Lite has no inbound IPv4, so it needs the IPv6 endpoint or a relay server.
- **A stable name for the endpoint.** Clients connect to the address or DNS name set with
  `drawbridge server set --endpoint`. If your public IPv4 address changes, run a dynamic DNS
  client (ddclient, or your router's built-in one) to keep the name's A record current.
  Drawbridge doesn't update DNS itself.
- **For an IPv6 endpoint:** your router must allow inbound UDP 51820 to the host's IPv6 address,
  and the AAAA record must point at the host's *stable* address. A "what's my IP" lookup returns
  a temporary (privacy) address that changes about daily; `ip -6 addr show scope global
  -temporary` lists the stable ones.

## Known roadblocks

### DNS for VPN clients needs a resolver on the host

**The default only works if a DNS resolver listens on the host's VPN addresses.** A new server
points clients' DNS at its own VPN addresses (`10.8.0.1` and its IPv6 counterpart). That suits a
host running AdGuard Home, Pi-hole, Unbound, or dnsmasq on all addresses. On a host without one,
clients connect but can't look up any name. systemd-resolved doesn't count: its stub listens on
`127.0.0.53` only.

*Workaround:* before handing out configs, open Settings → DNS for clients → Other servers, and
enter the resolvers you want (or `drawbridge server set --dns 1.1.1.1,2606:4700:4700::1111`).
Clients pick up a change when they download their config again.

*Planned fix:* a DNS step in first-run setup, and a default that points at the host only when
something answers DNS there.

### IPv6 forwarding can remove the host's own IPv6 address

**Hosts whose kernel configures IPv6 from router advertisements lose their IPv6 address and
default route after install.** The package turns on IPv6 forwarding for every interface, and
with forwarding on, the kernel ignores router advertisements wherever `accept_ra` is `1` (the
usual default). NetworkManager and systemd-networkd handle router advertisements themselves, so
they aren't affected. Hosts that use ifupdown, as a minimal Debian server install does, are.

*Workaround:* before installing, find your uplink interface (`ip -6 route show default`) and set
`accept_ra` to `2` for it, now and at boot:

```bash
echo 'net.ipv6.conf.eth0.accept_ra = 2' | sudo tee /etc/sysctl.d/80-accept-ra.conf
sudo sysctl -p /etc/sysctl.d/80-accept-ra.conf
```

(Use your interface's name in place of `eth0`.)

*Planned fix:* the installer detects this case and sets `accept_ra` itself.

### Host firewalls and Docker can block VPN traffic

Drawbridge manages only its own nftables table, `inet drawbridge`, and a packet has to be
accepted by every table it passes through. So another firewall that drops forwarded traffic
blocks VPN clients' traffic too, and Drawbridge can't override it:

- **ufw** drops forwarded traffic by default (`DEFAULT_FORWARD_POLICY="DROP"` in
  `/etc/default/ufw`).
- **firewalld** doesn't forward between zones unless told to.
- **Rootful Docker** sets the `FORWARD` chain's policy to `DROP` when it starts. (Rootless
  Docker doesn't touch the host firewall.)

*Symptom:* clients connect (the handshake succeeds), but nothing loads through the tunnel.

*Workaround:* allow forwarding from and to the WireGuard interface (`wg0`) in that firewall.

### Don't put the admin UI behind a reverse proxy on the same host

The admin UI answers only the home network and the VPN, and it decides by the address a
connection comes from. A reverse proxy on the same host (nginx, Caddy, Traefik, or a container
publishing ports 80 and 443) forwards every request from the host itself, which is always
allowed, so the proxy would put the UI on the internet. Reach the UI directly at
`https://<host>:51821` instead.

### The admin UI's home network is detected, not configured

The UI allows the subnets on the interfaces that carry the host's default routes, plus loopback,
link-local addresses, and the VPN. A device on another subnet can't reach it: the firewall
drops the connection, or the app answers 403 if the tunnel is down. To allow another
private network, such as a Tailscale tailnet, add it with `drawbridge server set --admin-allow`
(private ranges, `100.64.0.0/10`, and `fc00::/7` only, so no setting can expose the UI to the
internet).

### The host's clock has to be right

TLS certificates and WireGuard's handshakes depend on the time. A host without a battery-backed
clock, such as a Raspberry Pi without its RTC battery, needs network time
(`systemd-timesyncd`) running before it can be trusted.
