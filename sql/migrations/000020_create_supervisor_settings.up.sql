-- the supervisor of the agents, a single row: a language model that analyzes
-- every agent each interval_seconds while enabled, and answers the users of
-- the dashboard; provider, model, base_url, reasoning_effort and max_tokens as
-- the ones of the LLM agents (agent_llm_settings), timeout_seconds bounding an
-- answer; instructions are its criteria, edited on the dashboard; autonomy is
-- what it does alone, eg: {"block_entries": true, "stop_when_flat": true,
-- "cancel_pending": false, "max_actions_per_run": 3}, the rest proposed to the
-- users; its values are the ones of the seeds until changed
CREATE TABLE IF NOT EXISTS supervisor_settings (
    id               SMALLINT    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    enabled          BOOLEAN     NOT NULL DEFAULT FALSE,
    interval_seconds INTEGER     NOT NULL CHECK (interval_seconds BETWEEN 300 AND 86400),
    provider         TEXT        CHECK (provider IN ('openai', 'anthropic')),
    model            TEXT        CHECK (model <> ''),
    api_key          BYTEA,       -- encrypted
    base_url         TEXT,
    reasoning_effort TEXT        CHECK (reasoning_effort IN ('none', 'minimal', 'low', 'medium', 'high', 'xhigh')),
    max_tokens       INTEGER     CHECK (max_tokens > 0),
    timeout_seconds  INTEGER     NOT NULL CHECK (timeout_seconds BETWEEN 10 AND 600),
    instructions     TEXT        NOT NULL DEFAULT '',
    autonomy         JSONB       NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(autonomy) = 'object'),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (NOT enabled OR (provider IS NOT NULL AND model IS NOT NULL AND api_key IS NOT NULL))
);
