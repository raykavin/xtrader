-- the stops of the daily loss limits at the broker, set on the dashboard (the
-- tab Limites, the limit of an agent): while a loss limit of a scope is on,
-- enabled moves the stop losses of its open positions at the broker within
-- the loss left of the limit, every minute, only tightening them (see
-- controller/daily_stops.go). A row for all the agents (agent_id NULL) or an
-- agent once changed; none: on, the default
CREATE TABLE IF NOT EXISTS daily_limit_stops (
    id         BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id   BIGINT      REFERENCES agents (id),
    enabled    BOOLEAN     NOT NULL DEFAULT TRUE,
    updated_by TEXT        NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS daily_limit_stops_scope ON daily_limit_stops ((COALESCE(agent_id, 0)));
