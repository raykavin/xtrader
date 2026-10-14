-- the drawings of the decisions of the LLM agents whose prompt draws (# Drawings:
-- on): the structures of the analysis of the model on the chart, its zones,
-- levels and markers, the list of a decision in its order
CREATE TABLE IF NOT EXISTS decision_drawings (
    agent_id    BIGINT NOT NULL,
    decision_id TEXT   NOT NULL,
    drawings    JSONB  NOT NULL CHECK (jsonb_typeof(drawings) = 'array'),
    PRIMARY KEY (agent_id, decision_id),
    FOREIGN KEY (agent_id, decision_id) REFERENCES decisions (agent_id, id)
);
