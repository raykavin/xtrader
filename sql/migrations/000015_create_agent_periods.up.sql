-- the accounting periods of the agents: the cards of the dashboard (trades,
-- win rate, net profit, profit factor) count the trades closed since the start
-- of the last one, each reset of the results starting a new one; the earlier
-- ones are kept, their results those of the trades closed until the next one
-- started, the orders untouched; deposit is the balance the period started
-- with, in currency; kept once, never updated nor deleted, no deleted_at
CREATE TABLE IF NOT EXISTS agent_periods (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id   BIGINT           NOT NULL REFERENCES agents (id),
    started_at TIMESTAMPTZ      NOT NULL,
    deposit    DOUBLE PRECISION NOT NULL CHECK (deposit >= 0),
    currency   TEXT             NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    created_by TEXT             NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ      NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_periods_agent_id_started_at ON agent_periods (agent_id, started_at);
