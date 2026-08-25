-- +goose Up
ALTER TABLE job_attempts
    ADD COLUMN error_code TEXT,
    ADD COLUMN error_message TEXT;

UPDATE job_attempts
SET error_code = 'lease_expired',
    error_message = 'worker lease expired before the attempt completed'
WHERE status = 'abandoned';

ALTER TABLE job_attempts
    ADD CONSTRAINT job_attempts_outcome_valid CHECK (
        (
            status IN ('running', 'succeeded')
            AND error_code IS NULL
            AND error_message IS NULL
        )
        OR
        (
            status IN (
                'retryable_failed',
                'permanent_failed',
                'cancelled',
                'timed_out',
                'panicked',
                'abandoned'
            )
            AND error_code IS NOT NULL
            AND error_message IS NOT NULL
            AND error_code ~ '^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$'
            AND octet_length(error_code) <= 64
            AND btrim(error_message) <> ''
            AND octet_length(error_message) <= 1024
        )
    );

-- +goose Down
ALTER TABLE job_attempts
    DROP CONSTRAINT job_attempts_outcome_valid,
    DROP COLUMN error_message,
    DROP COLUMN error_code;
