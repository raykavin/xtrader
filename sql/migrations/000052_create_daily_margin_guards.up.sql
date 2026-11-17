-- the guards of the margin of the daily loss limits: the loss left to a loss
-- limit below its least for new entries (see daily_limit_margins), of all the
-- agents (agent_id NULL) or an agent, a day: the entries of its agents
-- blocked, nothing closed, until the margin is back, its least or its limit
-- turned off, or the day is over; the margin, its least and the result then,
-- in currency; why; tripped again after a release (trips). Their trips and
-- releases recorded in daily_limit_events (guard_id NULL, the guard in metric)
CREATE TABLE IF NOT EXISTS daily_margin_guards (
    id             BIGINT           GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    day            DATE             NOT NULL,
    agent_id       BIGINT           REFERENCES agents (id),
    state          TEXT             NOT NULL CHECK (state IN ('tripped', 'released')),
    margin         DOUBLE PRECISION NOT NULL DEFAULT 0,
    least          DOUBLE PRECISION NOT NULL DEFAULT 0,
    result         DOUBLE PRECISION NOT NULL DEFAULT 0,
    currency       TEXT             NOT NULL DEFAULT '',
    reason         TEXT             NOT NULL DEFAULT '',
    trips          INTEGER          NOT NULL DEFAULT 1 CHECK (trips > 0),
    tripped_at     TIMESTAMPTZ      NOT NULL DEFAULT now(),
    released_at    TIMESTAMPTZ,
    release_reason TEXT             NOT NULL DEFAULT '',
    updated_at     TIMESTAMPTZ      NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS daily_margin_guards_scope ON daily_margin_guards (day, (COALESCE(agent_id, 0)));
