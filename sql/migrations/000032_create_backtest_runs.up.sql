-- the runs of a batch of backtests: an agent (its name copied: the run stays
-- whatever the agent becomes) on a segment of the period of the batch (fit,
-- or validation on its end), queued, running, then succeeded, failed,
-- canceled, interrupted (the controller stopped meanwhile) or
-- budget_exhausted (an LLM at its ceiling); its progress (candles processed
-- of all of them); the config it ran with, copied (the params of the agent,
-- its code and prompt versions, the assumptions); its summary (the metrics
-- of the comparison) and its result (the report: the trades, the equity, the
-- warnings and the usage of its model, if any)
CREATE TABLE IF NOT EXISTS backtest_runs (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    batch_id    BIGINT      NOT NULL REFERENCES backtest_batches (id) ON DELETE CASCADE,
    agent       TEXT        NOT NULL,
    kind        TEXT        NOT NULL,
    segment     TEXT        NOT NULL CHECK (segment IN ('fit', 'validation')),
    period_from TIMESTAMPTZ NOT NULL,
    period_to   TIMESTAMPTZ NOT NULL CHECK (period_to > period_from),
    status      TEXT        NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled',
                                                        'interrupted', 'budget_exhausted')),
    processed   BIGINT      NOT NULL DEFAULT 0 CHECK (processed >= 0),
    total       BIGINT      NOT NULL DEFAULT 0 CHECK (total >= 0),
    error       TEXT        NOT NULL DEFAULT '',
    config      JSONB       NOT NULL CHECK (jsonb_typeof(config) = 'object'),
    summary     JSONB,
    result      JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_backtest_runs_batch ON backtest_runs (batch_id, id);
CREATE INDEX IF NOT EXISTS idx_backtest_runs_status ON backtest_runs (status);
