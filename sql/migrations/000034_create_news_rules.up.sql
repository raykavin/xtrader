-- the rules of the news of all the agents, a single row, set on the dashboard
-- (the tab Notícias) with news_settings: the MCP server of the economic
-- calendar every agent reads (its address, its token in news_settings), and
-- the window of the news block, in minutes: no entry from block_before_minutes
-- before a release of at least block_importance to block_after_minutes after
-- it, the pending entries canceled and the positions closed as it starts
-- (both 0: none); updated_by is who changed them last, empty for the seeds or
-- the migrator. Its values are the ones of the seeds until changed.
-- cmd/migrator carries to it the server the LLM agents had each
-- (agent_llm_settings.calendar_url), see store.MigrateNews
CREATE TABLE IF NOT EXISTS news_rules (
    id                   SMALLINT    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    mcp_url              TEXT        CHECK (mcp_url ~ '^https?://'),
    block_before_minutes INTEGER     NOT NULL DEFAULT 0 CHECK (block_before_minutes BETWEEN 0 AND 1440),
    block_after_minutes  INTEGER     NOT NULL DEFAULT 0 CHECK (block_after_minutes BETWEEN 0 AND 1440),
    block_importance     TEXT        NOT NULL DEFAULT 'high' CHECK (block_importance IN ('low', 'medium', 'high')),
    updated_by           TEXT        NOT NULL DEFAULT '',
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
