DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pending GROUP BY provider_id HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot restore provider_id primary key while duplicate provider IDs exist';
    END IF;
END;
$$;

ALTER TABLE pending DROP CONSTRAINT IF EXISTS pending_pkey;
ALTER TABLE pending ADD PRIMARY KEY (provider_id);
ALTER TABLE pending ALTER COLUMN provider DROP NOT NULL;
ALTER TABLE pending ALTER COLUMN provider DROP DEFAULT;

DROP INDEX IF EXISTS idx_pending_ready_received;
CREATE INDEX idx_pending_ready_received
    ON pending (source_system, received_at, provider_id)
    WHERE dlr_ready = TRUE;

ALTER TABLE pending DROP COLUMN IF EXISTS dlr_delivered;
ALTER TABLE pending DROP COLUMN IF EXISTS segment_count;
ALTER TABLE pending DROP COLUMN IF EXISTS segment_index;
ALTER TABLE pending DROP COLUMN IF EXISTS client_msg_id;
ALTER TABLE pending DROP COLUMN IF EXISTS account_id;
ALTER TABLE pending DROP COLUMN IF EXISTS tenant_id;
