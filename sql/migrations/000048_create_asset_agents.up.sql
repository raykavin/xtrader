-- the agents allowed on assets, of all the agents, a single row, set on the
-- dashboard (the tab Limites): rules, a JSON list of {"pairs": [...],
-- "agents": [...]}, each asset in one rule at most, on which only the agents
-- of its rule (by name) open positions; the assets without a rule free to
-- every agent; the exits, the stops, the targets and the positions open never
-- touched. revision grows with every change, the agents taking the newest;
-- none while never set
CREATE TABLE IF NOT EXISTS asset_agents (
    id         SMALLINT    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    rules      JSONB       NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(rules) = 'array'),
    revision   BIGINT      NOT NULL DEFAULT 0 CHECK (revision >= 0),
    updated_by TEXT        NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
