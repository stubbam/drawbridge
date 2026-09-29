-- Schema version 2: the admin account, its sessions, the first-run setup token, and the
-- event log (docs/PLAN.md §6.5 and §7).

CREATE TABLE users (
	id                  TEXT PRIMARY KEY,
	username            TEXT NOT NULL UNIQUE COLLATE NOCASE,
	password_hash       TEXT NOT NULL, -- Argon2id, in the PHC string format
	created_at          TEXT NOT NULL,
	password_changed_at TEXT NOT NULL,
	last_login_at       TEXT
);

-- A session's cookie holds a random token; only its SHA-256 hash is stored, so a copy of
-- the database can't be used to log in. id is a separate random value that the API can
-- show, so a session can be revoked without revealing anything about its token.
CREATE TABLE auth_sessions (
	id           TEXT PRIMARY KEY,
	token_hash   BLOB NOT NULL UNIQUE,
	user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	created_at   TEXT NOT NULL,
	last_seen_at TEXT NOT NULL,
	expires_at   TEXT NOT NULL,
	ip           TEXT NOT NULL,
	user_agent   TEXT NOT NULL
);

CREATE INDEX auth_sessions_user ON auth_sessions (user_id);

-- The one-time token that first-run setup needs, until the admin account exists. It's
-- sealed rather than hashed, because `drawbridge admin setup-token` shows it again.
CREATE TABLE setup_token (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	token_enc  BLOB NOT NULL,
	created_at TEXT NOT NULL
);

CREATE TABLE events (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	ts          TEXT NOT NULL,
	kind        TEXT NOT NULL,          -- for example client.added or auth.login_failed
	category    TEXT NOT NULL,          -- admin, system, or (from M4) connection
	actor       TEXT NOT NULL,          -- who: a username, "root", or "drawbridge"
	via         TEXT NOT NULL,          -- web, cli, or system
	source_ip   TEXT NOT NULL DEFAULT '',
	client_id   TEXT,                   -- no foreign key: events outlive their client
	client_name TEXT NOT NULL DEFAULT '',
	data        TEXT NOT NULL DEFAULT '{}' -- JSON object of strings
);

CREATE INDEX events_ts ON events (ts);
CREATE INDEX events_client ON events (client_id, id);
