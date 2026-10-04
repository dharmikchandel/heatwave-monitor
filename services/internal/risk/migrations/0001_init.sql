CREATE TABLE assessments (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    location_id    INTEGER NOT NULL,
    observation_id INTEGER NOT NULL,
    fetched_at     INTEGER NOT NULL,
    assessed_at    INTEGER NOT NULL,
    now_level      TEXT    NOT NULL,
    peak_level     TEXT    NOT NULL,
    alert_level    TEXT    NOT NULL,
    payload        TEXT    NOT NULL
);

CREATE INDEX assessments_location_time ON assessments (location_id, fetched_at DESC);

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
