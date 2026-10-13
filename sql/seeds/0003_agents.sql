-- Serialized with the seeds of any other migrator running at once (see
-- 0002_prompts.sql): the same key in every seed file.
SELECT pg_advisory_xact_lock(hashtext('xtrader.seeds'));

-- The agents of a new database, all of them off: each inserted once, when no agent
-- ever had its name (one deleted in the dashboard, or renamed since, see
-- agent_former_names, included), so the seeds, run again on every migration,
-- never overwrite an edit nor bring back an agent deleted or renamed. Their secrets never go here: set the API key of the LLM, the token
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
      AND NOT EXISTS (SELECT 1 FROM agent_former_names WHERE name = 'llm')
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
      AND NOT EXISTS (SELECT 1 FROM agent_former_names WHERE name = 'matrend-btc')
    RETURNING id
)
INSERT INTO agent_accounts (agent_id, exchange, bridge_url)
SELECT id, 'mt5', 'http://mt5:18812' FROM agent;

-- llm-smc: the LLM strategy with the prompt smc_ict (Smart Money Concepts /
-- ICT: the pending limit orders at the zones of the structure, the analysis
-- drawn on the chart), on the pairs, the lots, the timeframe, the higher
-- timeframe of the bias, the correlated markets and the model of the agent
-- llm, operated from the dashboard and from Telegram. Its comment of its own,
-- nb:llm-smc, gives it a magic number of its own: its orders and positions are
-- kept apart from the ones of llm on the same account. The rules of risk the
-- prompt measures with (llm-min-rr, llm-min-stop-atr, llm-max-spread-atr) and
-- the expiry of its pending orders (llm-pending-expiry) are left to the
-- operator, set in its parameters before it is turned on.
WITH agent AS (
    INSERT INTO agents (name, kind, enabled, mode, api_address, launched, restart, trade_panel, telegram_control,
                        params, position)
    SELECT 'llm-smc', 'llm', FALSE, 'live', 'localhost:5581', TRUE, TRUE, TRUE, TRUE,
           '{"pair": "BTCUSDm;XAUUSDm", "timeframe": "30m", "lot": "BTCUSDm=0.01;XAUUSDm=0.02",
             "leverage": "400"}'::jsonb,
           (SELECT COALESCE(MAX(position) + 1, 0) FROM agents WHERE deleted_at IS NULL)
    WHERE NOT EXISTS (SELECT 1 FROM agents WHERE name = 'llm-smc')
      AND NOT EXISTS (SELECT 1 FROM agent_former_names WHERE name = 'llm-smc')
    RETURNING id
), account AS (
    INSERT INTO agent_accounts (agent_id, exchange, bridge_url, comment)
    SELECT id, 'mt5', 'http://mt5:18812', 'nb:llm-smc' FROM agent
)
INSERT INTO agent_llm_settings (agent_id, provider, model, reasoning_effort, prompt_id, calendar_url, params)
SELECT agent.id, 'openai', 'gpt-5', 'low',
       (SELECT id FROM prompts WHERE name = 'smc_ict' AND deleted_at IS NULL),
       'http://mt5:22347/mcp',
       '{"llm-setup": "free", "llm-candles": "60", "llm-higher-timeframes": "4h",
         "llm-correlated": "BTCUSDm=DXYm+XAUUSDm;XAUUSDm=DXYm+USTECm", "calendar-importance": "high"}'::jsonb
FROM agent;

