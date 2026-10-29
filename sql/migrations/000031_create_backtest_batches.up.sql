-- the batches of backtests asked on the dashboard: a name, the period traded
-- (from until before to), the share of it kept for the validation (the runs of
-- the same agents repeated on its end, apart from the fit, 0 for none), the
-- assumptions of the runs (the money, the costs, the execution), who asked
-- and when; their runs in backtest_runs
CREATE TABLE IF NOT EXISTS backtest_batches (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name             TEXT             NOT NULL DEFAULT '',
    period_from      TIMESTAMPTZ      NOT NULL,
    period_to        TIMESTAMPTZ      NOT NULL CHECK (period_to > period_from),
    validation_share DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (validation_share >= 0 AND validation_share < 1),
    assumptions      JSONB            NOT NULL CHECK (jsonb_typeof(assumptions) = 'object'),
    created_by       TEXT             NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ      NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_backtest_batches_created_at ON backtest_batches (created_at DESC);
