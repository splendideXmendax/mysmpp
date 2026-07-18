CREATE OR REPLACE FUNCTION mysmpp_base36_to_bigint(value TEXT)
RETURNS BIGINT
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    result BIGINT := 0;
    digit INT;
    ch TEXT;
BEGIN
    FOR i IN 1..length(value) LOOP
        ch := lower(substr(value, i, 1));
        digit := strpos('0123456789abcdefghijklmnopqrstuvwxyz', ch) - 1;
        IF digit < 0 THEN
            RAISE EXCEPTION 'invalid base36 digit: %', ch;
        END IF;
        result := result * 36 + digit;
    END LOOP;
    RETURN result;
END;
$$;

INSERT INTO id_alloc (name, value)
SELECT 'gateway_id', GREATEST(
    COALESCE(MAX(substring(gateway_id FROM 2)::BIGINT) FILTER (WHERE gateway_id ~ '^g[0-9]+$'), 0),
    COALESCE(MAX(mysmpp_base36_to_bigint(substring(gateway_id FROM 2))) FILTER (WHERE gateway_id ~ '^m[0-9a-z]+$'), 0)
)
FROM messages
ON CONFLICT (name) DO UPDATE SET value = GREATEST(id_alloc.value, EXCLUDED.value);

DROP FUNCTION mysmpp_base36_to_bigint(TEXT);
