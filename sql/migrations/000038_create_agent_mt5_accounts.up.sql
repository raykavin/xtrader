-- the account of MetaTrader 5 each agent trades (see mt5_accounts): its
-- bridge and its token come from the account, agent_accounts keeping what
-- tells its orders apart there (magic, comment) and whether it may trade a real
-- account; changed only with the agent stopped and flat on the former one
CREATE TABLE IF NOT EXISTS agent_mt5_accounts (
    agent_id   BIGINT      PRIMARY KEY REFERENCES agents (id),
    account_id BIGINT      NOT NULL REFERENCES mt5_accounts (id),
    updated_by TEXT        NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_agent_mt5_accounts_account ON agent_mt5_accounts (account_id);
