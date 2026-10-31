-- the trading hours of all the agents, a single row, set on the dashboard
-- (the tab Limites): on, the agents open no position before start_time nor
-- from end_time (HH:MM, of the zone of the controller, America/Belem; a
-- window crossing midnight when start_time is after end_time); at end_time
-- the application closes every position of the agents (flatten) and cancels
-- their pending orders (cancel_pending), as checked; none while never set
CREATE TABLE IF NOT EXISTS trading_hours (
    id             SMALLINT    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    enabled        BOOLEAN     NOT NULL DEFAULT FALSE,
    start_time     TEXT        NOT NULL DEFAULT '' CHECK (start_time = '' OR start_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    end_time       TEXT        NOT NULL DEFAULT '' CHECK (end_time = '' OR end_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    flatten        BOOLEAN     NOT NULL DEFAULT FALSE,
    cancel_pending BOOLEAN     NOT NULL DEFAULT FALSE,
    updated_by     TEXT        NOT NULL DEFAULT '',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (NOT enabled OR (start_time <> '' AND end_time <> '' AND start_time <> end_time))
);
