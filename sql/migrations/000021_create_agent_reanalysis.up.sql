-- the switch of the reanalysis of the LLM agents ("Permitir reanálise" on the
-- dashboard): whether the language model of an agent may be asked again
-- between its closes, from the dashboard or from Telegram (its controls on
-- there), apart from its trade panel, which turned it on for the dashboard
-- until now; a table of its own, so the table of the agents isn't altered, a
-- row for each LLM agent: an agent without one may not be asked, as the ones
-- of the other kinds, without a language model
CREATE TABLE IF NOT EXISTS agent_reanalysis (
    agent_id BIGINT  PRIMARY KEY REFERENCES agents (id),
    allowed  BOOLEAN NOT NULL
);

-- the LLM agents keep the reanalysis they had: on in paper, whose control
-- panel is on in every channel, and in live with their trade panel or their
-- Telegram control on; the switches apart from then on
INSERT INTO agent_reanalysis (agent_id, allowed)
SELECT id, mode = 'paper' OR trade_panel OR telegram_control FROM agents WHERE kind = 'llm'
ON CONFLICT (agent_id) DO NOTHING;
