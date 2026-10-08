-- the reasoning efforts of the current models: none and xhigh besides minimal,
-- low, medium and high; the CHECK of the column replaced, its values kept
ALTER TABLE agent_llm_settings DROP CONSTRAINT IF EXISTS agent_llm_settings_reasoning_effort_check;
ALTER TABLE agent_llm_settings ADD CONSTRAINT agent_llm_settings_reasoning_effort_check
    CHECK (reasoning_effort IN ('none', 'minimal', 'low', 'medium', 'high', 'xhigh'));
