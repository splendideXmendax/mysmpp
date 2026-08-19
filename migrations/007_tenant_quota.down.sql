DROP TABLE IF EXISTS tenant_quota_usage;
DROP INDEX IF EXISTS idx_messages_tenant_received;

ALTER TABLE messages DROP COLUMN IF EXISTS client_msg_id;
ALTER TABLE messages DROP COLUMN IF EXISTS account_id;
ALTER TABLE messages DROP COLUMN IF EXISTS tenant_id;
