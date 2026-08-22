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
RETURNING id, job_type, payload, status, attempt_count, max_attempts, timeout_ms, created_at, updated_at;

-- name: GetJob :one
SELECT id, job_type, payload, status, attempt_count, max_attempts, timeout_ms, created_at, updated_at
FROM jobs
WHERE id = $1;
