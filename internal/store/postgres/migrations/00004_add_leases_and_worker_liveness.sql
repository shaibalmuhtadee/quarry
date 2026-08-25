-- +goose Up
ALTER TABLE workers
    ADD COLUMN state TEXT NOT NULL DEFAULT 'active',
    ADD COLUMN last_seen_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    ADD CONSTRAINT workers_state_valid CHECK (state IN ('active', 'lost'));

ALTER TABLE jobs
    ADD COLUMN lease_expires_at TIMESTAMPTZ;

UPDATE jobs
SET lease_expires_at = statement_timestamp()
WHERE status = 'running';

ALTER TABLE jobs
    ADD CONSTRAINT jobs_running_lease_valid CHECK (
        (status = 'running' AND current_worker_id IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR
        (status <> 'running' AND current_worker_id IS NULL AND lease_expires_at IS NULL)
    );

CREATE INDEX jobs_expired_lease_idx
    ON jobs (lease_expires_at)
    WHERE status = 'running';

-- +goose Down
DROP INDEX jobs_expired_lease_idx;

ALTER TABLE jobs
    DROP CONSTRAINT jobs_running_lease_valid,
    DROP COLUMN lease_expires_at;

ALTER TABLE workers
    DROP CONSTRAINT workers_state_valid,
    DROP COLUMN last_seen_at,
    DROP COLUMN state;
