# ADR 0004: The database is the source of truth

- **Status:** Accepted (2026-09-26)
- **Plan reference:** docs/PLAN.md §3, D4

## Context

WireGuard and nftables state live in the kernel and survive a daemon restart, but they can also
drift: someone runs `wg set` by hand, or `nftables.service` restarts and flushes every table.

## Decision

Treat the database as the only source of truth and derive kernel state from it with an idempotent
reconciler (docs/PLAN.md §4.3). Every change goes: validate, write the database in a transaction,
reconcile, record an event. The reconciler also runs at startup, from `drawbridge tunnel up`, and
every 30 seconds to correct drift.

## Consequences

- Nothing outside the reconciler touches kernel state.
- Restarts and crashes are safe, because reconciling again reaches the same state.
- Tests must check the kernel's state, not only the database's (CLAUDE.md, "When adding behavior,
  add a test").
