-- the deposit of the periods back to DOUBLE PRECISION, its cents kept
ALTER TABLE agent_periods ALTER COLUMN deposit TYPE DOUBLE PRECISION USING deposit::double precision;
