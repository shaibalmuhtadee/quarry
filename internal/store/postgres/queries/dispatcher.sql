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

-- name: RefreshWorkerHeartbeat :execrows
UPDATE workers
SET state = 'active',
    last_seen_at = statement_timestamp()
WHERE id = sqlc.arg(worker_id);

-- name: MarkLostWorkers :execrows
UPDATE workers
SET state = 'lost'
WHERE state = 'active'
  AND last_seen_at <= statement_timestamp()
        - sqlc.arg(liveness_timeout_ms)::bigint * interval '1 millisecond';

-- name: RenewAttemptLease :one
UPDATE jobs
SET lease_expires_at = statement_timestamp()
        + sqlc.arg(lease_duration_ms)::bigint * interval '1 millisecond',
    updated_at = statement_timestamp()
WHERE id = sqlc.arg(job_id)
  AND attempt_count = sqlc.arg(attempt_no)
  AND current_worker_id = sqlc.arg(worker_id)
  AND status = 'running'
  AND lease_expires_at > statement_timestamp()
RETURNING CAST(cancel_requested_at IS NOT NULL AS boolean) AS cancel_requested;

-- name: ClaimJobs :many
WITH eligible AS (
    SELECT id
    FROM jobs
    WHERE status IN ('queued', 'retry_wait')
      AND available_at <= now()
      AND cancel_requested_at IS NULL
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
    jobs.max_attempts,
    CAST(jobs.result IS NOT DISTINCT FROM sqlc.narg(result_json)::jsonb AS boolean) AS result_matches,
    jobs.lease_expires_at,
    jobs.cancel_requested_at,
    job_attempts.worker_id,
    job_attempts.status AS attempt_status,
    job_attempts.error_code,
    job_attempts.error_message
FROM jobs
JOIN job_attempts
  ON job_attempts.job_id = jobs.id
 AND job_attempts.attempt_no = sqlc.arg(attempt_no)
WHERE jobs.id = sqlc.arg(job_id)
FOR UPDATE OF jobs, job_attempts;

-- name: GetTransitionTime :one
SELECT statement_timestamp()::timestamptz;

-- name: FinishAttempt :execrows
UPDATE job_attempts
SET status = sqlc.arg(attempt_status),
    error_code = sqlc.narg(error_code),
    error_message = sqlc.narg(error_message),
    finished_at = sqlc.arg(transition_time)
WHERE job_id = sqlc.arg(job_id)
  AND attempt_no = sqlc.arg(attempt_no)
  AND worker_id = sqlc.arg(worker_id)
  AND status = 'running';

-- name: FinishJob :execrows
UPDATE jobs
SET status = sqlc.arg(job_status),
    result = sqlc.narg(result_json)::jsonb,
    current_worker_id = NULL,
    lease_expires_at = NULL,
    available_at = CASE
        WHEN sqlc.arg(job_status) = 'retry_wait' THEN sqlc.arg(transition_time)::timestamptz
            + sqlc.arg(retry_delay_ms)::bigint * interval '1 millisecond'
        ELSE available_at
    END,
    finished_at = CASE
        WHEN sqlc.arg(job_status) IN ('succeeded', 'dead_lettered', 'cancelled')
            THEN sqlc.arg(transition_time)::timestamptz
        ELSE NULL
    END,
    updated_at = sqlc.arg(transition_time)::timestamptz
WHERE id = sqlc.arg(job_id)
  AND attempt_count = sqlc.arg(attempt_no)
  AND current_worker_id = sqlc.arg(worker_id)
  AND status = 'running'
  AND lease_expires_at > sqlc.arg(transition_time)::timestamptz;

-- name: LockExpiredJobs :many
SELECT id, attempt_count, max_attempts, cancel_requested_at
FROM jobs
WHERE status = 'running'
  AND lease_expires_at <= statement_timestamp()
ORDER BY lease_expires_at, id
FOR UPDATE SKIP LOCKED
LIMIT sqlc.arg(batch_size);

-- name: AbandonExpiredAttempt :execrows
UPDATE job_attempts
SET status = 'abandoned',
    error_code = 'lease_expired',
    error_message = 'worker lease expired before the attempt completed',
    finished_at = sqlc.arg(transition_time)
WHERE job_id = sqlc.arg(job_id)
  AND attempt_no = sqlc.arg(attempt_no)
  AND status = 'running';

-- name: CancelExpiredAttempt :execrows
UPDATE job_attempts
SET status = 'cancelled',
    error_code = 'cancellation_requested',
    error_message = 'job cancellation was requested',
    finished_at = sqlc.arg(transition_time)
WHERE job_id = sqlc.arg(job_id)
  AND attempt_no = sqlc.arg(attempt_no)
  AND status = 'running';

-- name: RecoverExpiredJob :execrows
UPDATE jobs
SET status = sqlc.arg(job_status),
    current_worker_id = NULL,
    lease_expires_at = NULL,
    available_at = CASE
        WHEN sqlc.arg(job_status) = 'retry_wait' THEN sqlc.arg(transition_time)::timestamptz
            + sqlc.arg(retry_delay_ms)::bigint * interval '1 millisecond'
        ELSE available_at
    END,
    finished_at = CASE
        WHEN sqlc.arg(job_status) IN ('dead_lettered', 'cancelled')
            THEN sqlc.arg(transition_time)::timestamptz
        ELSE NULL
    END,
    updated_at = sqlc.arg(transition_time)::timestamptz
WHERE id = sqlc.arg(job_id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_no)
  AND lease_expires_at <= statement_timestamp();
