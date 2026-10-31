-- the record of the trading hours, never deleted: the hours changed (previous
-- and next settings, by whom, why), the window closed (the entries of the
-- agents blocked), opened or released (the hours off: the entries free
-- again), and the actions at its end (action_id, the close_positions or the
-- cancel_pending of supervisor_actions, and how it ended); the last of
-- closed, opened and released is how the hours stand; its id the revision of
-- the entries of the agents
CREATE TABLE IF NOT EXISTS trading_hours_events (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    kind         TEXT        NOT NULL CHECK (kind IN ('changed', 'closed', 'opened', 'released', 'end_action')),
    requested_by TEXT        NOT NULL DEFAULT '',
    user_id      BIGINT      REFERENCES users (id),
    reason       TEXT        NOT NULL DEFAULT '',
    previous     JSONB,
    next         JSONB,
    action_id    BIGINT      REFERENCES supervisor_actions (id),
    status       TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_trading_hours_events_state ON trading_hours_events (id)
    WHERE kind IN ('closed', 'opened', 'released');
