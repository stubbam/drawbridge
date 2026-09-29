# ADR 0001: Go for the backend

- **Status:** Accepted (2026-09-26)
- **Plan reference:** docs/PLAN.md §3, D1

## Context

The daemon runs on small Linux hosts (arm64 or amd64; the reference platform is a Raspberry Pi 5)
and must talk to the kernel over netlink: WireGuard's generic netlink family, rtnetlink for
addresses and routes, and nftables. It should install without a language runtime and use little
memory next to whatever else the host runs.

## Decision

Write the backend in Go. Build it with `CGO_ENABLED=0`, so it's one static binary that cross-
compiles from any machine to linux/arm64 and linux/amd64. `go.mod` sets `go 1.26.0`, the oldest
supported release, as the minimum; CI builds with the newest supported Go (1.27.x at the time of
writing).

## Consequences

- The WireGuard project's own `wgctrl` library and `vishvananda/netlink` cover the kernel work
  without shelling out.
- Every dependency must be pure Go (no CGO), which also rules out the common SQLite driver (see ADR
  0003).
- `go.mod` has no `toolchain` line: golangci-lint treats that line as the target version and refuses
  to run when it was built with an older Go (seen on 2026-09-26 with golangci-lint v2.14.0, built
  with Go 1.26.8).
