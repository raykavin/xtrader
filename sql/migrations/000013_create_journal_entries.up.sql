-- the calls to the model of the LLM agents with their whole context, one per
-- decision, of the same ID: read back for the memory of the prompt, per pair;
-- append-only, no deleted_at
CREATE TABLE IF NOT EXISTS journal_entries (
    agent_id       BIGINT      NOT NULL REFERENCES agents (id),
    id             TEXT        NOT NULL,
    at             TIMESTAMPTZ NOT NULL,
    pair           TEXT        NOT NULL,
    trigger        TEXT        NOT NULL DEFAULT '',
    requested_by   TEXT        NOT NULL DEFAULT '',
    model          TEXT        NOT NULL DEFAULT '',
    prompt_id      BIGINT      REFERENCES prompts (id),
    prompt_name    TEXT        NOT NULL DEFAULT '',
    prompt_version INTEGER,
    latency_ms     BIGINT      NOT NULL DEFAULT 0,
    input          JSONB       NOT NULL,
    decision       JSONB,
    error          TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (agent_id, id)
);
CREATE INDEX IF NOT EXISTS idx_journal_entries_agent_id_pair_at ON journal_entries (agent_id, pair, at DESC);
