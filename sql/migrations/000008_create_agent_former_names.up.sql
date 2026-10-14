-- the names an agent had before it was renamed: the seeds insert an agent
-- once, when no agent ever had its name, so an agent renamed (eg: llm to
-- llm-trend) isn't brought back under its old name
CREATE TABLE IF NOT EXISTS agent_former_names (
    agent_id   BIGINT      NOT NULL REFERENCES agents (id),
    name       TEXT        NOT NULL,
    renamed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, name)
);
CREATE INDEX IF NOT EXISTS idx_agent_former_names_name ON agent_former_names (name);
