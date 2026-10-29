-- the daily limits of all the agents together: the result of the day (from
-- midnight of the zone of the controller, America/Belem), realized (the exits
-- of the day with their swap, commission and fee, the commission and fee of
-- the entries of the day) plus the profit open now of the positions of the
-- agents, in currency, each account converted to it; at max_loss lost or
-- max_gain won the application closes every position of the agents, cancels
-- their pending entries and blocks their entries until the next day (see
-- daily_guards); a row once set, none while never set (no limit)
CREATE TABLE IF NOT EXISTS daily_limit_settings (
    id         SMALLINT         PRIMARY KEY CHECK (id = 1),
    enabled    BOOLEAN          NOT NULL DEFAULT FALSE,
    currency   TEXT             NOT NULL DEFAULT '' CHECK (currency = '' OR currency ~ '^[A-Z]{3}$'),
    max_loss   DOUBLE PRECISION CHECK (max_loss > 0),
    max_gain   DOUBLE PRECISION CHECK (max_gain > 0),
    updated_by TEXT             NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ      NOT NULL DEFAULT now(),
    CHECK (NOT enabled OR ((max_loss IS NOT NULL OR max_gain IS NOT NULL) AND currency <> ''))
);
