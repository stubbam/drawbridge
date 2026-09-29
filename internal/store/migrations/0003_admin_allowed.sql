-- Schema version 3: extra sources allowed to reach the admin UI, as a JSON array of
-- prefixes (docs/PLAN.md §6.5). Empty by default: the LAN, loopback, link-local
-- addresses, and the VPN are the only sources.

ALTER TABLE server ADD COLUMN admin_allowed TEXT NOT NULL DEFAULT '[]';
