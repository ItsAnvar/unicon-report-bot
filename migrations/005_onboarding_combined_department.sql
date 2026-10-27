BEGIN;
ALTER TABLE access_requests ADD COLUMN IF NOT EXISTS department_id BIGINT REFERENCES departments(id);
CREATE TABLE IF NOT EXISTS registration_drafts (
    telegram_id BIGINT PRIMARY KEY,
    telegram_username TEXT NOT NULL DEFAULT '',
    department_id BIGINT REFERENCES departments(id),
    step TEXT NOT NULL CHECK (step IN ('department','name')),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- IUTXRvaEQB is one department. Preserve the old ID where possible.
CREATE TEMP TABLE _unicon_combined_merge_ids ON COMMIT DROP AS
SELECT id FROM departments WHERE name IN ('IUTXR','EQB','Asosiy bo‘lim','Asosiy bo???lim');
UPDATE departments SET name='IUTXRvaEQB'
WHERE id=(SELECT id FROM departments WHERE name IN ('IUTXR','EQB','Asosiy bo‘lim','Asosiy bo???lim')
          ORDER BY CASE WHEN name='IUTXR' THEN 0 ELSE 1 END,id LIMIT 1)
AND NOT EXISTS(SELECT 1 FROM departments WHERE name='IUTXRvaEQB');
INSERT INTO departments(name) VALUES('IUTXRvaEQB') ON CONFLICT(name) DO NOTHING;

UPDATE employees SET department_id=(SELECT id FROM departments WHERE name='IUTXRvaEQB')
WHERE department_id IS NULL OR department_id IN (SELECT id FROM _unicon_combined_merge_ids);
UPDATE access_requests SET department_id=(SELECT id FROM departments WHERE name='IUTXRvaEQB')
WHERE department_id IS NULL OR department_id IN (SELECT id FROM _unicon_combined_merge_ids);
UPDATE registration_drafts SET department_id=(SELECT id FROM departments WHERE name='IUTXRvaEQB')
WHERE department_id IN (SELECT id FROM _unicon_combined_merge_ids);
UPDATE notification_jobs SET
    department_id=(SELECT id FROM departments WHERE name='IUTXRvaEQB'),
    payload=CASE WHEN kind='report' THEN jsonb_set(payload,'{department_name}','"IUTXRvaEQB"'::jsonb) ELSE payload END,
    message=CASE WHEN delivered_at IS NULL THEN '' ELSE message END,
    message_scope=CASE WHEN delivered_at IS NULL THEN '' ELSE message_scope END,
    next_part=CASE WHEN delivered_at IS NULL THEN 0 ELSE next_part END
WHERE department_id IN (SELECT id FROM _unicon_combined_merge_ids);
DELETE FROM departments WHERE name IN ('IUTXR','EQB','Asosiy bo‘lim','Asosiy bo???lim');
COMMIT;
