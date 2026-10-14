-- the positions the order controller of an agent discarded, closed outside the
-- bot (by hand in the terminal, by a stop out), which leave no fill: each one
-- after the last fill of its pair then, so the replay of the orders on the
-- next start drops it there again, rather than averaging the next trades of
-- the pair with it (model.Discard); written once, never updated nor deleted,
-- no deleted_at
CREATE TABLE IF NOT EXISTS position_discards (
    agent_id BIGINT           NOT NULL REFERENCES agents (id),
    pair     TEXT             NOT NULL CHECK (pair <> ''),
    after_id BIGINT           NOT NULL CHECK (after_id > 0),
    side     TEXT             NOT NULL CHECK (side IN ('BUY', 'SELL')),
    quantity DOUBLE PRECISION NOT NULL CHECK (quantity > 0),
    at       TIMESTAMPTZ      NOT NULL,
    PRIMARY KEY (agent_id, pair, after_id)
);
