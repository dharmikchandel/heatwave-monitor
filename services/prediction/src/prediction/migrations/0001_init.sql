CREATE TABLE predictions (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    location_id      INTEGER NOT NULL,
    observation_id   INTEGER NOT NULL,
    fetched_at       INTEGER NOT NULL,
    generated_at     INTEGER NOT NULL,
    method           TEXT    NOT NULL,
    model_version    TEXT    NOT NULL,
    peak_probability REAL    NOT NULL,
    payload          TEXT    NOT NULL
);

CREATE INDEX predictions_location_time ON predictions (location_id, fetched_at DESC);

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
