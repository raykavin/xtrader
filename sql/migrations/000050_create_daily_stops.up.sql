-- the stop losses the daily loss limits ask of the open positions of the
-- agents, a row by position of an agent on an asset of an account a day (its
-- tickets moving together under one stop): the level within the loss left of
-- the limit of its scope (scope: '' all the agents, else the agent), the loss
-- left and the share of the position then (budget), and its last move at the
-- broker, through the agent running or the bridge of a stopped one: the stop
-- before, the one asked, the one the broker confirmed, or why it wasn't moved.
-- ended once its position closed, its limit turned off, or the day over
CREATE TABLE IF NOT EXISTS daily_stops (
    id         BIGINT           GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    day        DATE             NOT NULL,
    agent_id   BIGINT           NOT NULL REFERENCES agents (id),
    account    TEXT             NOT NULL CHECK (account <> ''),
    pair       TEXT             NOT NULL CHECK (pair <> ''),
    side       TEXT             NOT NULL CHECK (side IN ('BUY', 'SELL')),
    tickets    JSONB            NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(tickets) = 'array'),
    scope      TEXT             NOT NULL DEFAULT '',
    currency   TEXT             NOT NULL DEFAULT '',
    level      DOUBLE PRECISION NOT NULL CHECK (level > 0),
    via        TEXT             NOT NULL DEFAULT '' CHECK (via IN ('', 'agent', 'bridge')),
    move       TEXT             NOT NULL DEFAULT ''
        CHECK (move IN ('', 'requested', 'confirmed', 'rejected', 'unknown', 'skipped')),
    previous   DOUBLE PRECISION NOT NULL DEFAULT 0,
    requested  DOUBLE PRECISION NOT NULL DEFAULT 0,
    confirmed  DOUBLE PRECISION NOT NULL DEFAULT 0,
    detail     TEXT             NOT NULL DEFAULT '',
    budget     JSONB            NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(budget) = 'object'),
    attempts   INTEGER          NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    created_at TIMESTAMPTZ      NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ      NOT NULL DEFAULT now(),
    ended_at   TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS daily_stops_position ON daily_stops (day, agent_id, account, pair)
    WHERE ended_at IS NULL;

CREATE INDEX IF NOT EXISTS daily_stops_in_force ON daily_stops (agent_id) WHERE ended_at IS NULL;
