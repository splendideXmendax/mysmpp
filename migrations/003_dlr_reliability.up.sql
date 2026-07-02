ALTER TABLE pending ADD COLUMN IF NOT EXISTS callback_url  TEXT;
ALTER TABLE pending ADD COLUMN IF NOT EXISTS callback_rule TEXT;

CREATE INDEX IF NOT EXISTS idx_pending_ready_received
    ON pending (source_system, received_at, provider_id)
    WHERE dlr_ready = TRUE;
