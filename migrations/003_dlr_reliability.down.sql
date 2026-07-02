DROP INDEX IF EXISTS idx_pending_ready_received;

ALTER TABLE pending DROP COLUMN IF EXISTS callback_rule;
ALTER TABLE pending DROP COLUMN IF EXISTS callback_url;
