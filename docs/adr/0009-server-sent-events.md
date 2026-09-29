# ADR 0009: Server-Sent Events for live updates

- **Status:** Accepted (2026-09-26)
- **Plan reference:** docs/PLAN.md §3, D9

## Context

The dashboard shows live client status and events. The data flows one way, from the server to the
browser.

## Decision

Push updates with Server-Sent Events at `/api/stream`, not WebSockets.

## Consequences

- It's plain HTTP, so it passes the same authentication, allowlist, and headers as the rest of the
  API.
- Browsers reconnect automatically.
- The HTTP server must not set a write timeout that would cut long-lived streams.
