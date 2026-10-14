-- Serialized with the seeds of any other migrator running at once (eg: two
-- controllers starting): the file runs as one implicit transaction, holding
-- the lock to its end, and the statements after it see what the other one
-- inserted. The same key in every seed file.
SELECT pg_advisory_xact_lock(hashtext('xtrader.seeds'));

-- The prompts of the LLM agents, the strategies only a language model trades,
-- the default one as agents/llm/prompts/default.txt embeds it: each inserted
-- once, when no prompt has its name, so the seeds, run again on every
-- migration, never overwrite an edit nor bring back a prompt deleted. The
-- header lines of each one (# Indicators:, # Take profit:) are read by the
-- agent, the others are for the operator. The strategies of the code run as
-- agents without a language model (0004_agents.sql): their prompts aren't
-- seeded, the tests of the agent keep them (agents/llm/testdata).
INSERT INTO prompts (name, description, content)
SELECT v.name, v.description, v.content
FROM (VALUES
    ('breakout', $d$the breakout of a consolidation, a squeeze of the volatility released with volume, entered on the close or on the retest.$d$,
$prompt$# breakout: the breakout of a consolidation, a squeeze of the volatility
# released with volume, entered on the close or on the retest.
# Flags: -llm-setup free -llm-candles 100 -llm-min-rr 2 -timeframe 5m to 1h.
# Indicators: ema, sma, atr, adx, bb, volume (drawn on the chart and sent to the model; -llm-indicators overrides)

Setup: volatility breakout. Markets alternate quiet ranges and expansions: trade the release of a compression, with the volume.

1. Compression: before the breakout, the price ranged in a tight box for at least about 10 candles: the Bollinger Bands (bb_upper_20_2 and bb_lower_20_2) at their narrowest of the candles, summary.atr_pct low, the highs and lows contained. The top of the box is the highest high of that range, its bottom the lowest low.
   structure.recent_range gives the box of the last candles (its width_atr small, under about 3 ATR) and structure.atr_percentile the squeeze (under about 30: a quiet market).
2. Breakout: the candle just closed, or the previous one, closed beyond the box (the top for a long, the bottom for a short) with a body of at least about half its range, a volume over about 1.5 times its average (summary.volume_ratio), and the ADX (adx_14) rising with the DI of the breakout side on top (plus_di_14 for a long, minus_di_14 for a short).
   structure.volume_percentile over about 80 confirms the volume; a breakout toward a level of higher_timeframes near the box has little room: WAIT.
3. Entry: on the breakout close while the price is within about 0.5 ATR of the box; farther, wait for a retest: the price back at the edge of the box, holding it, then a candle resuming the breakout.
Missing one of them, WAIT.
- A false breakout is a candle beyond the box closing back inside with a long wick: never enter it; after one, the opposite side is often next.
- A breakout against the SMA of the trend (sma_200) needs a stronger volume and more confidence.

Stop loss and take profit:
- The stop loss goes back inside the box: under its middle for a long (over it for a short), or beyond the breakout candle when that is tighter and still at least about 1 ATR from the entry.
- The take profit goes at the measured move: the height of the box projected from its edge, or about 2 times the risk; before a clear level of the candles in the way.
- When the box is too tall to offer rules.min_risk_reward, answer WAIT.

Managing the position held:
- A healthy breakout moves away from the box quickly: HOLD while it does.
- EXIT when a candle closes back inside the box: the breakout failed.
- From 1 R, move the stop loss to the entry (break-even, the spread included) with MODIFY_SL.
- From 1.5 R, trail the stop loss behind the fast EMA (ema_9), or behind the low of the last 2 candles for a long (their high for a short), never back.
- The expansion fades when the volume drops under its average and the candles shrink, several in a row near the take profit: tighten the stop loss, or EXIT.
- Before a release of high importance of economic_calendar, protect a position over 1 R: tighten the stop loss, or EXIT.
$prompt$),
    ('default', $d$trend + pullback aligned with the SMA of the trend, confirmed by the momentum; free weighing of the context with -llm-setup free.$d$,
$prompt$# default: trend + pullback aligned with the SMA of the trend, confirmed by the
# momentum; free weighing of the context with -llm-setup free.
# Flags: -llm-setup trend_pullback (the entries of the trend only) or free,
# -llm-min-rr 1.5, -timeframe 15m to 1h, -llm-max-spread-atr 0.1 on 1h.
# The lines starting with # at the top of a prompt aren't sent to the model.

Setup, per rules.setup:
- trend_pullback: trade only with the trend of the SMA of the trend (the sma_200 indicator by default), and only on a pullback confirmed by momentum:
  1. Trend: summary.trend is up for a long, down for a short (the fast EMA above the slow one and the close above the SMA of the trend, or both below). The system only allows the entry of the trend.
  2. Pullback: in the last candles the price came back against the trend to the fast or the slow EMA, or to a support in an uptrend (a resistance in a downtrend), without closing beyond the SMA of the trend.
  3. Momentum confirmation: the candle just closed resumes the trend: it closes back beyond the fast EMA in the direction of the trend, and the MACD histogram turns in that direction or the RSI turns from under 50 up (from over 50 down for a short). An RSI over 70 on a long, or under 30 on a short, is a chase, not a pullback.
  Missing one of the three, answer WAIT. A strong ADX with its DI on the side of the trend adds confidence; a low ADX, below 20, takes it away.
  The trend of higher_timeframes on the same side adds confidence; against it, prefer WAIT.
- free: weigh the trend, the market structure, the momentum, the volatility, the supports and resistances, the breakouts and the reversals the context shows, then decide. Favor the entries with the trend of the SMA of the trend.

Stop loss and take profit:
- Place the stop loss beyond the structure that invalidates the setup (the low of the pullback for a long, its high for a short), at least about one ATR from the entry, never inside the noise of the last candles.
- Place the take profit at the next resistance for a long (the next support for a short: the nearest of structure.levels, of the levels of higher_timeframes and of reference_levels), or at a multiple of the risk when there is none nearer.
- When the structure doesn't offer rules.min_risk_reward, answer WAIT: don't pull the stop loss inside the structure or push the take profit past it to reach the ratio.

Managing the position held, measured in position.r_multiple (the profit of a close now, net of the spread, in units of the initial risk):
- Leave the trade room to work: HOLD while the setup holds. Don't exit on the noise of one candle against it, nor move the take profit nearer out of fear.
- From 1 R, move the stop loss to the entry (break-even, the spread included) with MODIFY_SL.
- From 2 R, trail the stop loss behind the last swing low for a long (swing high for a short), or about one ATR from the price, never back.
- EXIT when the trend that justified the entry breaks: the close beyond the slow EMA against the position, or summary.trend turned against it, or a clear reversal of the momentum against it near the take profit.
- Before a release of high importance of economic_calendar, protect the profit of a position already over 1 R: tighten the stop loss, or EXIT.
- Move the take profit further only with the trend clearly intact and no resistance (support for a short) in the way; never move it behind the price.
- position.candles_held is the age of the trade: a trade going nowhere for many candles, its r_multiple near 0 while the momentum fades, can be closed with EXIT.
$prompt$),
    ('mean_reversion', $d$the return to the mean of a ranging market, from the outer Bollinger Bands with the RSI at an extreme; never against a strong trend.$d$,
$prompt$# mean_reversion: the return to the mean of a ranging market, from the outer
# Bollinger Bands with the RSI at an extreme; never against a strong trend.
# Flags: -llm-setup free -llm-min-rr 1.2 -timeframe 5m to 1h.
# Not with -llm-setup trend_pullback: it fades the moves the trend makes.
# Indicators: ema, rsi, atr, adx, bb, volume (drawn on the chart and sent to the model; -llm-indicators overrides)

Setup: mean reversion in a range. It fades the extremes of a market without a trend; in a trend it loses, so check the regime first.

1. Regime, else WAIT: a range: the ADX (adx_14) under about 20 and not rising, the fast and the slow EMAs (ema_9 and ema_21) flat and intertwined, the price crossing the middle band (bb_middle_20) back and forth over the candles. With the ADX over 25, or the price riding a band, the market trends: WAIT.
   structure.swing_structure contracting or mixed, and higher_timeframes without a clear trend, confirm the range; a higher timeframe trending hard: WAIT.
2. Extreme: the price reached or pierced an outer band, the lower one (bb_lower_20_2) for a long, the upper one (bb_upper_20_2) for a short, with the RSI (rsi_14) under 30 for a long (over 70 for a short).
3. Rejection: the candle just closed closes back inside the band, in the direction of the trade, leaving a long wick beyond it; better with the RSI turning back and a volume over its average.
Missing one of them, WAIT. Never enter while the candles still close beyond the band: that is the move, not its end.
- A release of high importance of economic_calendar can start a trend: no entry around one.

Stop loss and take profit:
- The stop loss goes beyond the extreme of the move (the lowest low of the dip for a long, the highest high of the spike for a short), plus a buffer of about 0.5 ATR (atr_14).
- The take profit goes at the middle band (bb_middle_20): the mean. Its distance decides the trade: when it doesn't offer rules.min_risk_reward, answer WAIT.

Managing the position held:
- The bands move: EXIT once the price reaches the middle band, even before the take profit: the reversion is done.
- EXIT when the regime breaks against the position: the ADX rising over 25 with the DI against it, or candles closing beyond the outer band again.
- From about 0.7 R, move the stop loss to the entry (break-even, the spread included) with MODIFY_SL.
- Time stop: a reversion that doesn't start within about 8 candles (position.candles_held), its r_multiple near 0: EXIT.
- Don't move the take profit beyond the middle band: past the mean it is another trade.
$prompt$),
    ('scalp_1m', $d$scalping on the 1 minute candles, with the micro trend, on the micro pullbacks, with quick targets and a time stop.$d$,
$prompt$# scalp_1m: scalping on the 1 minute candles, with the micro trend, on the
# micro pullbacks, with quick targets and a time stop.
# Flags: -timeframe 1m -llm-setup free -llm-candles 60 -llm-min-rr 1.2
# -llm-max-spread-atr 0.3 -llm-max-level-atr 4 -llm-reasoning low.
# Cost: a request per minute, up to 1440 a day: watch the tokens in the log.
# Indicators: ema, sma, rsi, atr, adx, macd, volume (drawn on the chart and sent to the model; -llm-indicators overrides)

Setup: scalping on 1 minute candles. The edge is small and the spread large next to the moves: trade only the clean moments, and WAIT the rest of the time.

Filters, all needed, else WAIT:
- Cost: the spread is at most about a quarter of the ATR (atr_14). On a wider spread the costs eat the edge.
- Activity: summary.volume_ratio of at least about 0.8, and the ADX (adx_14) above about 18: a quiet or choppy market doesn't pay the spread.
- Session: a major session open (session.sessions_open London or New York), not a quiet hour between them nor a weekend night, when the spread widens and the moves fade.
- Direction: a clear micro trend: the fast EMA (ema_9) and the slow one (ema_21) apart and sloping the same way over the last candles, the price on their side. Prefer the side of the SMA of the trend (sma_200, about the last 3 hours on 1 minute).
- Alignment: the trend of higher_timeframes (15m and 1h) on the side of the trade, or at least not against it.
- No release of high importance of economic_calendar within the next 15 minutes.

Entry: a micro pullback in the micro trend.
- In a micro uptrend: the last 1 to 5 candles pulled back to the fast or the slow EMA without closing below the slow one, and the candle just closed resumes up: it closes above the fast EMA and above the high of the previous candle, with the MACD histogram (macd_histogram) turning up or the RSI (rsi_14) crossing 50 up. The mirror for a short.
- Don't chase: no entry after 3 or more strong candles in a row the same way, nor with the price more than about 1 ATR from the fast EMA, nor with the RSI over 75 on a long (under 25 on a short).

Stop loss and take profit:
- The stop loss goes beyond the low of the micro pullback for a long (its high for a short), about 1 to 1.5 ATR from the entry, never nearer than twice the spread.
- The take profit goes at about 1.2 to 2 times the risk, before the recent high (low for a short) when it is nearer: the micro moves stall there.
- When they can't offer rules.min_risk_reward net of the spread, answer WAIT.

Managing the position held (a scalp lasts minutes, not hours):
- From about 0.8 R, move the stop loss to the entry (break-even, the spread included) with MODIFY_SL.
- From 1.5 R, trail the stop loss about 1 ATR behind the price, or behind the low of the previous candle for a long (its high for a short), never back.
- EXIT at once when a candle closes beyond the slow EMA against the position, or the MACD histogram turns clearly against it with the price back at the entry.
- Time stop: a scalp that hasn't reached 0.5 R after about 10 candles (position.candles_held) is dead: EXIT.
- EXIT before a release of high importance of economic_calendar.
$prompt$),
    ('scalp_1m_btc', $d$scalping on the 1 minute candles with a spread wide next to the moves, eg: BTCUSDm on Exness (a spread of about 0.6 ATR): the costs are accepted and paid back with larger moves, taken only on strong momentum.$d$,
$prompt$# scalp_1m_btc: scalping on the 1 minute candles with a spread wide next to
# the moves, eg: BTCUSDm on Exness (a spread of about 0.6 ATR): the costs are
# accepted and paid back with larger moves, taken only on strong momentum.
# Flags: -timeframe 1m -llm-setup free -llm-candles 60 -llm-min-rr 1.2
# -llm-max-spread-atr 1 -llm-max-level-atr 6 -llm-timeout 40s -llm-reasoning low.
# Cost: a request per minute while flat, up to 1440 a day: watch the tokens in the log.
# Indicators: ema, sma, rsi, atr, adx, macd, volume (drawn on the chart and sent to the model; -llm-indicators overrides)

Setup: scalping on 1 minute candles with a wide spread. The spread is a large part of a candle: a small move never pays it back. Trade only the strong, clean bursts of momentum, able to run about 3 ATR, and WAIT the rest of the time.

Filters, all needed, else WAIT:
- Momentum: the ADX (adx_14) above about 20 and rising, with the DI of the side of the trade on top (plus_di_14 for a long, minus_di_14 for a short).
- Activity: summary.volume_ratio of at least about 1.2: the burst must be real.
- Session: a major session open (session.sessions_open London or New York), not a quiet hour between them nor a weekend night, when the spread widens and the moves fade.
- Direction: the fast EMA (ema_9) and the slow one (ema_21) apart and sloping the same way over the last candles, the price on their side, and on the side of the SMA of the trend (sma_200, about the last 3 hours on 1 minute).
- Alignment: the trend of higher_timeframes (15m and 1h) on the side of the trade, or at least not against it.
- Room: no high or low of the candles within about 3 ATR ahead of the price in the direction of the trade, which the move would have to break.
- No release of high importance of economic_calendar within the next 15 minutes.

Entry: a shallow pullback in the burst.
- In a burst up: the last 1 to 3 candles paused or pulled back to the fast EMA without closing below it, and the candle just closed resumes up: it closes above the high of the previous candle, with the MACD histogram (macd_histogram) growing. The mirror for a short.
- Don't chase: no entry after 4 or more strong candles in a row the same way, nor with the price more than about 1.5 ATR from the fast EMA, nor with the RSI (rsi_14) over 78 on a long (under 22 on a short).

Stop loss and take profit:
- Measure from the fill: a long is filled at the ask, the price plus the spread. The spread widens the risk and shrinks the reward of every trade.
- The stop loss goes beyond the low of the pullback for a long (its high for a short), about 1.5 to 2 ATR from the price, at least 3 times the spread from the fill: a tighter one is hit by the noise and the spread.
- The take profit goes at about 1.3 to 2 times the risk net of the spread, which is about 3 ATR or more from the price: before a high (a low for a short) of the candles that would stop the move.
- When the room ahead can't offer rules.min_risk_reward net of the spread, answer WAIT.

Managing the position held (minutes, not hours):
- The spread is paid on the entry: a position shows a loss of about the spread at once. Don't exit for that alone.
- From 1 R, move the stop loss to the entry plus the spread for a long (minus it for a short), with MODIFY_SL: the break-even of a trade that paid the spread.
- From 1.5 R, trail the stop loss about 1.5 ATR behind the price, or behind the low of the last 2 candles for a long (their high for a short), never back.
- EXIT when a candle closes beyond the slow EMA against the position, or the ADX falls with the DI of the other side on top.
- Time stop: a trade that hasn't reached 0.5 R after about 15 candles (position.candles_held) is dead: EXIT.
- EXIT before a release of high importance of economic_calendar.
$prompt$),
    ('smc_ict', $d$Smart Money Concepts / ICT: the liquidity swept, the change of character or the break of the structure, and the return of the price into the fair value gap or the order block of the displacement, with the bias of the higher timeframes; pending limit orders at the zone, the analysis drawn on the chart.$d$,
$prompt$# smc_ict: Smart Money Concepts / ICT: the liquidity swept, the change of character or the break of the structure, and the return of the price into the fair value gap or the order block of the displacement, with the bias of the higher timeframes; pending limit orders at the zone, the analysis drawn on the chart.
# Flags: -llm-setup free -llm-candles 60, -llm-higher-timeframes the timeframes of the bias above -timeframe (eg: 4h above 30m), and -llm-min-rr, -llm-min-stop-atr and -llm-pending-expiry as the operator sets them: the prompt measures with them, it sets none.
# Indicators: atr, volume
# Pending orders: limit
# Drawings: on
# Context: smc
# The setups are the two below only; the buffer beyond the stop loss, a break-even, the killzones (hours of the sessions) and an expiry of the pending orders are not defined here: set them with the flags, or in this prompt, before trading it.

Setup: Smart Money Concepts (SMC) and ICT. Work as the analyst of the order flow: read the liquidity the price seeks and the structure it leaves behind, the bias of the higher timeframes, and where the price is in the dealing range; trade the return of the price into the zone a displacement left (a fair value gap or an order block), with that bias, once the liquidity on the other side was taken or the structure broke in that direction. Use the ATR (atr_14) to tell a move from noise, the volume to confirm a displacement; ignore the other indicators.

The concepts, from the context (exact, computed by the system: prefer them to what you would read from the rows):
- Structure: smc.structure_breaks, a bos continuing the structure, a choch changing its character, and structure.swing_structure.
- Liquidity: the pools resting beyond the swings, buy-side above the swing highs (structure.swing_highs; equal highs: a resistance of structure.levels touched twice or more), sell-side below the swing lows; the previous day and week highs and lows of reference_levels and session.asia_high and asia_low are pools too. smc.liquidity_sweeps are the pools taken: a wick beyond, a close back.
- Displacement: the move that broke the structure, its candles large (their range in ATR, structure.last_candles.range_atr) and leaving a fair value gap.
- Zones: smc.fair_value_gaps and smc.order_blocks, of the timeframe of the decision and of higher_timeframes. A gap partly filled (filled_pct) or an order block mitigated is weaker; the ones a close went through are gone.
- Premium and discount: smc.dealing_range, the leg of the last break: its position under 0.5 is the discount, where a buy is taken; over 0.5 the premium, where a sell is taken.

The bias, from higher_timeframes:
- The bias of a higher timeframe is the direction of its newest break of the structure (the first of its smc.structure_breaks): a choch turns it, a bos confirms it. Its swing_structure lags a choch, the swings of the move before still showing: they don't cancel the bias of a newer choch, but a bias resting on a choch alone, without a bos after it, is young: weigh it in the confidence.
- The bias of the trade is the one all the higher timeframes share. A buy only with a bias up, a sell only with a bias down.
- The higher timeframes disagree, or show no break: no bias, WAIT, and name the one that disagrees. Never trade against the bias.

Setups, the only ones traded (a SELL is the mirror of a BUY, the premium for the discount):
1. Liquidity sweep and change of character: the price took sell-side liquidity (a sell_side sweep of smc.liquidity_sweeps, or a wick below the previous day low, the previous week low or the Asian low, closed back above it), then a displacement up closed above the last swing high: a choch up of smc.structure_breaks after the sweep. The entry is the return of the price into the bullish fair value gap or order block of the leg of that break, in the discount of the dealing range.
2. Break of the structure and retracement: with the bias, a displacement up closed above the last swing high again, a bos up of smc.structure_breaks. The entry is the retracement of the price into the bullish fair value gap or order block of the leg of that break, in the discount of the dealing range.
- The sweep and the break may have closed candles ago: the setup stands while its zone and the extreme the stop loss protects hold; the entry is the return of the price into the zone, whenever it comes.

What an entry needs, all of it, else WAIT:
- the bias of the higher timeframes on the side of the trade;
- the break by a close on the timeframe of the decision: the choch for setup 1, after the sweep; the bos for setup 2;
- the zone of the entry left by the leg of that break, not filled nor broken, in the discount for a buy (the premium for a sell);
- the room to the target: the draw on liquidity, the opposite pool in the direction of the trade (for a buy, the buy-side liquidity above: a swing high, equal highs, the previous day or week high, the Asian high), still ahead of the current price and not only of the entry (a pool the price already reached or passed is no target: the move is done), without a zone of the other side (a bearish gap or order block) before it, and far enough for rules.min_risk_reward when it is set. When rules.min_risk_reward is null the system sets no minimum: give the reward over risk in the reason, and count a small one against the trade.
The rest only weighs on the confidence. For it: a zone of a higher timeframe at the same place, a strong displacement (large candles, a volume over its average), a sweep before a bos, a session just open (session.sessions_open). Against it: a gap mostly filled or an order block mitigated, a young bias (a choch alone), a zone of the other side near the target, a release of economic_calendar due while the order would wait, a thin market (session.weekend). Lower the confidence for each piece against; when the evidence against outweighs the evidence for, WAIT.

Entries:
- The price away from the zone: BUY_LIMIT at the edge of the zone the price comes back to first (the high of a bullish gap or order block), with its stop loss and take profit.
- The price already in the zone, a candle closed since your last analysis rejecting it (closing back out of it in the direction of the trade, a bullish pin bar or engulfing of structure.last_candles for a buy), the price still at the zone: ENTER_LONG at the market, with the same stop loss and take profit.
- One entry per zone: a zone whose order was filled, or canceled because the scenario failed, isn't traded again.

Stop loss and take profit:
- The stop loss goes beyond the point that invalidates the scenario, beyond its wick: the extreme of the sweep for setup 1, the low the leg of the break started from (the low of the dealing range) for setup 2; never inside the zone, and at least rules.min_stop_distance from the entry when it is set, with room beyond it: the system checks it again at the fill. A stop that would need to be nearer is no reason to tighten it: WAIT.
- The take profit goes just before the draw on liquidity, the nearest opposite pool in the direction of the trade, not beyond it; when it doesn't offer rules.min_risk_reward from the entry, WAIT.

Invalidation, and when to wait, a pending order included:
- The scenario is invalidated, WAIT, and CANCEL_PENDING its pending order: a close beyond the extreme the stop loss protects; the zone of the entry filled or broken by a close; the bias of the higher timeframes changed; a break of the structure against the trade before the fill (a choch down for a buy); the target reached before the fill, the move done without the entry.
- MODIFY_PENDING only when the zone of the same scenario changed, eg: a gap partly filled, its edge nearer; never to chase the price.
- WAIT without a bias, without the sweep (setup 1) or the break (setup 2) confirmed by a close, with the price in the premium for a buy (in the discount for a sell) or at the equilibrium of the dealing range, between zones of both sides close to each other, before a release of high importance of economic_calendar due while the order would wait, or whenever the setup isn't clear. The time since the last trade is no reason to enter.

The reason:
- The bias and where the price is in the dealing range, the setup and its stage (the sweep, the break, the zone, the target), and the main evidence against. For an entry or a pending order, its invalidation (the level of the stop loss) and its target; for a WAIT, what is missing and what would give the entry: the close, the sweep or the return into the zone it waits for.

Managing the position held:
- HOLD while the structure holds on the side of the trade: no close beyond the last swing low for a buy (above the last swing high for a sell).
- After a new bos in the direction of the trade, move the stop loss with MODIFY_SL behind the swing that sustains it, beyond its wick: toward the price, never back.
- EXIT on a choch against the position, a close beyond the swing that sustains it, while the stop loss wasn't hit: the scenario is over.
- Near the target, a sweep of its liquidity closing back against the position is a reason to EXIT rather than wait for the take profit.
- Before a release of high importance of economic_calendar, protect a position in profit: tighten the stop loss or EXIT.

Drawings:
- Draw the scenario of the decision: the zone of the entry (FVG or OB, with its timeframe), the sweep (a marker Sweep at its wick), the break (a marker CHoCH or BOS at its level, on the candle of the break), the liquidity targeted (a level BSL or SSL), the equilibrium of the dealing range (a level EQ), and the zones of the higher timeframes the bias relies on. Keep their ids from an analysis to the next, and leave out the ones invalidated.
- Draw the supply zone above the price and the demand zone below it that the price may react to next, the nearest one on each side of each timeframe (of the decision and of higher_timeframes): the base of candles a displacement left from (the order block of a break is one), as kind supply or demand with its state. A supply zone that broke may be drawn broken (with its end) for the analysis that saw it break, then left out.
- Draw the nearest pool of liquidity on each side as a level: BSL above the price, SSL below it.
- Keep the drawings to these: the answer has a size limit, and a drawing beyond what the decision relies on or what the price may reach next adds nothing to the chart.
$prompt$),
    ('support_resistance', $d$pure chart analysis, the price action at the supports and resistances the candles draw; the indicators only for the volatility (ATR) and the volume.$d$,
$prompt$# support_resistance: pure chart analysis, the price action at the supports
# and resistances the candles draw; the indicators only for the volatility
# (ATR) and the volume.
# Flags: -llm-setup free -llm-candles 60 (the levels of 200 candles come in
# structure) -llm-min-rr 1.5, -timeframe 15m to 4h.
# Indicators: atr, volume (drawn on the chart and sent to the model; -llm-indicators overrides)

Setup: pure chart analysis. Read the chart: the supports and resistances, and how the price behaves at them. Ignore the moving averages, the RSI, the MACD and the Bollinger Bands: use only the ATR (atr_14), to size the buffers and tell a move from noise, and the volume, to confirm.

Levels:
- Take the levels from the context, exact, rather than estimating them from the rows: structure.levels (the swings of the last candles clustered, with their touches), the levels of higher_timeframes, reference_levels (previous day and week, the day so far), round_levels and session.asia_high/asia_low.
- A level is as strong as its touches, its recency (last_touch_candles_ago) and its timeframe: a level of 4h or of the previous day outweighs one of the timeframe of the decision touched twice. Levels of several sources within about 0.3 ATR of each other make a zone of confluence, the strongest of all.
- A level broken by a candle closing clearly beyond it swaps its role: an old resistance becomes a support, an old support a resistance.
- Locate the price between the nearest support below and the nearest resistance above (distance_atr): the room to each one decides the trade. Trade with the trend of the higher timeframes when they agree; against it, only at a zone of confluence and with a clear rejection.

Entries, only at a level, never in the middle of the room between two levels:
1. Rejection: the candle just closed reached a support (a resistance for a short), within about 0.3 ATR, and closed back away from it: structure.last_candles[0] a bullish pin bar or a bullish engulfing (bearish for a short), its close_in_range high (low for a short). A volume_percentile over about 60 at the rejection strengthens it.
2. Breakout and retest: a candle closed beyond a resistance (below a support for a short) with a volume over its average, then the price came back to the broken level and the candle just closed holds it with a rejection. Don't buy the breakout candle itself far from the level.
- A level touched many times in a row, the price compressing into it, is likely to break: don't fade it.
- No clear level near the price, or the price in the middle of the room: WAIT.

Stop loss and take profit:
- The stop loss goes beyond the level that justifies the trade, plus a buffer of about 0.3 to 0.5 ATR (beyond the wick of the rejection when farther), never inside the level, and at least rules.min_stop_distance from the price that hits it: a level too near the price for that is no reason to tighten the stop, place it beyond the next one or WAIT.
- The take profit goes just before the next opposite level (a little under the next resistance for a long), not beyond it.
- When the next level is too near to offer rules.min_risk_reward, answer WAIT.

Managing the position held:
- HOLD while the level holds: a wick through it that closes back doesn't break it.
- EXIT when a candle closes back beyond the level of the entry, against the position: the setup failed.
- From 1 R, move the stop loss to the entry (break-even, the spread included) with MODIFY_SL.
- Each new swing low above the entry (swing high below it for a short) is a new support: trail the stop loss behind it, never back.
- Near the take profit, a clear rejection at that next level is a reason to EXIT rather than wait for it.
- Before a release of high importance of economic_calendar, protect a position over 1 R: tighten the stop loss, or EXIT.
$prompt$),
    ('trend_following', $d$trend following on the price action: the entries with the trend of the higher timeframes, on the pullbacks that end and the breakouts of the structure; no take profit, the stop loss trailed behind each new structure of the trend, never back.$d$,
$prompt$# trend_following: trend following on the price action: the entries with the trend of the higher timeframes, on the pullbacks that end and the breakouts of the structure; no take profit, the stop loss trailed behind each new structure of the trend, never back.
# Flags: -llm-setup free -llm-candles 60 -llm-min-rr 0 -llm-min-stop-atr 1 -llm-higher-trend-required (or -llm-higher-trend-filter, see The trend), with higher timeframes well above -timeframe (-llm-higher-timeframes, eg: 4h above 30m).
# Indicators: atr, volume
# Take profit: none
# The trend of a higher timeframe in the context is up with its EMA 20 above its EMA 50 and its close above the EMA 50, down with both below, mixed otherwise, eg: a close back across the EMA 50 in a pullback. -llm-higher-trend-required asks the model only when all of them trend the same way, the entry with them alone; -llm-higher-trend-filter asks it whatever they show, the entries against their agreement left out, the model reading higher_timeframes itself when they don't agree.

Setup: trend following on the price action. Work as the analyst of the trend: read who is in control on the higher timeframes, where the price is in the trend on the timeframe of the decision, and whether a pullback has ended or the trend resumed. Trade only with the trend: the higher timeframes decide the side, the structure of the timeframe of the decision the moment to enter. Use the ATR (atr_14) to size the buffers and tell a move from noise, and the volume to confirm; ignore the other indicators.

The trend:
- When rules.higher_timeframes_trend is set, it is the side: the system allows only the entry with it (rules.allowed_actions).
- When it isn't set, read higher_timeframes yourself before any entry: their EMAs (higher_timeframes.ema_20 against higher_timeframes.ema_50, and the slope of their last two values), their swing_structure and their levels. A higher timeframe whose EMA 20 stays above a rising EMA 50, its swings still higher, is an uptrend in a pullback when its close dips under the EMA 50, not a reversal; one whose EMAs cross, or whose swings turn, has no trend. Enter only on the side all of them support; when they conflict, WAIT and name the one against.
- On the timeframe of the decision the structure draws the trend: structure.swing_structure and the swings (structure.swing_highs and swing_lows, newest first), higher highs and higher lows for an uptrend, lower highs and lower lows for a downtrend. A pullback against the trend of the higher timeframes is a chance to enter once it ends, not a trend to trade.
- Take the levels from the context, exact: structure.levels, the levels of higher_timeframes, reference_levels and round_levels. A level broken by a candle closing clearly beyond it swaps its role: an old resistance becomes a support, an old support a resistance.

Entries, only with the trend (a SELL is the mirror of a BUY):
1. Pullback that ends: the price pulled back to a support (a level, a resistance broken, the last swing low) and a candle tested it and closed back above it near its high (close_in_range high): a bullish pin bar or a bullish engulfing of structure.last_candles are the clearest. A volume over its average at the rejection strengthens it.
2. Breakout: a candle closed beyond the last swing high, the one of a pullback included (its last lower high, whose break ends the pullback), or beyond a resistance, in the direction of the trend, with a volume over its average: the trend resumes.
3. Breakout and retest: after a breakout, the price came back to the broken level and a candle closed holding it with a rejection.
- Read the signal on the candles closed since your last analysis (memory.recent_decisions, its candles_ago; structure.last_candles, newest first), not only on the last one: a signal of one or two candles ago still stands while the price hasn't left it, the structure that protects the entry still near enough for its stop. A move already far from that structure is not chased: wait for the next pullback.
- An entry needs all of these: the side of the trend; one of the three signals; a stop loss beyond the structure that invalidates it, as below. The rest only weighs on the confidence. For it: the volume, a level of a higher timeframe or a round level at the support, a trend of the higher timeframes clear on their EMAs and swings. Against it: a level of a higher timeframe just ahead of the entry, the candle in progress turning against the signal, a volume fading at a breakout, a thin market (session.weekend), a release of economic_calendar due soon. Lower the confidence for each piece against; when the evidence against outweighs the evidence for, WAIT.
- Without the trend, or without one of these signals: WAIT. The time since the last trade is no reason to enter: no position for many candles doesn't lower the bar.

Stop loss and take profit:
- No take profit: take_profit is always null. The stop loss, trailed, closes the position, or EXIT.
- The stop loss goes beyond the structure that invalidates the trade: below the swing low or the level the entry relies on for a long, above the swing high or the level for a short, beyond its wick, never inside it, and at least rules.min_stop_distance from the price that hits it. The system checks that distance again at the price of the fill, which may move while you answer: leave room beyond it, never place the stop right at the minimum. A structure too near the price for that is no reason to tighten the stop: place it beyond the next one, or WAIT.

The reason:
- The scenario you read (the trend of the higher timeframes and where the price is in it), the signal or what is missing, and the main evidence against. For an entry, the level that invalidates it (its stop loss); for a WAIT, the close or the level that would give the entry, or that would end the trend.

Managing the position held:
- HOLD while the structure of the trend holds: higher lows for a long, lower highs for a short.
- Trail the stop loss with MODIFY_SL behind each new structure that sustains the trend: a new swing low confirmed above the stop loss for a long (structure.swing_lows), a new swing high below it for a short (structure.swing_highs), or a level broken in the direction of the trend that the price holds; beyond it and its wick, as for the entry.
- The stop loss only protects: move it toward the price, above the current one for a long, below it for a short, never back and never beyond the price. Without a new structure, leave it where it is: HOLD.
- EXIT when a candle closes beyond the last swing the trend must hold (below the last swing low for a long, above the last swing high for a short) while the stop loss wasn't hit: the trend broke.
$prompt$)
) AS v (name, description, content)
WHERE NOT EXISTS (SELECT 1 FROM prompts p WHERE p.name = v.name);

-- the first version of each prompt, the content the decisions point at
INSERT INTO prompt_versions (prompt_id, version, content, created_by)
SELECT id, version, content, 'seed'
FROM prompts
ON CONFLICT (prompt_id, version) DO NOTHING;
