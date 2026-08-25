-- +goose Up
ALTER TABLE jobs
    ADD COLUMN idempotency_key TEXT,
    ADD COLUMN request_hash BYTEA,
    ADD CONSTRAINT jobs_idempotency_fields_valid CHECK (
        (
            idempotency_key IS NULL
            AND request_hash IS NULL
        )
        OR
        (
            idempotency_key IS NOT NULL
            AND octet_length(idempotency_key) BETWEEN 1 AND 255
            AND btrim(idempotency_key) <> ''
            AND request_hash IS NOT NULL
            AND octet_length(request_hash) = 32
        )
    );

CREATE UNIQUE INDEX jobs_idempotency_idx
ON jobs (job_type, idempotency_key)
WHERE idempotency_key IS NOT NULL;

-- +goose Down
DROP INDEX jobs_idempotency_idx;

ALTER TABLE jobs
    DROP CONSTRAINT jobs_idempotency_fields_valid,
    DROP COLUMN request_hash,
    DROP COLUMN idempotency_key;
