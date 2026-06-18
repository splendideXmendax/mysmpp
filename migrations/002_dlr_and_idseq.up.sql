CREATE TABLE IF NOT EXISTS id_alloc (
    name            VARCHAR(64) PRIMARY KEY,
    value           BIGINT      NOT NULL DEFAULT 0
);

INSERT INTO id_alloc (name, value)
SELECT 'gateway_id', COALESCE(MAX(NULLIF(regexp_replace(gateway_id, '^g([0-9]+)$', '\1'), gateway_id)::BIGINT), 0)
FROM messages
WHERE gateway_id ~ '^g[0-9]+$'
ON CONFLICT (name) DO UPDATE SET value = GREATEST(id_alloc.value, EXCLUDED.value);

ALTER TABLE pending ADD COLUMN IF NOT EXISTS dlr_ready   BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE pending ADD COLUMN IF NOT EXISTS dlr_state   VARCHAR(16);
ALTER TABLE pending ADD COLUMN IF NOT EXISTS dlr_err     INT;
ALTER TABLE pending ADD COLUMN IF NOT EXISTS dlr_done_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_pending_dlr_ready
    ON pending(source_system) WHERE dlr_ready = TRUE;
