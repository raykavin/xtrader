-- the prompts of the LLM agents, the strategy the model trades: the content
-- with its header lines, eg: # Indicators: and # Take profit:, which the agent
-- reads; version counts the edits
CREATE TABLE IF NOT EXISTS prompts (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        TEXT        NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9_.-]{0,63}$'),
    description TEXT        NOT NULL DEFAULT '',
    content     TEXT        NOT NULL CHECK (btrim(content) <> ''),
    version     INTEGER     NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_prompts_name ON prompts (name) WHERE deleted_at IS NULL;
