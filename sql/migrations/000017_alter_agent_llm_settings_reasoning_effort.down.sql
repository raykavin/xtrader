-- the reasoning efforts back to minimal, low, medium and high: none and xhigh,
-- which the CHECK before refuses, left to the default of the model (NULL)
ALTER TABLE agent_llm_settings DROP CONSTRAINT IF EXISTS agent_llm_settings_reasoning_effort_check;
UPDATE agent_llm_settings SET reasoning_effort = NULL WHERE reasoning_effort IN ('none', 'xhigh');
ALTER TABLE agent_llm_settings ADD CONSTRAINT agent_llm_settings_reasoning_effort_check
    CHECK (reasoning_effort IN ('minimal', 'low', 'medium', 'high'));
