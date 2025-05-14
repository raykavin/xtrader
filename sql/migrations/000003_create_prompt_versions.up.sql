-- every version of each prompt, the current one included, so a decision
-- points at the exact content it was taken with: append-only, no deleted_at
CREATE TABLE IF NOT EXISTS prompt_versions (
    prompt_id  BIGINT      NOT NULL REFERENCES prompts (id),
    version    INTEGER     NOT NULL CHECK (version > 0),
    content    TEXT        NOT NULL,
    created_by TEXT        NOT NULL DEFAULT '', -- who edited it, eg: web:xtrader, telegram:123456, seed
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (prompt_id, version)
);
