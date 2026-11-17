-- the least margin of a daily loss limit for new entries, set on the
-- dashboard (the tab Limites, the limit of an agent): in percent of its loss
-- limit (0 off); the loss left to the limit below it, the entries of its
-- agents blocked without closing anything (see daily_guards, kind margin). A
-- row for all the agents (agent_id NULL) or an agent once changed; none: 10
CREATE TABLE IF NOT EXISTS daily_limit_margins (
    id             BIGINT           GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id       BIGINT           REFERENCES agents (id),
    min_margin_pct DOUBLE PRECISION NOT NULL CHECK (min_margin_pct >= 0 AND min_margin_pct < 100),
    updated_by     TEXT             NOT NULL DEFAULT '',
    updated_at     TIMESTAMPTZ      NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS daily_limit_margins_scope ON daily_limit_margins ((COALESCE(agent_id, 0)));
