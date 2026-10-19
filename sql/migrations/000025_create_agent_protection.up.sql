-- the protection of the open positions of each agent, applied by the agent on
-- every price, live (no restart): the breakeven, the stop loss moved to the
-- entry beyond it by breakeven_margin and the costs known once the price went
-- breakeven_trigger in its favour; the trailing stop, the stop loss moved
-- trailing_distance behind the price once it went trailing_trigger in its
-- favour, every time it tightens by trailing_step at least; the *_supervisor
-- switches let the XTrader AI arm a feature on a position, its trigger kept;
-- every distance in unit, atr: multiples of the ATR(14) Wilder of the closed
-- candles of the timeframe of the agent; a feature enabled or allowed to the
-- supervisor has its distances; a table of its own, the one of the agents not
-- altered, a row for an agent once set, none for an agent without protection
CREATE TABLE IF NOT EXISTS agent_protection (
    agent_id             BIGINT           PRIMARY KEY REFERENCES agents (id),
    unit                 TEXT             NOT NULL DEFAULT 'atr' CHECK (unit IN ('atr')),
    breakeven_enabled    BOOLEAN          NOT NULL DEFAULT FALSE,
    breakeven_supervisor BOOLEAN          NOT NULL DEFAULT FALSE,
    breakeven_trigger    DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (breakeven_trigger BETWEEN 0 AND 50),
    breakeven_margin     DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (breakeven_margin BETWEEN 0 AND 50),
    trailing_enabled     BOOLEAN          NOT NULL DEFAULT FALSE,
    trailing_supervisor  BOOLEAN          NOT NULL DEFAULT FALSE,
    trailing_trigger     DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (trailing_trigger BETWEEN 0 AND 50),
    trailing_distance    DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (trailing_distance BETWEEN 0 AND 50),
    trailing_step        DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (trailing_step BETWEEN 0 AND 50),
    updated_by           TEXT             NOT NULL DEFAULT '',
    updated_at           TIMESTAMPTZ      NOT NULL DEFAULT now(),
    CHECK (NOT (breakeven_enabled OR breakeven_supervisor) OR
           (breakeven_trigger > 0 AND breakeven_margin < breakeven_trigger)),
    CHECK (NOT (trailing_enabled OR trailing_supervisor) OR
           (trailing_trigger > 0 AND trailing_distance > 0 AND trailing_step > 0 AND
            trailing_step <= trailing_distance))
);
