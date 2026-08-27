-- name: GetQueueSnapshot :one
WITH clock AS (
    SELECT statement_timestamp() AS now
)
SELECT
    COUNT(*) FILTER (WHERE status = 'queued')::bigint AS queued_jobs,
    COUNT(*) FILTER (WHERE status = 'retry_wait')::bigint AS retry_wait_jobs,
    COALESCE(
        GREATEST(
            EXTRACT(EPOCH FROM (
                (SELECT now FROM clock)
                - MIN(available_at) FILTER (
                    WHERE status IN ('queued', 'retry_wait')
                      AND available_at <= (SELECT now FROM clock)
                )
            )),
            0
        ),
        0
    )::double precision AS oldest_eligible_age_seconds,
    COUNT(*) FILTER (WHERE status = 'running')::bigint AS active_jobs,
    (SELECT COUNT(*) FROM workers WHERE state = 'active')::bigint AS active_workers
FROM jobs;
