-- Serialized with the seeds of any other migrator running at once (see
-- 0002_prompts.sql): the same key in every seed file.
SELECT pg_advisory_xact_lock(hashtext('xtrader.seeds'));

-- The agents of a new database, both off: each inserted once, when no agent
-- ever had its name (one deleted in the dashboard included), so the seeds, run
-- again on every migration, never overwrite an edit nor bring back an agent
-- deleted. Their secrets never go here: set the API key of the LLM, the token
-- of the bridge (MT5_BRIDGE_TOKEN of services/mt5/.env) and the one of the MCP
-- server of the terminal on the page of the settings of the dashboard, then
-- turn the agent on. The terminal of MT5 is the service mt5 of the compose:
-- its bridge at http://mt5:18812 and its MCP server (the economic calendar) at
-- http://mt5:22347/mcp. Never run the same agent here and on another machine
-- at once: they would trade the same account with the same magic number.

-- llm: the LLM strategy (ChatGPT) on BTCUSDm and XAUUSDm, the prompt
-- trend_following on 30m with the trend of 4h required, without take profit,
-- gpt-5 with a low reasoning effort, the lot and the correlated markets of
-- each pair, operated from the dashboard and from Telegram.
WITH agent AS (
    INSERT INTO agents (name, kind, enabled, mode, api_address, launched, restart, trade_panel, telegram_control,
                        params, position)
    SELECT 'llm', 'llm', FALSE, 'live', 'localhost:5580', TRUE, TRUE, TRUE, TRUE,
           '{"pair": "BTCUSDm;XAUUSDm", "timeframe": "30m", "lot": "BTCUSDm=0.01;XAUUSDm=0.02",
             "leverage": "400"}'::jsonb,
           (SELECT COALESCE(MAX(position) + 1, 0) FROM agents WHERE deleted_at IS NULL)
    WHERE NOT EXISTS (SELECT 1 FROM agents WHERE name = 'llm')
    RETURNING id
), account AS (
    INSERT INTO agent_accounts (agent_id, exchange, bridge_url)
    SELECT id, 'mt5', 'http://mt5:18812' FROM agent
)
INSERT INTO agent_llm_settings (agent_id, provider, model, reasoning_effort, prompt_id, calendar_url, params)
SELECT agent.id, 'openai', 'gpt-5', 'low',
       (SELECT id FROM prompts WHERE name = 'trend_following' AND deleted_at IS NULL),
       'http://mt5:22347/mcp',
       '{"llm-setup": "free", "llm-candles": "60", "llm-max-spread-atr": "0.2", "llm-min-stop-atr": "1",
         "llm-higher-trend-required": "true", "llm-higher-timeframes": "4h",
         "llm-correlated": "BTCUSDm=DXYm+XAUUSDm;XAUUSDm=DXYm+USTECm", "calendar-importance": "high"}'::jsonb
FROM agent;

-- matrend-btc: a strategy of the agents on the same terminal, matrend on
-- BTCUSDm 5m (agents/trendagent), a stop of 1 and a take profit of 2 in
-- price, operated from Telegram.
WITH agent AS (
    INSERT INTO agents (name, kind, enabled, mode, api_address, launched, restart, trade_panel, telegram_control,
                        params, position)
    SELECT 'matrend-btc', 'trend', FALSE, 'live', 'localhost:5570', TRUE, TRUE, FALSE, TRUE,
           '{"strategy": "matrend", "pair": "BTCUSDm", "timeframe": "5m", "lot": "0.01", "leverage": "20",
             "stop-value": "1", "take-profit-value": "2", "max-distance": "0.5"}'::jsonb,
           (SELECT COALESCE(MAX(position) + 1, 0) FROM agents WHERE deleted_at IS NULL)
    WHERE NOT EXISTS (SELECT 1 FROM agents WHERE name = 'matrend-btc')
    RETURNING id
)
INSERT INTO agent_accounts (agent_id, exchange, bridge_url)
SELECT id, 'mt5', 'http://mt5:18812' FROM agent;
