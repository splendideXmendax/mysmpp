ALTER TABLE pending ADD COLUMN IF NOT EXISTS tenant_id       VARCHAR(64);
ALTER TABLE pending ADD COLUMN IF NOT EXISTS account_id      VARCHAR(64);
ALTER TABLE pending ADD COLUMN IF NOT EXISTS client_msg_id   VARCHAR(128);
ALTER TABLE pending ADD COLUMN IF NOT EXISTS segment_index   SMALLINT NOT NULL DEFAULT 1;
ALTER TABLE pending ADD COLUMN IF NOT EXISTS segment_count   SMALLINT NOT NULL DEFAULT 1;
ALTER TABLE pending ADD COLUMN IF NOT EXISTS dlr_delivered   BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE pending p
SET provider = COALESCE(NULLIF(p.provider, ''), NULLIF(m.provider, ''), '')
FROM messages m
WHERE p.gateway_id = m.gateway_id
  AND COALESCE(p.provider, '') = '';

UPDATE pending SET provider = '' WHERE provider IS NULL;
ALTER TABLE pending ALTER COLUMN provider SET DEFAULT '';
ALTER TABLE pending ALTER COLUMN provider SET NOT NULL;

ALTER TABLE pending DROP CONSTRAINT IF EXISTS pending_pkey;
ALTER TABLE pending ADD PRIMARY KEY (provider, provider_id);

DROP INDEX IF EXISTS idx_pending_ready_received;
CREATE INDEX idx_pending_ready_received
    ON pending (source_system, received_at, provider, provider_id)
    WHERE dlr_ready = TRUE;
