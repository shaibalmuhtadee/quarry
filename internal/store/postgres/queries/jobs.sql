-- name: CreateJob :one
INSERT INTO jobs (
    id,
    job_type,
    payload,
    status,
    max_attempts,
    timeout_ms
)
VALUES ($1, $2, $3, 'queued', $4, $5)
RETURNING id, job_type, payload, result, status, attempt_count, max_attempts, timeout_ms, created_at, updated_at, finished_at;

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
