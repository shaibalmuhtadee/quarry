-- +goose Up
ALTER TABLE jobs
    ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN max_attempts INTEGER NOT NULL,
    ADD COLUMN timeout_ms BIGINT NOT NULL,
    ADD CONSTRAINT jobs_attempt_count_nonnegative CHECK (attempt_count >= 0),
    ADD CONSTRAINT jobs_max_attempts_positive CHECK (max_attempts > 0),
    ADD CONSTRAINT jobs_attempt_count_within_limit CHECK (attempt_count <= max_attempts),
    ADD CONSTRAINT jobs_timeout_ms_valid CHECK (timeout_ms BETWEEN 1 AND 9223372036854);

-- +goose Down
ALTER TABLE jobs
    DROP COLUMN timeout_ms,
    DROP COLUMN max_attempts,
    DROP COLUMN attempt_count;
