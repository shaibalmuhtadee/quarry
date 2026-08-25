-- name: SubmitJob :one
INSERT INTO jobs (
    id,
    job_type,
    payload,
    status,
    max_attempts,
    timeout_ms,
    idempotency_key,
    request_hash
)
VALUES (
    sqlc.arg(id),
    sqlc.arg(job_type),
    sqlc.arg(payload),
    'queued',
    sqlc.arg(max_attempts),
    sqlc.arg(timeout_ms),
    sqlc.narg(idempotency_key),
    sqlc.narg(request_hash)
)
ON CONFLICT (job_type, idempotency_key) WHERE idempotency_key IS NOT NULL
DO NOTHING
RETURNING
    id,
    job_type,
    payload,
    result,
    status,
    attempt_count,
    max_attempts,
    timeout_ms,
    created_at,
    updated_at,
    finished_at,
    cancel_requested_at;

-- name: GetJobByIdempotencyKey :one
SELECT
    id,
    job_type,
    payload,
    result,
    status,
    attempt_count,
    max_attempts,
    timeout_ms,
    created_at,
    updated_at,
    finished_at,
    cancel_requested_at,
    request_hash
FROM jobs
WHERE job_type = $1 AND idempotency_key = $2;

-- name: GetJob :one
SELECT
    jobs.id,
    jobs.job_type,
    jobs.payload,
    jobs.result,
    jobs.status,
    jobs.attempt_count,
    jobs.max_attempts,
    jobs.timeout_ms,
    jobs.created_at,
    jobs.updated_at,
    jobs.finished_at,
    jobs.cancel_requested_at,
    latest_failure.error_code AS latest_failure_error_code,
    latest_failure.error_message AS latest_failure_error_message
FROM jobs
LEFT JOIN LATERAL (
    SELECT error_code, error_message
    FROM job_attempts
    WHERE job_id = jobs.id
      AND error_code IS NOT NULL
    ORDER BY attempt_no DESC
    LIMIT 1
) AS latest_failure ON true
WHERE jobs.id = $1;

-- name: LockJobForCancellation :one
SELECT status, cancel_requested_at
FROM jobs
WHERE id = $1
FOR UPDATE;

-- name: CancelPendingJob :execrows
UPDATE jobs
SET status = 'cancelled',
    cancel_requested_at = sqlc.arg(transition_time),
    finished_at = sqlc.arg(transition_time),
    updated_at = sqlc.arg(transition_time)
WHERE id = sqlc.arg(job_id)
  AND status IN ('queued', 'retry_wait')
  AND cancel_requested_at IS NULL;

-- name: RequestRunningJobCancellation :execrows
UPDATE jobs
SET cancel_requested_at = sqlc.arg(transition_time),
    updated_at = sqlc.arg(transition_time)
WHERE id = sqlc.arg(job_id)
  AND status = 'running'
  AND cancel_requested_at IS NULL;

-- name: GetJobAttempts :many
SELECT
    jobs.id AS job_id,
    job_attempts.attempt_no,
    job_attempts.worker_id,
    job_attempts.status,
    job_attempts.error_code,
    job_attempts.error_message,
    job_attempts.started_at,
    job_attempts.finished_at
FROM jobs
LEFT JOIN job_attempts ON job_attempts.job_id = jobs.id
WHERE jobs.id = $1
ORDER BY job_attempts.attempt_no;
