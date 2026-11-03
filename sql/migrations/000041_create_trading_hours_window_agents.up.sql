-- the agents allowed to open positions in a window of the trading hours (see
-- trading_hours_windows), by id, so that a rename keeps them; a window
-- without any is for every agent; the actions at its end are for every agent
-- whatever its agents
CREATE TABLE IF NOT EXISTS trading_hours_window_agents (
    window_id BIGINT NOT NULL REFERENCES trading_hours_windows (id) ON DELETE CASCADE,
    agent_id  BIGINT NOT NULL REFERENCES agents (id),
    PRIMARY KEY (window_id, agent_id)
);
