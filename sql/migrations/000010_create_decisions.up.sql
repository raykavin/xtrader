-- the decisions of the LLM agents, one per call to the model, and what came of
-- them: their ID is their time and pair, eg: 20260928T123000.921355-XAUUSDm;
-- kept once, never updated nor deleted, no deleted_at
CREATE TABLE IF NOT EXISTS decisions (
    agent_id       BIGINT           NOT NULL REFERENCES agents (id),
    id             TEXT             NOT NULL,
    pair           TEXT             NOT NULL,
    timeframe      TEXT             NOT NULL DEFAULT '',
    candle_at      TIMESTAMPTZ      NOT NULL, -- the open time of the candle analysed
    decided_at     TIMESTAMPTZ      NOT NULL,
    trigger        TEXT             NOT NULL DEFAULT '',
    requested_by   TEXT             NOT NULL DEFAULT '',
    request_note   TEXT             NOT NULL DEFAULT '',
    model          TEXT             NOT NULL DEFAULT '',
    prompt_id      BIGINT           REFERENCES prompts (id),
    prompt_name    TEXT             NOT NULL DEFAULT '',
    prompt_version INTEGER,
    latency_ms     BIGINT           NOT NULL DEFAULT 0,
    price          DOUBLE PRECISION NOT NULL DEFAULT 0,
    position       TEXT             NOT NULL DEFAULT '',
    action         TEXT             NOT NULL DEFAULT '',
    confidence     DOUBLE PRECISION NOT NULL DEFAULT 0,
    reason         TEXT             NOT NULL DEFAULT '',
    entry          DOUBLE PRECISION,
    stop_loss      DOUBLE PRECISION,
    take_profit    DOUBLE PRECISION,
    levels         JSONB            NOT NULL DEFAULT '[]'::jsonb,
    notes          JSONB            NOT NULL DEFAULT '[]'::jsonb,
    outcome        TEXT             NOT NULL DEFAULT '',
    error          TEXT             NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ      NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, id)
);
CREATE INDEX IF NOT EXISTS idx_decisions_agent_id_decided_at ON decisions (agent_id, decided_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_decisions_agent_id_pair_decided_at ON decisions (agent_id, pair, decided_at DESC, id DESC);
