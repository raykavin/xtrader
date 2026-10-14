-- the agents the controller serves and launches: params holds the settings of
-- their kind that no column holds, by the name of their flag, only the ones
-- set, eg: {"strategy": "matrend", "pair": "XAUUSDm;BTCUSDm", "timeframe": "15m"};
-- position is their order on the dashboard; order_id_next is the first ID of
-- the next range of order IDs leased to the agent
CREATE TABLE IF NOT EXISTS agents (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name             TEXT        NOT NULL CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'),
    kind             TEXT        NOT NULL CHECK (kind IN ('llm', 'trend', 'momentum', 'breakout', 'intraday')),
    enabled          BOOLEAN     NOT NULL DEFAULT FALSE,
    mode             TEXT        NOT NULL DEFAULT 'live' CHECK (mode IN ('live', 'paper')),
    api_address      TEXT        NOT NULL,
    launched         BOOLEAN     NOT NULL DEFAULT TRUE,
    restart          BOOLEAN     NOT NULL DEFAULT TRUE,
    trade_panel      BOOLEAN     NOT NULL DEFAULT FALSE,
    telegram_control BOOLEAN     NOT NULL DEFAULT FALSE,
    params           JSONB       NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(params) = 'object'),
    position         INTEGER     NOT NULL DEFAULT 0,
    order_id_next    BIGINT      NOT NULL DEFAULT 1 CHECK (order_id_next > 0),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at       TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_agents_name ON agents (name) WHERE deleted_at IS NULL;
