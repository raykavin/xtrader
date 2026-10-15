-- the analyses of the supervisor of the agents, periodic (schedule) or asked
-- on the dashboard (manual): running until they end, ok with the summary and
-- the findings of the model, failed with the error, eg: the model unavailable,
-- skipped when another one was running; snapshot is the data sent to the
-- model, exactly, never a secret; usage the tokens of its answers
CREATE TABLE IF NOT EXISTS supervisor_runs (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    origin       TEXT        NOT NULL CHECK (origin IN ('schedule', 'manual')),
    status       TEXT        NOT NULL CHECK (status IN ('running', 'ok', 'failed', 'skipped')),
    requested_by TEXT        NOT NULL DEFAULT '',
    model        TEXT        NOT NULL DEFAULT '',
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ,
    snapshot     JSONB,
    summary      TEXT        NOT NULL DEFAULT '',
    findings     JSONB       NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(findings) = 'array'),
    error        TEXT        NOT NULL DEFAULT '',
    usage        JSONB
);
CREATE INDEX IF NOT EXISTS idx_supervisor_runs_started_at ON supervisor_runs (started_at DESC);
