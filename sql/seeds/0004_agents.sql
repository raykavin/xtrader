-- Serialized with the seeds of any other migrator running at once (see
-- 0002_prompts.sql): the same key in every seed file.
SELECT pg_advisory_xact_lock(hashtext('xtrader.seeds'));

-- The agents of a new database, as the database had them, all of them off and
-- without their secrets: each inserted once, when no agent ever had its name
-- (one deleted in the dashboard, or renamed since, see agent_former_names,
-- included), so the seeds, run again on every migration, never overwrite an
-- edit nor bring back an agent deleted or renamed. Each trades XAUUSDm and
-- BTCUSDm, a lot of 0.01 on each. An agent finds its prompt by its name, never
-- by an ID.
--
-- Their secrets never go here: set the API key of the LLM, the token of the
-- bridge (MT5_BRIDGE_TOKEN of services/mt5/.env) and the one of the MCP server
-- of the terminal on the page of the settings of the dashboard, then turn the
-- agent on: off, the controller never launches it. The terminal of MT5 is the
-- service mt5 of the compose: its bridge at http://mt5:18812 and its MCP server
-- (the economic calendar) at http://mt5:22347/mcp. The comment of an agent on
-- MT5 (its default when null, eg: nb:llm) names its magic number, so it finds
-- its positions on the account again: never run the same agent here and on
-- another machine at once, they would trade the same positions.

-- llm-trend: the LLM strategy (ChatGPT) with the prompt trend_following on 15m,
-- consulted every 15 min, the trends of 1h and 4h as a filter, gpt-6-astra
-- with a high reasoning effort, operated from Telegram, its reanalysis
-- allowed; its comment the default of an LLM agent, nb:llm.
WITH agent AS (
    INSERT INTO agents (name, kind, enabled, mode, api_address, launched, restart, trade_panel, telegram_control,
                        params, position)
    SELECT 'llm-trend', 'llm', FALSE, 'live', 'localhost:5580', TRUE, TRUE, FALSE, TRUE,
           '{"pair": "XAUUSDm;BTCUSDm", "lot": "XAUUSDm=0.01;BTCUSDm=0.01", "timeframe": "15m",
             "leverage": "400"}'::jsonb,
           (SELECT COALESCE(MAX(position) + 1, 0) FROM agents WHERE deleted_at IS NULL)
    WHERE NOT EXISTS (SELECT 1 FROM agents WHERE name = 'llm-trend')
      AND NOT EXISTS (SELECT 1 FROM agent_former_names WHERE name = 'llm-trend')
    RETURNING id
), account AS (
    INSERT INTO agent_accounts (agent_id, exchange, bridge_url)
    SELECT id, 'mt5', 'http://mt5:18812' FROM agent
), reanalysis AS (
    INSERT INTO agent_reanalysis (agent_id, allowed)
    SELECT id, TRUE FROM agent
)
INSERT INTO agent_llm_settings (agent_id, provider, model, reasoning_effort, prompt_id, calendar_url, params)
SELECT agent.id, 'openai', 'gpt-6-astra', 'high',
       (SELECT id FROM prompts WHERE name = 'trend_following' AND deleted_at IS NULL),
       'http://mt5:22347/mcp',
       '{"llm-setup": "free", "llm-candles": "60", "llm-min-stop-atr": "1", "llm-max-spread-atr": "0.2",
         "calendar-importance": "high", "llm-consult-interval": "15m", "llm-higher-timeframes": "1h;4h",
         "llm-higher-trend-filter": "true", "llm-higher-trend-required": "false"}'::jsonb
FROM agent;

-- matrend: the strategy matrend (agents/trendagent) on 5m, a stop of 1 and a
-- take profit of 2 in price, operated from Telegram; its comment the default
-- of its strategy, nb:matrend.
WITH agent AS (
    INSERT INTO agents (name, kind, enabled, mode, api_address, launched, restart, trade_panel, telegram_control,
                        params, position)
    SELECT 'matrend', 'trend', FALSE, 'live', 'localhost:5570', TRUE, TRUE, FALSE, TRUE,
           '{"strategy": "matrend", "pair": "XAUUSDm;BTCUSDm", "lot": "XAUUSDm=0.01;BTCUSDm=0.01",
             "timeframe": "5m", "leverage": "400", "stop-value": "1", "take-profit-value": "2",
             "max-distance": "0.5"}'::jsonb,
           (SELECT COALESCE(MAX(position) + 1, 0) FROM agents WHERE deleted_at IS NULL)
    WHERE NOT EXISTS (SELECT 1 FROM agents WHERE name = 'matrend')
      AND NOT EXISTS (SELECT 1 FROM agent_former_names WHERE name = 'matrend')
    RETURNING id
)
INSERT INTO agent_accounts (agent_id, exchange, bridge_url)
SELECT id, 'mt5', 'http://mt5:18812' FROM agent;

