-- the assets a window of the trading hours (see trading_hours_windows) lets
-- the agents open positions on, as MT5 names them (eg: XAUUSDm); a window
-- without any is for every asset; the actions at its end are for every
-- position whatever its assets
CREATE TABLE IF NOT EXISTS trading_hours_window_pairs (
    window_id BIGINT NOT NULL REFERENCES trading_hours_windows (id) ON DELETE CASCADE,
    pair      TEXT   NOT NULL CHECK (pair ~ '^[A-Za-z0-9][A-Za-z0-9._#-]{0,31}$'),
    PRIMARY KEY (window_id, pair)
);
