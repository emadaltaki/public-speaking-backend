-- AI speaking coach results. The recording itself is never stored.
CREATE TABLE IF NOT EXISTS speech_reports (
    id         BIGSERIAL PRIMARY KEY,
    uid        TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    report     JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS speech_reports_uid_created_at ON speech_reports (uid, created_at DESC);
