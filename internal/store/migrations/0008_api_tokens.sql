-- Schema version 8: read-only API tokens (docs/PLAN.md §6.5, §7), for a dashboard such as
-- Homepage that can't log in.
--
-- Only a token's SHA-256 hash is stored, like a session's: the token is shown once, when it's
-- made. prefix is its first few characters, so the admin can tell one from another in a list.
-- scope is "read" and nothing else for now. last_used_at is NULL until the first use, and is
-- written at most once an hour (internal/service/apitokens.go), so a dashboard that polls every
-- few seconds doesn't wear an SD card.

CREATE TABLE api_tokens (
	id           TEXT PRIMARY KEY,
	user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	name         TEXT NOT NULL,
	prefix       TEXT NOT NULL,
	token_hash   BLOB NOT NULL UNIQUE,
	scope        TEXT NOT NULL CHECK (scope = 'read'),
	created_at   TEXT NOT NULL,
	last_used_at TEXT
);

CREATE INDEX api_tokens_user ON api_tokens (user_id);
-- Two tokens with the same name would be indistinguishable in a list.
CREATE UNIQUE INDEX api_tokens_name ON api_tokens (user_id, name COLLATE NOCASE);
