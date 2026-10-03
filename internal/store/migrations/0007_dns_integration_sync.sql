-- Schema version 7: turning the AdGuard Home integration on, and the record of the clients
-- Drawbridge named there (docs/PLAN.md §6.3, §7).
--
-- enabled is the admin's switch for using the connection at all, off until they turn it on,
-- and sync_names is whether Drawbridge writes its clients' names into AdGuard Home (the other
-- use, reading a client's DNS log, writes nothing there).
--
-- dns_integration_clients is what sync last wrote for each client, so that it changes only
-- what it made: an AdGuard Home client it has no row for is never edited or deleted. It has
-- no foreign key to clients on purpose: a deleted client's row is how sync knows which name
-- to delete in AdGuard Home.

ALTER TABLE dns_integration ADD COLUMN enabled    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE dns_integration ADD COLUMN sync_names INTEGER NOT NULL DEFAULT 1;

CREATE TABLE dns_integration_clients (
	client_id TEXT PRIMARY KEY,
	name      TEXT NOT NULL, -- the name it was given in AdGuard Home
	ids       TEXT NOT NULL  -- JSON array of the addresses it was given
);
