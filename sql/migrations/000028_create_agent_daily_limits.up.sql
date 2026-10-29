-- the daily limits of each agent, apart from the ones of all the agents
-- together (daily_limit_settings): its result of the day, realized plus open,
-- in the currency of its account; at max_loss lost or max_gain won its
-- positions are closed, its pending entries canceled and its entries blocked
-- until the next day; a row for an agent once set, none without limits
CREATE TABLE IF NOT EXISTS agent_daily_limits (
    agent_id   BIGINT           PRIMARY KEY REFERENCES agents (id),
    enabled    BOOLEAN          NOT NULL DEFAULT FALSE,
    max_loss   DOUBLE PRECISION CHECK (max_loss > 0),
    max_gain   DOUBLE PRECISION CHECK (max_gain > 0),
    updated_by TEXT             NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ      NOT NULL DEFAULT now(),
    CHECK (NOT enabled OR max_loss IS NOT NULL OR max_gain IS NOT NULL)
);
