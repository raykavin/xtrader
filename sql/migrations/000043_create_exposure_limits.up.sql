-- the exposure limit of all the agents, a single row, set on the dashboard
-- (the tab Limites): at most max_agents agents holding a position or a
-- pending entry on an asset in the same direction at once (0 for none), pairs
-- that limit for some assets apart ({"XAUUSDm": 2}, 0 for none on it); an
-- entry beyond it refused, the exits, the stops, the targets and the orders by
-- hand never; none while never set
CREATE TABLE IF NOT EXISTS exposure_limits (
    id         SMALLINT    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    max_agents INTEGER     NOT NULL DEFAULT 0 CHECK (max_agents BETWEEN 0 AND 100),
    pairs      JSONB       NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(pairs) = 'object'),
    updated_by TEXT        NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
