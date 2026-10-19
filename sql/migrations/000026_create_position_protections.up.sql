-- the protections the XTrader AI armed on open positions: the breakeven or
-- the trailing stop (feature) of the position position_ref of an agent on a
-- pair (mt5:<identifier of the position>, paper:<run>:<entry>), asked by an
-- analysis (run_id) or a user confirming a proposal (user_id), for reason,
-- resting on evidence; the agent applies it once its trigger is reached:
-- requested while sent to it, armed waiting for its trigger, active once
-- applied, refused when not armed (eg: not allowed, another position), ended
-- once the position closed; its last move of the stop loss: previous,
-- requested and confirmed (by the broker), 0 for none, and move, confirmed,
-- rejected (the stop loss kept, detail telling why) or unknown (the answer
-- lost, the stop loss read again); one arm in force per position and feature
CREATE TABLE IF NOT EXISTS position_protections (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id     BIGINT           NOT NULL REFERENCES agents (id),
    run_id       BIGINT           REFERENCES supervisor_runs (id),
    user_id      BIGINT           REFERENCES users (id),
    pair         TEXT             NOT NULL CHECK (pair <> ''),
    position_ref TEXT             NOT NULL CHECK (position_ref <> ''),
    ticket       BIGINT           NOT NULL DEFAULT 0,
    side         TEXT             NOT NULL CHECK (side IN ('BUY', 'SELL')),
    feature      TEXT             NOT NULL CHECK (feature IN ('breakeven', 'trailing')),
    reason       TEXT             NOT NULL CHECK (reason <> ''),
    category     TEXT             CHECK (category IN ('operational', 'risk', 'performance')),
    evidence     JSONB            NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(evidence) = 'array'),
    requested_by TEXT             NOT NULL DEFAULT '',
    status       TEXT             NOT NULL CHECK (status IN ('requested', 'armed', 'active', 'refused', 'ended')),
    move         TEXT             NOT NULL DEFAULT '' CHECK (move IN ('', 'confirmed', 'rejected', 'unknown')),
    previous     DOUBLE PRECISION NOT NULL DEFAULT 0,
    requested    DOUBLE PRECISION NOT NULL DEFAULT 0,
    confirmed    DOUBLE PRECISION NOT NULL DEFAULT 0,
    detail       TEXT             NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ      NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ      NOT NULL DEFAULT now(),
    ended_at     TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_position_protections_in_force ON position_protections (agent_id, position_ref, feature) WHERE status IN ('requested', 'armed', 'active');
CREATE INDEX IF NOT EXISTS idx_position_protections_created_at ON position_protections (created_at DESC);
