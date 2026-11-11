-- Serialized with the seeds of any other migrator running at once (eg: two
-- controllers starting): the file runs as one implicit transaction, holding
-- the lock to its end, and the statements after it see what the other one
-- inserted. The same key in every seed file.
SELECT pg_advisory_xact_lock(hashtext('xtrader.seeds'));

-- The fixed role of the administrators: every permission of the dashboard, the
-- ones added later too (see ui.PermissionAreas), never changed nor deleted
-- there. cmd/migrator gives it once to the users of a database without roles
-- yet; store.Bootstrap to the first user of a new database.
INSERT INTO roles (name, description, system, updated_by)
VALUES ('Administrador', 'Todas as permissões, inclusive as que forem criadas depois.', 'admin', 'seeds')
ON CONFLICT (system) WHERE system <> '' DO NOTHING;
