-- Schema version 6: the optional connection to a DNS resolver on the host, the first
-- slice of the AdGuard Home integration (docs/PLAN.md §6.3, §7). One row at most, because
-- only one resolver can answer on the VPN addresses; kind says which (today, 'adguard').
-- No row means there's no connection. The password is sealed (CLAUDE.md, "Secrets stay
-- secret"), and it's only ever sent to the address saved with it.

CREATE TABLE dns_integration (
	id           INTEGER PRIMARY KEY CHECK (id = 1),
	kind         TEXT NOT NULL,
	base_url     TEXT NOT NULL,
	username     TEXT NOT NULL DEFAULT '', -- empty when the resolver has no login
	password_enc BLOB,                     -- NULL when there's no password
	updated_at   TEXT NOT NULL
);
