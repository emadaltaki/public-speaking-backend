-- Fix-it attempts on a coach report's moments. No audio is stored.
CREATE TABLE IF NOT EXISTS speech_retries (
    id         BIGSERIAL PRIMARY KEY,
    uid        TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    report_id  BIGINT NOT NULL REFERENCES speech_reports(id) ON DELETE CASCADE,
    moment     INTEGER NOT NULL,
    stars      INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS speech_retries_uid_created_at ON speech_retries (uid, created_at DESC);
CREATE INDEX IF NOT EXISTS speech_retries_report ON speech_retries (report_id);
