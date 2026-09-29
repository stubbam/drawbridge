-- Schema version 4: one row per client connection, so the API can show bytes transferred
-- during a client's current connection separately from its all-time totals (docs/PLAN.md
-- §6.4). This is the session-tracking slice of M4, built ahead of the rest of it.

CREATE TABLE client_sessions (
	id          TEXT PRIMARY KEY,
	client_id   TEXT NOT NULL REFERENCES clients (id) ON DELETE CASCADE,
	started_at  TEXT NOT NULL,
	ended_at    TEXT,
	endpoint    TEXT NOT NULL DEFAULT '',
	-- The peer's cumulative counters when the session started, so rx_bytes/tx_bytes (this
	-- session's own bytes) survive a daemon restart without any in-memory state.
	baseline_rx INTEGER NOT NULL,
	baseline_tx INTEGER NOT NULL,
	rx_bytes    INTEGER NOT NULL DEFAULT 0,
	tx_bytes    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX client_sessions_client ON client_sessions (client_id, started_at);

-- At most one open session per client at a time.
CREATE UNIQUE INDEX client_sessions_one_open ON client_sessions (client_id) WHERE ended_at IS NULL;
