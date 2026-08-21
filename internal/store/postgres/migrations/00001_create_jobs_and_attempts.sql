-- +goose Up
CREATE TABLE jobs (
    id UUID PRIMARY KEY,
    job_type TEXT NOT NULL CHECK (btrim(job_type) <> ''),
    payload JSONB NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('queued', 'running', 'retry_wait', 'succeeded', 'dead_lettered', 'cancelled')
    ),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE job_attempts (
    job_id UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    attempt_no INTEGER NOT NULL CHECK (attempt_no > 0),
    status TEXT NOT NULL CHECK (btrim(status) <> ''),
    started_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (job_id, attempt_no)
);

-- +goose Down
DROP TABLE job_attempts;
DROP TABLE jobs;
