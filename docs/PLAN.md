# Drawbridge: Project Plan

A self-hosted WireGuard VPN server with a web management GUI, installed natively (no Docker)
on Debian-family Linux with systemd, for arm64 and amd64. The reference platform, where it's
tested on real hardware, is a Raspberry Pi 5 running Debian 13 (trixie). The feature set is
modeled on [wg-easy](https://github.com/wg-easy/wg-easy), but the code is written from scratch.

The name: a drawbridge controls who crosses into the castle, and raising it (pausing a client)
keeps them out. "WireGuard" is a registered trademark, so it appears only in descriptions, never
in the product name.

> Status: **M0–M3 are built** (the tunnel, the CLI, the authenticated API, and the web UI), and
> so are the session tracker and traffic history from M4. The kernel tests pass in CI, and
> `docs/MANUAL_CHECKLIST.md` records what has run on real hardware. The rest of M4 (the log
> viewer's CSV export, structured journald fields, and AdGuard Home integration) is next.
> `drawbridge doctor`, the first slice of M5, is built too; the diagnostics page isn't.
> `docs/REQUIREMENTS.md` lists what the host and network need, and the known roadblocks.

---

## 1. Goals and non-goals

### Goals

- Run WireGuard on the **in-kernel** implementation on a Debian-family Linux host, managed by
  systemd, with no containers.
- Provide a **web GUI** that covers day-to-day management:
  - **Clients:** add, edit, remove, pause/resume, download the config, show a QR code, and view
    live status, connection history, and traffic.
  - **Server settings:** endpoint FQDN and port, interface addresses and subnets, MTU,
    keepalive, NAT and routing, and defaults for new clients.
  - **DNS settings:** resolvers pushed to clients (global and per client), and later an optional
    local resolver on the host.
  - **Logs:** connection events, traffic history, and an admin audit trail.
- **Dual stack.** Clients reach the server over IPv4 or IPv6 and tunnel both IPv4 and IPv6
  traffic.
- **Safe by default:** least-privilege processes, strong admin authentication, no arbitrary
  root command hooks, and an admin UI that isn't exposed to the internet by default.
- **Robust:** the VPN keeps running if the web UI crashes, is upgraded, or is stopped.
- **Easy to install and upgrade:** one `.deb` package that contains a single binary.

### Non-goals (for v1.0)

- Multi-server or clustered management.
- Replacing the host's general firewall. Drawbridge manages only its own nftables table.
- A userspace WireGuard implementation (`wireguard-go`). The kernel module is used.
- Native mobile apps. Clients use the official WireGuard apps.

---

## 2. Target platform and assumptions

`docs/REQUIREMENTS.md` is the user-facing version of this section, with workarounds for the
setups that need them.

| Item | Requirement |
|---|---|
| Hardware | Any arm64 or amd64 machine. WireGuard throughput isn't a bottleneck at typical home uplink speeds, even on a single-board computer. |
| OS | Debian-family Linux with systemd (the package is a `.deb`). |
| Kernel | The WireGuard module (`CONFIG_WIREGUARD`, mainline since 5.6) and nftables. Both ship with the Debian, Ubuntu, and Raspberry Pi kernels. |
| Userland packages | `nftables` (a dependency) and `wireguard-tools` (recommended, for debugging with `wg show`). |
| Network | A public IPv4 address with UDP 51820 forwarded to the host, or an IPv6 endpoint the router lets through; a DNS name that stays current (a dynamic DNS client if the public IPv4 address changes, since Drawbridge doesn't update DNS); for IPv6, a **stable** host address (not a temporary/privacy one). |
| Network stack | NetworkManager, systemd-networkd, or ifupdown. On ifupdown hosts the installer sets `accept_ra=2` on the uplink (§5.5). |
| Other services | Drawbridge uses only UDP 51820 and TCP 51821, so it coexists with a DNS resolver (port 53), web servers and reverse proxies (80, 443), and admin UIs like AdGuard Home's (3000). Using the host's own resolver as the clients' DNS needs it listening on the VPN addresses; setup checks (§6.3). |
| Uplink name | Not hard-coded (`eth0`, `end0`, `wlan0`, …). It's detected from the default routes. |
| Storage | microSD or SSD. Write volume is kept low by default to limit SD card wear, and configurable for SSDs (§6.4). |

**Reference platform.** Everything in `docs/MANUAL_CHECKLIST.md` marked verified ran on a
Raspberry Pi 5 (arm64) running Debian 13, with NetworkManager managing the network, AdGuard Home
on the host as the clients' resolver, and a consumer router that forwards UDP 51820 over IPv4
and lets it in over IPv6. The kernel integration tests also run on Ubuntu 24.04 (amd64) in CI.

---

## 3. Key decisions (summary)

| # | Decision | Recommendation | Why |
|---|---|---|---|
| D1 | Backend language | **Go** (decided) | A single static binary (arm64 or amd64) with no runtime needed on the host. It has first-class WireGuard and netlink libraries (`wgctrl`, `vishvananda/netlink`), uses little memory (about 20–40 MB), and cross-compiles easily. |
| D2 | Frontend | **Svelte 5 + SvelteKit (static adapter) + TypeScript + Tailwind** (decided) | Small bundles and little boilerplate. The build output is embedded in the Go binary with `go:embed`, so Node.js is needed only at build time. |
| D3 | Datastore | **SQLite** (`modernc.org/sqlite`, pure Go, WAL mode) | No database server and no CGO. One file, easy to back up. |
| D4 | Source of truth | **The database.** The kernel state is *derived* from it by a reconciler. | Changes are idempotent, restarts are safe, and drift is detected and corrected. |
| D5 | WireGuard control | **Netlink directly** (`wgctrl` + `netlink`), not `wg-quick` | Peer, port, key, address, and MTU changes apply live without restarting the interface, so other clients' sessions aren't dropped. It also avoids `wg-quick`'s root-run `PostUp` hooks (see §10). |
| D6 | Firewall and NAT | **nftables**, with a dedicated `table inet drawbridge` rendered to a file and applied with `nft -f` | The update is one atomic transaction and the ruleset is human-readable. It never touches the user's other rulesets. |
| D7 | Privileges | Runs as the unprivileged **`drawbridge` user with only `CAP_NET_ADMIN`**, with no root at runtime | Compromising the web app can't turn into arbitrary root code execution. |
| D8 | VPN vs UI lifecycle | **Two systemd units:** `drawbridge-tunnel.service` (oneshot, brings up the VPN at boot) and `drawbridge.service` (web UI and API daemon) | The VPN comes up at boot and keeps running even if the UI fails to start or is stopped. |
| D9 | Live updates in the UI | **Server-Sent Events (SSE)** | Simpler than WebSockets and sufficient for one-way status pushes. |
| D10 | Distribution | A **`.deb` built with nfpm** for arm64 (plus amd64 for VM testing), published as a GitHub Release | Installs and upgrades natively with `apt`/`dpkg`. |
| D11 | Admin UI exposure | **Home network and VPN only** (decided), enforced both in the app and in nftables. The admin may add extra private-range sources, such as a Tailscale tailnet, as a setting (2026-09-26) | Anyone who controls the UI can reach the whole home network, so it must never face the internet. Two independent layers keep it off the internet even if one is misconfigured. |
| D12 | Client DNS | **Public resolvers by default; a resolver on the host, such as AdGuard Home, when one answers on the VPN addresses** (decided; amended 2026-09-29); optional AdGuard Home integration syncs client names through its REST API (M4) | Ad-blocking and per-client DNS query logs for VPN clients, with no second resolver to run, on hosts that have one; a working default on hosts that don't. The setup wizard and Settings check the VPN addresses (`GET /api/server/dns-check`) and preselect the host only when it answers. |

---

## 4. Architecture

### 4.1 Components

```mermaid
flowchart LR
  Browser["Browser - Svelte SPA"]
  Peers(("WireGuard clients"))

  subgraph Host["Host - Debian-family Linux"]
    subgraph Daemon["drawbridge.service - user drawbridge, CAP_NET_ADMIN"]
      API["HTTP API + SSE"]
      SVC["Service layer - validation, IPAM, keys"]
      REC["Reconciler"]
      MON["Monitor - sessions, traffic"]
    end
    CLI["drawbridge CLI"]
    Tunnel["drawbridge-tunnel.service - oneshot at boot"]
    DB[("SQLite - /var/lib/drawbridge")]
    Kernel["Kernel - wg0 + nft table inet drawbridge"]
  end

  Browser -->|"HTTPS :51821"| API
  API --> SVC
  CLI -->|"unix socket"| SVC
  SVC --> DB
  SVC --> REC
  REC -->|"wgctrl / netlink / nft -f"| Kernel
  Tunnel -->|"same reconciler code"| Kernel
  MON -->|"poll peers"| Kernel
  MON --> DB
  Peers -->|"UDP 51820 over IPv4 or IPv6"| Kernel
```

All roles are subcommands of a single binary, `drawbridge`:

| Subcommand | Purpose |
|---|---|
| `drawbridge serve` | The long-running daemon: HTTP API, SSE, embedded SPA, reconciler, and monitor. |
| `drawbridge tunnel up\|down` | Used by `drawbridge-tunnel.service` to bring the VPN up or down from DB state (M1). |
| `drawbridge server show\|set` | Shows or changes the server's settings: endpoint, port, MTU, DNS, keepalive, client isolation (M1). |
| `drawbridge client list\|add\|show\|pause\|resume\|rename\|delete\|config\|qr` | Headless client management. `qr` prints the QR code in the terminal (M1; `rename` M2). |
| `drawbridge events [--client NAME]` | The event log: changes from the web and the CLI, logins, and corrected drift (M2). |
| `drawbridge apply [--dry-run]` | Reconciles once and prints the diff (M5). |
| `drawbridge admin create\|reset-password\|disable-2fa\|setup-token` | Recovery when locked out of the UI (M2; `disable-2fa` M5). |
| `drawbridge backup create\|restore` | Consistent DB snapshot (`VACUUM INTO`), optionally encrypted (M5). |
| `drawbridge export --format wg-quick` | Prints an equivalent `wg-quick` config, for transparency or migrating away (M6). |
| `drawbridge import --from wg-quick\|wg-easy <file>` | Migration from an existing setup (M6). |
| `drawbridge doctor` | Host diagnostics in the terminal (see §6.6, M5). |

The CLI talks to the daemon over `/run/drawbridge/control.sock` (mode 0660, owned by
`drawbridge:drawbridge`, in a 0750 directory), so root and members of the drawbridge group can
use it. The socket's peer credentials name the CLI user in the event log. In M2, the `admin`
commands go through the socket too. Making the recovery commands (`admin`, `backup`, `doctor`)
work with the daemon stopped, by opening the DB directly, is M5.

### 4.2 Process and privilege model

`CAP_NET_ADMIN` is enough to create a WireGuard interface, configure its keys and peers over
generic netlink, set addresses, routes, and MTU over rtnetlink, and load nftables rules. Linux
passes ambient capabilities to exec'd binaries such as `nft`, so no process needs root at runtime.

Settings that need root are set once by the package's `postinst`:

- `/etc/sysctl.d/90-drawbridge.conf`: `net.ipv4.ip_forward=1` and `net.ipv6.conf.all.forwarding=1`
  (plus the `accept_ra` drop-in in §5.5, `/etc/sysctl.d/91-drawbridge-accept-ra.conf`).
- `/etc/modules-load.d/drawbridge.conf`: `wireguard`, so the module is loaded at boot.
- The `drawbridge` system user, the directories, the TLS certificate, and the at-rest
  encryption key.

`drawbridge.service` (abridged):

```ini
[Unit]
Description=Drawbridge WireGuard management daemon
Wants=network-online.target drawbridge-tunnel.service
After=network-online.target drawbridge-tunnel.service

[Service]
Type=notify
User=drawbridge
Group=drawbridge
ExecStart=/usr/bin/drawbridge serve --config /etc/drawbridge/drawbridge.toml
Restart=on-failure
AmbientCapabilities=CAP_NET_ADMIN
CapabilityBoundingSet=CAP_NET_ADMIN
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
RestrictNamespaces=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
StateDirectory=drawbridge
RuntimeDirectory=drawbridge
UMask=0077

[Install]
WantedBy=multi-user.target
```

`drawbridge-tunnel.service` is `Type=oneshot` with `RemainAfterExit=yes`, the same user, the same
capabilities, and the same sandboxing. It runs `ExecStart=drawbridge tunnel up` and
`ExecStop=drawbridge tunnel down`, and is ordered `After=network-online.target nftables.service`.

The two units have separate lifecycles:

- `systemctl stop drawbridge` stops the UI only. The VPN keeps running.
- `systemctl stop drawbridge-tunnel` takes the VPN down.

### 4.3 Reconciler (desired state to kernel state)

Every change follows the same path:
**validate → write the DB (transaction) → reconcile → emit an event**.

The reconciler is idempotent. It is serialized by a lock and runs in four situations: on
startup, after every change, every 30 s to detect drift, and from `drawbridge tunnel up`.

1. **Validate** the desired state:
   - No overlapping or duplicate addresses or keys.
   - The VPN subnets don't overlap any subnet on the host's interfaces.
   - MTU ≥ 1280 when IPv6 is enabled.
2. **Interface:**
   - Create `wg0` over netlink if it's missing. Only `drawbridge tunnel up` does this. The
     daemon's runs leave a missing interface alone, so a tunnel the admin stopped stays
     stopped (ADR 0008).
   - Set the private key and listen port.
   - Add or remove addresses to match the desired state.
   - Set the MTU and bring the link up.
3. **Peers:** diff the kernel peers against the enabled clients in the DB.
   - Add new peers and remove stale ones.
   - Update changed peers with `ReplaceAllowedIPs`. This doesn't interrupt their sessions.
4. **Routes:** add a route through `wg0` for each extra subnet routed to a client (site-to-site).
5. **Firewall:** render `table inet drawbridge` and apply it atomically with `nft -f`
   (`table …; delete table …; table … { … }` in a single transaction).
6. **Record** the applied revision. If step 1 corrected drift (for example, after someone ran
   `wg set` by hand, or `nftables.service` ran `flush ruleset`), log a warning event.

**Safe apply (commit-confirm).** Some changes can cut off an admin who is connected through the
VPN: listen port, subnets, server key, and firewall or NAT changes. The UI warns first. After it
applies the change, the admin has 60 s to click "Keep changes". If they don't (for example,
because the change disconnected them), the previous settings are restored automatically.

### 4.4 Filesystem layout

| Path | Owner / mode | Contents |
|---|---|---|
| `/usr/bin/drawbridge` | root 0755 | Single binary with the SPA embedded |
| `/etc/drawbridge/drawbridge.toml` | root:drawbridge 0640 | Bootstrap config: listen addresses, allowed admin source ranges, TLS paths, log level, DB path |
| `/etc/drawbridge/secret.key` | root:drawbridge 0640 | 32-byte key for encrypting private keys at rest |
| `/var/lib/drawbridge/drawbridge.db` | drawbridge 0600 | SQLite DB (WAL) |
| `/var/lib/drawbridge/nftables.conf` | drawbridge 0600 | Last rendered ruleset, kept for inspection |
| `/var/lib/drawbridge/tls/` | drawbridge 0700 | Self-signed or uploaded or ACME certificates |
| `/var/lib/drawbridge/backups/` | drawbridge 0700 | Nightly rotating DB snapshots |
| `/run/drawbridge/control.sock` | root:drawbridge 0660 | CLI control socket |

---

## 5. Networking design

### 5.1 Addressing and IPAM

| Setting | Default | Notes |
|---|---|---|
| IPv4 subnet | `10.8.0.0/24`, server `10.8.0.1` | Any RFC 1918 CIDR from /16 to /29. Checked for overlap with LAN subnets. |
| IPv6 subnet | Random **ULA /64** generated at install per RFC 4193 (e.g., `fd3a:5c1e:92b0:1::/64`), server `::1` | A random prefix avoids collisions with other ULA networks. |
| Client addresses | Next free host in each family, e.g., `10.8.0.23/32` + `fd3a:5c1e:92b0:1::23/128` | The IPv6 host ID mirrors the IPv4 host index so the two addresses are easy to match up. Both are editable. |
| Listen port | UDP `51820` | The kernel socket listens on both IPv4 and IPv6. |

Changing a subnet re-addresses every client and keeps host IDs where possible. It goes through
safe apply, and every client config is then flagged as outdated (§6.1).

### 5.2 IPv6 modes

1. **NAT66 (default).** Clients get ULA addresses, and traffic to the internet is masqueraded
   behind the host's global IPv6 address. It works with any ISP and router and needs no router
   changes.
   - Per RFC 6724, many operating systems prefer IPv4 over a ULA source for dual-stack
     destinations. IPv6-only destinations still work. This is expected behavior, not a bug.
2. **Routed GUA (advanced, M6).** Clients get global addresses, so there's no NAT and they get
   native IPv6. It depends on a stable prefix from the ISP. There are two ways to do
   it:
   - **Routed /64:** the router routes a separate /64 from the ISP's delegated prefix to the host.
     This needs the ISP to delegate more than a /64 (for example, a /56) and the router to accept
     an **IPv6** static route. Many consumer routers accept IPv4 static routes only (§16).
   - **NDP proxy:** clients get addresses from a reserved slice of the LAN's own /64, and the host
     answers neighbor discovery for them (`proxy_ndp` sysctl plus proxy entries managed by the
     reconciler). It needs no router support, so it's the fallback when the router can't route
     IPv6.
3. **IPv6 disabled.** Clients still get `::/0` in `AllowedIPs` (optional, on by default). IPv6
   traffic is then dropped inside the tunnel instead of leaking outside it.

### 5.3 Firewall and NAT (nftables)

Drawbridge owns only `table inet drawbridge`. A rendered example (the LAN prefixes are
examples; the real ones are detected from the uplink interface):

```nft
# Generated by Drawbridge. Do not edit; changes are overwritten.
table inet drawbridge {
  set admin_allowed4 {                              # LAN + VPN (§6.5)
    type ipv4_addr; flags interval
    elements = { 192.168.4.0/22, 10.8.0.0/24 }
  }
  set admin_allowed6 {                              # link-local + LAN /64 + VPN
    type ipv6_addr; flags interval
    elements = { fe80::/10, 2001:db8:1234:5600::/64, fd3a:5c1e:92b0:1::/64 }
  }
  chain input {
    type filter hook input priority filter; policy accept;
    iif "lo" accept
    tcp dport 51821 ip  saddr != @admin_allowed4 drop   # admin UI: never from the internet
    tcp dport 51821 ip6 saddr != @admin_allowed6 drop
  }
  chain forward {
    type filter hook forward priority filter; policy accept;
    iifname "wg0" oifname "wg0" drop                  # client isolation (toggle)
    # per-client policies, e.g., "internet only" (M6):
    # iifname "wg0" ip saddr @internet_only4 ip daddr @private4 drop
  }
  chain mss_clamp {
    type filter hook forward priority mangle; policy accept;
    oifname "wg0" tcp flags syn tcp option maxseg size set rt mtu
    iifname "wg0" tcp flags syn tcp option maxseg size set rt mtu
  }
  chain postrouting {
    type nat hook postrouting priority srcnat; policy accept;
    ip saddr 10.8.0.0/24 oifname != "wg0" masquerade
    ip6 saddr fd3a:5c1e:92b0:1::/64 oifname != "wg0" masquerade   # NAT66 mode
  }
}
```

NAT masquerades the VPN subnets on every interface except the tunnel. That covers whichever
interface is the uplink (Ethernet or Wi-Fi) without detecting it, and the home LAN too, whose
devices have no route back to the VPN subnets.

The admin sets hold the LAN's subnets as detected at each reconcile (every 30 s), so a new
IPv6 prefix from the router is admitted within half a minute, and the always-allowed loopback and
link-local ranges. `tunnel down` deletes the whole table, admin chain included; while the tunnel
is stopped, the app's own allowlist is the only layer (§6.5).

In nftables, an `accept` in one table can't override a `drop` in another. For that reason,
Drawbridge adds only **restrictive rules and NAT**. It never tries to "open" the host firewall.
Because a `drop` in any table is final, the `input` chain above enforces the admin UI allowlist
no matter how the host firewall or the router's IPv6 firewall is set up. For the opposite case, the
diagnostics page (§6.6) detects configurations that block VPN traffic and explains how to fix
them:

- A `policy drop` in the user's own `forward` or `input` chains.
- Docker's iptables `FORWARD` policy `DROP`, which silently breaks VPN forwarding when a
  *rootful* Docker daemon runs on the same host. Rootless Docker keeps its rules in its own
  network namespace and doesn't touch the host firewall, so it isn't affected.

Debian's default `/etc/nftables.conf` begins with `flush ruleset`, so restarting
`nftables.service` wipes the Drawbridge table. The 30-second drift check restores it.

A **routed IPv4 mode** (no NAT, M6) is also available. It needs a static route to `10.8.0.0/24`
via the host's LAN address on the home router, which most routers support. LAN devices then see
real client addresses instead of the host's. NAT stays the default because it needs no router
changes.

### 5.4 MTU

- The server MTU defaults to **1420**, which fits a 1500-byte path over either family:
  - IPv4 outer header: 1500 − 20 (IPv4) − 8 (UDP) − 32 (WireGuard) = 1440.
  - IPv6 outer header: 1500 − 40 (IPv6) − 8 (UDP) − 32 (WireGuard) = 1420.
- Client MTU defaults to the server value and can be overridden per client. For example, use
  **1412** or lower over PPPoE, and lower values for some mobile carriers.
- Validation enforces 1280 ≤ MTU ≤ 1500 when IPv6 is enabled. IPv6 requires at least 1280.
- MSS clamping (§5.3) protects TCP when path MTU discovery is broken.

### 5.5 Host prerequisites (handled by the installer and verified by `doctor`)

- **Forwarding sysctls** for IPv4 and IPv6.
- **Router Advertisements.** Enabling IPv6 forwarding makes the kernel **ignore RAs** on
  interfaces where `accept_ra=1`. A host that relies on kernel SLAAC (ifupdown) would lose its
  own IPv6 address and default route.
  - NetworkManager and systemd-networkd handle RAs in userspace and set `accept_ra=0`, so
    they aren't affected.
  - The installer runs `/usr/lib/drawbridge/accept-ra` before it enables forwarding. For each
    uplink (the default route's interface, IPv6 first, then IPv4 for an upgrade where the IPv6
    route is already gone) whose `accept_ra` is `1`, it sets `accept_ra=2` now and writes
    `/etc/sysctl.d/91-drawbridge-accept-ra.conf` for boot. An upgrade keeps an interface
    already in that file while it still reads `2`. `postrm` removes the file. The kernel
    integration tests send a Router Advertisement to a namespace with forwarding on: the host
    ignores it before the step and configures its address and default route after.
- **NetworkManager and `wg0`:** when NetworkManager is active, the installer adds
  `/etc/NetworkManager/conf.d/drawbridge.conf` with `[keyfile]`
  `unmanaged-devices=interface-name:wg0`, so NetworkManager never tries to configure the VPN
  interface or remove its addresses.
- **Kernel module:** `wireguard` is loaded at boot.
- **Time sync:** `systemd-timesyncd` is active. Hosts without a battery-backed clock (such as a
  Raspberry Pi without its RTC battery) start with the wrong time, and TLS, TOTP, and handshake
  timestamps depend on the correct time.
- **Uplink detection:** the default route for each address family, with an optional manual
  override in the UI.

### 5.6 Endpoint and FQDN

- Clients use `Endpoint = <FQDN>:<port>`. The FQDN should have an **A** record and an **AAAA**
  record (for the host's stable IPv6 address).
- A per-client **endpoint override** lets a client use, for example, an IPv4-only hostname
  (`vpn4.example.com`) when it's on a network with broken IPv6.
- **Dynamic IPv4 address:** a dynamic DNS client the admin already runs (ddclient, or the
  router's built-in one) keeps the A record current, so Drawbridge doesn't include its own DDNS
  updater. Diagnostics compare the A record with the current public IPv4 address (looked up only
  when the check runs) and warn when they differ.
- **AAAA record:** with a stable IPv6 prefix, the simplest setup is a static AAAA record
  pointing at the host's stable address. If a dynamic DNS client updates AAAA too, it must use
  the host's *stable* address. An external "what's my IP" lookup returns the temporary (privacy)
  address that the host uses for outgoing connections, and that address changes every day or so.
  Diagnostics warn if the AAAA record points at a temporary address. `ip -6 addr show scope global
  -temporary` lists the stable addresses.
- WireGuard clients resolve the endpoint once, when they connect. After the public IPv4 address
  changes, clients need to reconnect (phones usually do this on their own when they switch
  networks).
- **CGNAT or DS-Lite** (no inbound IPv4) can't be fixed on the host. The docs explain two
  options: an IPv6-only endpoint, or a relay VPS.

---

## 6. Feature specification

### 6.1 Clients

| Feature | Behavior |
|---|---|
| **Add** | Fields: name (required, unique) and notes. IPv4 and IPv6 addresses are auto-assigned (editable). A server-side keypair and a preshared key (PSK) are generated by default. **Bring-your-own-key** mode instead takes the client's public key, so the private key never touches the server. |
| **Access presets** | Client `AllowedIPs` presets: *Full tunnel* (`0.0.0.0/0, ::/0`), *VPN subnet only*, *VPN + home LAN* (LAN prefixes auto-detected), or *Custom*. |
| **Advanced per client** | Overrides for DNS, MTU, PersistentKeepalive (default 25 s), and endpoint. Server-side extra routed subnets for site-to-site. Expiry date. |
| **Pause / resume** | Pausing sets `enabled=false` and **removes the peer from the kernel** while keeping all config in the DB, so the client can't handshake at all. Resuming re-adds the peer. Clients can also be paused until a set date and time (M6). |
| **Remove** | Deletes the peer and the DB row after confirmation. Past events keep a snapshot of the client name, so the logs stay readable. |
| **Config delivery** | Download a `.conf` file or show a QR code (generated in the browser; the view is logged). One-time download links that expire (M6). |
| **Outdated-config tracking** | Stores the hash of the config the client last received. When server-side changes (endpoint, port, server key, DNS, and so on) alter the rendered config, the client is flagged **"config outdated, re-import needed"**. |
| **Key rotation** | Regenerates the client keypair and PSK, and flags the config as outdated. |
| **Live status** | Online, idle, or never connected; last handshake; current endpoint (IP:port); RX/TX totals; live throughput. |

Example generated client config:

```ini
[Interface]
PrivateKey = <client private key>
Address = 10.8.0.23/32, fd3a:5c1e:92b0:1::23/128
DNS = 1.1.1.1, 2606:4700:4700::1111
MTU = 1420

[Peer]
PublicKey = <server public key>
PresharedKey = <psk>
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
```

### 6.2 Server settings

The settings are grouped into sections. Each field shows its impact ("applies live",
"disconnects clients", or "client configs must be re-imported"), and risky changes go through
safe apply (§4.3).

- **Endpoint:** public FQDN or IP and the advertised port (which can differ from the listen port
  when the router uses port translation). An optional "detect public IP" button calls an external
  service only when clicked.
- **Interface:** interface name, listen port, MTU, and rotating the server keypair (with a strong
  warning, because every client must re-import its config).
- **Addressing:** IPv4 CIDR and server address; IPv6 on or off, its CIDR and server address, and
  the mode (NAT66 or routed).
- **Routing and firewall:** uplink interface (auto or manual), NAT on or off for each family,
  client isolation, and MSS clamping.
- **Defaults for new clients:** DNS, AllowedIPs preset, MTU, keepalive, PSK on or off, and
  whether client private keys are stored on the server.

### 6.3 DNS

Drawbridge doesn't install or manage a resolver. When the host already runs one on all
addresses (AdGuard Home, Pi-hole, Unbound, dnsmasq), it becomes the clients' resolver, and
AdGuard Home in particular gets an optional integration (below).

- **Default for new clients:** Cloudflare's public resolvers (`1.1.1.1` and `1.0.0.1`, plus the
  IPv6 pair when the VPN has IPv6), because they work on any host. *This server* (the server's VPN
  addresses, `10.8.0.1` and `fd…::1`) is a choice the admin makes, and only after the server has
  checked that something answers DNS there. The other choices are *Other servers* and *None*.
  Each preset has IPv4 and IPv6 addresses.
  - **The check** (`GET /api/server/dns-check`): the daemon sends a UDP DNS query for
    `example.com` (an A record, recursion desired) to each VPN address on port 53, in parallel,
    and waits two seconds. NOERROR and NXDOMAIN count as an answer. REFUSED and SERVFAIL mean a
    resolver is listening but unusable (its access settings may exclude the VPN, or its upstream
    servers are down); a refused connection means nothing is listening; silence means a firewall
    or a stopped tunnel. Each address is reported on its own, and "this server" saves only the
    ones that answered, so a resolver bound to IPv4 alone doesn't leave clients waiting on an IPv6
    address that never replies. systemd-resolved's stub listens on `127.0.0.53` only, so the
    check finds nothing there.
  - **The CLI** (`drawbridge server set --dns server`) runs the same check through the control
    socket (`GET /v1/dns-check`) and saves only the addresses that answer. When none does, it
    refuses and says why; `--force` saves both VPN addresses anyway.
  - **The setup wizard** (§9) runs the check on its DNS step and preselects *This server* when it
    finds an address that answers; otherwise it preselects the public resolvers. Settings has the
    same choices and a check button.
- Optional **search domains** (written to the `DNS =` line; `wg-quick` supports them, and support
  varies across the client apps).
- **Per-client override.**
- **AdGuard Home requirements** (checked by diagnostics, with fix hints):
  - It must answer DNS on both VPN addresses. If its `dns.bind_hosts` setting lists specific
    addresses rather than all interfaces, the VPN addresses must be added, and AdGuard Home must
    start after `drawbridge-tunnel.service` so those addresses exist. When it listens on all
    addresses (its default), the VPN addresses are covered with no changes.
  - Its access settings must allow the VPN subnets. The default allows all clients.
  - There are no port clashes: AdGuard Home uses port 53 and its own web port, while Drawbridge
    uses UDP 51820 and TCP 51821.
- **AdGuard Home integration (optional)** through its REST API (under `/control`, with HTTP
  basic auth; for a local install, `http://127.0.0.1:3000/control` by default). It uses a
  dedicated AdGuard Home account whose password Drawbridge stores encrypted. Without AdGuard
  Home, everything else works the same.
  - **Client name sync (M4):** each Drawbridge client becomes an AdGuard Home persistent client
    with its VPN IPv4 and IPv6 addresses (`/control/clients/add`, `/update`, `/delete`). AdGuard
    Home's query log and statistics then show names like `phone` instead of `10.8.0.23`.
  - **Per-client DNS log (M4):** the client detail page shows the client's recent DNS queries
    from AdGuard Home's query log (`/control/querylog?search=<client IP>`), with a link to
    AdGuard Home.
  - **Per-client ad-blocking switch (M6):** turns AdGuard Home filtering off or on for one client,
    for example a device that breaks when ads are blocked.
  - **Client hostnames (M6):** AdGuard Home DNS rewrites (`/control/rewrite/*`) give clients names
    such as `<client>.vpn.lan`.
  - If AdGuard Home is unreachable, VPN management keeps working. Sync retries in the background,
    and the dashboard shows a warning.
- **Health check:** if clients are pointed at the host but nothing answers on port 53 at the VPN
  addresses, the UI shows a warning.

### 6.4 Monitoring and logging

WireGuard is connectionless and logs nothing itself, so Drawbridge derives activity from peer state.
The monitor polls `wgctrl` every 5 s, which costs very little.

*Built ahead of the rest of M4 (2026-09-27): the poller (`Service.TrackConnections`,
`internal/service/conntrack.go`), the `connected`/`disconnected`/`roamed` events below, and the
`client_sessions` table, scoped to the bytes a client's current connection has moved (the API's
`session_receive_bytes`/`session_send_bytes`, alongside the peer's all-time totals). The traffic
table, sampler, and rollup/retention job are also built (2026-09-28:
`internal/service/traffic.go`, `internal/store/traffic.go`), along with the API routes that read
both it and a client's session history (§8: `GET /api/clients/{id}/traffic`, `GET /api/traffic`,
`GET /api/clients/{id}/sessions`). The dashboard's total-throughput chart, the client-detail
page's throughput and cumulative charts, and its session-history list are built too (uPlot, per
§9). Every chart has the same range control (1m/1h/12h/24h/7d/30d/90d), and the choice is one for
the whole app, remembered in the browser. A Charts page (an icon in the header, between Clients
and Server Settings) shows the same history per client, as Received, Sent, Cumulative Received,
and Cumulative Sent charts, stacked (`GET /api/traffic/clients`). The log viewer's CSV export,
structured journald fields, and AdGuard Home integration (§6.3) are still pending.*

```mermaid
stateDiagram-v2
  [*] --> Never
  Never --> Online: first handshake
  Online --> Idle: no handshake for more than 180 s (+ margin)
  Idle --> Online: new handshake
  Online --> Online: endpoint changed (roamed)
```

- **Events** (one table, filterable by client, type, and date, exportable as CSV):
  - Connection events: `connected`, `disconnected` (with session duration and bytes), and
    `roamed` (with the new endpoint IP).
  - Admin events: `created`, `updated`, `paused`, `resumed`, `deleted`, `config_downloaded`,
    `qr_shown`, and `keys_rotated`.
  - System events: `apply_ok`, `apply_failed`, `drift_corrected`, `login_ok`, `login_failed`, and
    `settings_changed`.
- **Sessions table:** one row per connection (start, end, endpoint, bytes), which gives a
  per-client connection history.
- **Traffic history:**
  - RX/TX deltas are stored at a "raw" resolution (a minute wide by default) for 48 h and a
    "hourly" resolution for 90 days, both windows configurable, and the raw resolution's bucket
    width is too: a host on an SD card can keep the conservative defaults, and one on an NVMe
    SSD can afford a finer interval and/or longer retention without the same write-wear concern
    (`--traffic-raw-interval`, `--traffic-raw-retention`, `--traffic-hourly-retention`). The
    stored values are named "raw"/"hourly" rather than a literal "1m"/"1h", since the interval
    isn't always a minute.
  - Counter resets (a peer re-added or the interface recreated) are detected when a new counter
    value is lower than the previous one, the same way `client_sessions` already does it.
  - **Chart ranges:** 1m, 1h, 12h, 24h, 7d, 30d, and 90d. The stored raw buckets are too coarse
    for 1m (a minute wide by default, and the newest isn't flushed until its minute is over), so
    1m is served from the last two minutes of 5 s polls, kept in memory only
    (`Service.live`, `internal/service/traffic.go`). Nothing is written to the database for it,
    so a restart starts it empty and it fills back in within a minute. The UI refetches it every
    5 s, and every other range every 60 s. 1h, 12h, and 24h read the raw buckets; 7d, 30d, and
    90d read the hourly rollup, which the 90-day retention covers.
  - **The Charts page** draws one line per client (Beszel's network charts, with a client where it
    has an interface), and leaves out a client that moved nothing in the range. Received and
    Sent are the server's, as everywhere else in Drawbridge: the bytes it received from the client
    (`receive_bytes`) and the bytes it sent the client (`send_bytes`), as bit rates, where Beszel
    says Download and Upload. The two cumulative charts are running totals of those bytes from
    the start of the range, which is the closest thing to Beszel's interface counters that
    survives a reset. `GET /api/traffic/clients` returns every
    client's samples, plus `step_seconds` (what a sample covers, so bytes become a rate) and
    `until`. It leaves out a bucket until it has ended and the poll after it has saved it, so a
    bucket that's still filling or isn't written yet is never drawn as a drop to zero. The page
    averages a stored range into about 144 points (a day becomes ten minutes, a week two hours,
    90 days one day), counted back from `until`, because a day of minutes is 1,440 points in a few
    hundred pixels and blurs into a solid band. Lines are monotone splines, so a quiet stretch
    dips to zero without overshooting it.
  - **The dashboard and a client's page** chart the total, or the client's own, as Beszel's
    bandwidth chart does: a Received line and a Sent line as bit rates, and a tooltip that follows
    the cursor with both and their total. A client's page adds a cumulative chart of the same two
    lines as running totals. `GET /api/traffic` and `GET /api/clients/{id}/traffic` return the
    same window as the per-client route: `step_seconds`, `until`, and `samples`.
  - **The x axis** is labeled by range: every 15 s for 1m (to the second), every 5 min for 1h,
    every hour for 12h, every 3 h for 24h, every day for 1w, every 3 days for 30d, and every week
    for 90d. Under a week it shows times only, from a week up dates only, and never a year. The
    hours and days are on the browser's local clock and calendar, and a label is dropped when it
    wouldn't fit (a phone).
  - **Times in the UI** are on a 24-hour clock (`18:24`, and `18:24:05` where seconds matter) and
    dates are a day and an English month (`9 Sep`), written out by `web/src/lib/format.ts` and not
    left to the browser's locale. A year is added only to a date in another year than this one.
- **Low write volume:** samples are buffered in memory and flushed once per raw interval in one
  transaction, and a periodic job rolls old raw rows up into hourly ones, then prunes both past
  their retention windows. This keeps SD card writes low (and stays low on an SSD too, unless the
  admin explicitly widens the budget above).
- **journald:** each event is also logged as structured JSON (`journalctl -u drawbridge`). A
  `DRAWBRIDGE_CLIENT=` field makes it possible to filter by client.
- **DNS queries:** what each client looked up comes from AdGuard Home's query log (§6.3), which
  has its own retention settings.
- **"Online" is a heuristic.** An idle client without keepalive shows as *idle* after about
  3 minutes even though its app still says "active". The UI says this.
- **Opt-in flow logging (M6):** per-destination connection logs from conntrack netlink events.
  This is privacy-sensitive, so it's off by default and has its own retention setting.
- **Optional extras (M6):** a Prometheus `/metrics` endpoint and GeoIP/ASN for endpoints.

### 6.5 Admin and security features

- **First-run setup:**
  - A one-time **setup token** is printed by `postinst` and to the journal
    (`drawbridge admin setup-token` shows it again). This stops anyone else on the LAN from claiming
    the admin account first.
  - The wizard creates the admin account, then asks for the endpoint FQDN and the clients' DNS
    (subnets aren't asked yet).
- **Passwords:** hashed with Argon2id (RFC 9106's 64 MiB, three-pass profile, one hash at a
  time so logins can't exhaust the host's memory), in the PHC string format. A password needs at
  least 10 characters; there are no composition rules.
- **Sessions:**
  - Stored server-side in the DB, in a `__Host-drawbridge` cookie that is `HttpOnly`, `Secure`,
    and `SameSite=Strict`. The DB holds only a SHA-256 hash of each token.
  - They expire after an hour idle, and twelve hours after login at most. Their last use is
    written at most every five minutes, to spare the SD card. A visible page's live status
    counts as use; background tabs stop polling, so they do idle out.
  - The API lists active sessions and can revoke them. Changing the password ends every other
    session.
- **TOTP 2FA** with recovery codes (M5).
- **Brute-force protection:** failed logins are rate-limited per source (IPv6 by /64) and per
  account. After five failures, each attempt waits twice as long as the last, from 2 seconds up
  to 15 minutes; an hour without a failure resets the count. `drawbridge admin reset-password`
  lifts an account's lockout.
- **CSRF protection:** `SameSite=Strict`, plus state-changing requests must carry the
  `X-Drawbridge` header, send JSON, and, when the browser says, come from the same origin
  (`Origin`, `Sec-Fetch-Site`).
- **Security headers:** a strict CSP (no inline scripts except the app shell's bootstrap script,
  allowed by its hash, which the server computes from the embedded build at startup),
  `X-Frame-Options: DENY`, `Referrer-Policy`, cross-origin isolation headers, and HSTS only when a
  trusted certificate is used (M6).
- **Audit:** every change, login, failed login, and config download is an event with its actor:
  the username and source address for the web, the account name for the CLI, or the daemon.
- **Admin access: home network and VPN only (decided).** The UI must never be reachable from the
  internet. Two independent layers enforce this:
  - **In the app:** requests are accepted only from the LAN's own subnets (the router's IPv4
    subnet and the LAN's IPv6 /64, detected from the uplink interface), link-local addresses,
    loopback, and the VPN subnets. The LAN is the on-link subnets of the interfaces that carry the
    default routes and hold one of the host's addresses; with no default route, only loopback,
    link-local, and the VPN are allowed. The allowlist doesn't simply trust "private ranges,"
    because LAN devices often reach the host over the LAN's *global* IPv6 prefix.
  - **In the firewall:** the `input` chain in Drawbridge's nftables table drops traffic to the
    admin port from any other source (§5.3). This protects the UI even if the router's IPv6
    firewall lets inbound traffic through to the host.
  - The router forwards only UDP 51820, never the admin port.
  - **No reverse proxy in front of the admin UI.** Many hosts also run a reverse proxy (or a
    container publishing ports 80 and 443). If the admin UI were routed through one on the same
    host, requests would reach Drawbridge from the host itself, pass both allowlist layers, and
    be exposed if 80 or 443 is forwarded from the internet. Drawbridge therefore serves the UI
    directly on port 51821, and it ignores `X-Forwarded-For` and similar headers, so no proxy
    can make a request look like it came from the LAN.
  - **Extra sources (decided 2026-09-26).** The admin can add prefixes to both layers with the
    `admin_allowed` setting (`drawbridge server set --admin-allow 100.64.10.0/24`), for a Tailscale
    tailnet, say. Each prefix must lie inside 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16,
    Tailscale's 100.64.0.0/10, or fc00::/7, so no setting can admit a globally routable source.
    Only the TCP connection's source address counts, so a spoofed private source that got past the
    router would pass; the router's WAN side must drop those.
  - An optional stricter mode allows VPN access only.
- **One admin account** in v1.0 (decided). Multiple admins and API tokens for automation are
  optional (M6).

### 6.6 System

- **Diagnostics** (the web page and `drawbridge doctor` run the same checks). Each check shows
  pass, warn, or fail with a fix hint, or skip when the daemon can't read what the check needs:
  - The tunnel is up, and the kernel module is loaded.
  - Forwarding sysctls set, and `accept_ra` correct.
  - Uplink interface detected.
  - VPN subnets don't overlap the LAN.
  - Drawbridge's own nftables table is loaded.
  - Host firewall and Docker `FORWARD` policy aren't blocking traffic.
  - Clients' DNS servers answer, when they're this server's own VPN addresses.
  - The FQDN resolves to an address a client on the internet can reach, and its AAAA record
    isn't a temporary address.
  - Time is synced.
  - Free disk space.
  - The TLS certificate hasn't expired.

  *`drawbridge doctor` is built (`internal/diag`); the web page is not. The daemon runs the
  checks, because `nft -j list ruleset` needs `CAP_NET_ADMIN`, and the CLI prints them over the
  control socket (`GET /v1/diagnostics`). It changes nothing, so it records no events. Its
  exit status is 1 when a check failed, and 0 otherwise. Known limits:*
  - *Comparing the A record with the current public IPv4 address (§5.6) isn't built (§16).*
  - *The host firewall check is a best guess from `nft -j list ruleset`: it doesn't model rule
    order, can't see iptables-legacy, and doesn't check input-chain drops of UDP 51820.*
  - *The clock check recognizes only systemd-timesyncd, so a host that uses chrony or ntpd sees
    a warning.*
  - *The overlap hint is limited because the VPN's subnets can't change after setup.*
- **Backup and restore:**
  - One-click download of a consistent snapshot, optionally encrypted with a passphrase
    (Argon2id key derivation + XChaCha20-Poly1305).
  - Restore through the UI or CLI.
  - Nightly local snapshots with rotation.
- **TLS:** the daemon creates a self-signed ECDSA certificate on first start, with SANs for the
  hostname (and `.local`), loopback, and the LAN and VPN addresses, and replaces it 30 days before
  it expires. It lasts 800 days, under the 825 days Apple's platforms accept. Its SHA-256
  fingerprint is in the journal and in `drawbridge admin setup-token`, so the admin can check the
  browser's warning is about this certificate. Users can upload their own certificate (M5).
  ACME DNS-01 is available in M6; HTTP-01 isn't a good fit because the UI shouldn't be exposed
  to the internet.
- **Retention settings, about/version, and an optional update check.**

---

## 7. Data model (SQLite)

```text
server              (singleton) iface, listen_port, endpoint_host, endpoint_port,
                    private_key_enc, public_key, mtu,
                    ipv4_cidr, ipv4_addr, ipv6_enabled, ipv6_cidr, ipv6_addr, ipv6_mode,
                    uplink_iface NULL(auto), nat4, nat6, client_isolation, mss_clamp,
                    default_dns JSON, default_allowed_ips JSON, default_keepalive,
                    default_mtu, store_client_keys, admin_allowed JSON, revision, updated_at
clients             id (uuid), name UNIQUE, notes, enabled, ipv4 UNIQUE, ipv6 UNIQUE,
                    public_key UNIQUE, private_key_enc NULL, psk_enc NULL,
                    allowed_ips JSON NULL, routed_subnets JSON, dns JSON NULL,
                    mtu NULL, keepalive NULL, endpoint_override NULL,
                    access_policy, expires_at NULL, paused_until NULL,
                    delivered_config_hash NULL, created_at, updated_at
client_sessions     id, client_id, started_at, ended_at NULL, endpoint,
                    baseline_rx, baseline_tx, rx_bytes, tx_bytes
traffic             client_id, resolution ('raw'|'hourly'), bucket_start, rx_bytes, tx_bytes
                    PK(client_id, resolution, bucket_start); INDEX(resolution, bucket_start)
events              id, ts, kind, category ('connection'|'admin'|'system'),
                    actor (username, CLI account, or 'drawbridge'), via ('web'|'cli'|'system'),
                    source_ip, client_id NULL, client_name, data JSON
                    INDEX(ts), INDEX(client_id, ts)
users               id, username UNIQUE, password_hash, created_at, password_changed_at,
                    last_login_at NULL, totp_secret_enc NULL (M5),
                    recovery_codes_hash JSON NULL (M5)
auth_sessions       id PK (public, for revoking), token_hash UNIQUE, user_id, created_at,
                    last_seen_at, expires_at, ip, user_agent
setup_token         (singleton) token_enc, created_at; deleted once the admin exists
one_time_links      token_hash PK, client_id, expires_at, used_at NULL        (M6)
api_tokens          id, name, token_hash, scopes, created_at, last_used_at    (M6)
adguard             (singleton) enabled, base_url, username, password_enc,
                    sync_names, last_sync_at NULL, last_error NULL             (M4)
schema_migrations   version, applied_at
```

- Every `*_enc` column is encrypted with XChaCha20-Poly1305 using `/etc/drawbridge/secret.key`. This
  protects DB copies and backups. It doesn't protect against a full compromise of the host.
- Migrations are embedded in the binary. They run at startup after an automatic pre-migration
  snapshot.

---

## 8. HTTP API (JSON, documented as OpenAPI 3.1)

`internal/api/openapi.json` documents the built endpoints, and the daemon serves it at
`/api/openapi.json`. A test keeps it in step with the routes. Clients are addressed by ID in the
API (names can change); the CLI uses names.

```text
Built (M2):
GET    /healthz                          GET /api/version     GET /api/openapi.json
GET    /api/setup                        POST /api/setup      (first run, needs the setup token)
POST   /api/auth/login | /api/auth/logout                      GET /api/auth/me
POST   /api/auth/password                change the password; ends the other sessions
GET    /api/auth/sessions                DELETE /api/auth/sessions/{id}

GET    /api/server                       PATCH /api/server    (settings)
GET    /api/server/status                tunnel up or down, client counts (M3)
GET    /api/server/dns-check             asks the VPN addresses for DNS; which ones answer

GET    /api/clients                      POST /api/clients
GET    /api/clients/{id}                 PATCH /api/clients/{id} (rename)   DELETE /api/clients/{id}
POST   /api/clients/{id}/pause           POST /api/clients/{id}/resume
GET    /api/clients/{id}/config          text/plain; attachment

GET    /api/events?client=&category=&before=&limit=

GET    /api/clients/{id}/traffic?range=1m|1h|12h|24h|7d|30d|90d            (M4)
GET    /api/traffic?range=1m|1h|12h|24h|7d|30d|90d   summed across clients (M4)
GET    /api/traffic/clients?range=1m|1h|12h|24h|7d|30d|90d   one series per client (M4)
GET    /api/clients/{id}/sessions?before=&limit=                           (M4)

Later:
POST   /api/auth/totp/enroll | /verify                                    (M5)
POST   /api/server/rotate-key                                              (M5)
POST   /api/server/apply/confirm         confirm a safe-apply change       (M5)
POST   /api/clients/{id}/rotate-keys                                       (M5)
GET    /api/dns                          PUT /api/dns                      (M4)
GET    /api/integrations/adguard         PUT /api/integrations/adguard     (M4)
POST   /api/integrations/adguard/test    check credentials and DNS on the VPN addresses
GET    /api/clients/{id}/dns-log         recent queries from AdGuard Home  (M4)
GET    /api/events?from=&to=&format=csv                                    (M4)
GET    /api/stream                       SSE: peer status every 5 s + live events (M4)
GET    /api/system/health                diagnostics                       (M5)
POST   /api/system/backup                POST /api/system/restore          (M5)
```

---

## 9. Web UI

Built mobile-first, responsive, with light and dark themes and an auto mode that follows the
browser's setting. It's English-only at first, with text kept in message files so translations
can be added later (i18n).

| Page | Contents |
|---|---|
| **Setup wizard** | Setup token → admin account → endpoint FQDN → DNS (with a check of the host's resolver) → done. A subnets step (IPv4/IPv6) is planned. |
| **Login** | Username, password, and TOTP code |
| **Dashboard** | Server card (up/down, endpoint, public key, port, addresses), client counts (total / online / paused / outdated), client list sortable by name or status (each connected client's endpoint address, session and total traffic), total throughput chart, recent events, diagnostics warnings |
| **Clients** | Searchable, filterable list, sortable by name, status, last handshake, or IP address: status dot, name, addresses, last handshake, endpoint, RX/TX, pause toggle, and quick actions (QR, download, edit, delete) |
| **Client detail** | Overview, config and QR, throughput and cumulative charts, session history, recent DNS queries (from AdGuard Home), and events. Pause (or Resume), Rename, and Delete are buttons at the top: Rename opens a dialog like Add Client's, and Delete asks to confirm in one. An "Advanced" edit section and rotating keys are planned. |
| **Charts** | Received, Sent, Cumulative Received, and Cumulative Sent charts, stacked at the dashboard chart's width, with a line per client, a range control, a legend, and a tooltip that follows the cursor (§6.4) |
| **Server settings** | The sections from §6.2, each marked with its impact |
| **DNS** | Presets and custom resolvers, search domains, AdGuard Home connection (address, account, test button, sync status) |
| **Logs** | Events table with filters and CSV export, plus an audit tab |
| **System** | Diagnostics, backup/restore, admin account and 2FA, sessions, TLS, retention, about |

Libraries: Tailwind CSS, uPlot for charts (small and fast), and `qrcode` for rendering QR codes
in the browser.

---

## 10. Security design

**Threat model.** Anyone who controls the admin UI can create a VPN client and so reach the whole
home LAN. The UI is therefore treated as a high-value target:

- **No arbitrary hooks.** `PostUp`/`PostDown` commands that can be edited in a GUI (common in
  WireGuard GUIs) run as root under `wg-quick`, which turns a compromised web login into root on
  the host. Drawbridge doesn't offer them.
  - Everything that hooks usually do (NAT, forwarding, routes) is a typed, validated setting.
- **Least privilege:**
  - The daemon runs as `drawbridge` with only `CAP_NET_ADMIN` and heavy systemd sandboxing (§4.2).
  - A compromised daemon can change network configuration but can't read `/home`, write system
    files, or run code as root.
- **Strict input validation:** nothing reaches a rendered file unvalidated.
  - Keys must be 32-byte base64. CIDRs and addresses are parsed with `net/netip`. Ports and MTU
    are range-checked.
  - Names, which can contain arbitrary text, never appear in the nftables or WireGuard files.
    This prevents injection through newlines or quotes.
- **Secrets:**
  - Private keys and PSKs are encrypted at rest, and the DB file is 0600.
  - Storing client private keys is optional ("show once, never store").
  - Secrets are never logged, and the config and QR views are logged as events.
- **Transport:** HTTPS only. The admin UI allowlist defaults to private ranges and the VPN
  subnets.
- **Supply chain:**
  - Dependencies are pinned (`go.sum`, lockfile).
  - CI runs `govulncheck` and `npm audit`.
  - Builds are reproducible (`-trimpath`, `SOURCE_DATE_EPOCH`), and releases ship an SBOM and
    checksums.
- **Before v1.0:** a security review against a checklist covering authentication, sessions,
  CSRF, headers, injection, file permissions, and the systemd sandbox, plus a test run with
  `systemd-analyze security drawbridge`.

---

## 11. Packaging, installation, and upgrades

- **Build:**
  - `pnpm build` (the SPA) → `go build` with `CGO_ENABLED=0 GOARCH=arm64` → **nfpm** produces
    `drawbridge_<ver>_arm64.deb`.
  - An amd64 package is also built for testing in VMs.
- **Package metadata:**
  - `Depends: nftables, wireguard-tools`
  - `Recommends: systemd-timesyncd`
- **`postinst`:**
  1. Create the `drawbridge` system user from `/usr/lib/sysusers.d/drawbridge.conf`
     (`systemd-sysusers`, falling back to `adduser`), and the directories.
  2. Generate `secret.key`. (The daemon creates its self-signed TLS certificate on first
     start, in its state directory.)
  3. Install the sysctl and modules-load drop-ins, then apply them. Set `accept_ra=2` where
     it's needed, before forwarding goes on. If NetworkManager is active, install the drop-in
     that leaves `wg0` unmanaged (§5.5).
  4. Initialize the DB (random ULA prefix, server keypair).
  5. Enable and start both units, then print the URL, the setup token, and the certificate's
     fingerprint, until the admin account exists.
- **Upgrades:** `apt install ./drawbridge_<new>.deb`. The daemon restarts, while
  `drawbridge-tunnel.service` isn't restarted, so the VPN stays up. Migrations run after a DB
  snapshot.
- **Remove and purge:**
  - `remove` stops both units and deletes the interface and nft table.
  - `purge` also deletes `/var/lib/drawbridge` and `/etc/drawbridge`.
- **`install.sh`** (convenience): checks the architecture and OS, downloads the latest release,
  verifies the checksum, and runs `apt install`.
- **Later:** a signed APT repository so upgrades come through `apt upgrade`.

---

## 12. Testing strategy

| Level | What | Where |
|---|---|---|
| Unit | IPAM (IPv4 and IPv6, exhaustion, re-addressing), validation, client config and nft rendering (golden files), session state machine (simulated clocks), traffic downsampling and counter resets, crypto helpers | `go test`, every push |
| Integration | Real kernel WireGuard in **network namespaces**: server netns + client netns + an "internet" netns. Covers handshake over IPv4 and IPv6 endpoints, full-tunnel traffic for both families, NAT44 and NAT66, pause/resume, drift correction, `tunnel up/down`, and the admin port: reachable through the tunnel and from the LAN, dropped from the internet | CI's "Integration" job, on a GitHub-hosted Ubuntu VM (root through sudo; the job loads the modules). A self-hosted runner that meets docs/MANUAL_CHECKLIST.md §4 could run it too, but only on a private repo: in a public one it would run code from forks' pull requests |
| API | Auth flows, CSRF, rate limiting, permission errors, the full client lifecycle | `go test` with `httptest` and a **fake WireGuard backend** |
| AdGuard Home client | Name sync (add, rename, delete, retry after an outage) and query log parsing | `go test` against a fake `/control` server; checked against a real AdGuard Home before each release |
| Frontend | Component tests | Vitest |
| E2E | Browser flows (setup, the client lifecycle with the QR code and the download, pausing, settings, logs, logins, and password changes) against `drawbridge serve --backend fake`, failing on any script error or CSP violation | Playwright: `make test-e2e`, and CI's "E2E (browser)" job |
| On hardware | Manual checklist for each release (below) | The reference platform (§2) |

The on-hardware checklist:

- The VPN survives a reboot.
- Clients connect over the IPv4 endpoint and over the IPv6 endpoint.
- Tested clients: Android, iOS, Windows, macOS, and Linux.
- test-ipv6.com and a DNS leak test pass, and ads are blocked for VPN clients.
- Throughput measured with `iperf3`.
- Upgrading from the previous version keeps the tunnel up.
- Restore works on a freshly flashed SD card.

The WireGuard backend sits behind an interface, `wg.Backend` (kernel implementation or in-memory
fake). This lets UI and API work happen on macOS or Windows without root.

---

## 13. Repository layout

```text
drawbridge/                repository root
├── cmd/drawbridge/        main.go: subcommands (serve, tunnel, apply, client, admin, …)
├── internal/
│   ├── version/           build-time version and commit (M0)
│   ├── sdnotify/          systemd readiness notification (M0)
│   ├── webui/             embeds the web app's build from dist/ (M0)
│   ├── config/            bootstrap TOML config
│   ├── store/             SQLite, embedded migrations, repositories
│   ├── model/             domain types + validation
│   ├── ipam/              IPv4/IPv6 allocation, ULA generation
│   ├── keys/              key generation, at-rest encryption
│   ├── wg/                Backend interface; kernel (wgctrl+netlink) and fake impls
│   ├── firewall/          nftables rendering + atomic apply
│   ├── reconcile/         desired → actual engine, drift loop, safe-apply
│   ├── monitor/           peer polling, session state machine, traffic sampling
│   ├── events/            event bus, SSE fan-out, journald sink
│   ├── auth/              users, Argon2id, sessions, TOTP, rate limiting
│   ├── api/               HTTP router, handlers, OpenAPI, SSE, static SPA
│   ├── clientconf/        client .conf rendering
│   ├── adguard/           AdGuard Home REST client (name sync, query log)
│   ├── diag/              diagnostics (shared by UI and `doctor`)
│   └── control/           unix-socket server/client for the CLI
├── web/                   SvelteKit SPA (build output embedded)
├── packaging/
│   ├── systemd/           drawbridge.service, drawbridge-tunnel.service
│   ├── sysusers/          drawbridge.conf (the system user)
│   ├── sysctl/            90-drawbridge.conf
│   ├── modules-load/      drawbridge.conf
│   ├── deb/               postinst, prerm, postrm
│   └── nfpm.yaml
├── scripts/install.sh
├── test/integration/      netns-based tests (build tag: integration)
├── docs/
│   ├── PLAN.md            this document
│   ├── adr/               architecture decision records (D1–D12)
│   ├── MANUAL_CHECKLIST.md  what has actually run on real hardware
│   ├── REQUIREMENTS.md    what the host and network need, and known roadblocks
│   ├── install.md, router-setup.md, troubleshooting.md
├── Makefile               every build, lint, test, and package command
└── .github/workflows/     ci.yml, release.yml
```

---

## 14. Roadmap and milestones

Each milestone ends in a usable, tested state.

### M0: Foundations

- Scaffold the Go module, SvelteKit app, Makefile, linting (golangci-lint, eslint, prettier,
  svelte-check, and a U.S. English spelling check), and CI (lint + unit tests + arm64 build).
  CI runs on GitHub-hosted x86_64 runners and cross-compiles for arm64 (§12).
- A minimal `.deb` (the daemon unit only), so the hello-world build installs on a real host the
  same way releases will.
- Write ADRs for D1–D12, and start `docs/MANUAL_CHECKLIST.md`.
- **Prepare a host and the router** (docs/REQUIREMENTS.md):
  - A Debian-family OS, SSH, and a reserved LAN IPv4 address (a DHCP reservation on the router).
  - A stable IPv6 address.
  - A port forward (IPv4) for UDP 51820, and an inbound IPv6 rule for it if the router offers one.
  - DNS A and AAAA records for the FQDN.
  - Note which DNS resolver, if any, runs on the host and which addresses it listens on (§6.3).
- **Exit:** CI is green, and a hello-world build runs on a real host as a hardened systemd
  service.

### M1: Core engine (headless)

- Domain model, SQLite and migrations, key generation, IPAM for IPv4 and IPv6, and the client
  config renderer.
- The kernel backend (`wgctrl` + `netlink`), the nftables renderer, the reconciler with drift
  loop, and `tunnel up/down`.
- The CLI, through the daemon's control socket: `server show|set` and
  `client list|add|show|pause|resume|delete|config|qr`.
- Packaging: `drawbridge-tunnel.service`, `CAP_NET_ADMIN` for both units, and the forwarding,
  module, and NetworkManager drop-ins.
- Netns integration tests.
- **Exit:**
  - `drawbridge client add phone` shows a terminal QR code, and the phone connects over **both** the
    IPv4 and IPv6 endpoints.
  - Full-tunnel IPv4 and IPv6 browsing works.
  - Pause takes effect immediately. After resume, the client reconnects within about 15
    seconds (WireGuard's retry timer), or at once when its tunnel is switched off and on.
  - The VPN survives a reboot.

### M2: API and authentication

- HTTPS with a self-signed certificate, the setup token, the admin account (Argon2id), sessions,
  CSRF protection, rate limiting, and security headers.
- The REST API and OpenAPI spec, the control socket, and admin and system events.
- The admin allowlist in both layers: the app, and the nftables `input` chain with the detected
  LAN.
- **Exit:** the full client and server-settings lifecycle works through an authenticated API, and
  the API tests pass. *Built; the API tests pass. On real hardware: docs/MANUAL_CHECKLIST.md §5.*

### M3: Web UI MVP → **v0.1**

- The setup wizard, login, dashboard, client list and detail, add/edit/pause/delete, download and
  QR, server settings, DNS settings (public resolvers by default, with a check for a resolver on
  the host), and live status. *Built. Live status polls every 5 seconds while the page is visible; the SSE stream
  (§8, ADR 0009) arrives with monitoring in M4. The System page (diagnostics, backup, TLS) is
  M5; M3's Account page covers the password and sessions. On real hardware:
  docs/MANUAL_CHECKLIST.md §6.*
- The `.deb` carries both systemd units (the tunnel unit arrives in M1).
- **Exit:**
  - Every requested capability (add, remove, pause, basic logs, FQDN, IPs, MTU, DNS, IPv4 and
    IPv6) can be done from a phone or desktop browser.
  - It's installed from the `.deb` on a real host.

### M4: Monitoring and logging

- The session tracker, events and `client_sessions`, traffic sampling with downsampling and
  retention, charts, the log viewer with filters and CSV export, and journald structured logs.
  *Built: the session tracker, its three events, and `client_sessions`; traffic sampling with
  rollup and retention, and the dashboard and client-detail charts (§6.4). Not built yet: the log
  viewer's CSV export, structured journald fields, and the AdGuard Home integration.*
- AdGuard Home integration: client name sync and the per-client DNS log.
- **Exit:**
  - Connect, disconnect, and roam events are correct in simulated tests and on real hardware,
    with real clients *(done — docs/MANUAL_CHECKLIST.md §7)*.
  - Measured DB writes per day stay within a set budget: SD-card-safe by default, and
    configurable for hosts on an SSD (§6.4).

### M5: Hardening and operations → **v1.0**

- Full systemd sandboxing, TOTP 2FA, safe apply with automatic rollback, outdated-config
  tracking, and encrypted backup/restore.
- The diagnostics page and `drawbridge doctor`, the upgrade and migration test matrix, and the docs
  (install, router setup for IPv4 and IPv6, dynamic DNS and DNS records, troubleshooting),
  growing out of `docs/REQUIREMENTS.md`. *`drawbridge doctor` is built (§6.6); the diagnostics
  page is not.*
- **Exit:**
  - The security checklist passes.
  - Upgrading from v0.x keeps all data and keeps the tunnel up.
  - Restoring onto a fresh host works.

### M6: Extras (pick as needed)

- One-time links, client expiry, and timed pause.
- Per-client access policies (internet-only / LAN / custom destinations).
- Routed GUA IPv6 and NDP proxy, and routed IPv4 mode.
- ACME DNS-01 certificates. (A built-in DDNS updater isn't planned: existing clients such as
  ddclient, or a router's built-in one, already cover it.)
- AdGuard Home extras: a per-client ad-blocking switch and client hostnames (DNS rewrites).
- Prometheus metrics, API tokens, and multiple admins.
- Import from `wg-quick` or wg-easy.
- Opt-in flow logging, GeoIP, i18n, and multiple WireGuard interfaces.

### Requested features by milestone

| Requested capability | Milestone |
|---|---|
| Add / remove clients | M1 (CLI), M3 (GUI) |
| Pause clients | M1 (CLI), M3 (GUI); timed pause in M6 |
| Client logging (connections, traffic, admin audit, DNS queries) | Basic in M3, full in M4 (including DNS queries from AdGuard Home), flow logs in M6 |
| Server settings: FQDN, IPs/subnets, port, MTU | M3; safe apply in M5 |
| DNS settings (global and per client) | M3 (AdGuard Home as the default); name sync in M4 |
| IPv4 + IPv6 (endpoint and tunnel) | M1 (NAT66); routed IPv6 in M6 |
| Native install (no Docker) | M3 (`.deb`), polished in M5 |

---

## 15. Risks and mitigations

| Risk | Mitigation |
|---|---|
| An admin locks themselves out by changing settings over the VPN | Safe apply with a 60 s automatic rollback, plus local CLI recovery (`drawbridge admin`, `drawbridge apply`) |
| Enabling IPv6 forwarding breaks the host's own SLAAC (ifupdown hosts) | The installer sets `accept_ra=2` on a kernel-SLAAC uplink (§5.5); `doctor` will check it (M5). `docs/REQUIREMENTS.md` has a manual workaround for setups it doesn't detect |
| The host firewall or Docker drops forwarded traffic | Drawbridge uses its own table and adds only restrictive rules. Diagnostics detect `policy drop` and a rootful Docker's `FORWARD DROP`, with fix hints. Rootless Docker doesn't touch the host firewall |
| NetworkManager tries to manage `wg0` | The installer marks `wg0` as unmanaged in NetworkManager |
| The admin UI is put behind a reverse proxy on the same host | Documented as unsupported. Drawbridge serves the UI only on its own port and ignores forwarded-for headers |
| `nftables.service` restarts flush the Drawbridge table | 30 s drift loop, and `drawbridge-tunnel` is ordered after `nftables.service` |
| The dynamic public IPv4 address changes | The admin's dynamic DNS client updates the A record. Diagnostics compare it with the current address, and the docs explain client reconnect behavior |
| The AAAA record points at a temporary IPv6 address | A static AAAA record for the stable address is recommended, and diagnostics warn about temporary addresses |
| CGNAT or DS-Lite (no inbound IPv4) | Documented: use an IPv6-only endpoint or a relay VPS |
| The router's IPv6 firewall lets inbound traffic reach the host | The admin UI is blocked from non-LAN, non-VPN sources both in the app and in Drawbridge's nftables table |
| No resolver answers on the VPN addresses | New servers default to public resolvers, and the wizard and Settings check the VPN addresses before offering them (§6.3). Planned: diagnostics that repeat the check, with fix hints for AdGuard Home's `bind_hosts` and startup order |
| AdGuard Home is down or its API changes | VPN management doesn't depend on it. Name sync retries, and the integration is tested against the real AdGuard Home before each release |
| SD card wear from logging | Batched writes, downsampling, retention limits, and a write budget test. An SSD is recommended where available |
| No battery-backed clock, so the time is wrong at boot | timesyncd check in diagnostics. TOTP allows ±1 time step |
| Leaked private keys | At-rest encryption, strict permissions, optional "don't store" mode, encrypted backups |
| Lower IPv6 preference with ULA + NAT66 | Documented behavior; routed GUA mode in M6 |
| Scope creep | Milestones with exit criteria. M6 items are optional and independent |

---

## 16. Decisions and open questions

### Decided

| Question | Answer | Effect on the plan |
|---|---|---|
| Stack | Go + Svelte | D1 and D2 confirmed |
| Network defaults | NAT44 and NAT66 | They work with any ISP and router. Routed IPv4 and routed IPv6 are later options (§5.2, §5.3) |
| Dynamic DNS | Not built in | Existing clients (ddclient, or a router's built-in one) cover it. Diagnostics check the A and AAAA records (§5.6) |
| Admin UI exposure | Home network and VPN only, never the internet; plus extra private-range sources the admin adds, such as a Tailscale tailnet | D11: enforced in the app and in nftables (§5.3, §6.5) |
| DNS | Public resolvers by default; a resolver on the host (such as AdGuard Home) at the server's VPN addresses when a check finds one answering | D12: the wizard offers the host's resolver only when it works, and there's optional AdGuard Home name sync and per-client DNS logs (§6.3) |
| Network stack | NetworkManager, systemd-networkd, or ifupdown | The installer marks `wg0` unmanaged for NetworkManager. On ifupdown hosts the installer sets `accept_ra=2` on the uplink (§5.5) |
| Admins | One admin account (default) | Multiple admins stay optional (M6) |
| Client private keys | Stored on the server (default) | Configs can be downloaded again at any time |
| Flow logging | Off (default) | A DNS resolver's query log, such as AdGuard Home's, covers what each client looked up |
| IPv6 endpoint | Supported when the router allows inbound UDP 51820 to the host's stable address | Verified on the reference platform with a real client (docs/MANUAL_CHECKLIST.md §2, 2026-09-28) |

### Still open

1. **Routed IPv6 (M6):** it depends on the router accepting IPv6 static routes and on the prefix
   size the ISP delegates, both of which vary by network. The design picks between a routed /64
   and the NDP proxy (§5.2) based on what the network supports.
2. **Whether the doctor compares the A record with the public IPv4 address (§5.6).** Learning the
   current public address takes a request to a third party (a "what's my IP" service), which is
   the only outside lookup the daemon would make, from a project that otherwise stays local. What
   `drawbridge doctor` does instead: warn when the endpoint name resolves to an address a client
   on the internet can't reach (private, CGNAT, or link-local), and when its AAAA record is a
   temporary address. That catches a stale record only when it points somewhere unroutable, not
   when it points at an old public address. The choices are to leave it, or to add the lookup as
   an opt-in setting that names the service.
