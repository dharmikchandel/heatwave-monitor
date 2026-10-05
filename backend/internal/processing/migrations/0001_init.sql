CREATE TABLE snapshots (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    location_id    INTEGER NOT NULL,
    observation_id INTEGER NOT NULL,
    source         TEXT    NOT NULL,
    fetched_at     INTEGER NOT NULL,
    processed_at   INTEGER NOT NULL,
    quality        REAL    NOT NULL,
    payload        TEXT    NOT NULL
);

CREATE INDEX snapshots_location_time ON snapshots (location_id, fetched_at DESC);

CREATE TABLE rejections (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id       TEXT    NOT NULL,
    location_id    INTEGER,
    observation_id INTEGER,
    reason         TEXT    NOT NULL,
    detail         TEXT    NOT NULL,
    created_at     INTEGER NOT NULL
);

CREATE INDEX rejections_time ON rejections (created_at DESC);