-- fibonacci-btc and fibonacci-xau: the strategy fibonacci (agents/trendagent),
-- the pullbacks of the impulses to their Fibonacci retracements, without LLM,
-- on BTCUSDm and on XAUUSDm, an agent each, on 15m: the impulses between swings
-- of 5 candles, 3 ATR long at least, entered on the close of a candle rejecting
-- the zone from 50% to 61.8%, the stop loss 0.25 ATR beyond 78.6% and the take
-- profit at the extreme of the impulse, operated from Telegram. Their comments
-- of their own give them magic numbers of their own on the account.
WITH agent AS (
    INSERT INTO agents (name, kind, enabled, mode, api_address, launched, restart, trade_panel, telegram_control,
                        params, position)
    SELECT 'fibonacci-btc', 'trend', FALSE, 'live', 'localhost:5571', TRUE, TRUE, FALSE, TRUE,
           '{"strategy": "fibonacci", "pair": "BTCUSDm", "timeframe": "15m", "lot": "0.01", "leverage": "20",
             "fib-swing": "pivot", "fib-pivot": "5", "fib-min-impulse-atr": "3", "fib-entry-low": "0.5",
             "fib-entry-high": "0.618", "fib-trigger": "close", "fib-stop-level": "0.786",
             "fib-stop-buffer-atr": "0.25", "fib-target": "extreme"}'::jsonb,
           (SELECT COALESCE(MAX(position) + 1, 0) FROM agents WHERE deleted_at IS NULL)
    WHERE NOT EXISTS (SELECT 1 FROM agents WHERE name = 'fibonacci-btc')
      AND NOT EXISTS (SELECT 1 FROM agent_former_names WHERE name = 'fibonacci-btc')
    RETURNING id
)
INSERT INTO agent_accounts (agent_id, exchange, bridge_url, comment)
SELECT id, 'mt5', 'http://mt5:18812', 'nb:fibonacci-btc' FROM agent;

WITH agent AS (
    INSERT INTO agents (name, kind, enabled, mode, api_address, launched, restart, trade_panel, telegram_control,
                        params, position)
    SELECT 'fibonacci-xau', 'trend', FALSE, 'live', 'localhost:5572', TRUE, TRUE, FALSE, TRUE,
           '{"strategy": "fibonacci", "pair": "XAUUSDm", "timeframe": "15m", "lot": "0.01", "leverage": "20",
             "fib-swing": "pivot", "fib-pivot": "5", "fib-min-impulse-atr": "3", "fib-entry-low": "0.5",
             "fib-entry-high": "0.618", "fib-trigger": "close", "fib-stop-level": "0.786",
             "fib-stop-buffer-atr": "0.25", "fib-target": "extreme"}'::jsonb,
           (SELECT COALESCE(MAX(position) + 1, 0) FROM agents WHERE deleted_at IS NULL)
    WHERE NOT EXISTS (SELECT 1 FROM agents WHERE name = 'fibonacci-xau')
      AND NOT EXISTS (SELECT 1 FROM agent_former_names WHERE name = 'fibonacci-xau')
    RETURNING id
)
INSERT INTO agent_accounts (agent_id, exchange, bridge_url, comment)
SELECT id, 'mt5', 'http://mt5:18812', 'nb:fibonacci-xau' FROM agent;

-- fimathe: the strategy fimathe (agents/breakoutagent), the first entry of the
-- trend of the reference channel of each week of the Fimathe technique (lesson
-- 3 of Fimathe 4.0), without LLM, on XAUUSDm and BTCUSDm: the analysis on 1h,
-- the entries on the closes of 15m, the stop loss beyond the neutral zone
-- surfing the subcycle by 50%, no take profit, at most 3% of the free balance
-- lost at the stop loss, its levels drawn on the chart, operated from
-- Telegram. Its comment of its own gives it a magic number of its own.
WITH agent AS (
    INSERT INTO agents (name, kind, enabled, mode, api_address, launched, restart, trade_panel, telegram_control,
                        params, position)
    SELECT 'fimathe', 'breakout', FALSE, 'live', 'localhost:5573', TRUE, TRUE, FALSE, TRUE,
           '{"strategy": "fimathe", "pair": "XAUUSDm;BTCUSDm", "timeframe": "15m", "lot": "0.01", "leverage": "20",
             "fimathe-analysis-timeframe": "1h", "fimathe-mapping": "week", "fimathe-mark": "auto",
             "fimathe-trail": "surf", "fimathe-trail-step": "0.5", "fimathe-max-risk-pct": "3"}'::jsonb,
           (SELECT COALESCE(MAX(position) + 1, 0) FROM agents WHERE deleted_at IS NULL)
    WHERE NOT EXISTS (SELECT 1 FROM agents WHERE name = 'fimathe')
      AND NOT EXISTS (SELECT 1 FROM agent_former_names WHERE name = 'fimathe')
    RETURNING id
)
INSERT INTO agent_accounts (agent_id, exchange, bridge_url, comment)
SELECT id, 'mt5', 'http://mt5:18812', 'nb:fimathe' FROM agent;
