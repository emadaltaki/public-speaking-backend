ALTER TABLE users ADD COLUMN IF NOT EXISTS email_verified BOOLEAN NOT NULL DEFAULT false;

-- One live code per user; sending a new code replaces the old one.
CREATE TABLE IF NOT EXISTS email_codes (
    uid        TEXT PRIMARY KEY REFERENCES users(uid) ON DELETE CASCADE,
    email      TEXT NOT NULL,
    code_hash  TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    attempts   INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Every code email sent, for per-user rate limits.
CREATE TABLE IF NOT EXISTS email_code_sends (
    id      BIGSERIAL PRIMARY KEY,
    uid     TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    sent_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS email_code_sends_uid_sent_at ON email_code_sends (uid, sent_at DESC);
