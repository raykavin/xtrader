-- the switch of the reanalysis of the LLM agents ("Permitir reanálise" on the
-- dashboard): whether the language model of an agent may be asked again
-- between its closes, from the dashboard or from Telegram (its controls on
-- there), apart from its trade panel; a row for each LLM agent: an agent
-- without one may not be asked, as the ones of the other kinds, without a
-- language model
CREATE TABLE IF NOT EXISTS agent_reanalysis (
    agent_id BIGINT  PRIMARY KEY REFERENCES agents (id),
    allowed  BOOLEAN NOT NULL
);
