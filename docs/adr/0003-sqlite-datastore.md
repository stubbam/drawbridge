# ADR 0003: SQLite as the datastore

- **Status:** Accepted (2026-09-26)
- **Plan reference:** docs/PLAN.md §3, D3

## Context

Drawbridge stores its settings, clients, events, and traffic history on the host. The data is
small, there's one writer, and the admin needs simple backups. A database server would add a service
to run and secure.

## Decision

Use SQLite through `modernc.org/sqlite`, a pure-Go driver, in WAL mode, with the file at
`/var/lib/drawbridge/drawbridge.db`. Migrations are embedded in the binary and run at startup after
an automatic snapshot.

## Consequences

- No CGO and no database server.
- Backups are a consistent `VACUUM INTO` snapshot of one file.
- Writes must stay batched to protect the SD card (docs/PLAN.md §6.4).
