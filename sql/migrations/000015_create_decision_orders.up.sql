-- the orders of each decision: placed by it, the first decision listing the
-- order, or moved by it. order_id is a weak reference, without a foreign key:
-- the decision may reach the database before the orders it placed
CREATE TABLE IF NOT EXISTS decision_orders (
    agent_id    BIGINT  NOT NULL,
    decision_id TEXT    NOT NULL,
    order_id    BIGINT  NOT NULL,
    placed      BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (agent_id, decision_id, order_id),
    FOREIGN KEY (agent_id, decision_id) REFERENCES decisions (agent_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_decision_orders_agent_id_order_id ON decision_orders (agent_id, order_id) WHERE placed;