-- llm-smc-liquidity: the LLM strategy with the prompt smc_ict (Smart Money
-- Concepts / ICT: the pending limit orders at the zones of the structure, the
-- analysis drawn on the chart) on 5m, consulted every 30 min, the bias of 1h,
-- 2000 candles of context, the model given 3 min, gpt-6-astra with a high
-- reasoning effort, operated from Telegram, its reanalysis allowed. Its comment
-- of its own, nb:llm-smc, gives it a magic number of its own, its orders and
-- positions kept apart from the ones of llm-trend on the same account.
WITH agent AS (
    INSERT INTO agents (name, kind, enabled, mode, api_address, launched, restart, trade_panel, telegram_control,
                        params, position)
    SELECT 'llm-smc-liquidity', 'llm', FALSE, 'live', 'localhost:5581', TRUE, TRUE, FALSE, TRUE,
           '{"pair": "XAUUSDm;BTCUSDm", "lot": "XAUUSDm=0.01;BTCUSDm=0.01", "timeframe": "5m",
             "leverage": "400"}'::jsonb,
           (SELECT COALESCE(MAX(position) + 1, 0) FROM agents WHERE deleted_at IS NULL)
    WHERE NOT EXISTS (SELECT 1 FROM agents WHERE name = 'llm-smc-liquidity')
      AND NOT EXISTS (SELECT 1 FROM agent_former_names WHERE name = 'llm-smc-liquidity')
    RETURNING id
), account AS (
    INSERT INTO agent_accounts (agent_id, exchange, bridge_url, comment)
    SELECT id, 'mt5', 'http://mt5:18812', 'nb:llm-smc' FROM agent
), reanalysis AS (
    INSERT INTO agent_reanalysis (agent_id, allowed)
    SELECT id, TRUE FROM agent
)
INSERT INTO agent_llm_settings (agent_id, provider, model, reasoning_effort, prompt_id, calendar_url, params)
SELECT agent.id, 'openai', 'gpt-6-astra', 'high',
       (SELECT id FROM prompts WHERE name = 'smc_ict' AND deleted_at IS NULL),
       'http://mt5:22347/mcp',
       '{"llm-setup": "free", "llm-candles": "2000", "llm-timeout": "3m", "calendar-importance": "high",
         "llm-consult-interval": "30m", "llm-higher-timeframes": "1h"}'::jsonb
FROM agent;

-- fibonacci: the strategy fibonacci (agents/trendagent), the pullbacks of the
-- impulses to their Fibonacci retracements, without LLM, on 15m: the impulses
-- between swings of 5 candles, 3 ATR long at least, entered on the close of a
-- candle rejecting the zone from 50% to 61.8%, the stop loss 0.25 ATR beyond
-- 78.6% and the take profit at the extreme of the impulse, operated from
-- Telegram. Its comment of its own, nb:fibonacci-xau (its name before it was
-- renamed), keeps the magic number of its positions.
WITH agent AS (
    INSERT INTO agents (name, kind, enabled, mode, api_address, launched, restart, trade_panel, telegram_control,
                        params, position)
    SELECT 'fibonacci', 'trend', FALSE, 'live', 'localhost:5572', TRUE, TRUE, FALSE, TRUE,
           '{"strategy": "fibonacci", "pair": "XAUUSDm;BTCUSDm", "lot": "XAUUSDm=0.01;BTCUSDm=0.01",
             "timeframe": "15m", "leverage": "2000", "fib-swing": "pivot", "fib-pivot": "5",
             "fib-min-impulse-atr": "3", "fib-entry-low": "0.5", "fib-entry-high": "0.618", "fib-trigger": "close",
             "fib-stop-level": "0.786", "fib-stop-buffer-atr": "0.25", "fib-target": "extreme"}'::jsonb,
           (SELECT COALESCE(MAX(position) + 1, 0) FROM agents WHERE deleted_at IS NULL)
    WHERE NOT EXISTS (SELECT 1 FROM agents WHERE name = 'fibonacci')
      AND NOT EXISTS (SELECT 1 FROM agent_former_names WHERE name = 'fibonacci')
    RETURNING id
)
INSERT INTO agent_accounts (agent_id, exchange, bridge_url, comment)
SELECT id, 'mt5', 'http://mt5:18812', 'nb:fibonacci-xau' FROM agent;

-- fimathe: the strategy fimathe (agents/breakoutagent), the first entry of the
-- trend of the reference channel of each week of the Fimathe technique (lesson
-- 3 of Fimathe 4.0), without LLM: the analysis on 1h, the entries on the closes
-- of 15m, the stop loss beyond the neutral zone surfing the subcycle by 50%, no
-- take profit, at most 3% of the free balance lost at the stop loss, its levels
-- drawn on the chart, operated from Telegram. Its comment of its own gives it a
-- magic number of its own.
WITH agent AS (
    INSERT INTO agents (name, kind, enabled, mode, api_address, launched, restart, trade_panel, telegram_control,
                        params, position)
    SELECT 'fimathe', 'breakout', FALSE, 'live', 'localhost:5573', TRUE, TRUE, FALSE, TRUE,
           '{"strategy": "fimathe", "pair": "XAUUSDm;BTCUSDm", "lot": "XAUUSDm=0.01;BTCUSDm=0.01",
             "timeframe": "15m", "leverage": "20", "fimathe-analysis-timeframe": "1h", "fimathe-mapping": "week",
             "fimathe-mark": "auto", "fimathe-trail": "surf", "fimathe-trail-step": "0.5",
             "fimathe-max-risk-pct": "3"}'::jsonb,
           (SELECT COALESCE(MAX(position) + 1, 0) FROM agents WHERE deleted_at IS NULL)
    WHERE NOT EXISTS (SELECT 1 FROM agents WHERE name = 'fimathe')
      AND NOT EXISTS (SELECT 1 FROM agent_former_names WHERE name = 'fimathe')
    RETURNING id
)
INSERT INTO agent_accounts (agent_id, exchange, bridge_url, comment)
SELECT id, 'mt5', 'http://mt5:18812', 'nb:fimathe' FROM agent;
