BEGIN;
ALTER TABLE departments ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE access_requests ADD COLUMN IF NOT EXISTS requested_role TEXT NOT NULL DEFAULT 'employee'
    CHECK (requested_role IN ('employee','manager','advisor'));
ALTER TABLE registration_drafts ADD COLUMN IF NOT EXISTS full_name TEXT NOT NULL DEFAULT '';
ALTER TABLE registration_drafts DROP CONSTRAINT IF EXISTS registration_drafts_step_check;
ALTER TABLE registration_drafts ADD CONSTRAINT registration_drafts_step_check CHECK (step IN ('name','department','role'));
-- Preserve older department-first drafts by asking for their name again.
UPDATE registration_drafts SET step='name',department_id=NULL WHERE full_name='';
-- Preserve a report's original department when its author changes role or department.
ALTER TABLE reports ADD COLUMN IF NOT EXISTS department_id BIGINT REFERENCES departments(id);
UPDATE reports r SET department_id=e.department_id FROM employees e WHERE r.employee_id=e.id AND r.department_id IS NULL;
UPDATE employees SET department_id=NULL WHERE role='advisor';
CREATE TABLE IF NOT EXISTS admin_drafts (
    telegram_id BIGINT PRIMARY KEY,
    action TEXT NOT NULL,
    target_id BIGINT NOT NULL DEFAULT 0,
    token TEXT NOT NULL,
    expected_version BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS management_audit (
    id BIGSERIAL PRIMARY KEY,
    actor_id BIGINT NOT NULL,
    action TEXT NOT NULL,
    target_id BIGINT NOT NULL,
    details JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
COMMIT;
