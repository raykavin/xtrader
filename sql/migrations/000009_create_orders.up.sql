-- the orders of the agents as their order controller keeps them (model.Order):
-- their IDs of the agent, the timestamps as the agent sets them, and the
-- profit of the closing fills; updated in place, never deleted, no deleted_at
CREATE TABLE IF NOT EXISTS orders (
    agent_id     BIGINT           NOT NULL REFERENCES agents (id),
    id           BIGINT           NOT NULL CHECK (id > 0),
    exchange_id  BIGINT           NOT NULL,
    pair         TEXT             NOT NULL,
    side         TEXT             NOT NULL CHECK (side IN ('BUY', 'SELL')),
    type         TEXT             NOT NULL CHECK (type <> ''),
    status       TEXT             NOT NULL CHECK (status <> ''),
    price        DOUBLE PRECISION NOT NULL,
    quantity     DOUBLE PRECISION NOT NULL,
    fee          DOUBLE PRECISION NOT NULL DEFAULT 0,
    stop         DOUBLE PRECISION,
    group_id     BIGINT,
    ref_price    DOUBLE PRECISION NOT NULL DEFAULT 0,
    profit       DOUBLE PRECISION NOT NULL DEFAULT 0,
    profit_value DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ      NOT NULL,
    updated_at   TIMESTAMPTZ      NOT NULL,
    PRIMARY KEY (agent_id, id)
);
CREATE INDEX IF NOT EXISTS idx_orders_agent_id_updated_at ON orders (agent_id, updated_at, id);
