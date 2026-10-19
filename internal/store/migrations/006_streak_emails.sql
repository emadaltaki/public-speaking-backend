ALTER TABLE users ADD COLUMN IF NOT EXISTS time_zone TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS streak_emails BOOLEAN NOT NULL DEFAULT true;

-- One row per streak email sent, so each kind goes out at most once per
-- user per local day.
CREATE TABLE IF NOT EXISTS streak_email_log (
    uid        TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    local_date DATE NOT NULL,
    sent_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (uid, kind, local_date)
);
