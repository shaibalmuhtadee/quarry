-- name: RegisterWorker :one
INSERT INTO workers (
    id,
    hostname,
    version,
    concurrency,
    started_at,
    state,
    last_seen_at
)
VALUES ($1, $2, $3, $4, $5, 'active', statement_timestamp())
ON CONFLICT (id) DO UPDATE
SET state = 'active',
    last_seen_at = statement_timestamp()
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
        lease_expires_at = statement_timestamp()
            + sqlc.arg(lease_duration_ms)::bigint * interval '1 millisecond',
        updated_at = statement_timestamp()
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
        statement_timestamp()
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

-- name: LockAttemptReport :one
SELECT
    jobs.status AS job_status,
    jobs.attempt_count,
    CAST(COALESCE(jobs.result = sqlc.arg(result_json)::jsonb, false) AS boolean) AS result_matches,
    job_attempts.worker_id,
    job_attempts.status AS attempt_status
FROM jobs
JOIN job_attempts
  ON job_attempts.job_id = jobs.id
 AND job_attempts.attempt_no = sqlc.arg(attempt_no)
WHERE jobs.id = sqlc.arg(job_id)
FOR UPDATE OF jobs, job_attempts;

-- name: FinishAttemptSuccess :execrows
UPDATE job_attempts
SET status = 'succeeded',
    finished_at = sqlc.arg(finished_at)
WHERE job_id = sqlc.arg(job_id)
  AND attempt_no = sqlc.arg(attempt_no)
  AND worker_id = sqlc.arg(worker_id)
  AND status = 'running';

-- name: FinishJobSuccess :execrows
UPDATE jobs
SET status = 'succeeded',
    result = sqlc.arg(result_json)::jsonb,
    current_worker_id = NULL,
    lease_expires_at = NULL,
    finished_at = sqlc.arg(finished_at),
    updated_at = sqlc.arg(finished_at)
WHERE id = sqlc.arg(job_id)
  AND attempt_count = sqlc.arg(attempt_no)
  AND current_worker_id = sqlc.arg(worker_id)
  AND status = 'running';
