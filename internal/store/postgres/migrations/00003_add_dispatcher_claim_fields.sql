-- +goose Up
CREATE TABLE workers (
    id UUID PRIMARY KEY,
    hostname TEXT NOT NULL CHECK (btrim(hostname) <> ''),
    version TEXT NOT NULL CHECK (btrim(version) <> ''),
    concurrency INTEGER NOT NULL CHECK (concurrency > 0),
    started_at TIMESTAMPTZ NOT NULL,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE jobs
    ADD COLUMN result JSONB,
    ADD COLUMN available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN current_worker_id UUID REFERENCES workers(id),
    ADD COLUMN finished_at TIMESTAMPTZ;

CREATE INDEX jobs_eligible_idx
    ON jobs (available_at, created_at)
    WHERE status IN ('queued', 'retry_wait');

ALTER TABLE job_attempts
    ADD COLUMN worker_id UUID NOT NULL REFERENCES workers(id),
    ADD COLUMN finished_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE job_attempts
    DROP COLUMN finished_at,
    DROP COLUMN worker_id;

DROP INDEX jobs_eligible_idx;

ALTER TABLE jobs
    DROP COLUMN finished_at,
    DROP COLUMN current_worker_id,
    DROP COLUMN available_at,
    DROP COLUMN result;

DROP TABLE workers;
