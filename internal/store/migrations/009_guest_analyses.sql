-- One free AI coach try for visitors without an account. Only the guest id
-- from the browser and the IP are kept, to enforce the daily limit.
CREATE TABLE IF NOT EXISTS guest_analyses (
    id         BIGSERIAL PRIMARY KEY,
    guest_id   TEXT NOT NULL,
    ip         TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS guest_analyses_guest ON guest_analyses (guest_id, created_at DESC);
CREATE INDEX IF NOT EXISTS guest_analyses_ip ON guest_analyses (ip, created_at DESC);
