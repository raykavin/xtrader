-- the actions on the agents asked by the supervisor (alone, as its autonomy
-- allows, or proposed) or by the users: the entries blocked or allowed, an
-- agent stopped (when flat, or now), its pending entries canceled, positions
-- closed; scope holds the agents, the pairs and the tickets confirmed, options
-- what goes with it (cancel the pending entries, keep the entries blocked),
-- evidence the data it rests on; a proposal expires unconfirmed; result is
-- what was checked on the accounts once done; kept for good
CREATE TABLE IF NOT EXISTS supervisor_actions (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source       TEXT        NOT NULL CHECK (source IN ('supervisor', 'user')),
    run_id       BIGINT      REFERENCES supervisor_runs (id),
    user_id      BIGINT      REFERENCES users (id),
    action       TEXT        NOT NULL CHECK (action IN ('block_entries', 'allow_entries', 'stop_agent', 'cancel_pending',
                                                        'close_positions')),
    mode         TEXT        CHECK (mode IN ('when_flat', 'now')),
    scope        JSONB       NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(scope) = 'object'),
    options      JSONB       NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(options) = 'object'),
    reason       TEXT        NOT NULL CHECK (reason <> ''),
    category     TEXT        CHECK (category IN ('operational', 'risk', 'performance')),
    evidence     JSONB       NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(evidence) = 'array'),
    status       TEXT        NOT NULL CHECK (status IN ('proposed', 'executing', 'succeeded', 'partial', 'failed',
                                                        'unknown', 'rejected', 'expired')),
    result       JSONB,
    requested_by TEXT        NOT NULL DEFAULT '',
    confirmed_by TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ,
    decided_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_supervisor_actions_created_at ON supervisor_actions (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_supervisor_actions_status ON supervisor_actions (status);
