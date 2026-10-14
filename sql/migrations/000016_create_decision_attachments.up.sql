-- the attachments of the decisions, eg: an image the model answered with
CREATE TABLE IF NOT EXISTS decision_attachments (
    agent_id    BIGINT  NOT NULL,
    decision_id TEXT    NOT NULL,
    number      INTEGER NOT NULL CHECK (number >= 0),
    media_type  TEXT    NOT NULL,
    data        BYTEA   NOT NULL,
    PRIMARY KEY (agent_id, decision_id, number),
    FOREIGN KEY (agent_id, decision_id) REFERENCES decisions (agent_id, id)
);
