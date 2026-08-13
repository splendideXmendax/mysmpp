UPDATE messages
SET client_id = meta->>'client_id'
WHERE COALESCE(client_id, '') = ''
  AND COALESCE(meta->>'client_id', '') <> '';

CREATE INDEX IF NOT EXISTS idx_messages_client_received
ON messages(client_id, received_at ASC, gateway_id ASC)
WHERE client_id IS NOT NULL;
