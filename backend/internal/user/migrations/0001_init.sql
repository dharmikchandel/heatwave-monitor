CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    email         TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    display_name  TEXT    NOT NULL DEFAULT '',
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL CHECK (role IN ('user', 'admin')),
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER
);

-- Sessions are opaque random tokens; only their SHA-256 hash is stored, so a copy of
-- this database does not let anyone sign in as a user.
CREATE TABLE sessions (
    token_hash TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    user_agent TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX sessions_by_user ON sessions (user_id);
CREATE INDEX sessions_by_expiry ON sessions (expires_at);

-- The cities a user follows. The location id is the weather service's; the name and
-- coordinates are copied so the list can be shown (and re-created) without asking it.
CREATE TABLE watchlist (
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    location_id INTEGER NOT NULL,
    name        TEXT    NOT NULL,
    country     TEXT    NOT NULL DEFAULT '',
    admin1      TEXT    NOT NULL DEFAULT '',
    latitude    REAL    NOT NULL,
    longitude   REAL    NOT NULL,
    timezone    TEXT    NOT NULL DEFAULT '',
    added_at    INTEGER NOT NULL,
    PRIMARY KEY (user_id, location_id)
);
