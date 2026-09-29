# Drawbridge

A self-hosted web manager for a WireGuard VPN server, installed natively (no Docker) on
Debian-family Linux with systemd, for arm64 and amd64. You can add, remove, and pause clients,
view their connections and traffic, manage server settings (endpoint, addresses, MTU) and DNS,
and use full IPv4 and IPv6 support. It's tested on a Raspberry Pi 5 running Debian 13.

The WireGuard tunnel and its clients are managed from a web UI at `https://<host>:51821` that
only the home network and the VPN can reach, from the command line
(`sudo drawbridge client add phone --qr`), or through the same authenticated API. See
[docs/PLAN.md](docs/PLAN.md) for the architecture, feature spec, and roadmap.

**Before you install**, read [docs/REQUIREMENTS.md](docs/REQUIREMENTS.md): it lists what
Drawbridge needs from the host and your network, and the setups where it needs a workaround
(for example, a host without a local DNS resolver, or one that configures IPv6 with ifupdown).

## Building

You need Go 1.26 or later, Node.js 24 (npm 11), and `make`.

```bash
make check              # lint, spelling, and tests
make test-integration   # kernel WireGuard tests (root, IPv6, the wireguard module)
make test-e2e           # the web app in a browser, against a fake tunnel
make deb                # dist/drawbridge_<version>_arm64.deb and _amd64.deb
```

`make help` lists every target. To install a build, see
[docs/MANUAL_CHECKLIST.md](docs/MANUAL_CHECKLIST.md) §1.

## License

Drawbridge is licensed under the [Apache License 2.0](LICENSE).

"WireGuard" is a registered trademark of Jason A. Donenfeld. Drawbridge isn't affiliated with or
endorsed by the WireGuard project.
