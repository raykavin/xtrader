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

-- The rules of the news: the MCP server of the terminal of the stack, and the
-- window of the news block the LLM agents had by default, 15 minutes before
-- and after a release of high importance; changed on the dashboard.
INSERT INTO news_rules (id, mcp_url, block_before_minutes, block_after_minutes, block_importance)
VALUES (1, 'http://mt5:22347/mcp', 15, 15, 'high')
ON CONFLICT (id) DO NOTHING;
