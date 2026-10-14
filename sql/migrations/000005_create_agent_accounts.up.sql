-- the trading account of an agent: the terminal of MT5 through its bridge, and
-- what tells the orders of the agent apart there, its magic number, a hash of
-- its comment unless set
CREATE TABLE IF NOT EXISTS agent_accounts (
    agent_id     BIGINT      PRIMARY KEY REFERENCES agents (id),
    exchange     TEXT        NOT NULL DEFAULT 'mt5' CHECK (exchange = 'mt5'),
    bridge_url   TEXT        NOT NULL DEFAULT 'http://127.0.0.1:18812',
    bridge_token BYTEA,       -- encrypted
    magic        BIGINT,      -- null: a hash of the comment
    comment      TEXT,        -- null: the default of the kind, eg: nb:llm
    real_allowed BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
