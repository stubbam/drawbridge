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

## Implementation notes (2026-10-03)

- The server has no write timeout. Each message the stream writes has a 10 second deadline instead
  (`http.ResponseController`), so a reader that has stopped is dropped.
- A stream checks its session on every status, so a page that stays open stays logged in, and one
  whose session ends is closed. The daemon closes the streams when it stops, because the graceful
  shutdown would otherwise wait on them until it timed out.
- The daemon speaks HTTP/1.1 only, so each open stream holds one of a browser's six connections
  to the host. Tabs that are hidden close theirs. Enabling HTTP/2 would remove the limit.
- The pages fall back to polling when the stream isn't open, so it's an improvement and nothing
  depends on it.
