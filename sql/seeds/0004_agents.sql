-- Serialized with the seeds of any other migrator running at once (see
-- 0002_prompts.sql): the same key in every seed file.
SELECT pg_advisory_xact_lock(hashtext('xtrader.seeds'));

-- The agents of a new database, all of them off and without their secrets:
-- the ones the database had, an LLM agent for each prompt of a strategy only a
-- language model trades, and an agent without LLM for each strategy of the
-- code. Each inserted once, when no agent ever had its name (one deleted in
-- the dashboard, or renamed since, see agent_former_names, included), so the
-- seeds, run again on every migration, never overwrite an edit nor bring back
-- an agent deleted or renamed. Each trades XAUUSDm and BTCUSDm, a lot of 0.01
-- on each. An agent finds its prompt by its name, never by an ID.
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

-- matrend: the strategy matrend (agents/trendagent) on 5m, a stop of 5.4 and a
-- take profit of 10.8 in the currency of the account (BRL: about 1 and 2 in
-- price for a unit of XAUUSDm, its point value 5.39 BRL on 2026-10-29),
-- operated from Telegram; its comment the default of its strategy, nb:matrend.
WITH agent AS (
    INSERT INTO agents (name, kind, enabled, mode, api_address, launched, restart, trade_panel, telegram_control,
                        params, position)
    SELECT 'matrend', 'trend', FALSE, 'live', 'localhost:5570', TRUE, TRUE, FALSE, TRUE,
           '{"strategy": "matrend", "pair": "XAUUSDm;BTCUSDm", "lot": "XAUUSDm=0.01;BTCUSDm=0.01",
             "timeframe": "5m", "leverage": "400", "stop-value": "5.4", "take-profit-value": "10.8",
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

-- llm-support-resistance: the LLM strategy with the prompt support_resistance
-- (pure chart analysis, the price action at the supports and resistances, the
-- indicators only for the volatility and the volume) on 15m, consulted every
-- 15 min, a risk-reward of 1.5 at least, gpt-6-astra with a high reasoning
-- effort, as llm-trend, operated from Telegram, its reanalysis allowed. Its
-- comment of its own, nb:llm-support-resistance, gives it a magic number of its
-- own, its positions kept apart from the ones of llm-trend (nb:llm).
WITH agent AS (
    INSERT INTO agents (name, kind, enabled, mode, api_address, launched, restart, trade_panel, telegram_control,
                        params, position)
    SELECT 'llm-support-resistance', 'llm', FALSE, 'live', 'localhost:5582', TRUE, TRUE, FALSE, TRUE,
           '{"pair": "XAUUSDm;BTCUSDm", "lot": "XAUUSDm=0.01;BTCUSDm=0.01", "timeframe": "15m",
             "leverage": "400"}'::jsonb,
           (SELECT COALESCE(MAX(position) + 1, 0) FROM agents WHERE deleted_at IS NULL)
    WHERE NOT EXISTS (SELECT 1 FROM agents WHERE name = 'llm-support-resistance')
      AND NOT EXISTS (SELECT 1 FROM agent_former_names WHERE name = 'llm-support-resistance')
    RETURNING id
), account AS (
    INSERT INTO agent_accounts (agent_id, exchange, bridge_url, comment)
    SELECT id, 'mt5', 'http://mt5:18812', 'nb:llm-support-resistance' FROM agent
), reanalysis AS (
    INSERT INTO agent_reanalysis (agent_id, allowed)
    SELECT id, TRUE FROM agent
)
INSERT INTO agent_llm_settings (agent_id, provider, model, reasoning_effort, prompt_id, calendar_url, params)
SELECT agent.id, 'openai', 'gpt-6-astra', 'high',
       (SELECT id FROM prompts WHERE name = 'support_resistance' AND deleted_at IS NULL),
       'http://mt5:22347/mcp',
       '{"llm-setup": "free", "llm-candles": "60", "llm-min-rr": "1.5", "calendar-importance": "high",
         "llm-consult-interval": "15m"}'::jsonb
FROM agent;

