# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this
repository.

## What this is

Drawbridge is a self-hosted web manager for a WireGuard VPN server. It installs natively (a
`.deb`, no Docker) on Debian-family Linux with systemd, for arm64 and amd64, and it manages:

- Clients: add, remove, pause, and view their logs.
- Server settings: FQDN, addresses, and MTU.
- DNS.

It supports both IPv4 and IPv6. wg-easy is the feature reference; none of its code is used.

**Status: M0–M3 are built, the session tracker and traffic history from M4 shipped ahead of
the rest of it (2026-09-26–28), and `drawbridge doctor` from M5 followed (2026-09-29). Nearly
every check in docs/MANUAL_CHECKLIST.md has passed on the reference platform (a Raspberry Pi 5
running Debian 13); what's left there needs a laptop or a second Tailscale device.
docs/REQUIREMENTS.md lists what a host and network need, and the known roadblocks on other
setups.** What exists:

- The tunnel: `drawbridge tunnel up|down`, run by `drawbridge-tunnel.service`.
- The daemon's reconciler and drift loop, and its connection tracker
  (`internal/service/conntrack.go`, docs/PLAN.md §6.4): `connected`/`disconnected`/`roamed`
  events and a `client_sessions` table, built ahead of the rest of M4.
- The traffic-history sampler and rollup/retention job (`internal/service/traffic.go`,
  `internal/store/traffic.go`, docs/PLAN.md §6.4), storing and pruning per-client RX/TX history
  server-side; the API routes that read it and a client's session history
  (`GET /api/clients/{id}/traffic`, `GET /api/traffic`, `GET /api/clients/{id}/sessions`); and
  the uPlot charts and session-history list that use them, on the dashboard and a client's
  detail page. Also ahead of the rest of M4 (the log viewer's CSV export, structured journald
  fields, and AdGuard Home sync aren't built yet).
- The CLI, which talks to the daemon over the control socket: `server show|set`,
  `client list|add|show|pause|resume|rename|delete|config|qr`, `events`, `doctor`, and
  `admin setup-token|create|reset-password`.
- `drawbridge doctor`, the first slice of M5 (2026-09-29): 13 host and network checks, each with
  a fix hint, run by the daemon (`internal/diag`) and printed by the CLI. Exit status 1 when any
  check fails, 0 otherwise. The web diagnostics page isn't built yet, and neither is the
  comparison of the endpoint's A record with the current public IPv4 address (docs/PLAN.md §16).
- The authenticated JSON API over HTTPS on port 51821 (`internal/api/openapi.json`): first-run
  setup, sessions, server settings, clients, and the event log, reachable only from the LAN and
  the VPN.
- The web UI: setup (account, endpoint, and a DNS step that checks whether the host answers DNS
  on the VPN addresses), login, dashboard, clients (QR codes and downloads), settings (DNS
  included), logs, and account.
- Kernel integration tests, and browser end-to-end tests.

`docs/PLAN.md` is the specification. It covers:

- Architecture and the networking design.
- The feature spec, data model, and API.
- The security design.
- Milestones M0–M6, each with exit criteria.
- The project's decisions (§3, §16).

Read it before writing code here. It describes the target, not what exists. Don't assume
something is built because the plan describes it.

When a decision changes, update `docs/PLAN.md` (§3 and §16) in the same PR. When a command,
convention, or invariant changes, update this file too.

## Usually no real host here

**Check before assuming, in both directions.** Development usually happens away from a real
host, in a cloud container or on a laptop. Anything that needs a real network can't be exercised
there: the router, the public endpoint, a DNS resolver on the host, and real clients.

The cloud container was checked on 2026-09-26 (see "Verified facts"):

- It has root, network namespaces, nftables, and TUN devices.
- It has **no kernel WireGuard**, and its CPU is x86_64.

CI runs on GitHub-hosted `ubuntu-latest` (x86_64) and cross-compiles for arm64. The kernel
WireGuard integration tests (`test/integration`, the "Integration" job) run on a GitHub-hosted
`ubuntu-24.04` VM, which has root through sudo and Ubuntu's kernel. The job loads the kernel
modules first, and the tests pass there (first on 2026-09-26). The tests work only inside
network namespaces they create. `test/integration/preflight.sh` checks every requirement at once
before the tests run.

Keep the Integration job on GitHub-hosted runners. A self-hosted runner (docs/MANUAL_CHECKLIST.md
§4 lists what one needs) must never be attached to this repo while it's public: it would run code
from forks' pull requests.

If a session does have kernel WireGuard or arm64, use it: an observed answer beats a documented
one. Say plainly when a claim is documented but not yet exercised, rather than observed on real
hardware.

`docs/MANUAL_CHECKLIST.md` is the authoritative record of what has actually run on real
hardware (the reference platform in docs/PLAN.md §2). Its
`[UNVERIFIED]`, `[VERIFIED …]`, and `[NEXT]` tags matter; keep them current when you touch the code
a step describes.

## Conventions

- **U.S. English only.** This covers code, comments, docs, UI strings, log messages, and commit
  messages: "color," "canceled," and "behavior," never the British forms. `make spell` (misspell
  with `-locale US`) enforces it on every tracked file, and CI runs it, so don't quote a British
  spelling even as an example. "WireGuard," "AdGuard Home," and "Raspberry Pi" are product
  names and are correct as-is.
- **The repo is public, so nothing in it is personal.** Write for anyone running Drawbridge:
  "the admin" (whoever runs it), "the maintainer" (project decisions), "the host" (the machine
  it runs on). No home-lab specifics: no router models, hostnames, FQDNs, real LAN subnets,
  Tailscale or public addresses, or personal names in fixtures. Use documentation ranges
  (`192.0.2.0/24` and friends, `2001:db8::/32`), generic private ones (`192.168.4.0/22`,
  `100.64.10.0/24`), and `example.com`. "Raspberry Pi 5" and "Debian 13" appear only as the
  tested reference platform. When a feature assumes something about the host or network, say so
  in docs/REQUIREMENTS.md.
- **Version numbers are prefixed with a lowercase `v`** in prose, git tags, release names, and
  `CHANGELOG.md` headings: `v1.0.0`, never `1.0.0`. Three places use a bare string because their
  own rules require it:
  - The Debian package version, which must start with a digit (`drawbridge_1.0.0_arm64.deb`).
  - The version constant set at build time.
  - The OpenAPI spec's `info.version`.
- **Licensed under Apache-2.0** (`LICENSE`, also shipped as `/usr/share/doc/drawbridge/copyright`
  in the `.deb`). No copyright or license headers in individual files.
- **Names:** the product is "Drawbridge" in prose. Everything else uses `drawbridge`:
  - The repository, Go module, and binary.
  - The systemd units (`drawbridge.service`, `drawbridge-tunnel.service`) and the system user.
  - The paths (`/etc/drawbridge`, `/var/lib/drawbridge`, `/run/drawbridge`).
  - The nftables table (`inet drawbridge`) and the journald field (`DRAWBRIDGE_CLIENT=`).

  "WireGuard" is a registered trademark. It appears only descriptively ("a web manager for
  WireGuard"), never in the product name.
- **Dependencies are deliberate.** The plan names the core libraries: `wgctrl`,
  `vishvananda/netlink`, and `modernc.org/sqlite` in §3, Tailwind, uPlot, and `qrcode` in §9, and
  Playwright in §12.
  Anything else needs an explicit argument in its PR, and the default answer is no. Everything is
  pure Go (`CGO_ENABLED=0`), so the arm64 build stays a plain cross-compile.
- **When adding behavior, add a test.** This project is most exposed to quiet failures: a change
  that reports success while the kernel disagrees. Examples:
  - A paused client still listed in `wg show`.
  - A rendered nftables rule that was never applied.
  - IPv6 that silently doesn't forward.

  A test that checks only the DB or the rendered text isn't a test of the effect.
- **Formatting:** `gofmt` and `goimports` for Go, and Prettier for the web app. Markdown wraps at
  100 columns.

## Commands

Everything goes through the Makefile, and `make help` lists the targets. The first lint, spelling,
or package run installs the pinned tools (golangci-lint, nfpm, misspell) into `./bin`.

```bash
make check      # everything CI checks: Go and web linting, spelling, and all tests
make test-go    # Go tests, with the race detector
make test-web   # the web app's unit tests (Vitest)
make lint       # golangci-lint; Prettier, ESLint, and svelte-check; misspell
make spell      # the U.S. English check on every tracked file
make fmt        # format Go (gofmt, goimports) and the web app (Prettier)
make deb        # web build, arm64 and amd64 binaries, and both .deb files in dist/
make test-integration   # kernel WireGuard end to end (root, IPv6, the wireguard module)
make test-e2e   # the web app in Chromium against `serve --backend fake` (Playwright)
```

The integration tests run the built binary: `tunnel up` and `serve` in a server namespace, the
CLI against its control socket, and a client namespace that configures WireGuard from
`client config` and checks NAT44 and NAT66 through an "internet" namespace. Without
`DRAWBRIDGE_INTEGRATION=1` they skip, so they never fail silently on a machine that can't run
them; the Makefile target and CI set it.

One Go test, or one web test file:

```bash
go test ./internal/api -run TestAppFallsBackToShell -v
cd web && npx vitest run src/lib/version.spec.ts
```

The end-to-end tests start `drawbridge serve --backend fake` on port 51899 (not 51821, so a real
daemon on the same machine is left alone) with a fresh state directory, and use the CLI for the
setup token. Where Playwright can't download its own Chromium, point it at one:
`PLAYWRIGHT_CHROMIUM_PATH=/opt/pw-browsers/chromium-1194/chrome-linux/chrome make test-e2e` in the
cloud container.

Run it locally. Without `make web` first, the embedded UI isn't there and `/` says so.
`--backend fake` keeps the tunnel and the firewall in memory, so the UI works without root or
WireGuard:

```bash
make web && go run ./cmd/drawbridge serve --backend fake --db /tmp/db.sqlite \
  --secret-key /tmp/secret.key --control /tmp/control.sock \
  --listen 127.0.0.1:51821   # https://127.0.0.1:51821
cd web && npm run dev          # Vite on port 5173, proxying /api to https://127.0.0.1:51821
go run ./cmd/drawbridge admin setup-token --control /tmp/control.sock
```

`serve` and `tunnel` need a database, a secret key, and `CAP_NET_ADMIN`. To try them without
installing the package, point them at scratch files: `--db /tmp/db.sqlite --secret-key
/tmp/secret.key` (32 random bytes, mode 0600), and `--control /tmp/control.sock` for `serve` and
every CLI command.
`serve` keeps its self-signed certificate in `tls/` next to the database (`--tls-dir`).
Without `CAP_NET_ADMIN`, or without kernel WireGuard, they report why and keep the database
consistent.

`go build ./cmd/...` at the repository root writes a `drawbridge` binary there, because the
pattern matches one main package. Use `make build` (which writes `dist/`) or `-o`, and never
commit the binary.

**Use npm 11 (Node 24).** npm 10.9.7, the npm bundled with Node 22, fails `npm install` in `web/`
with "Cannot read properties of null (reading 'edgesOut')". CI uses Node 24. On Node 22, pass
`NPM="npx -y npm@11"` to make.

## Invariants (condensed; see docs/PLAN.md for the full design)

These are the rules most likely to get silently broken.

- **The database is the source of truth, and the kernel state is derived from it.**
  - Every change goes: validate → DB transaction → reconcile → event (§4.3).
  - Nothing outside the reconciler touches kernel state (`wgctrl`, netlink, `nft`).
  - Only `drawbridge tunnel up` creates the interface. The daemon's reconcile (`Sync`) leaves
    a missing interface alone, so it never restarts a tunnel the admin stopped (ADR 0008).
  - The CLI changes state only through the daemon's control socket. It never opens the
    database, which belongs to the `drawbridge` user.
  - The reconciler is idempotent and serialized by a lock. It also runs every 30 s to correct
    drift.
- **Live changes never disrupt other clients.** Peers are diffed and updated in place
  (`ReplaceAllowedIPs`). The interface is never recreated to apply a change.
- **No root at runtime.** Both units run as the `drawbridge` user with only `CAP_NET_ADMIN`
  (§4.2).
  - Anything that needs root (sysctls, the NetworkManager drop-in, the system user) belongs in
    the package's `postinst`, not in the daemon.
  - The only external command is `nft -f`, called with an argv list. Never use a shell.
- **No arbitrary command hooks** (`PostUp`/`PostDown` or anything like them). Anything a hook
  would do is a typed, validated setting instead (§10).
- **Free text never reaches a rendered file.**
  - Client names and notes stay out of nftables rulesets and WireGuard configs.
  - Keys, CIDRs, ports, and MTUs are parsed and validated (`net/netip` and range checks) before
    they're rendered.
- **The VPN doesn't depend on the UI.** `drawbridge-tunnel.service` brings the tunnel up at boot
  from the DB on its own. If `drawbridge.service` stops or crashes, the VPN keeps running.
- **Drawbridge owns only `table inet drawbridge`.** Never `flush ruleset` or touch another table.
  Only restrictive rules and NAT go in it, because an `accept` in one table can't override a
  `drop` in another (§5.3).
- **The admin UI is reachable from the home network and the VPN only, never the internet**
  (D11). Two layers enforce this, and both use `lan.Allowlist`:
  - The app's allowlist (`internal/api`, 403): the detected LAN prefixes, loopback, link-local
    addresses, and the VPN subnets. It doesn't trust "private ranges," because LAN devices reach
    the host over the global IPv6 prefix.
  - The nftables `input` chain, which drops TCP 51821 from everyone else. It's part of
    Drawbridge's table, so `tunnel down` removes it too.

  The admin's extra sources are the `admin_allowed` setting (`server set --admin-allow`, for
  Tailscale, say), which both layers read. Each prefix is validated to lie in a private range,
  100.64.0.0/10, or fc00::/7, so no setting can admit a public source.

  Only the TCP connection's address counts. Never trust `X-Forwarded-For` or similar headers.
  Never serve the UI through a reverse proxy on the same host: its requests arrive from the host
  itself and would pass both layers (§6.5).
- **Every change is an event.** Service methods that change something call `record` with the
  actor from the context (the web user, the CLI's account from the socket's peer credentials,
  or the daemon). A new change needs its event, and a test that checks it.
- **The API and its OpenAPI document agree.** Routes live only in the `routes` table in
  `internal/api/api.go`, and a test fails when it and `openapi.json` differ. Everything except
  the setup and login endpoints needs a session; every change needs the `X-Drawbridge` header.
- **The web app talks to the server only through `web/src/lib/api.ts`**, which adds the CSRF
  header and sends a lapsed session to the login. Internal links and `goto` go through
  SvelteKit's `resolve()` (ESLint enforces it); route IDs with parameters include the group:
  `resolve('/(app)/clients/[id]', { id })`.
- **The CSP stays strict.** `script-src` allows `'self'` and the app shell's bootstrap script by
  hash, nothing else. Don't add `'unsafe-inline'` or `'unsafe-eval'` to it; change the UI instead.
- **The two address families are kept in lockstep.** Every networking feature is built and
  tested for both IPv4 and IPv6: addressing, NAT, firewall rules, allowlists, DNS, diagnostics,
  and tests. The quiet failure here is a feature that works on IPv4 and does nothing on IPv6.
- **AdGuard Home is optional at runtime.** VPN management keeps working when it's down, and name
  sync retries in the background (§6.3).
- **Don't default to common ports.** Drawbridge uses only UDP 51820 (WireGuard) and TCP 51821
  (admin UI), because hosts often run other services on the usual ones: a DNS resolver on 53,
  web servers and reverse proxies on 80 and 443, and admin UIs like AdGuard Home's on 3000.
  Never default anything to one of those. That includes dev servers and test fixtures that
  might run on a real host. The planned AdGuard Home integration (M4) reaches its API at
  `http://127.0.0.1:3000/control` by default; that address is a setting (`adguard.base_url`,
  §7), not hard-coded.
- **Protect the SD card.** Monitoring samples are buffered in memory and flushed once per raw
  interval (a minute by default) in one transaction. Nothing writes to the DB on every poll
  (§6.4).
- **Secrets stay secret.** Private keys, PSKs, and the setup token are encrypted at rest (the
  `*_enc` columns) and never logged, except the setup token, which the journal shows until the
  admin account exists (§6.5). Session tokens are stored only as SHA-256 hashes. Views never
  carry keys, and a 500 response never carries the internal error; the journal does (§7, §10).

## Verified facts, worth not re-deriving

All of these were established on 2026-09-26 unless noted.

**The reference platform's network stack.** NetworkManager manages the network there, and
systemd-networkd and ifupdown's `networking` are inactive. That's why its IPv6 kept working
without the `accept_ra=2` fix (NetworkManager sets `accept_ra` to 0 on its uplink, so the installer
leaves it alone; docs/PLAN.md §5.5), and why `wg0` must still be marked unmanaged in
NetworkManager. Don't generalize from it: on an ifupdown host, turning on IPv6 forwarding removes
the host's own SLAAC address unless `accept_ra` is 2 (docs/REQUIREMENTS.md). The integration tests
prove that in namespaces; a real ifupdown host hasn't run it.

**AdGuard Home's API.** This was checked against `openapi/openapi.yaml` on its `master` branch,
not yet against a running install:

- Everything is under `/control`, with HTTP basic auth.
- Persistent clients use `/control/clients/add`, `/update`, and `/delete`. A client's `ids`
  accept IPs, CIDRs, MACs, or ClientIDs.
- `/control/querylog?search=` filters by domain or client IP.
- DNS rewrites are under `/control/rewrite/*`.

**The cloud container** (x86_64, kernel 6.18, Go 1.24.7 and Node 22 with npm 10.9.7
preinstalled, running as root):

- Kernel WireGuard is **not** available: `ip link add … type wireguard` fails with "Unknown
  device type."
- Network namespaces, TUN devices, and nftables 1.0.9 work.
- `nft` and `ip` aren't preinstalled. Install them with
  `apt-get update && apt-get install -y nftables iproute2`.
- The `go` command downloads Go 1.26.0 on its own, because that's `go.mod`'s minimum.
- It has **no IPv6** (`/proc/sys/net/ipv6` doesn't exist, and IPv6 addresses can't be
  added) and no `dummy` module. So the integration tests stop at their first IPv6 address
  here; they need CI.
- The M0 `.deb` installs, removes, and purges cleanly here. There's no running systemd, so
  `postinst` skips the service steps. `systemd-sysusers` created the `drawbridge` user, the
  binary served the UI as that user, and headless Chromium rendered the page with the live
  version.
- The M1 `.deb` installs too. `postinst` created `/etc/drawbridge/secret.key` (32 bytes,
  `root:drawbridge` 0640) and turned on IPv4 forwarding, and it warned and carried on when IPv6
  forwarding failed. Run as the `drawbridge` user with only `CAP_NET_ADMIN` (`setpriv
  --ambient-caps +net_admin`, as systemd will):
  - The daemon's effective capabilities were exactly `CAP_NET_ADMIN` (`CapEff` 0x1000).
  - Its control socket was `drawbridge:drawbridge` 0660, and the CLI worked through it as
    root, names with apostrophes included.
  - `tunnel up` failed with the "is the wireguard module loaded" hint, as it should without
    kernel WireGuard.

**Build tooling:**

- The latest Go is 1.27.1. `go.mod` says `go 1.26.0` with no `toolchain` line, because
  golangci-lint v2.14.0 (built with Go 1.26.8) refuses to run when the toolchain line targets a
  newer Go than it was built with.
- The sv CLI (0.17.1) puts SvelteKit's configuration inside `web/vite.config.ts`. There's no
  `svelte.config.js`.
- nfpm expands `${ARCH}` and `${VERSION}` in `arch` and `version`, but not in `contents` paths.
  It turns version `0.0.0-dev` into the Debian version `0.0.0~dev`.
- `systemd-analyze verify` (systemd 255) accepts both units. `systemd-analyze security
  --offline=true` rated the M0 unit 1.5 ("OK"); with `CAP_NET_ADMIN` (M1), both units rate 1.8
  ("OK").
- Go 1.27's gofmt aligns key-value tables differently from 1.26's (a map literal whose keys vary
  a lot in length), and CI's golangci-lint follows Go 1.27. Check with
  `"$(GOTOOLCHAIN=go1.27.1 go env GOROOT)/bin/gofmt" -l cmd internal test`, or write such tables
  as slices of structs, which have no alignment to disagree about.
- Go's `http.ServeMux` answers a path containing `..` with a 307 redirect to the cleaned path,
  and it refuses to register `GET /` alongside a method-less `/api/` pattern (it panics about
  the conflict).
- nftables 1.0.9 keeps a table's `comment` and reports it in `nft -j list table`. That's how
  the drift check reads the ruleset's revision. A transaction of `table …; delete table …;
  table … { … }` replaces the table atomically, even when it doesn't exist yet.
- The terminal QR code (rsc.io/qr, which always uses mask 0) decodes byte for byte back to the
  rendered config with zbarimg.
- `go test ./...` also finds a Go package inside `web/node_modules` (from `flatted`). The
  Makefile lists Drawbridge's own packages instead, and golangci-lint excludes `web/`.
- `nft_rt` and `nft_exthdr` (the MSS clamp's `rt mtu` and `tcp option maxseg`) are compiled
  into `nf_tables.ko`, as are meta, payload, and the set types (`nf_tables-objs` in the
  kernel's `net/netfilter/Makefile`). They never appear in `/sys/module` or to `modprobe`. The
  ruleset's only separate nftables modules are `nft_chain_nat` and `nft_masq`.
- GitHub's `ubuntu-24.04` runner (2026-09-26): kernel 6.17.0-1022-azure, `ip` and `nft`
  preinstalled, and root through passwordless sudo. The modules load without
  `linux-modules-extra`, the preflight's probes all pass, and the kernel tests pass there
  (about 40 seconds).
- Current GitHub Actions majors: `actions/checkout@v7`, `actions/setup-go@v7`,
  `actions/setup-node@v7`, `actions/upload-artifact@v7`, `actions/download-artifact@v8`, and
  `golangci/golangci-lint-action@v9`. All run on Node 24.

**The reference platform as a dev host** (a session can run there: a Raspberry Pi 5 on Debian
13, aarch64, a 6.18 Raspberry Pi kernel, passwordless sudo, real IPv6, kernel WireGuard):

- `make test-integration` passes there (about 40 seconds), and the M1–M3 `.deb` installs, runs,
  upgrades in place, and keeps the tunnel up.
- `make test-go` fails on every package with "ThreadSanitizer: unsupported VMA range": the
  kernel's 47-bit address space is one bit short of what the race detector supports. Run
  `go test ./cmd/... ./internal/...` without `-race` there; CI runs the race detector.
- `make test-e2e` can't start Playwright's Chromium: the host is headless and lacks its libraries
  (`libatk`, `libgbm`, and others). Run the tests in Playwright's container instead, which is
  rootless and leaves the host alone: `docker run --rm --ipc=host -v "$PWD":/work -w /work/web
  -e DRAWBRIDGE_BIN=/work/dist/drawbridge mcr.microsoft.com/playwright:v1.63.0-noble npx
  playwright test` (after `make deb` or `make build`; the image's version matches
  `@playwright/test`).
- Where Go and Node were installed by hand (`/usr/local/go/bin`, nvm's `bin`), the tools' PATH
  can miss them until the shell is restarted.

**The plan's example ruleset** (§5.3) passes `nft -c` and loads in a network namespace with
nftables 1.0.9.

**M2 in the cloud container** (no WireGuard needed), with the daemon in a network namespace whose
uplink (the interface with the default route) is on 192.0.2.0/24, and a second interface on
198.51.100.0/24:

- LAN detection found exactly 192.0.2.0/24.
- With only the app's allowlist, `/healthz` answered 200 from the LAN and 403 from the other
  network. With the rendered ruleset loaded as well, the other network's connection timed out
  (dropped) and the LAN still got 200.
- Headless Chromium loaded the real SvelteKit build under the CSP with no violations, and an
  injected inline script didn't run.
- The M2 `.deb`'s daemon, run as the `drawbridge` user with only `CAP_NET_ADMIN`, created its
  certificate in `/var/lib/drawbridge/tls` (key 0600, directory 0700), logged the fingerprint
  and the setup token, served HTTPS on `:51821` to loopback and to the container's LAN address,
  and `drawbridge admin setup-token` showed both.
- Go's `tls.LoadX509KeyPair` fills in `Certificate.Leaf` (Go 1.23 and later).

**Still unverified** (plan §16): routed IPv6 (M6) depends on the router accepting IPv6 static
routes and on the ISP's delegated prefix size, which vary by network.

**Verified on the reference platform** (docs/MANUAL_CHECKLIST.md): an IPv6 endpoint works when
the router allows inbound UDP 51820 to the host's stable address (with a real client,
2026-09-28).

**`drawbridge doctor`** (2026-09-29, the reference platform):

- Run as `drawbridge.service`'s sandbox (a transient `systemd-run` unit with the unit file's
  hardening properties, `--backend fake`, and a scratch state directory), every read worked:
  `/proc/sys`, `/sys/module`, `/run/systemd/timesync/synchronized`, netlink, `statfs`, the
  system resolver, and `nft -j list ruleset` with only `CAP_NET_ADMIN`. A binary under `/home`
  can't run in such a unit (`ProtectHome=yes`), so copy it somewhere under `/usr/local` first.
- The integration test (`test/integration/doctor_test.go`) drives the real kernel and real
  `nft -j` output: `accept_ra` 1 fails and 2 passes, forwarding off fails for each family, and
  another table's `policy drop` forward chain warns until a rule accepts `wg0`.
- `fwd` is an nftables keyword, so a chain can't be named that.

## Where things live

- `docs/PLAN.md` is the specification. Read it first. §13 has the planned repository layout.
- `docs/adr/` holds one decision record per decision in the plan's §3 (D1–D12).
- `docs/MANUAL_CHECKLIST.md` records what has actually run on real hardware.
- `docs/REQUIREMENTS.md` lists what a host and network need, and the known roadblocks.
- `cmd/drawbridge/` is the binary: `serve` (serve.go), `tunnel`, `server`, `client`,
  `events` and `admin` (admin_cmd.go), `doctor` (doctor_cmd.go), `version`, and `help`.
- The core engine, in `internal/`:
  - `model/` holds the domain types and validation, and `store/` is SQLite with embedded
    migrations.
  - `keys/` generates keys and seals secrets; `ipam/` allocates addresses.
  - `wg/` is the WireGuard backend, a kernel implementation plus a fake for tests.
    `firewall/` renders and applies the nftables table; its golden file is in `testdata/`.
  - `reconcile/` applies the database to the kernel.
  - `service/` is the management logic: clients and settings (service.go), setup, logins, and
    sessions (auth.go), and the event log (events.go). `control/` is the Unix-socket API and
    its client, and `views/` holds the JSON shapes both APIs share.
  - `clientconf/` renders client configs and terminal QR codes.
  - `diag/` holds the doctor's checks. The daemon runs them, not the CLI, because `nft -j list
    ruleset` needs `CAP_NET_ADMIN`. Every read goes through `diag.Host` (an `fs.FS` rooted at
    `/`, plus hooks for the routing table, the resolver, `statfs`, and nft), so a check is
    tested by handing it a `fstest.MapFS`. A check that can't read what it needs is a skip,
    never a fail. `service.Diagnose` feeds it the database's and the tunnel's state.
  - `auth/` has password hashing, tokens, and the login rate limiter; `lan/` detects the LAN
    and builds the admin allowlist; `tlscert/` makes the self-signed certificate.
- `internal/api/` serves the JSON API (documented in `openapi.json`, with the security
  middleware in middleware.go) and the embedded web app. `internal/webui/` embeds the
  build that `make web` copies into `internal/webui/dist/`. `internal/sdnotify/` reports
  readiness to systemd, and `internal/version/` holds the build-time version.
- `test/integration/` holds the kernel tests (build tag `integration`).
- `web/` is the SvelteKit app. `src/routes/(auth)/` holds setup and login, and
  `src/routes/(app)/` everything behind a session (its `+layout.ts` is the guard). `src/lib/`
  has the API client (`api.ts`), formatting (`format.ts`), and components. `e2e/` holds the
  Playwright tests.
- `packaging/` holds:
  - Both systemd units.
  - The sysusers.d, sysctl.d, modules-load.d, and NetworkManager drop-ins.
  - `libexec/accept-ra`, which `postinst` runs (installed at `/usr/lib/drawbridge/accept-ra`).
  - The maintainer scripts and `nfpm.yaml`.
- `.github/workflows/ci.yml` is CI. Its golangci-lint version must match the Makefile's.
- `README.md` is the short project description.
