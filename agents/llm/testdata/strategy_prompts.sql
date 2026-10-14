-- The prompts of the strategies of the code (the ones of agents/trendagent,
-- momentumagent, breakoutagent and intradayagent), traded by the LLM agent:
-- once seeded, now run as agents without a language model (see
-- sql/seeds/0004_agents.sql) and kept here for the tests of the agent, which
-- read their headers (the indicators, the take profit, the context) as the
-- ones of the prompts seeded; a prompt of them can still be created in the
-- dashboard. Not run on a database.
INSERT INTO prompts (name, description, content)
SELECT v.name, v.description, v.content
FROM (VALUES
    ('adxdonchian', $d$ADX + Donchian Breakout, the close beyond the channel of 20 candles with the ADX above 25: the strategy -strategy adxdonchian of agents/breakoutagent, traded by the AI agent.$d$,
$prompt$# adxdonchian: ADX + Donchian Breakout, the close beyond the channel of 20 candles with the ADX above 25: the strategy -strategy adxdonchian of agents/breakoutagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m or 1h.
# Indicators: donchian 20, adx, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: ADX + Donchian Breakout. Trade the breakouts of the channel of 20 candles in a trending market.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The close above donchian_upper_20 for a BUY, below donchian_lower_20 for a SELL.
2. adx_14 above 25.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('adxema', $d$ADX + EMA, the close beyond the EMA 50 with the ADX above 25 and the DI on its side: the strategy -strategy adxema of agents/momentumagent, traded by the AI agent.$d$,
$prompt$# adxema: ADX + EMA, the close beyond the EMA 50 with the ADX above 25 and the DI on its side: the strategy -strategy adxema of agents/momentumagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m or 1h.
# Indicators: ema 50, adx, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: ADX + EMA. Trade the start of a strong trend on the side of the EMA 50.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. On the candle just closed, the three together: the close above ema_50, adx_14 above 25 and plus_di_14 above minus_di_14 for a BUY; the close below ema_50, adx_14 above 25 and minus_di_14 above plus_di_14 for a SELL.
2. The candle before didn't hold all three, on its close and its values: they hold together for the first time.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('asialondon', $d$Asia London Breakout, the breakouts of the Asian range at the London open: the strategy -strategy asialondon of agents/intradayagent, traded by the AI agent.$d$,
$prompt$# asialondon: Asia London Breakout, the breakouts of the Asian range at the London open: the strategy -strategy asialondon of agents/intradayagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m or 15m.
# Indicators: range 00:00 07:00 10:00 Europe/London, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Asia London Breakout. Trade the breakout of the range of the Asian session, from 00:00 to 07:00 London time, during the London open, from 07:00 to 10:00.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. range_high and range_low known: the London open trades them now (null otherwise).
2. The first close of the London open above range_high by 0.1 ATR (atr_14) at least for a BUY, below range_low by 0.1 ATR for a SELL: no candle since 07:00 London (the rows since then) closed beyond the range before.
3. The candle just closed with a body of 0.5 ATR at least in the direction of the breakout: close - open at least 0.5 times atr_14 for a BUY (open - close for a SELL).
4. One position per London open at most: none if memory.recent_trades shows one opened since 07:00 London of this day.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 2 ATR from the entry, on the other side.

Managing the position held:
- EXIT when range_high and range_low turn null: the London open is over (10:00 London).
- EXIT when the close falls back past the middle of the Asian range, (range_high + range_low) / 2: below it while long, above it while short: the breakout failed.
- Otherwise HOLD: don't move the stop loss nor the take profit.
$prompt$),
    ('atrbreakout', $d$ATR Breakout, the breakouts of the channel of 20 candles on an expansion of the volatility: the strategy -strategy atrbreakout of agents/breakoutagent, traded by the AI agent.$d$,
$prompt$# atrbreakout: ATR Breakout, the breakouts of the channel of 20 candles on an expansion of the volatility: the strategy -strategy atrbreakout of agents/breakoutagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m, 15m or 1h.
# Indicators: atr 14 50, donchian 20
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: ATR Breakout. Trade the breakouts of the recent range while the volatility expands.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. Expansion: atr_14 of the candle just closed at least 1.2 times the value before of atr_14_sma_50 (the average ATR of the 50 candles before it).
2. Breakout: the close above donchian_upper_20 for a BUY, below donchian_lower_20 for a SELL.
3. The candle just closed with a body of 0.8 ATR at least in the direction of the breakout: close - open at least 0.8 times atr_14 for a BUY (open - close for a SELL).
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1.5 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 2.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT once position.candles_held reaches 12.
- Otherwise HOLD: don't move the stop loss nor the take profit.
$prompt$),
    ('atrtrailing', $d$ATR Trailing Trend, the flips of the Chandelier Exit 22/3: the strategy -strategy atrtrailing of agents/trendagent, traded by the AI agent.$d$,
$prompt$# atrtrailing: ATR Trailing Trend, the flips of the Chandelier Exit 22/3: the strategy -strategy atrtrailing of agents/trendagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m, 15m or 1h.
# Indicators: chandelier 22 3, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: ATR Trailing Trend. Trade the flips of the ATR trailing stop of the Chandelier Exit: 22 candles, 3 ATR of 22 candles.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. chandelier_side_22_3 turns 1 on the candle just closed, -1 on the candle before, for a BUY: the close crossed above the trailing stop of the shorts; it turns -1, 1 before, for a SELL: the close crossed below the trailing stop of the longs.
Without it, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('bbbreakout', $d$Bollinger Band Trend Breakout, the close crossing the Bollinger Band 20/2 with its middle moving its way: the strategy -strategy bbbreakout of agents/breakoutagent, traded by the AI agent.$d$,
$prompt$# bbbreakout: Bollinger Band Trend Breakout, the close crossing the Bollinger Band 20/2 with its middle moving its way: the strategy -strategy bbbreakout of agents/breakoutagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m or 1h.
# Indicators: bb, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Bollinger Band Trend Breakout. Trade the close breaking out of the Bollinger Bands 20/2 in the direction of their middle.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The close crosses above bb_upper_20_2 for a BUY (above it on the candle just closed, the close before at or below its value before), below bb_lower_20_2 for a SELL.
2. bb_middle_20 rising for a BUY (its last value above the one before), falling for a SELL.
Missing one of them, WAIT. A cross is on the candle just closed: the last values on the new side, the values of the candle before on the other side or equal.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('channel', $d$Channel Breakout, the close beyond the channel of 55 candles: the strategy -strategy channel of agents/breakoutagent, traded by the AI agent.$d$,
$prompt$# channel: Channel Breakout, the close beyond the channel of 55 candles: the strategy -strategy channel of agents/breakoutagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m, 1h or 4h.
# Indicators: donchian 55, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Channel Breakout. Trade the close beyond the channel of the last 55 candles, the Turtles' system 2.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The close above donchian_upper_55, the highest high of the 55 candles before it, for a BUY; below donchian_lower_55, their lowest low, for a SELL.
Without it, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('donchian', $d$Donchian Breakout, the close beyond the channel of 20 candles: the strategy -strategy donchian of agents/breakoutagent, traded by the AI agent.$d$,
$prompt$# donchian: Donchian Breakout, the close beyond the channel of 20 candles: the strategy -strategy donchian of agents/breakoutagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m, 1h or 4h.
# Indicators: donchian 20, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Donchian Breakout. Trade the close beyond the highest high or the lowest low of the last 20 candles.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The close above donchian_upper_20, the highest high of the 20 candles before it, for a BUY; below donchian_lower_20, their lowest low, for a SELL.
Without it, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('donchianatr', $d$Donchian + ATR, the close beyond the channel of 20 candles, its stop loss 2 ATR away trailing, as the Turtles: the strategy -strategy donchianatr of agents/breakoutagent, traded by the AI agent.$d$,
$prompt$# donchianatr: Donchian + ATR, the close beyond the channel of 20 candles, its stop loss 2 ATR away trailing, as the Turtles: the strategy -strategy donchianatr of agents/breakoutagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m, 1h or 4h.
# Indicators: donchian 20, atr
# Take profit: optional
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Donchian + ATR. Trade the breakouts of the channel of 20 candles as the Turtles: a stop loss in ATR that follows the trend, no target.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The close above donchian_upper_20, the highest high of the 20 candles before it, for a BUY; below donchian_lower_20, their lowest low, for a SELL.
Without it, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the move, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 2 ATR (atr_14) from the entry: below it for a long, above it for a short.
- No take profit: take_profit null, the trailing stop loss and the opposite signal close the position.

Managing the position held:
- EXIT on the opposite signal: the close below donchian_lower_20 while long, above donchian_upper_20 while short.
- Trail the stop loss 2 ATR behind the best price since the entry: the highest high of the candles since it for a long (the last position.candles_held rows), the lowest low for a short, minus (plus) 2 times the last atr_14. MODIFY_SL to that level when it is nearer the price than the stop loss of now and still on its losing side of the close; never move it back.
- Otherwise HOLD.
$prompt$),
    ('ema200', $d$EMA 200 Trend Following, the close crossing a rising EMA 200: the strategy -strategy ema200 of agents/trendagent, traded by the AI agent.$d$,
$prompt$# ema200: EMA 200 Trend Following, the close crossing a rising EMA 200: the strategy -strategy ema200 of agents/trendagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m, 15m or 1h.
# Indicators: ema 200, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: EMA 200 Trend Following. Trade the close crossing the EMA 200 the way the EMA 200 moves.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The close crosses above ema_200 for a BUY (the close of the candle before, the row before the last, at or below the value of ema_200 before), below it for a SELL.
2. ema_200 rising for a BUY: its last value above the one before; falling for a SELL.
Missing one of them, WAIT. A cross is on the candle just closed: the last values on the new side, the values of the candle before on the other side or equal.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('emacross', $d$EMA Crossover, the EMA 9 crossing the EMA 21: the strategy -strategy emacross of agents/trendagent, traded by the AI agent.$d$,
$prompt$# emacross: EMA Crossover, the EMA 9 crossing the EMA 21: the strategy -strategy emacross of agents/trendagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m or 1h.
# Indicators: ema 9 21, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: EMA Crossover. Trade the cross of a fast EMA over a slow one: the start of a move.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. ema_9 crosses above ema_21 for a BUY, below it for a SELL.
Without it, WAIT. A cross is on the candle just closed: the last values on the new side, the values of the candle before on the other side or equal.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('emamacd', $d$EMA + MACD, the EMA 9 crossing the EMA 21 with the MACD histogram on its side: the strategy -strategy emamacd of agents/momentumagent, traded by the AI agent.$d$,
$prompt$# emamacd: EMA + MACD, the EMA 9 crossing the EMA 21 with the MACD histogram on its side: the strategy -strategy emamacd of agents/momentumagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m or 1h.
# Indicators: ema 9 21, macd, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: EMA + MACD. Trade the cross of the EMA 9 over the EMA 21 confirmed by the momentum of the MACD.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. ema_9 crosses above ema_21 for a BUY, below it for a SELL.
2. macd_histogram above zero for a BUY, below zero for a SELL, on the candle just closed.
Missing one of them, WAIT. A cross is on the candle just closed: the last values on the new side, the values of the candle before on the other side or equal.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('emapullback', $d$EMA Pullback, the pullbacks to the EMA 20 in the trend of the EMA 20/50: the strategy -strategy emapullback of agents/trendagent, traded by the AI agent.$d$,
$prompt$# emapullback: EMA Pullback, the pullbacks to the EMA 20 in the trend of the EMA 20/50: the strategy -strategy emapullback of agents/trendagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -llm-history 6 -timeframe 15m or 1h.
# Indicators: ema 20 50, rsi, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: EMA Pullback. Trade the pullbacks to the fast EMA in a trend of two EMAs, confirmed by the candle and the momentum.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. Trend: ema_20 above ema_50 on each of the last 5 candles, and ema_20 higher than 5 candles ago (its last value above the first of the 6 sent), for a BUY; below and lower for a SELL.
2. Pullback: one of the 5 candles before the signal one reached back to ema_20, its low at most 0.25 ATR (atr_14) above ema_20 of that candle (its high at most 0.25 ATR below it for a SELL), while the closes of the signal candle and of the 5 before stayed above ema_50 (below for a SELL).
3. Confirmation: the candle just closed closes up (close above open), above ema_20 and above the high of the candle before (down, below ema_20 and the low before for a SELL).
4. Momentum: rsi_14 above 50 and rising (above its value before) for a BUY; below 50 and falling for a SELL.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 2 ATR from the entry, on the other side.

Managing the position held:
- EXIT when the close crosses back beyond ema_50: below it while long, above it while short.
- EXIT once position.candles_held reaches 24.
- Otherwise HOLD: don't move the stop loss nor the take profit.
$prompt$),
    ('emarsiadx', $d$EMA + RSI + ADX, the EMA 9 crossing the EMA 21 with the RSI beyond 50 and the ADX above 20: the strategy -strategy emarsiadx of agents/momentumagent, traded by the AI agent.$d$,
$prompt$# emarsiadx: EMA + RSI + ADX, the EMA 9 crossing the EMA 21 with the RSI beyond 50 and the ADX above 20: the strategy -strategy emarsiadx of agents/momentumagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m or 1h.
# Indicators: ema 9 21, rsi, adx, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: EMA + RSI + ADX. Trade the cross of the EMA 9 over the EMA 21 with the momentum and a trending market.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. ema_9 crosses above ema_21 for a BUY, below it for a SELL.
2. rsi_14 above 50 for a BUY, below 50 for a SELL.
3. adx_14 above 20.
Missing one of them, WAIT. A cross is on the candle just closed: the last values on the new side, the values of the candle before on the other side or equal.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('emasupertrend', $d$EMA + Supertrend, the flips of the Supertrend 10/3 on the side of the EMA 200: the strategy -strategy emasupertrend of agents/trendagent, traded by the AI agent.$d$,
$prompt$# emasupertrend: EMA + Supertrend, the flips of the Supertrend 10/3 on the side of the EMA 200: the strategy -strategy emasupertrend of agents/trendagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m, 15m or 1h.
# Indicators: supertrend 10 3, ema 200, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: EMA + Supertrend. Trade the flips of the Supertrend 10/3 in the direction of the EMA 200.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. supertrend_side_10_3 turns 1 on the candle just closed, -1 on the candle before, for a BUY; -1, 1 before, for a SELL.
2. The close above ema_200 for a BUY, below it for a SELL.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('highlow', $d$High/Low Breakout, the close crossing the high or the low of the previous day: the strategy -strategy highlow of agents/breakoutagent, traded by the AI agent.$d$,
$prompt$# highlow: High/Low Breakout, the close crossing the high or the low of the previous day: the strategy -strategy highlow of agents/breakoutagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m, 15m or 1h.
# Indicators: atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: High/Low Breakout. Trade the close crossing the high or the low of the previous day (UTC).

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The close crosses above reference_levels.previous_day_high for a BUY: above it on the candle just closed, the close before (the row before the last) at or below it; below reference_levels.previous_day_low for a SELL.
2. reference_levels known: without them, WAIT.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('ichimoku', $d$Ichimoku Trend, the Tenkan crossing the Kijun beyond the cloud: the strategy -strategy ichimoku of agents/trendagent, traded by the AI agent.$d$,
$prompt$# ichimoku: Ichimoku Trend, the Tenkan crossing the Kijun beyond the cloud: the strategy -strategy ichimoku of agents/trendagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m, 1h or 4h.
# Indicators: ichimoku 9 26 52, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Ichimoku Trend. Trade the cross of the conversion line over the base line with the price beyond the cloud.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. ichimoku_tenkan_9 crosses above ichimoku_kijun_26 for a BUY, below it for a SELL.
2. The close above the cloud for a BUY: above both ichimoku_senkou_a and ichimoku_senkou_b_52 of the candle just closed; below both for a SELL.
Missing one of them, WAIT. A cross is on the candle just closed: the last values on the new side, the values of the candle before on the other side or equal.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('ichimokuadx', $d$Ichimoku + ADX, the Tenkan crossing the Kijun beyond the cloud with the ADX above 25: the strategy -strategy ichimokuadx of agents/trendagent, traded by the AI agent.$d$,
$prompt$# ichimokuadx: Ichimoku + ADX, the Tenkan crossing the Kijun beyond the cloud with the ADX above 25: the strategy -strategy ichimokuadx of agents/trendagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m or 1h.
# Indicators: ichimoku 9 26 52, adx, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Ichimoku + ADX. Trade the Ichimoku trend signal only in a trending market.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. ichimoku_tenkan_9 crosses above ichimoku_kijun_26 for a BUY, below it for a SELL.
2. The close above the cloud for a BUY: above both ichimoku_senkou_a and ichimoku_senkou_b_52; below both for a SELL.
3. adx_14 above 25.
Missing one of them, WAIT. A cross is on the candle just closed: the last values on the new side, the values of the candle before on the other side or equal.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('keltner', $d$Keltner Channel Breakout, the close crossing the Keltner Channel of the EMA 20 and 2 ATR 10: the strategy -strategy keltner of agents/breakoutagent, traded by the AI agent.$d$,
$prompt$# keltner: Keltner Channel Breakout, the close crossing the Keltner Channel of the EMA 20 and 2 ATR 10: the strategy -strategy keltner of agents/breakoutagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m or 1h.
# Indicators: keltner 20 10 2, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Keltner Channel Breakout. Trade the close breaking out of the Keltner Channel: the EMA 20 plus and minus 2 ATR of 10 candles.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The close crosses above keltner_upper_20_10_2 for a BUY (above it on the candle just closed, the close before at or below its value before), below keltner_lower_20_10_2 for a SELL.
Without it, WAIT. A cross is on the candle just closed: the last values on the new side, the values of the candle before on the other side or equal.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('macdtrend', $d$MACD Trend Following, the MACD 12/26/9 crossing its signal line beyond zero: the strategy -strategy macdtrend of agents/momentumagent, traded by the AI agent.$d$,
$prompt$# macdtrend: MACD Trend Following, the MACD 12/26/9 crossing its signal line beyond zero: the strategy -strategy macdtrend of agents/momentumagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m or 1h.
# Indicators: macd, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: MACD Trend Following. Trade the MACD crossing its signal line on the side of zero of the trend.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. macd_12_26 crosses above macd_signal_9 for a BUY, below it for a SELL.
2. Both above zero for a BUY on the candle just closed, both below zero for a SELL.
Missing one of them, WAIT. A cross is on the candle just closed: the last values on the new side, the values of the candle before on the other side or equal.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('matrend', $d$MA Trend, the break of the SMA 200 with the EMA 9/21 on its side: the strategy -strategy matrend of agents/trendagent, traded by the AI agent.$d$,
$prompt$# matrend: MA Trend, the break of the SMA 200 with the EMA 9/21 on its side: the strategy -strategy matrend of agents/trendagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 1h (or 5m).
# Indicators: ema 9 21, sma 200, atr
# Take profit: optional
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.
# The strategy checks its stop on the close of the candles; the AI agent places it at the broker, where a wick hits it.

Setup: MA Trend. Trade the close breaking the SMA 200 with the EMA 9 and the EMA 21 already on its side, then trail it: no target.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The close crosses above sma_200 with ema_9 already above ema_21 for a BUY (the close before, the row before the last, at or below the value of sma_200 before); below it with ema_21 above ema_9 for a SELL.
2. The close at most 3% away from sma_200: (close - sma_200) / sma_200 at most 0.03 for a BUY, (sma_200 - close) / sma_200 for a SELL: a break far from it comes late.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the move, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) beyond sma_200 at the entry: sma_200 - 1 ATR for a long, sma_200 + 1 ATR for a short.
- No take profit: take_profit null, the trailing stop loss and the cross of the EMAs close the position.

Managing the position held:
- EXIT when ema_9 crosses ema_21 against the position: below it while long, above it while short.
- From the candle after the entry, trail the stop loss 3 ATR behind the best price since the entry: the highest high of the candles since it for a long (the last position.candles_held rows), the lowest low for a short, minus (plus) 3 times the last atr_14. MODIFY_SL to that level when it is nearer the price than the stop loss of now and still on its losing side of the close; never move it back.
- Otherwise HOLD.
$prompt$),
    ('momentumbreakout', $d$Momentum Breakout, the close beyond the channel of 20 candles on a large candle, the ROC 10 on its side: the strategy -strategy momentumbreakout of agents/breakoutagent, traded by the AI agent.$d$,
$prompt$# momentumbreakout: Momentum Breakout, the close beyond the channel of 20 candles on a large candle, the ROC 10 on its side: the strategy -strategy momentumbreakout of agents/breakoutagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m, 15m or 1h.
# Indicators: donchian 20, roc 10, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Momentum Breakout. Trade the breakouts of the channel of 20 candles made with momentum.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The close above donchian_upper_20 for a BUY, below donchian_lower_20 for a SELL.
2. roc_10 above zero for a BUY, below zero for a SELL.
3. The candle just closed in the direction of the breakout with a body of 60% of its range at least: close - open at least 0.6 times high - low for a BUY (open - close for a SELL), from the last row.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('mtfema', $d$Multi-Timeframe EMA Alignment, the EMA 20 crossing the EMA 50 lined up on the timeframes above: the strategy -strategy mtfema of agents/trendagent, traded by the AI agent.$d$,
$prompt$# mtfema: Multi-Timeframe EMA Alignment, the EMA 20 crossing the EMA 50 lined up on the timeframes above: the strategy -strategy mtfema of agents/trendagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m or 1h.
# Indicators: ema 20 50, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.
# The strategy reads the timeframes 4 and 16 times the one of the candles: the context has them on 15m (1h and 4h); on 1h it has 4h and 1d, on 5m 15m and 1h.

Setup: Multi-Timeframe EMA Alignment. Trade the cross of the EMA 20 over the EMA 50 once the timeframes above line up the same way.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. ema_20 crosses above ema_50 for a BUY, below it for a SELL.
2. On both higher_timeframes, the last value of ema_20 above the last one of ema_50 for a BUY, below it for a SELL.
3. Without two higher_timeframes, WAIT.
Missing one of them, WAIT. A cross is on the candle just closed: the last values on the new side, the values of the candle before on the other side or equal.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('mtftrend', $d$Multi-Timeframe Trend, the EMA 20 crossing the EMA 50 with the EMA 50 of the timeframe above moving its way: the strategy -strategy mtftrend of agents/trendagent, traded by the AI agent.$d$,
$prompt$# mtftrend: Multi-Timeframe Trend, the EMA 20 crossing the EMA 50 with the EMA 50 of the timeframe above moving its way: the strategy -strategy mtftrend of agents/trendagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m or 1h.
# Indicators: ema 20 50, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.
# The strategy reads the timeframe 4 times the one of the candles: the first higher timeframe of the context is that one on 15m (1h) and 1h (4h); on 5m it is 15m, not 20m.

Setup: Multi-Timeframe Trend. Trade the cross of the EMA 20 over the EMA 50 with the trend of the timeframe above.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. ema_20 crosses above ema_50 for a BUY, below it for a SELL.
2. The EMA 50 of the timeframe above rising for a BUY: the last value of ema_50 of the first of higher_timeframes above the one before; falling for a SELL.
3. Without higher_timeframes, WAIT.
Missing one of them, WAIT. A cross is on the candle just closed: the last values on the new side, the values of the candle before on the other side or equal.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('orb', $d$Opening Range Breakout, the breakouts of the range of the first 15 minutes of New York: the strategy -strategy orb of agents/intradayagent, traded by the AI agent.$d$,
$prompt$# orb: Opening Range Breakout, the breakouts of the range of the first 15 minutes of New York: the strategy -strategy orb of agents/intradayagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m or 15m.
# Indicators: range 09:30 09:45 12:00 America/New_York, vwap 09:30 America/New_York, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Opening Range Breakout. Trade the breakout of the opening range of the New York session: the high and the low of its first 15 minutes, from 09:30 to 09:45 New York time, traded until 12:00.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. range_high and range_low known: the session trades them now (null before 09:45 and from 12:00).
2. The first close of the session above range_high by 0.1 ATR (atr_14) at least for a BUY, below range_low by 0.1 ATR for a SELL: no candle since 09:45 (the rows since then) closed beyond the range before.
3. The close above vwap for a BUY, below it for a SELL.
4. One position per session at most: none if memory.recent_trades shows one opened since 09:45 of this session.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 2 ATR from the entry, on the other side.

Managing the position held:
- EXIT when range_high and range_low turn null: the session is over (12:00 New York).
- EXIT when the close falls back past the middle of the range, (range_high + range_low) / 2: below it while long, above it while short: the breakout failed.
- Otherwise HOLD: don't move the stop loss nor the take profit.
$prompt$),
    ('papullback', $d$Price Action Pullback, the close beyond the previous candle after a pullback, on the side of a rising EMA 50: the strategy -strategy papullback of agents/momentumagent, traded by the AI agent.$d$,
$prompt$# papullback: Price Action Pullback, the close beyond the previous candle after a pullback, on the side of a rising EMA 50: the strategy -strategy papullback of agents/momentumagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m or 15m.
# Indicators: ema 50, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Price Action Pullback. Trade the resumption of a trend after a short pullback, read on the candles.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The close above ema_50 and ema_50 rising (its last value above the one before) for a BUY; below a falling ema_50 for a SELL.
2. The 2 candles before the last one (the two rows before the last) closed down, each its close under its open, for a BUY; up for a SELL.
3. The close of the candle just closed above the high of the candle before for a BUY, under its low for a SELL.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('psarema', $d$Parabolic SAR + EMA, the flips of the Parabolic SAR on the side of the EMA 50: the strategy -strategy psarema of agents/trendagent, traded by the AI agent.$d$,
$prompt$# psarema: Parabolic SAR + EMA, the flips of the Parabolic SAR on the side of the EMA 50: the strategy -strategy psarema of agents/trendagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m or 15m.
# Indicators: psar 0.02 0.2, ema 50, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Parabolic SAR + EMA. Trade the flips of the Parabolic SAR (step 0.02 up to 0.2) with the EMA 50.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. psar_side_0.02_0.2 turns 1 on the candle just closed, -1 on the candle before, for a BUY: the SAR flipped below the price; -1, 1 before, for a SELL.
2. The close above ema_50 for a BUY, below it for a SELL.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('ribbon', $d$Moving Average Ribbon, the EMAs 8/13/21/34/55 lining up in order: the strategy -strategy ribbon of agents/trendagent, traded by the AI agent.$d$,
$prompt$# ribbon: Moving Average Ribbon, the EMAs 8/13/21/34/55 lining up in order: the strategy -strategy ribbon of agents/trendagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m or 1h.
# Indicators: ema 8 13 21 34 55, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Moving Average Ribbon. Trade the moment the ribbon of five EMAs lines up: a trend unfolding.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. On the candle just closed ema_8 > ema_13 > ema_21 > ema_34 > ema_55, all of them in that order, for a BUY; ema_8 < ema_13 < ema_21 < ema_34 < ema_55 for a SELL.
2. The candle before wasn't lined up that way: one of those comparisons failed on the values before.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('rsitrend', $d$RSI Trend Continuation, the RSI crossing back 50 on the side of the EMA 50: the strategy -strategy rsitrend of agents/momentumagent, traded by the AI agent.$d$,
$prompt$# rsitrend: RSI Trend Continuation, the RSI crossing back 50 on the side of the EMA 50: the strategy -strategy rsitrend of agents/momentumagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 15m or 1h.
# Indicators: ema 50, rsi, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: RSI Trend Continuation. Trade the end of a dip of the momentum in a trend: the RSI back beyond 50.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The close above ema_50 for a BUY, below it for a SELL.
2. rsi_14 crosses back beyond 50 the way of the entry: above 50 on the candle just closed and at or below 50 on the candle before for a BUY; below 50 now and at or above 50 before for a SELL.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('stochpullback', $d$Stochastic Trend Pullback, the Stochastic crossing out of its extreme zone on the side of the EMA 50: the strategy -strategy stochpullback of agents/momentumagent, traded by the AI agent.$d$,
$prompt$# stochpullback: Stochastic Trend Pullback, the Stochastic crossing out of its extreme zone on the side of the EMA 50: the strategy -strategy stochpullback of agents/momentumagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m or 15m.
# Indicators: ema 50, stoch 14 3 3, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Stochastic Trend Pullback. Trade the end of a pullback in a trend: the Slow Stochastic 14/3/3 turning in its extreme zone.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The close above ema_50 for a BUY, below it for a SELL.
2. stoch_k_14_3 crosses above stoch_d_3 with stoch_k_14_3 under 20 on the candle just closed for a BUY; below it with stoch_k_14_3 above 80 for a SELL.
Missing one of them, WAIT. A cross is on the candle just closed: the last values on the new side, the values of the candle before on the other side or equal.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('supertrend', $d$Supertrend, the flips of the Supertrend 10/3: the strategy -strategy supertrend of agents/trendagent, traded by the AI agent.$d$,
$prompt$# supertrend: Supertrend, the flips of the Supertrend 10/3: the strategy -strategy supertrend of agents/trendagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m, 15m or 1h.
# Indicators: supertrend 10 3, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Supertrend. Trade the flips of the Supertrend of an ATR of 10 candles and a factor of 3.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. supertrend_side_10_3 turns 1 on the candle just closed, -1 on the candle before, for a BUY: the line flipped below the close; it turns -1, 1 before, for a SELL.
Without it, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('sweep', $d$Liquidity Sweep, the reversal after the sweep of a swing level: the strategy -strategy sweep of agents/intradayagent, traded by the AI agent.$d$,
$prompt$# sweep: Liquidity Sweep, the reversal after the sweep of a swing level: the strategy -strategy sweep of agents/intradayagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m or 15m.
# Indicators: atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Liquidity Sweep. Trade the reversal after the price sweeps the liquidity resting beyond a swing high or low, and closes back.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The levels: the swing lows of structure.swing_lows for a BUY (swing_highs for a SELL), 3 candles on each side of them, formed within the last 50 candles (candles_ago up to 50) and not taken out since.
2. The sweep: within the last 3 candles, a wick went below such a level (above it for a SELL) by 0.05 ATR (atr_14) at least and 1.5 ATR at most: deeper is a breakout, not a sweep.
3. The close of the candle just closed is the first one back above the level since that wick (below it for a SELL).
4. The reversal: the candle just closed closes up (down for a SELL), at least 0.5 ATR from the extreme of the sweep, the lowest low since the wick (the highest high for a SELL).
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 2 ATR from the entry, on the other side.

Managing the position held:
- EXIT once position.candles_held reaches 24.
- Otherwise HOLD: don't move the stop loss nor the take profit.
$prompt$),
    ('trendconfluence', $d$RK Trend Confluence, the cross of the EMA 9/21 confirmed by the regime, the slope, the ADX and the MACD: the strategy -strategy trendconfluence of agents/trendagent, traded by the AI agent.$d$,
$prompt$# trendconfluence: RK Trend Confluence, the cross of the EMA 9/21 confirmed by the regime, the slope, the ADX and the MACD: the strategy -strategy trendconfluence of agents/trendagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -llm-history 6 -timeframe 1h.
# Indicators: ema 9 21, sma 200, macd, adx, atr
# Take profit: optional
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.
# The strategy has no stop by default: the 3 ATR of the example of agents/trendagent (-stop-atr 3), agreed for the AI agent.

Setup: RK Trend Confluence. Trade the cross of the EMA 9 over the EMA 21 only when every gate of the trend agrees.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. Trigger: ema_9 crosses above ema_21 on the candle just closed (below for a SELL).
2. Regime: the close and ema_21 above sma_200 for a BUY, both below it for a SELL.
3. Slope: sma_200 moving the way of the entry by 0.02 ATR a candle at least: (the last sma_200 - the first of the 6 sent) / 5 at least 0.02 times atr_14 for a BUY, at most -0.02 times atr_14 for a SELL.
4. ADX: adx_14 at least 20, plus_di_14 above minus_di_14 for a BUY, minus_di_14 above plus_di_14 for a SELL.
5. MACD: macd_histogram above zero for a BUY, below zero for a SELL.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the move, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 3 ATR (atr_14) from the entry: below it for a long, above it for a short.
- No take profit: take_profit null, the opposite signal closes the position, as in the strategy.

Managing the position held:
- EXIT on the opposite signal: every condition of a SELL while long, of a BUY while short.
- Otherwise HOLD: don't move the stop loss.
$prompt$),
    ('volumebreakout', $d$Volume + Breakout, the close beyond the channel of 20 candles on 1.5 times the average volume: the strategy -strategy volumebreakout of agents/breakoutagent, traded by the AI agent.$d$,
$prompt$# volumebreakout: Volume + Breakout, the close beyond the channel of 20 candles on 1.5 times the average volume: the strategy -strategy volumebreakout of agents/breakoutagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m, 15m or 1h.
# Indicators: donchian 20, volume, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Volume + Breakout. Trade the breakouts of the channel of 20 candles carried by volume.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The close above donchian_upper_20 for a BUY, below donchian_lower_20 for a SELL.
2. The volume of the candle just closed (the last row) at least 1.5 times volume_sma_20.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT on the opposite signal: the rule of a SELL while long, the rule of a BUY while short.
- Otherwise HOLD: the stop loss and the take profit close the position at the broker; don't move them.
$prompt$),
    ('vwap', $d$VWAP Reclaim/Reject, the reclaim and the reject of the VWAP of the day: the strategy -strategy vwap of agents/intradayagent, traded by the AI agent.$d$,
$prompt$# vwap: VWAP Reclaim/Reject, the reclaim and the reject of the VWAP of the day: the strategy -strategy vwap of agents/intradayagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m or 15m.
# Indicators: vwap 00:00 UTC, rsi, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.
# Its trend following variant, -setup reject in intradayagent, trades the reject only: drop the reclaim setup for it.

Setup: VWAP. Trade the VWAP of the day (from 00:00 UTC): its reclaim, the price crossing it after holding on the other side, and its reject, the price coming back to it and turning away, the continuation of its trend.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. The candle just closed closes in the direction of the entry (up for a BUY, close above open) and beyond vwap (above for a BUY), with rsi_14 above 50 for a BUY, below 50 for a SELL.
2. And one of the two setups: the reclaim, the 3 closes before it below vwap (their values of vwap) for a BUY and the close at least 0.1 ATR (atr_14) above vwap; or the reject, the 3 closes before it above vwap for a BUY, the low of the candle just closed at most 0.1 ATR above vwap (it came back to it) and its lower wick, from the low to the lower of its open and close, at least 0.4 times its range. A SELL mirrors them, with the high and the upper wick.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 1.5 ATR from the entry, on the other side.

Managing the position held:
- EXIT when the close crosses back beyond vwap: below it while long, above it while short.
- EXIT once position.candles_held reaches 24.
- Otherwise HOLD: don't move the stop loss nor the take profit.
$prompt$),
    ('zscore', $d$Z-Score Reversion, the close 2 standard deviations away from its mean of 20 candles in a ranging market: the strategy -strategy zscore of agents/intradayagent, traded by the AI agent.$d$,
$prompt$# zscore: Z-Score Reversion, the close 2 standard deviations away from its mean of 20 candles in a ranging market: the strategy -strategy zscore of agents/intradayagent, traded by the AI agent.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 0 -timeframe 5m or 15m.
# Indicators: bb, adx, atr
# The exits are the strategy's, in ATR: -llm-min-rr and -llm-min-stop-atr at 0, or under their ratio and distance, not to refuse them.

Setup: Z-Score Reversion. Trade the return to the mean of a stretched price in a ranging market. The Z-Score of the close is (close - bb_middle_20) / ((bb_upper_20_2 - bb_middle_20) / 2): the Bollinger Bands 20/2 are Z at 2 and -2.

Entry, on the candle just closed (a SELL is the mirror of a BUY):
1. Z crosses down to -2 for a BUY: the close at or below bb_lower_20_2 on the candle just closed, above its value before on the candle before; Z crosses up to 2 for a SELL: the close at or above bb_upper_20_2, below it before.
2. adx_14 under 25: the market ranging.
Missing one of them, WAIT.
The rule is the strategy: never enter without its signal. On a signal, WAIT only when the context clearly stands against it, eg: a level of higher_timeframes right in the way of the take profit, or a release of high importance of economic_calendar about to come; say which in the reason.

Stop loss and take profit:
- The stop loss 1.5 ATR (atr_14) from the entry: below it for a long, above it for a short.
- The take profit 3 ATR from the entry, on the other side.

Managing the position held:
- EXIT when Z reverts to 0: the close back at or above bb_middle_20 while long, at or below it while short.
- EXIT once position.candles_held reaches 36.
- Otherwise HOLD: don't move the stop loss nor the take profit.
$prompt$)
) AS v (name, description, content);
