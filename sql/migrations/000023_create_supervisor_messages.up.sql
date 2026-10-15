-- the conversation of each user with the supervisor of the agents: the
-- questions, the answers (analyses), the proposals of actions, the results of
-- the ones confirmed, written once checked, and the errors, eg: the model
-- unavailable; data_at is when the data the answer rests on was read
CREATE TABLE IF NOT EXISTS supervisor_messages (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES users (id),
    role       TEXT        NOT NULL CHECK (role IN ('user', 'assistant')),
    kind       TEXT        NOT NULL CHECK (kind IN ('question', 'answer', 'proposal', 'result', 'error')),
    content    TEXT        NOT NULL DEFAULT '',
    action_id  BIGINT      REFERENCES supervisor_actions (id),
    data_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_supervisor_messages_user_id_id ON supervisor_messages (user_id, id DESC);
