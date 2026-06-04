CREATE TABLE IF NOT EXISTS messages (
    gateway_id      VARCHAR(64)  PRIMARY KEY,
    provider_id     VARCHAR(128),
    direction       VARCHAR(4)   NOT NULL,
    from_addr       VARCHAR(32)  NOT NULL,
    to_addr         VARCHAR(32)  NOT NULL,
    text            TEXT         NOT NULL,
    encoding        VARCHAR(8),
    data_coding     SMALLINT,
    segments        SMALLINT     DEFAULT 1,
    route           VARCHAR(64),
    provider        VARCHAR(64),
    source_kind     VARCHAR(16),
    source_session  VARCHAR(64),
    source_system   VARCHAR(64),
    client_id       VARCHAR(64),
    state           VARCHAR(16)  NOT NULL,
    error_code      INT          DEFAULT 0,
    received_at     TIMESTAMPTZ  NOT NULL,
    sent_at         TIMESTAMPTZ,
    done_at         TIMESTAMPTZ,
    meta            JSONB
);

CREATE INDEX IF NOT EXISTS idx_messages_provider_id ON messages(provider_id);
CREATE INDEX IF NOT EXISTS idx_messages_received_at ON messages(received_at DESC);
CREATE INDEX IF NOT EXISTS idx_messages_to_addr ON messages(to_addr);
CREATE INDEX IF NOT EXISTS idx_messages_state ON messages(state) WHERE state IN ('queued', 'sent', 'submitted');

CREATE TABLE IF NOT EXISTS pending (
    provider_id     VARCHAR(128) PRIMARY KEY,
    gateway_id      VARCHAR(64)  NOT NULL,
    source_kind     VARCHAR(16)  NOT NULL,
    source_session  VARCHAR(64),
    source_system   VARCHAR(64),
    from_addr       VARCHAR(32),
    to_addr         VARCHAR(32),
    text            TEXT,
    received_at     TIMESTAMPTZ  NOT NULL,
    expires_at      TIMESTAMPTZ  NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_pending_expires ON pending(expires_at);
CREATE INDEX IF NOT EXISTS idx_pending_gw ON pending(gateway_id);

CREATE TABLE IF NOT EXISTS outbox (
    id              BIGSERIAL    PRIMARY KEY,
    gateway_id      VARCHAR(64)  NOT NULL,
    provider        VARCHAR(64)  NOT NULL,
    payload         JSONB        NOT NULL,
    state           VARCHAR(16)  NOT NULL,
    claimed_by      VARCHAR(64),
    claimed_at      TIMESTAMPTZ,
    next_retry_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    attempt         INT          NOT NULL DEFAULT 0,
    max_attempts    INT          NOT NULL DEFAULT 5,
    last_error      TEXT,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_outbox_claim ON outbox(state, next_retry_at) WHERE state = 'pending';

CREATE TABLE IF NOT EXISTS idempotency (
    client_id       VARCHAR(64)  NOT NULL,
    key             VARCHAR(128) NOT NULL,
    gateway_id      VARCHAR(64)  NOT NULL,
    expires_at      TIMESTAMPTZ  NOT NULL,
    PRIMARY KEY (client_id, key)
);

CREATE INDEX IF NOT EXISTS idx_idempotency_expires ON idempotency(expires_at);

CREATE TABLE IF NOT EXISTS config_history (
    id          BIGSERIAL PRIMARY KEY,
    config      JSONB NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    applied_by  VARCHAR(64)
);
