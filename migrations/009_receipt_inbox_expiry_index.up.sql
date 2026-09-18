-- Pending inbox lookups during expiry; no customer or quota data changes.
CREATE INDEX IF NOT EXISTS idx_receipt_inbox_pending_mapping
ON receipt_jobs ((payload->>'Provider'), (payload->>'ProviderID'))
WHERE kind='inbox' AND state IN ('pending','claimed');
