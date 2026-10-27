BEGIN;

CREATE TABLE IF NOT EXISTS departments (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO departments (name)
VALUES ('Asosiy bo‘lim')
ON CONFLICT (name) DO NOTHING;

ALTER TABLE employees
ADD COLUMN IF NOT EXISTS department_id BIGINT REFERENCES departments(id);

UPDATE employees
SET department_id = (SELECT id FROM departments WHERE name = 'Asosiy bo‘lim')
WHERE department_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_employees_department ON employees(department_id);

COMMIT;
