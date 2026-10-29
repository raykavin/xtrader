-- the record of the daily limits, never deleted: a limit changed (previous
-- and next settings, by whom, through the settings or the chat, why), a guard
-- tripped or released (by the system at the day's end or by a change of the
-- limits), the data incomplete or complete again, the positions closed
-- (action_id, the close_positions of supervisor_actions); metric the result
-- of the day then; of an agent (agent_id) or of all the agents (NULL); its
-- id the revision of the entries of the agents it concerns
CREATE TABLE IF NOT EXISTS daily_limit_events (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    day          DATE        NOT NULL,
    agent_id     BIGINT      REFERENCES agents (id),
    guard_id     BIGINT      REFERENCES daily_guards (id),
    kind         TEXT        NOT NULL CHECK (kind IN ('limit_changed', 'tripped', 'released', 'incomplete', 'flatten', 'day_rollover')),
    via          TEXT        NOT NULL CHECK (via IN ('system', 'settings', 'chat')),
    requested_by TEXT        NOT NULL DEFAULT '',
    user_id      BIGINT      REFERENCES users (id),
    reason       TEXT        NOT NULL DEFAULT '',
    previous     JSONB,
    next         JSONB,
    metric       JSONB,
    action_id    BIGINT      REFERENCES supervisor_actions (id)
);
CREATE INDEX IF NOT EXISTS idx_daily_limit_events_agent_id ON daily_limit_events (agent_id, id);
