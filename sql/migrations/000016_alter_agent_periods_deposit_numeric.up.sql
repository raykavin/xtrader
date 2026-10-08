-- the deposit of the periods in cents, exactly: NUMERIC(16,2) rather than
-- DOUBLE PRECISION, the ones kept rounded to the cent as the store rounded
-- them; its CHECK (deposit >= 0) kept
ALTER TABLE agent_periods ALTER COLUMN deposit TYPE NUMERIC(16, 2) USING round(deposit::numeric, 2);
