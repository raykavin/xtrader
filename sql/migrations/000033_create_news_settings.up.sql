-- the news of the agents, a single row, set on the dashboard (the tab
-- Notícias): the feature on or off for all the agents, and the token of the
-- MCP server of the economic calendar (MetaTrader 5: Tools > Options > the MCP
-- server), shared by the LLM agents, encrypted; updated_by is who changed them
-- last. Its values are the ones of the seeds until changed. cmd/migrator
-- carries to it the tokens the agents kept each (agent_llm_settings.
-- calendar_token), see store.MigrateNewsToken
CREATE TABLE IF NOT EXISTS news_settings (
    id         SMALLINT    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    enabled    BOOLEAN     NOT NULL DEFAULT FALSE,
    mcp_token  BYTEA,      -- encrypted
    updated_by TEXT        NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
