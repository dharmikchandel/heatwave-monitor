CREATE TABLE locations (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    geo_key         TEXT    NOT NULL UNIQUE,   -- "lat,lon" rounded to 2 decimals (~1 km)
    name            TEXT    NOT NULL,
    country         TEXT    NOT NULL DEFAULT '',
    admin1          TEXT    NOT NULL DEFAULT '',
    latitude        REAL    NOT NULL,
    longitude       REAL    NOT NULL,
    timezone        TEXT    NOT NULL DEFAULT '',
    active          INTEGER NOT NULL DEFAULT 1,
    created_at      INTEGER NOT NULL,
    last_fetched_at INTEGER,
    last_error      TEXT
);

CREATE TABLE observations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    location_id INTEGER NOT NULL REFERENCES locations(id) ON DELETE CASCADE,
    fetched_at  INTEGER NOT NULL,
    source      TEXT    NOT NULL,
    payload     TEXT    NOT NULL
);

CREATE INDEX observations_location_time ON observations (location_id, fetched_at DESC);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
