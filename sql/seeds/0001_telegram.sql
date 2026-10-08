-- Serialized with the seeds of any other migrator running at once (eg: two
-- controllers starting): the file runs as one implicit transaction, holding
-- the lock to its end, and the statements after it see what the other one
-- inserted. The same key in every seed file.
SELECT pg_advisory_xact_lock(hashtext('xtrader.seeds'));

-- The Telegram bot of the controller, off until it has a token and the users
-- it answers (see the settings of the dashboard): no credential here.
INSERT INTO telegram_settings (id, enabled)
VALUES (1, FALSE)
ON CONFLICT (id) DO NOTHING;
