-- Nullable sidecars avoid rewriting historical text or changing existing types.
-- When present, text_bytes holds the exact original UTF-8 bytes, including NUL.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS text_bytes BYTEA;
ALTER TABLE pending ADD COLUMN IF NOT EXISTS text_bytes BYTEA;
