-- Schema version 5: per-client traffic history at two resolutions (docs/PLAN.md §6.4, §7),
-- the traffic-history slice of M4. Rows are written by a once-per-raw-interval buffered
-- flush (internal/service/traffic.go), never once per poll, to keep SD card writes low
-- (CLAUDE.md, "Protect the SD card"). "raw" is the sampler's own interval, which defaults
-- to a minute but is configurable (a host on an NVMe SSD can afford a finer one); "hourly"
-- rows are always exactly an hour wide, produced by rolling old "raw" rows up. Naming the
-- resolutions this way, instead of the plan's literal "1m"/"1h", keeps the label honest
-- once the raw interval isn't exactly a minute.

CREATE TABLE traffic (
	client_id    TEXT NOT NULL REFERENCES clients (id) ON DELETE CASCADE,
	resolution   TEXT NOT NULL, -- 'raw' or 'hourly'
	bucket_start TEXT NOT NULL, -- UTC, truncated to the resolution's width
	rx_bytes     INTEGER NOT NULL,
	tx_bytes     INTEGER NOT NULL,
	PRIMARY KEY (client_id, resolution, bucket_start)
);

-- For the all-clients aggregate (GET /api/traffic) and the retention job, both of which
-- scan by resolution and a bucket_start range across every client, not by client_id.
CREATE INDEX traffic_resolution_bucket ON traffic (resolution, bucket_start);
