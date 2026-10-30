-- Serialized with the seeds of any other migrator running at once (eg: two
-- controllers starting): the file runs as one implicit transaction, holding
-- the lock to its end, and the statements after it see what the other one
-- inserted. The same key in every seed file.
SELECT pg_advisory_xact_lock(hashtext('xtrader.seeds'));

-- The news of the agents, off and without the token of the MCP server until
-- set on the dashboard (the tab Notícias); cmd/migrator takes the token the
-- LLM agents kept each, when they all held the same one. No credential here.
INSERT INTO news_settings (id, enabled)
VALUES (1, FALSE)
ON CONFLICT (id) DO NOTHING;
