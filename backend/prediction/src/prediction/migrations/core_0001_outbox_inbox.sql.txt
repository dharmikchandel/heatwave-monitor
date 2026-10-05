-- Tables every service shares: the transactional outbox and the inbox used to
-- dedupe at-least-once deliveries.

CREATE TABLE outbox (
    id              TEXT    PRIMARY KEY,
    target          TEXT    NOT NULL,
    type            TEXT    NOT NULL,
    payload         TEXT    NOT NULL,
    created_at      INTEGER NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    delivered_at    INTEGER,
    last_error      TEXT
);

CREATE INDEX outbox_pending ON outbox (target, created_at) WHERE delivered_at IS NULL;

CREATE TABLE inbox (
    event_id    TEXT    PRIMARY KEY,
    type        TEXT    NOT NULL,
    received_at INTEGER NOT NULL
);
