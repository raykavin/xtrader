-- the supervision of each agent: its entries blocked, by the supervisor or a
-- user, and why, entries_revision growing with every block and allowance so
-- that the agent never applies an older one; and its stop by the supervisor:
-- pending_flat while it waits for the agent to hold no position nor pending
-- order, stopped once disabled, until a user enables it again; a table of its
-- own, the one of the agents not altered, a row for an agent once supervised
CREATE TABLE IF NOT EXISTS agent_supervision (
    agent_id          BIGINT      PRIMARY KEY REFERENCES agents (id),
    entries_blocked   BOOLEAN     NOT NULL DEFAULT FALSE,
    entries_revision  BIGINT      NOT NULL DEFAULT 0 CHECK (entries_revision >= 0),
    entries_reason    TEXT        NOT NULL DEFAULT '',
    entries_by        TEXT        NOT NULL DEFAULT '',
    entries_action_id BIGINT      REFERENCES supervisor_actions (id),
    entries_at        TIMESTAMPTZ,
    stop_state        TEXT        NOT NULL DEFAULT 'none' CHECK (stop_state IN ('none', 'pending_flat', 'stopped')),
    stop_reason       TEXT        NOT NULL DEFAULT '',
    stop_category     TEXT        CHECK (stop_category IN ('operational', 'risk', 'performance')),
    stop_by           TEXT        NOT NULL DEFAULT '',
    stop_action_id    BIGINT      REFERENCES supervisor_actions (id),
    stop_at           TIMESTAMPTZ,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
