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
    finished_at;

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
    request_hash
FROM jobs
WHERE job_type = $1 AND idempotency_key = $2;

-- name: GetJob :one
SELECT id, job_type, payload, result, status, attempt_count, max_attempts, timeout_ms, created_at, updated_at, finished_at
FROM jobs
WHERE id = $1;

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
