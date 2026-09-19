-- Never silently discard text that the old schema cannot represent.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM messages WHERE text_bytes IS NOT NULL)
       OR EXISTS (SELECT 1 FROM pending WHERE text_bytes IS NOT NULL)
       OR EXISTS (SELECT 1 FROM outbox WHERE payload ? 'text_bytes')
       OR EXISTS (SELECT 1 FROM receipt_jobs WHERE payload ? 'text_bytes') THEN
        RAISE EXCEPTION 'lossless text exists; preserve/export it before downgrading';
    END IF;
END $$;
ALTER TABLE pending DROP COLUMN IF EXISTS text_bytes;
ALTER TABLE messages DROP COLUMN IF EXISTS text_bytes;
