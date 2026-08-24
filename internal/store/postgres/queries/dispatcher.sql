-- name: RegisterWorker :one
INSERT INTO workers (
    id,
    hostname,
    version,
    concurrency,
    started_at
)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (id) DO UPDATE
SET id = EXCLUDED.id
WHERE workers.hostname = EXCLUDED.hostname
  AND workers.version = EXCLUDED.version
  AND workers.concurrency = EXCLUDED.concurrency
  AND workers.started_at = EXCLUDED.started_at
RETURNING id;

-- name: LockWorker :one
SELECT concurrency
FROM workers
WHERE id = $1
FOR UPDATE;

-- name: CountWorkerRunningJobs :one
SELECT count(*)
FROM jobs
WHERE current_worker_id = $1
  AND status = 'running';

-- name: ClaimJobs :many
WITH eligible AS (
    SELECT id
    FROM jobs
    WHERE status = 'queued'
      AND available_at <= now()
      AND job_type = ANY(sqlc.arg(supported_job_types)::text[])
    ORDER BY available_at, created_at
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(claim_limit)
), claimed AS (
    UPDATE jobs
    SET status = 'running',
        attempt_count = attempt_count + 1,
        current_worker_id = sqlc.arg(worker_id),
        updated_at = now()
    FROM eligible
    WHERE jobs.id = eligible.id
    RETURNING
        jobs.id,
        jobs.job_type,
        jobs.payload,
        jobs.attempt_count,
        jobs.timeout_ms,
        jobs.available_at,
        jobs.created_at
), attempts AS (
    INSERT INTO job_attempts (
        job_id,
        attempt_no,
        worker_id,
        status,
        started_at
    )
    SELECT
        id,
        attempt_count,
        sqlc.arg(worker_id),
        'running',
        now()
    FROM claimed
    RETURNING job_id, attempt_no
)
SELECT
    claimed.id,
    claimed.job_type,
    claimed.payload,
    claimed.attempt_count,
    claimed.timeout_ms
FROM claimed
JOIN attempts ON attempts.job_id = claimed.id
ORDER BY claimed.available_at, claimed.created_at;
