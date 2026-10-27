BEGIN;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM departments WHERE name='IUTXRvaEQB')
       AND EXISTS (SELECT 1 FROM departments WHERE name='Eri KORM') THEN
        RAISE EXCEPTION 'Both department names exist; resolve them without deleting user data';
    END IF;
    UPDATE departments SET name='Eri KORM' WHERE name='IUTXRvaEQB';
    IF NOT EXISTS (SELECT 1 FROM departments WHERE name='Eri KORM') THEN
        INSERT INTO departments(name) VALUES('Eri KORM');
    END IF;
END
$$;
COMMIT;
