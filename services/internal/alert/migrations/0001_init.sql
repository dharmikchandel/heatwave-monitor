CREATE TABLE subscriptions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    location_id INTEGER,                       -- NULL = every location
    min_level   TEXT    NOT NULL,
    channel     TEXT    NOT NULL,              -- 'webhook' or 'log'
    target      TEXT    NOT NULL DEFAULT '',   -- webhook URL
    secret      TEXT    NOT NULL DEFAULT '',   -- optional HMAC key for webhook signatures
    label       TEXT    NOT NULL DEFAULT '',
    active      INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL
);

-- An alert is an episode for one location: it opens when the risk reaches the
-- open level, tracks escalations, and resolves once the danger has passed.
CREATE TABLE alerts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    location_id     INTEGER NOT NULL,
    location_name   TEXT    NOT NULL,
    status          TEXT    NOT NULL CHECK (status IN ('open', 'resolved')),
    current_level   TEXT    NOT NULL,
    peak_level      TEXT    NOT NULL,
    headline        TEXT    NOT NULL,
    summary         TEXT    NOT NULL,
    details         TEXT    NOT NULL,
    opened_at       INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    resolved_at     INTEGER,
    acknowledged_at INTEGER,
    below_since     INTEGER,                   -- when the risk first dropped below the open level
    reopen_count    INTEGER NOT NULL DEFAULT 0
);

-- At most one open alert per location.
CREATE UNIQUE INDEX alerts_one_open_per_location ON alerts (location_id) WHERE status = 'open';
CREATE INDEX alerts_by_time ON alerts (opened_at DESC);

CREATE TABLE alert_events (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    alert_id INTEGER NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
    at       INTEGER NOT NULL,
    kind     TEXT    NOT NULL,                 -- opened, escalated, deescalated, resolved, reopened
    level    TEXT    NOT NULL,
    note     TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX alert_events_by_alert ON alert_events (alert_id, id);

-- What each subscription was told about an alert, and the state of delivering it.
CREATE TABLE notifications (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    alert_id        INTEGER NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
    subscription_id INTEGER NOT NULL REFERENCES subscriptions(id),
    kind            TEXT    NOT NULL,          -- opened, escalated, resolved
    level           TEXT    NOT NULL,
    created_at      INTEGER NOT NULL,
    channel         TEXT    NOT NULL,
    status          TEXT    NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed', 'cancelled')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    sent_at         INTEGER,
    last_error      TEXT,
    response_code   INTEGER,
    payload         TEXT    NOT NULL
);

CREATE INDEX notifications_pending ON notifications (id) WHERE status = 'pending';
CREATE INDEX notifications_by_alert ON notifications (alert_id, subscription_id);

-- The newest observation applied per location, to drop out-of-order deliveries.
CREATE TABLE location_state (
    location_id     INTEGER PRIMARY KEY,
    last_fetched_at INTEGER NOT NULL
);

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
