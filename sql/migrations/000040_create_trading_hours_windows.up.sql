-- the windows of the trading hours of all the agents (see trading_hours), in
-- the order of the day (position): on, no agent opens a position outside them,
-- and at the end of each the application closes every position of the agents
-- (flatten) and cancels their pending orders (cancel_pending), as checked;
-- start_time before end_time (HH:MM, of the zone of the controller,
-- America/Belem): a window never crosses midnight, nor overlaps or touches
-- another (checked by the application); trading_hours keeps the first one in
-- its own columns, for the readers of a single window, and while none is here
-- its own start_time and end_time are its only window
CREATE TABLE IF NOT EXISTS trading_hours_windows (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    hours_id       SMALLINT NOT NULL DEFAULT 1 REFERENCES trading_hours (id) ON DELETE CASCADE,
    position       SMALLINT NOT NULL CHECK (position >= 0),
    start_time     TEXT     NOT NULL CHECK (start_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    end_time       TEXT     NOT NULL CHECK (end_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    flatten        BOOLEAN  NOT NULL DEFAULT FALSE,
    cancel_pending BOOLEAN  NOT NULL DEFAULT FALSE,
    CHECK (start_time < end_time),
    UNIQUE (hours_id, position)
);
