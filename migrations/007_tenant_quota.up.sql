ALTER TABLE messages ADD COLUMN IF NOT EXISTS tenant_id     VARCHAR(64);
ALTER TABLE messages ADD COLUMN IF NOT EXISTS account_id    VARCHAR(64);
ALTER TABLE messages ADD COLUMN IF NOT EXISTS client_msg_id VARCHAR(128);

CREATE INDEX IF NOT EXISTS idx_messages_tenant_received
    ON messages (tenant_id, received_at ASC, gateway_id ASC)
    WHERE tenant_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS tenant_quota_usage (
    tenant_id       VARCHAR(64) NOT NULL,
    quota_date      DATE        NOT NULL,
    used_segments   BIGINT      NOT NULL CHECK (used_segments >= 0),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, quota_date)
);