-- The strategies of the code, an agent each, without LLM, named after its
-- strategy (matrend, fibonacci and fimathe have theirs above): the ones that
-- had a prompt for the LLM agent, and the rules of prompts of the LLM agent
-- coded since: on the timeframe of each strategy by default, a leverage of
-- 2000, operated from Telegram. The comment of each, the default of its
-- strategy (eg: nb:emacross), gives it a magic number of its own. Each inserted
-- once, as the ones above.
WITH strategy (name, kind, api_address, number) AS (
    VALUES
        -- trend: agents/trendagent
        -- trendconfluence: RK Trend Confluence, the cross of the EMA 9/21 confirmed by the regime, the slope, the ADX and the MACD
        ('trendconfluence', 'trend', 'localhost:5600', 0),
        -- emapullback: EMA Pullback, the pullbacks to the EMA 20 in the trend of the EMA 20/50
        ('emapullback', 'trend', 'localhost:5601', 1),
        -- emacross: EMA Crossover, the EMA 9 crossing the EMA 21
        ('emacross', 'trend', 'localhost:5602', 2),
        -- ema200: EMA 200 Trend Following, the close crossing a rising EMA 200
        ('ema200', 'trend', 'localhost:5603', 3),
        -- ribbon: Moving Average Ribbon, the EMAs 8/13/21/34/55 lining up in order
        ('ribbon', 'trend', 'localhost:5604', 4),
        -- supertrend: Supertrend, the flips of the Supertrend 10/3
        ('supertrend', 'trend', 'localhost:5605', 5),
        -- emasupertrend: EMA + Supertrend, the flips of the Supertrend 10/3 on the side of the EMA 200
        ('emasupertrend', 'trend', 'localhost:5606', 6),
        -- psarema: Parabolic SAR + EMA, the flips of the Parabolic SAR on the side of the EMA 50
        ('psarema', 'trend', 'localhost:5607', 7),
        -- atrtrailing: ATR Trailing Trend, the flips of the Chandelier Exit 22/3
        ('atrtrailing', 'trend', 'localhost:5608', 8),
        -- ichimoku: Ichimoku Trend, the Tenkan crossing the Kijun beyond the cloud
        ('ichimoku', 'trend', 'localhost:5609', 9),
        -- ichimokuadx: Ichimoku + ADX, the Tenkan crossing the Kijun beyond the cloud with the ADX above 25
        ('ichimokuadx', 'trend', 'localhost:5610', 10),
        -- mtftrend: Multi-Timeframe Trend, the EMA 20 crossing the EMA 50 with the EMA 50 of the timeframe above moving its way
        ('mtftrend', 'trend', 'localhost:5611', 11),
        -- mtfema: Multi-Timeframe EMA Alignment, the EMA 20 crossing the EMA 50 lined up on the timeframes above
        ('mtfema', 'trend', 'localhost:5612', 12),
        -- momentum: agents/momentumagent
        -- adxema: ADX + EMA, the close beyond the EMA 50 with the ADX above 25 and the DI on its side
        ('adxema', 'momentum', 'localhost:5613', 13),
        -- macdtrend: MACD Trend Following, the MACD 12/26/9 crossing its signal line beyond zero
        ('macdtrend', 'momentum', 'localhost:5614', 14),
        -- emamacd: EMA + MACD, the EMA 9 crossing the EMA 21 with the MACD histogram on its side
        ('emamacd', 'momentum', 'localhost:5615', 15),
        -- emarsiadx: EMA + RSI + ADX, the EMA 9 crossing the EMA 21 with the RSI beyond 50 and the ADX above 20
        ('emarsiadx', 'momentum', 'localhost:5616', 16),
        -- rsitrend: RSI Trend Continuation, the RSI crossing back 50 on the side of the EMA 50
        ('rsitrend', 'momentum', 'localhost:5617', 17),
        -- stochpullback: Stochastic Trend Pullback, the Stochastic crossing out of its extreme zone on the side of the EMA 50
        ('stochpullback', 'momentum', 'localhost:5618', 18),
        -- papullback: Price Action Pullback, the close beyond the previous candle after a pullback, on the side of a rising EMA 50
        ('papullback', 'momentum', 'localhost:5619', 19),
        -- breakout: agents/breakoutagent
        -- atrbreakout: ATR Breakout, the breakouts of the channel of 20 candles on an expansion of the volatility
        ('atrbreakout', 'breakout', 'localhost:5620', 20),
        -- donchian: Donchian Breakout, the close beyond the channel of 20 candles
        ('donchian', 'breakout', 'localhost:5621', 21),
        -- channel: Channel Breakout, the close beyond the channel of 55 candles
        ('channel', 'breakout', 'localhost:5622', 22),
        -- adxdonchian: ADX + Donchian Breakout, the close beyond the channel of 20 candles with the ADX above 25
        ('adxdonchian', 'breakout', 'localhost:5623', 23),
        -- donchianatr: Donchian + ATR, the close beyond the channel of 20 candles, its stop loss 2 ATR away trailing, as the Turtles
        ('donchianatr', 'breakout', 'localhost:5624', 24),
        -- bbbreakout: Bollinger Band Trend Breakout, the close crossing the Bollinger Band 20/2 with its middle moving its way
        ('bbbreakout', 'breakout', 'localhost:5625', 25),
        -- keltner: Keltner Channel Breakout, the close crossing the Keltner Channel of the EMA 20 and 2 ATR 10
        ('keltner', 'breakout', 'localhost:5626', 26),
        -- volumebreakout: Volume + Breakout, the close beyond the channel of 20 candles on 1.5 times the average volume
        ('volumebreakout', 'breakout', 'localhost:5627', 27),
        -- highlow: High/Low Breakout, the close crossing the high or the low of the previous day
        ('highlow', 'breakout', 'localhost:5628', 28),
        -- momentumbreakout: Momentum Breakout, the close beyond the channel of 20 candles on a large candle, the ROC 10 on its side
        ('momentumbreakout', 'breakout', 'localhost:5629', 29),
        -- intraday: agents/intradayagent
        -- orb: Opening Range Breakout, the breakouts of the range of the first 15 minutes of New York
        ('orb', 'intraday', 'localhost:5630', 30),
        -- asialondon: Asia London Breakout, the breakouts of the Asian range at the London open
        ('asialondon', 'intraday', 'localhost:5631', 31),
        -- vwap: VWAP Reclaim/Reject, the reclaim and the reject of the VWAP of the day
        ('vwap', 'intraday', 'localhost:5632', 32),
        -- sweep: Liquidity Sweep, the reversal after the sweep of a swing level
        ('sweep', 'intraday', 'localhost:5633', 33),
        -- zscore: Z-Score Reversion, the close 2 standard deviations away from its mean of 20 candles in a ranging market
        ('zscore', 'intraday', 'localhost:5634', 34),
        -- the rules of prompts of the LLM agent, coded as the user approved them, their stop loss moved by the R
        -- multiple of the position
        -- meanreversion: the rejections of the Bollinger Bands of a range back to its mean (the prompt mean_reversion)
        ('meanreversion', 'intraday', 'localhost:5635', 35),
        -- scalp1m: the micro pullbacks of the micro trend on 1 minute candles (the prompt scalp_1m)
        ('scalp1m', 'intraday', 'localhost:5636', 36),
        -- scalp1mbtc: the strong bursts of momentum on 1 minute candles, for a wide spread (the prompt scalp_1m_btc)
        ('scalp1mbtc', 'intraday', 'localhost:5637', 37),
        -- trendpullback: the end of the pullbacks of a trend (the setup trend_pullback of the prompt default)
        ('trendpullback', 'trend', 'localhost:5638', 38),
        -- squeezebreakout: the release of a compression with the volume (the prompt breakout)
        ('squeezebreakout', 'breakout', 'localhost:5639', 39)
), agent AS (
    INSERT INTO agents (name, kind, enabled, mode, api_address, launched, restart, trade_panel, telegram_control,
                        params, position)
    SELECT s.name, s.kind, FALSE, 'live', s.api_address, TRUE, TRUE, FALSE, TRUE,
           jsonb_build_object('strategy', s.name, 'pair', 'XAUUSDm;BTCUSDm', 'lot', 'XAUUSDm=0.01;BTCUSDm=0.01',
                              'leverage', '2000'),
           (SELECT COALESCE(MAX(position) + 1, 0) FROM agents WHERE deleted_at IS NULL) + s.number
    FROM strategy s
    WHERE NOT EXISTS (SELECT 1 FROM agents a WHERE a.name = s.name)
      AND NOT EXISTS (SELECT 1 FROM agent_former_names f WHERE f.name = s.name)
    RETURNING id
)
INSERT INTO agent_accounts (agent_id, exchange, bridge_url)
SELECT id, 'mt5', 'http://mt5:18812' FROM agent;
