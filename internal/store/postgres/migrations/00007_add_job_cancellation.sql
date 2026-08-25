-- +goose Up
ALTER TABLE jobs
    ADD COLUMN cancel_requested_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE jobs
    DROP COLUMN cancel_requested_at;
