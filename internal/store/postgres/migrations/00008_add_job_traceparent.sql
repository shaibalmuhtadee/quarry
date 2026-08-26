-- +goose Up
ALTER TABLE jobs
    ADD COLUMN traceparent TEXT,
    ADD CONSTRAINT jobs_traceparent_valid CHECK (
        traceparent IS NULL
        OR (
            traceparent ~ '^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$'
            AND split_part(traceparent, '-', 2) <> repeat('0', 32)
            AND split_part(traceparent, '-', 3) <> repeat('0', 16)
        )
    );

-- +goose Down
ALTER TABLE jobs
    DROP COLUMN traceparent;
