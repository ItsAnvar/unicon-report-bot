BEGIN;

-- Recover the known legacy name damaged by an earlier Windows SQL pipe too.
-- Rename first so existing department IDs are kept whenever possible.
UPDATE departments SET name = 'IUTXR'
WHERE id = (SELECT id FROM departments
            WHERE name IN ('Asosiy bo‘lim', 'Asosiy bo???lim') ORDER BY id LIMIT 1)
  AND NOT EXISTS (SELECT 1 FROM departments WHERE name = 'IUTXR');

-- Keep existing employees assigned to the renamed IUTXR department.
INSERT INTO departments (name)
VALUES ('IUTXR')
ON CONFLICT (name) DO NOTHING;

-- Add the second requested department.
INSERT INTO departments (name)
VALUES ('EQB')
ON CONFLICT (name) DO NOTHING;

UPDATE employees
SET department_id = (SELECT id FROM departments WHERE name = 'IUTXR')
WHERE department_id IN (
    SELECT id FROM departments WHERE name IN ('Asosiy bo‘lim', 'Asosiy bo???lim')
);

DELETE FROM departments
WHERE name IN ('Asosiy bo‘lim', 'Asosiy bo???lim');

-- Assign any employees without a department to IUTXR by default.
UPDATE employees
SET department_id = (SELECT id FROM departments WHERE name = 'IUTXR')
WHERE department_id IS NULL;

COMMIT;
