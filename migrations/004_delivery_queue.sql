BEGIN;
ALTER TABLE employees ADD COLUMN IF NOT EXISTS telegram_username TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS notification_jobs (
    id BIGSERIAL PRIMARY KEY,
    event_key TEXT NOT NULL UNIQUE,
    recipient_telegram_id BIGINT NOT NULL REFERENCES employees(telegram_id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('report', 'daily_summary', 'role_summary')),
    department_id BIGINT REFERENCES departments(id),
    payload JSONB NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    message_scope TEXT NOT NULL DEFAULT '',
    next_part INTEGER NOT NULL DEFAULT 0 CHECK (next_part >= 0),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    locked_until TIMESTAMPTZ,
    lease BIGINT NOT NULL DEFAULT 0,
    delivered_at TIMESTAMPTZ,
    cancelled_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE notification_jobs ADD COLUMN IF NOT EXISTS message_scope TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_notification_jobs_pending ON notification_jobs(next_attempt_at,id)
WHERE delivered_at IS NULL AND cancelled_at IS NULL;
COMMIT;
