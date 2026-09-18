-- Destructive: drain and archive receipt jobs before deliberately rolling back
-- the schema. Binary rollback does not require removing these additive tables.
DROP TABLE IF EXISTS receipt_jobs;
DROP TABLE IF EXISTS multipart_bindings;
ALTER TABLE pending DROP COLUMN IF EXISTS reliability_managed;
ALTER TABLE pending DROP COLUMN IF EXISTS upstream_status;
