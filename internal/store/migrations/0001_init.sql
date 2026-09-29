-- Schema version 1: the server's settings and its clients (docs/PLAN.md §7).

CREATE TABLE server (
	id                 INTEGER PRIMARY KEY CHECK (id = 1),
	iface              TEXT    NOT NULL,
	listen_port        INTEGER NOT NULL,
	endpoint_host      TEXT    NOT NULL DEFAULT '',
	endpoint_port      INTEGER NOT NULL DEFAULT 0,
	private_key_enc    BLOB    NOT NULL,
	mtu                INTEGER NOT NULL,
	ipv4_cidr          TEXT    NOT NULL,
	ipv6_cidr          TEXT    NOT NULL DEFAULT '',
	dns                TEXT    NOT NULL, -- JSON array of addresses
	keepalive          INTEGER NOT NULL,
	client_isolation   INTEGER NOT NULL,
	client_allowed_ips TEXT    NOT NULL, -- JSON array of prefixes
	updated_at         TEXT    NOT NULL
);

CREATE TABLE clients (
	id              TEXT    PRIMARY KEY,
	name            TEXT    NOT NULL UNIQUE COLLATE NOCASE,
	enabled         INTEGER NOT NULL DEFAULT 1,
	ipv4            TEXT    NOT NULL UNIQUE,
	ipv6            TEXT    UNIQUE,
	public_key      TEXT    NOT NULL UNIQUE,
	private_key_enc BLOB,   -- NULL when the server doesn't keep the client's private key
	psk_enc         BLOB,
	created_at      TEXT    NOT NULL,
	updated_at      TEXT    NOT NULL
);
