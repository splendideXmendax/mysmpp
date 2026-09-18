ALTER TABLE pending ADD COLUMN IF NOT EXISTS reliability_managed BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE pending ADD COLUMN IF NOT EXISTS upstream_status BIGINT NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS multipart_bindings (
    key TEXT PRIMARY KEY,
    payload JSONB NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_multipart_expiry ON multipart_bindings(expires_at);
CREATE TABLE IF NOT EXISTS receipt_jobs (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    payload JSONB NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    attempt INTEGER NOT NULL DEFAULT 0,
    lease BIGINT NOT NULL DEFAULT 0,
    lease_until TIMESTAMPTZ,
    next_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_receipt_jobs_due ON receipt_jobs(kind, next_at) WHERE state IN ('pending','claimed');
CREATE INDEX IF NOT EXISTS idx_receipt_jobs_expiry ON receipt_jobs(expires_at);
