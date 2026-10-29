-- the daily guards: a limit of a day reached (kind limit, trigger loss or
-- gain), or the result of the day unknown (kind incomplete, an account not
-- read or a rate missing), of an agent (agent_id) or of all the agents
-- together (agent_id NULL); tripped, the entries of its agents are blocked
-- (and, of a limit, their positions closed: flatten_*) until released, at the
-- next day or by an explicit change of the limits; result and limit_amount in
-- currency when tripped; trips counts the times it was tripped on its day;
-- one guard a day, scope and kind
CREATE TABLE IF NOT EXISTS daily_guards (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    day               DATE             NOT NULL,
    agent_id          BIGINT           REFERENCES agents (id),
    kind              TEXT             NOT NULL CHECK (kind IN ('limit', 'incomplete')),
    trigger           TEXT             CHECK (trigger IN ('loss', 'gain')),
    state             TEXT             NOT NULL CHECK (state IN ('tripped', 'released')),
    result            DOUBLE PRECISION NOT NULL DEFAULT 0,
    limit_amount      DOUBLE PRECISION NOT NULL DEFAULT 0,
    currency          TEXT             NOT NULL DEFAULT '',
    reason            TEXT             NOT NULL DEFAULT '',
    trips             INTEGER          NOT NULL DEFAULT 1 CHECK (trips > 0),
    tripped_at        TIMESTAMPTZ      NOT NULL DEFAULT now(),
    released_at       TIMESTAMPTZ,
    released_by       TEXT             NOT NULL DEFAULT '',
    release_reason    TEXT             NOT NULL DEFAULT '',
    flatten_status    TEXT             NOT NULL DEFAULT '' CHECK (flatten_status IN ('', 'running', 'done', 'partial', 'failed')),
    flatten_action_id BIGINT           REFERENCES supervisor_actions (id),
    flatten_attempts  INTEGER          NOT NULL DEFAULT 0 CHECK (flatten_attempts >= 0),
    updated_at        TIMESTAMPTZ      NOT NULL DEFAULT now(),
    CHECK (kind <> 'limit' OR trigger IS NOT NULL)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_daily_guards_scope ON daily_guards (day, COALESCE(agent_id, 0), kind);
CREATE INDEX IF NOT EXISTS idx_daily_guards_tripped ON daily_guards (state) WHERE state = 'tripped';
