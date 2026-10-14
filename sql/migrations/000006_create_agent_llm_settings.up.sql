-- the language model of an LLM agent, with the reasoning efforts of the
-- current models: params holds its other settings by the name of their flag,
-- eg: {"llm-min-stop-atr": "1", "calendar-importance": "high"}; prompt_status
-- is the last prompt the agent applied or refused, and why
CREATE TABLE IF NOT EXISTS agent_llm_settings (
    agent_id         BIGINT      PRIMARY KEY REFERENCES agents (id),
    provider         TEXT        NOT NULL CHECK (provider IN ('openai', 'anthropic')),
    model            TEXT        NOT NULL CHECK (model <> ''),
    api_key          BYTEA,       -- encrypted
    base_url         TEXT,
    reasoning_effort TEXT        CHECK (reasoning_effort IN ('none', 'minimal', 'low', 'medium', 'high', 'xhigh')),
    max_tokens       INTEGER     CHECK (max_tokens > 0),
    prompt_id        BIGINT      REFERENCES prompts (id), -- null: the default one, embedded in the agent
    prompt_status    JSONB,
    calendar_url     TEXT,
    calendar_token   BYTEA,       -- encrypted
    params           JSONB       NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(params) = 'object'),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_agent_llm_settings_prompt_id ON agent_llm_settings (prompt_id);
