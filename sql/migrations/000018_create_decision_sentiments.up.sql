-- the sentiment of the decisions of the LLM agents: the reading of the market
-- of the symbol by the model, from 0 (extremely bearish) to 100 (extremely
-- bullish), 50 neutral, shown on the dashboard, it doesn't trade; a decision
-- without one (failed, skipped) has no row
CREATE TABLE IF NOT EXISTS decision_sentiments (
    agent_id    BIGINT           NOT NULL,
    decision_id TEXT             NOT NULL,
    sentiment   DOUBLE PRECISION NOT NULL CHECK (sentiment >= 0 AND sentiment <= 100),
    PRIMARY KEY (agent_id, decision_id),
    FOREIGN KEY (agent_id, decision_id) REFERENCES decisions (agent_id, id)
);
