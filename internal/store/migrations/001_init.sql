CREATE TABLE IF NOT EXISTS users (
    uid          TEXT PRIMARY KEY,
    email        TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    photo_url    TEXT NOT NULL DEFAULT '',
    language     TEXT NOT NULL DEFAULT 'en',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS game_sessions (
    id               BIGSERIAL PRIMARY KEY,
    uid              TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    game_id          TEXT NOT NULL,
    duration_seconds INTEGER NOT NULL DEFAULT 0,
    score            INTEGER,
    details          JSONB,
    played_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS game_sessions_uid_played_at ON game_sessions (uid, played_at DESC);

CREATE TABLE IF NOT EXISTS bookings (
    id             BIGSERIAL PRIMARY KEY,
    uid            TEXT REFERENCES users(uid) ON DELETE SET NULL,
    name           TEXT NOT NULL,
    contact        TEXT NOT NULL,
    preferred_time TEXT NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'new',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS bookings_uid ON bookings (uid);
CREATE INDEX IF NOT EXISTS bookings_created_at ON bookings (created_at DESC);
