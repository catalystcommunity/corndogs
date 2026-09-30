-- +goose Up
-- Resilience contract (0.8.0): task revisions, guarded tasks, and durable
-- receipts. Each new column has a constant default, so PostgreSQL 11 and later
-- add it without a table rewrite.
ALTER TABLE tasks ADD COLUMN revision bigint NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN guarded boolean NOT NULL DEFAULT false;
ALTER TABLE archived_tasks ADD COLUMN revision bigint NOT NULL DEFAULT 0;
ALTER TABLE archived_tasks ADD COLUMN guarded boolean NOT NULL DEFAULT false;

CREATE TABLE submission_receipts (
    queue text NOT NULL,
    submission_key text NOT NULL,
    request_digest bytea NOT NULL,
    task_uuid uuid NOT NULL,
    accepted_at bigint NOT NULL,
    expires_at bigint NOT NULL,
    guarded boolean NOT NULL,
    PRIMARY KEY (queue, submission_key)
);
CREATE INDEX submission_receipts_idx_expires_at ON submission_receipts (expires_at);

CREATE TABLE operation_receipts (
    operation_id text PRIMARY KEY,
    op text NOT NULL,
    request_digest bytea NOT NULL,
    task_uuid uuid NOT NULL,
    queue text NOT NULL,
    at bigint NOT NULL,
    expires_at bigint NOT NULL,
    result jsonb NOT NULL
);
CREATE INDEX operation_receipts_idx_expires_at ON operation_receipts (expires_at);

-- +goose Down
DROP TABLE operation_receipts;
DROP TABLE submission_receipts;
ALTER TABLE archived_tasks DROP COLUMN guarded;
ALTER TABLE archived_tasks DROP COLUMN revision;
ALTER TABLE tasks DROP COLUMN guarded;
ALTER TABLE tasks DROP COLUMN revision;
