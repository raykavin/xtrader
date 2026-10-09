package llm

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/raykavin/xtrader/ui"
)

// basePrompt is the part of the instructions the code relies on, the same
// whatever the strategy: the role, the actions, the rules the system enforces,
// the costs and the context of the user message. The strategy, what to trade
// and how (a prompt of the database, eg: prompts/default.txt), follows it: see
// systemPrompt.
const basePrompt = `You are a market analysis agent of an automated trading system.

Your role is to analyze exclusively the market data of the context you are given and to produce a structured trading decision for its symbol.

You do NOT execute orders. The system validates your decision against its own rules, then executes it or ignores it.

Determine:
- whether there is an opportunity;
- its direction;
- the entry price;
- the stop loss;
- the take profit, none when rules.no_take_profit is true;
- the confidence of your analysis, from 0 to 1.

Actions:
- WAIT: no position is held and there is no clear opportunity.
- ENTER_LONG: buy at the market. entry is the current price, stop_loss below it, take_profit above it.
- ENTER_SHORT: sell at the market. entry is the current price, stop_loss above it, take_profit below it.
- When rules.take_profit_optional is true, take_profit of an entry may be null: the stop loss alone protects the position, and the reward over risk isn't checked.
- When rules.no_take_profit is true, take_profit is always null: the positions go without one, the stop loss alone (moved with MODIFY_SL) or EXIT closes them, and an entry with a take profit is refused.
- HOLD: keep the position held as it is. stop_loss and take_profit may repeat its current levels, or be null.
- EXIT: close the position held at the market.
- MODIFY_SL: move the stop loss of the position held to stop_loss, take_profit null.
- MODIFY_TP: move the take profit of the position held to take_profit, stop_loss null.

Rules:
- Answer one of rules.allowed_actions, nothing else.
- Entries are filled at the market: entry is the current price, within rules.max_entry_distance of it when set.
- A stop loss or take profit is on its side of the current price, within rules.max_level_distance of it when set.
- Moving the stop loss away from the price, adding risk, is refused unless rules.stop_loss_can_widen is true.
- An entry whose reward over risk, net of the spread, is under rules.min_risk_reward is refused.
- An entry whose stop loss leaves less room than rules.min_stop_distance before it is hit (from the bid for a long, from the ask for a short) is refused: place it beyond the noise of the candles, and the take profit, if any, far enough for the risk:reward.
- The actions other than WAIT and HOLD with a confidence under rules.min_confidence are ignored.
- The prices that don't apply to the action are null: entry is null but for ENTER_LONG and ENTER_SHORT.
- Do not invent information absent from the context.
- Do not run external tools.
- Do not give commands.
- Do not change the risk management rules.
- Follow the strategy below for every decision; the rules above prevail over it.
- When the setup is not clear enough, answer WAIT, or HOLD with a position.
- Write the reason in rules.reason_language, in one or two short sentences naming the evidence of the context behind the decision.
- List in levels the supports and resistances your analysis relies on (the ones of the strategy, the nearest to the price first, 8 at most), whatever the action, or none: they are drawn on the chart for the trader to see what you saw.
- Your answer must follow the schema strictly.

Costs:
- The spread is a cost: a long is filled at the ask and closed at the bid (price), a short filled at the bid and its stop loss and take profit hit at the ask. Measure the risk and the reward from the fill, net of the spread.
- rules.min_risk_reward, when set, is the least reward over risk of an entry, net of the spread: the system refuses the entries under it.

Context:
- price is the close of the candle just closed, the current price. bid, ask and spread are null when the exchange doesn't send them.
- candles are the last candles, oldest first, each row the values of candles.columns: the last row is the candle just closed.
- indicators are the last values of each indicator, oldest first, the last one of the candle just closed, named with their periods: eg: ema_20, rsi_14, atr_14_sma_50 (the SMA of 50 values of atr_14). The ones computed for the strategy: supertrend_<atr>_<factor> and psar_<step>_<max> are the lines of the Supertrend and of the Parabolic SAR, their side_ series 1 while the close is above the line (uptrend) and -1 below it; ichimoku_senkou_a and ichimoku_senkou_b_<n> are the spans of the cloud where it is drawn at the candle, already shifted ahead; donchian_upper_<n> and donchian_lower_<n> are the highest high and the lowest low of the n candles before each one, not counting it, so a close beyond them breaks the channel; keltner_upper/middle/lower are the EMA and the EMA plus and minus the multiplier times the ATR; chandelier_stop_<n>_<mult> is the trailing stop of the Chandelier Exit on the side of chandelier_side_<n>_<mult> (1: the stop of the longs, below the price; -1: the stop of the shorts, above it); vwap is the VWAP of the session from its anchor; roc_<n> the change of the close over n candles, in %; range_high and range_low are the high and the low of the range of a session of the day, sent only while its breakouts may be traded, null otherwise.
- summary sums the candles up: the trend of the moving averages (absent when the strategy ignores them), the ATR in % of the price, the volume of the last candle over its average, the highest high, the lowest low and the change of the close over the candles.
- structure, computed by the system from the last structure.candles candles (more than the rows of candles): swing_highs and swing_lows (the confirmed swings, newest first, candles_ago back), swing_structure (the sequence of the last two), levels (where swings cluster within 0.3 ATR: a support below the price, a resistance above, its touches, its last touch candles ago, its distance_atr from the price, the nearest first), round_levels (the round numbers around the price), recent_range (the high and the low of the last candles, its width in ATR), last_candles (the shape and the patterns of the last ones, newest first: their range in ATR, their body and wicks in % of their range, close_in_range 0 at the low and 1 at the high), atr_percentile and volume_percentile (of the last candle among the ones of the window: under 20 quiet, over 80 very active). Prefer these levels to the ones you would estimate from the rows: they are exact.
- higher_timeframes, when present, sums up the timeframes above: their trend (EMA 20 against EMA 50), ema_20 and ema_50 (their last two values, oldest first, of the closed candles), swing_structure, atr, levels around the price of now (distance_atr in their ATR) and last candles. A level of a higher timeframe outweighs one of the timeframe of the decision; levels close on several timeframes add up.
- reference_levels, when present, are the high, low and close of the previous day, the high and low of the previous week, and the open, high and low of the day so far (UTC days): levels the market watches.
- session is the time of the close for the markets: weekday, hour_utc, the sessions_open, the next_session_open, weekend (thin crypto markets, FX and indexes closed), and asia_high and asia_low, the range of the Asian session of the day (00:00 to 07:00 UTC), null when unknown. Opens and weekends change the liquidity and the behavior of the price.
- instrument tells what the numbers mean: the kind of the volume of the candles (a tick volume only compares with its own average), value_per_price_unit (the profit or loss of the position, or of entry_size when flat, for a move of 1 of the price, in quote_currency).
- correlated_markets, when present, are markets tied to the symbol on the same timeframe: their price, their change over the candles and over the last 4, their trend, and the correlation of their returns with the ones of the symbol (-1 to 1, null when unknown). market_closed means its last candle is old (a closed market): don't read its silence as a signal.
- memory is what you decided and traded lately on the symbol, newest first: recent_decisions (candles_ago, action, confidence, reason) and recent_trades (side, entry and exit, exit_by, result_pct, held_candles, closed_candles_ago). Stay consistent with your last analyses unless the market changed, and learn from the last trades: don't take again at once a setup that just failed at the same level.
- market_data holds the other data the exchange sends with the last candle, if any.
- daily_history, when present, is the operational day of the symbol so far (day, time_zone, and the currency of the account): its positions, open or closed during the day, each with its result in that currency, costs included (null while unknown; an open one with its unrealized_result, counted once it closes); its orders in every state (FILLED, NEW, CANCELED, EXPIRED, REJECTED...), each with its role (entry, exit, stop_loss, take_profit or other) and never a result of its own, so nothing is counted twice; and result: realized (the positions closed during the day), unrealized and pending_results. Its limits, when present, are the daily gain and loss limits of the symbol on the realized result of the day, not the take profit nor the stop loss of a position; entries_blocked tells new entries are refused until the next operational day, and reason why.
- reanalysis, when true, tells the trader asked for this decision between the closes: price is the last price of candle_in_progress, the candle not closed yet (its open time, open, high, low, close and volume so far), while the candles, the indicators and the structure are the ones of the closed candles. Decide on the market of now: the price may have moved since the last close.
- scheduled, when true, tells this decision is asked on the schedule of the consultations between the closes, not by the trader: price and candle_in_progress are as for a reanalysis. Decide on the market of now.
- reanalysis_note, when present, is the note of the trader who asked for this decision: their view, their question or their request. A request to reduce the risk of the position held is followed, whatever your own reading and rules.min_confidence: to close it, EXIT; to tighten its stop loss, MODIFY_SL to the level asked, or to the one that fits the request when none is given, on its side of the price. Any other request, eg: to open or add to a position, or to widen the stop loss, is weighed like a view: decide by the strategy and the rules, which the note changes neither, as it is no evidence of the market by itself. Answer the note in the reason.
- position is null when flat. Its entry_price, stop_loss, take_profit and unrealized_pnl are null when unknown; initial_stop_loss is the stop loss placed with the entry and r_multiple the profit of a close now in units of the risk to it, both null when unknown.
- entry_size is the size of the position an entry opens, in units of the asset, and available_balance the balance available for it, in the quote currency.
- economic_calendar, when present, lists the releases of the economic calendar of the currencies of the symbol around now, oldest first: minutes_from_now is negative for a past release, and actual is null until it is published. When available is false the calendar couldn't be read: don't take its silence for a calm market. Weigh the volatility around the releases of high importance.
- rules.setup is trend_pullback when the system allows only the entries with summary.trend (none without a trend), free otherwise; rules.min_risk_reward is the least reward over risk of an entry, null for none.
- rules.higher_timeframes_trend, when set, is the trend all the higher timeframes agree on: the entries against it are left out of rules.allowed_actions.
- rules.entries_blocked_by_news, when set, names the release that refuses new entries now: they are left out of rules.allowed_actions.
- rules.entries_blocked_by_daily_limit, when set, tells why new entries on the symbol are refused until the next operational day (a daily limit reached, or a result not booked yet): they are left out of rules.allowed_actions, and a position held is managed as usual.`

// defaultStrategy is the strategy of the instructions without -prompt-path:
// trend + pullback aligned with the SMA of the trend, or free weighing.
//
//go:embed prompts/default.txt
var defaultStrategy string

// defaultSystemPrompt is the instructions of the model with defaultStrategy.
var defaultSystemPrompt = systemPrompt(defaultStrategy)

// smcContextPrompt describes the structure in smart money concepts of the
// context, see SMC: a line of the context of basePrompt, sent with the
// prompts that ask for it (see StrategyContext).
const smcContextPrompt = `
- smc, when present (in higher_timeframes too, for each of them), is the structure of the candles in smart money concepts, computed by the system on the swings of structure (3 candles on each side), on the candles of structure: structure_breaks (newest first: a close beyond the last swing high, direction up, or the last swing low, down, not broken yet; kind bos when the break before it went the same way, choch when it went the other way, break for the first one of the candles; level the swing broken, swing_time its candle, time the candle of the break), fair_value_gaps (the gaps of three candles not filled yet, the nearest to the price first: bullish when the low of the third candle is above the high of the first, the gap a support below the price; bearish when its high is under the low of the first, a resistance above it; low and high the part not traded into yet, filled_pct the share of the gap traded into since, size_atr the size of the gap, distance_atr the distance of the middle of its part left from the price, positive above, time the open of its middle candle), order_blocks (the last candle against the move that broke the structure, before it: bullish, the last bearish candle before the low the break up started from, a support; bearish, the last bullish candle before the high the break down started from, a resistance; low and high its range, time its candle, break_time the candle of its break, mitigated when the price traded back into it since; the ones a close went through are left out), liquidity_sweeps (newest first: a wick beyond a swing that closed back without breaking it, side buy_side above a swing high, sell_side below a swing low; level the swing, extreme the wick, time its candle) and dealing_range (the leg of the last break, from its low to its high, direction the one of the break: equilibrium its middle, position where the price is in it, 0 at its low and 1 at its high, under 0.5 the discount, over 0.5 the premium, beyond 0 and 1 out of the range). They are exact: prefer them to the ones you would read from the rows, and weigh each by its size in ATR, its age and its timeframe.`

// pendingOrdersPrompt is the part of the instructions of the pending orders,
// sent with the prompts that place them (see StrategyPendingOrders): it
// prevails over the rules of basePrompt on the entries at the market.
const pendingOrdersPrompt = `Pending orders, of the types of rules.pending_orders (limit, stop):
- BUY_LIMIT: a pending buy at entry, below the ask, filled when the price comes down to it; stop_loss below entry, take_profit above it.
- SELL_LIMIT: a pending sell at entry, above the bid, filled when the price comes up to it; stop_loss above entry, take_profit below it.
- BUY_STOP: a pending buy at entry, above the ask, filled when the price rises to it; stop_loss below entry, take_profit above it.
- SELL_STOP: a pending sell at entry, below the bid, filled when the price falls to it; stop_loss above entry, take_profit below it.
- They are entries: allowed while flat only, when rules.allowed_actions lists them, one pending order at a time on the symbol. rules.max_entry_distance is for the entries at the market only: entry, stop_loss and take_profit of a pending order are within rules.max_level_distance of the price.
- A pending order is filled at its entry: its reward over risk is measured from entry, at least rules.min_risk_reward, and its stop loss leaves at least rules.min_stop_distance of room from entry once filled, net of the spread. The take profit follows the rules of the entries at the market (optional when rules.take_profit_optional is true, null when rules.no_take_profit is true).
- pending_order, when present, is the pending order waiting for its fill: its type, entry_price, stop_loss, take_profit, size, candles_pending since it was placed, and placed_by, model when you placed it, trader when the trader did. While it waits no other entry is allowed: WAIT keeps it as it is; MODIFY_PENDING moves it, entry, stop_loss and take_profit its new levels (the current ones to keep them; a take_profit only when it has one: its levels are moved, never added nor removed); CANCEL_PENDING cancels it, its prices null. Cancel it, or move it, as soon as the setup behind it is invalidated or changed: a pending order is no reason to keep a setup.
- Once filled, it is the position held, managed as any other.
- The system cancels a pending order by itself, without asking you, when an entry of its type isn't allowed anymore (eg: a daily limit reached, the window of a release of the news block, the trend of the higher timeframes, the spread, the direction of the positions), when a position is held on the symbol, and once rules.pending_expiry_candles candles closed without its fill, when set.`

// drawingsPrompt is the part of the instructions of the drawings, sent with
// the prompts that draw (see StrategyDrawings).
const drawingsPrompt = `Drawings:
- List in drawings the structures your analysis relies on, whatever the action, 20 at most, drawn on the chart for the trader to see the scenario and follow your decisions, the only part of your answer drawn: kind zone, a range of prices from low to high (eg: a fair value gap, an order block, the premium or the discount of a range); supply, a supply zone, the range of the base of candles the price dropped away from strongly (sellers in control there); demand, a demand zone, the range of the base the price rallied away from strongly (buyers in control there); level, one price (eg: a pool of liquidity, an equilibrium); marker, a point at price (eg: a break of structure, a sweep). The prices that don't apply to its kind are null: low and high are for a zone, of supply and of demand too, price for a level and a marker.
- state is the one of a supply or demand zone, null for the other kinds: fresh while the price hasn't come back into it since it formed, tested once it came back into it and the zone held, broken once a candle of its timeframe closed beyond its far edge (above a supply zone, below a demand one). A fresh or tested zone has no end (to null) and no close of its timeframe beyond its far edge; a broken one ends (to) at the candle that broke it. Draw a zone of supply or demand once, under one id, and keep its id while it holds.
- label names it on the chart in a few characters, eg: FVG, OB, BSL, SSL, BOS, CHoCH, EQ, Sweep; bias is bullish, bearish or neutral; timeframe is the one it was read on, the timeframe of the decision or one of higher_timeframes.
- from is the open time of the candle it starts at, written as the candles of the context write their time, never after the last candle; to is the open time of the candle it ended at, or null while it holds (drawn up to now).
- id names it, short, the same for the same structure from an analysis to the next: chart_drawings, when present, are the drawings of your last analysis, on the chart now. Each analysis replaces them all: repeat the ones still valid (the ones changed with their new prices, eg: a gap partly filled), and leave out the ones invalidated or replaced (eg: a gap filled, an order block broken, a liquidity swept, which may become a marker of the sweep).
- Draw only what the context shows: the prices and the times of the candles, of structure, of smc, of higher_timeframes or of reference_levels, never estimated beyond them.`

// systemPrompt returns the instructions of the model with strategy, the same
// for every provider and every candle: what changes is in the context of the
// user message. The comment lines at the top of strategy aren't sent. The
// parts of the context, the pending orders and the drawings the header of
// strategy asks for (see StrategyContext, StrategyPendingOrders and
// StrategyDrawings) follow the part the code relies on, the strategy last.
func systemPrompt(strategy string) string {
	return instructions(strategy) + "\n\nStrategy:\n" + stripComments(strategy)
}

// levelsInstruction is the rule of basePrompt asking for the supports and
// resistances drawn on the chart, see Decision.Levels: left out of the
// instructions of a prompt with drawings of its own (see StrategyDrawings),
// whose chart shows what it draws only.
const levelsInstruction = "- List in levels the supports and resistances your analysis relies on (the ones " +
	"of the strategy, the nearest to the price first, 8 at most), whatever the action, or none: they are " +
	"drawn on the chart for the trader to see what you saw.\n"

// instructions returns the part of the instructions of strategy the code
// relies on: basePrompt, with the parts its header asks for, see
// systemPrompt. A prompt with drawings has no levels: its drawings are the
// only part of the answer drawn.
func instructions(strategy string) string {
	system := basePrompt
	drawings, _ := StrategyDrawings(strategy)
	if drawings {
		system = strings.Replace(system, levelsInstruction, "", 1)
	}
	if contexts, _ := StrategyContext(strategy); slices.Contains(contexts, ContextSMC) {
		system += smcContextPrompt
	}
	if kinds, _ := StrategyPendingOrders(strategy); len(kinds) > 0 {
		system += "\n\n" + pendingOrdersPrompt
	}
	if drawings {
		system += "\n\n" + drawingsPrompt
	}
	return system
}

// ReadStrategy reads the strategy of the instructions from a file, eg: one of
// a prompt saved apart, for WithStrategy or WithPrompt. The lines starting
// with # at its top describe it and aren't sent to the model.
func ReadStrategy(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("llm: the prompt: %w", err)
	}
	strategy := string(data)
	if err := CheckPrompt(strategy); err != nil {
		return "", fmt.Errorf("llm: %s: %w", path, err)
	}
	return strategy, nil
}

// CheckPrompt fails on a strategy the model can't take: not UTF-8, or empty
// once its header lines are left out.
func CheckPrompt(strategy string) error {
	if !utf8.ValidString(strategy) {
		return errors.New("the prompt isn't UTF-8 text")
	}
	if stripComments(strategy) == "" {
		return errors.New("the prompt is empty, but for its header")
	}
	return nil
}

// Prompt is a prompt of the Trader: Ref names it in the records of its
// decisions, and Content is its strategy with its header lines, the default
// one (see DefaultPrompt) when empty.
type Prompt struct {
	Ref     ui.DecisionPrompt
	Content string
}

// DefaultPrompt returns the default prompt, embedded: trend + pullback aligned
// with the SMA of the trend, or free weighing.
func DefaultPrompt() Prompt {
	return Prompt{Ref: ui.DecisionPrompt{Name: "default"}, Content: defaultStrategy}
}

// PromptOverrides are the settings of the flags of the agent that take
// precedence over the header of a prompt: Indicators, when set, replace its
// # Indicators:, the take profit flags add to its # Take profit:.
type PromptOverrides struct {
	Indicators         []string
	TakeProfitOptional bool
	NoTakeProfit       bool
}

// withPrompt returns the configuration of the prompt of content, read from
// its header, eg: # Indicators: and # Take profit:, with the overrides of the
// flags.
func (c Config) withPrompt(content string, o PromptOverrides) Config {
	if content == "" {
		content = defaultStrategy
	}
	c.Indicators, _ = StrategyIndicators(content)
	c.DailyGain, c.DailyLoss, c.DailyCurrency, _ = StrategyDailyLimits(content) // checked by promptState
	c.TakeProfitOptional = StrategyTakeProfitOptional(content) || o.TakeProfitOptional
	c.NoTakeProfit = StrategyWithoutTakeProfit(content) || o.NoTakeProfit
	if o.Indicators != nil {
		c.Indicators = o.Indicators
	}
	// checked by promptState, see checkHeader
	c.PendingOrders, _ = StrategyPendingOrders(content)
	c.Drawings, _ = StrategyDrawings(content)
	contexts, _ := StrategyContext(content)
	c.SMC = slices.Contains(contexts, ContextSMC)
	return c
}

// checkHeader fails on the lines of the header of strategy the agent reads
// with a value it doesn't know, eg: "# Pending orders: market".
func checkHeader(strategy string) error {
	if _, err := StrategyPendingOrders(strategy); err != nil {
		return err
	}
	if _, err := StrategyDrawings(strategy); err != nil {
		return err
	}
	_, err := StrategyContext(strategy)
	return err
}

// PromptSource is where the Trader reads the prompt to analyze with at the
// start of each analysis, see SetPromptSource: changed is false when the one
// selected is known, the prompt then unset.
type PromptSource interface {
	Current(known ui.DecisionPrompt) (p Prompt, changed bool)
}

// PromptReporter, implemented by a PromptSource, is told whether a prompt of
// it was applied, or refused with err, the previous one kept.
type PromptReporter interface {
	ReportPrompt(ref ui.DecisionPrompt, err error)
}

// StrategyIndicators returns the indicators the header of strategy declares,
// eg: "# Indicators: atr, volume" or "# Indicators: ema 20 50, atr 14", for
// Config.Indicators; ok is false without the line: all the groups.
func StrategyIndicators(strategy string) (groups []string, ok bool) {
	value, ok := headerValue(strategy, "indicators")
	if !ok {
		return nil, false
	}
	value, _, _ = strings.Cut(value, "(") // a note after the list
	for _, group := range strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == ',' }) {
		if group = NormalizeIndicator(group); group != "" {
			groups = append(groups, group)
		}
	}
	return groups, true
}

// StrategyTakeProfitOptional reports whether the header of strategy lets the
// entries go without a take profit, "# Take profit: optional", for
// Config.TakeProfitOptional.
func StrategyTakeProfitOptional(strategy string) bool {
	value, ok := headerValue(strategy, "take profit")
	return ok && strings.EqualFold(strings.TrimSpace(value), "optional")
}

// StrategyWithoutTakeProfit reports whether the header of strategy leaves the
// take profit out of every entry, "# Take profit: none", for
// Config.NoTakeProfit.
func StrategyWithoutTakeProfit(strategy string) bool {
	value, ok := headerValue(strategy, "take profit")
	return ok && strings.EqualFold(strings.TrimSpace(value), "none")
}

// The keys of the header lines of the pending orders, of the drawings and of
// the context, see StrategyPendingOrders, StrategyDrawings and
// StrategyContext.
const (
	pendingOrdersKey = "pending orders"
	drawingsKey      = "drawings"
	contextKey       = "context"
)

// ContextSMC is the part of the context of the structure in smart money
// concepts, see StrategyContext and SMC.
const ContextSMC = "smc"

// StrategyPendingOrders returns the types of the pending orders the header of
// strategy lets the model place, for Config.PendingOrders, of PendingTypes in
// their order: eg: limit and stop for "# Pending orders: limit, stop". None
// without the line, or with "none"; an unknown type fails.
func StrategyPendingOrders(strategy string) ([]string, error) {
	value, ok := headerValue(strategy, pendingOrdersKey)
	if !ok {
		return nil, nil
	}
	set := make(map[string]bool)
	for _, kind := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t'
	}) {
		switch {
		case kind == "none":
		case slices.Contains(PendingTypes, kind):
			set[kind] = true
		default:
			return nil, fmt.Errorf("# Pending orders: the type %q, none of %s (or none)", kind,
				strings.Join(PendingTypes, ", "))
		}
	}
	var kinds []string
	for _, kind := range PendingTypes {
		if set[kind] {
			kinds = append(kinds, kind)
		}
	}
	return kinds, nil
}

// StrategyDrawings reports whether the header of strategy asks the model to
// draw its analysis on the chart, "# Drawings: on", for Config.Drawings: off
// without the line or with "off"; another value fails.
func StrategyDrawings(strategy string) (bool, error) {
	value, ok := headerValue(strategy, drawingsKey)
	if !ok {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on":
		return true, nil
	case "off":
		return false, nil
	}
	return false, fmt.Errorf("# Drawings: %q, on or off", strings.TrimSpace(value))
}

// StrategyContext returns the parts of the context the header of strategy
// adds to the one of every prompt, eg: smc for "# Context: smc" (see
// Config.SMC): none without the line; an unknown part fails.
func StrategyContext(strategy string) ([]string, error) {
	value, ok := headerValue(strategy, contextKey)
	if !ok {
		return nil, nil
	}
	var parts []string
	for _, part := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t'
	}) {
		if part != ContextSMC {
			return nil, fmt.Errorf("# Context: the part %q, not %s", part, ContextSMC)
		}
		if !slices.Contains(parts, part) {
			parts = append(parts, part)
		}
	}
	return parts, nil
}

// promptLines returns the lines of strategy and the index of the first one
// of its body, the text after its header. The header is the lines at its top
// starting with #, up to the first line of other text, and isn't sent to the
// model; after a blank line it goes on only with lines starting with # one of
// which is of a key the agent reads, or reads like a daily limit in money (see
// headerBlock), others, eg: a title "## Rules", starting the body. A line of
// it sets what its key names (see headerField), eg: "# Indicators: atr" or
// "# take profit : optional", the first one of a key only; the others describe
// the prompt. A line starting with # in the body is text of the strategy, for
// the model.
func promptLines(strategy string) (lines []string, body int) {
	lines = strings.Split(strings.ReplaceAll(strategy, "\r\n", "\n"), "\n")
	seen, blank := false, false // a line of the header, a blank line after it
	for ; body < len(lines); body++ {
		line := strings.TrimSpace(lines[body])
		if line == "" {
			blank = seen
			continue
		}
		if !strings.HasPrefix(line, "#") || blank && !headerBlock(lines[body:]) {
			break
		}
		seen, blank = true, false
	}
	return lines, body
}

// headerBlock reports whether the lines starting with # at the top of lines,
// after a blank line of the header, up to a blank line or a line of other
// text, are of the header still: one of them of a key the agent reads, or
// reading like a daily limit in money (see keepsHeader).
func headerBlock(lines []string) bool {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") {
			return false
		}
		if keepsHeader(line) {
			return true
		}
	}
	return false
}

// headerKeys are the keys of the header lines the agent reads.
var headerKeys = []string{"indicators", "take profit", pendingOrdersKey, drawingsKey, contextKey, dailyGainKey,
	dailyLossKey}

// The amounts in a currency of a line, see moneyIn: an amount, eg: 150 or
// 1.000,50, and a code of 3 capital letters after it, first in the value of
// the line, or last in it.
var (
	moneyFirstRegexp = regexp.MustCompile(`^\s*[-+]?\d[\d.,]*\s*([A-Z]{3})\b`)
	moneyLastRegexp  = regexp.MustCompile(`\d[\d.,]*\s*([A-Z]{3})[\s.,;:!?)]*$`)
)

// isoCurrencies are the codes of the currencies in use of ISO 4217, but the
// ones of funds, metals and tests, and USC, the US cent of the cent accounts
// of some brokers, eg: Exness.
var isoCurrencies = func() map[string]bool {
	codes := map[string]bool{"USC": true}
	for _, code := range strings.Fields(`AED AFN ALL AMD AOA ARS AUD AWG AZN BAM BBD BDT BGN BHD BIF BMD BND BOB
		BRL BSD BTN BWP BYN BZD CAD CDF CHF CLP CNY COP CRC CUP CVE CZK DJF DKK DOP DZD EGP ERN ETB EUR FJD FKP GBP
		GEL GHS GIP GMD GNF GTQ GYD HKD HNL HTG HUF IDR ILS INR IQD IRR ISK JMD JOD JPY KES KGS KHR KMF KPW KRW KWD
		KYD KZT LAK LBP LKR LRD LSL LYD MAD MDL MGA MKD MMK MNT MOP MRU MUR MVR MWK MXN MYR MZN NAD NGN NIO NOK NPR
		NZD OMR PAB PEN PGK PHP PKR PLN PYG QAR RON RSD RUB RWF SAR SBD SCR SDG SEK SGD SHP SLE SOS SRD SSP STN SVC
		SYP SZL THB TJS TMT TND TOP TRY TTD TWD TZS UAH UGX USD UYU UZS VED VES VND VUV WST XAF XCD XCG XOF XPF YER
		ZAR ZMW ZWG`) {
		codes[code] = true
	}
	return codes
}()

// moneyIn reports whether re (see moneyFirstRegexp) matches an amount in a
// currency in text: its code one of isoCurrencies, not any 3 capital letters,
// eg: "2 ATR".
func moneyIn(re *regexp.Regexp, text string) bool {
	match := re.FindStringSubmatch(text)
	return match != nil && isoCurrencies[match[1]]
}

// keepsHeader reports whether line, starting with #, keeps the lines after a
// blank line of the header in it (see headerBlock): of a key the agent reads,
// or reading like a daily limit in money (see dailyMoneyLine), refused by
// checkDailyLines rather than left off.
func keepsHeader(line string) bool {
	key, _, colon := headerField(line)
	return colon && slices.Contains(headerKeys, key) || dailyMoneyLine(line)
}

// dailyMoneyLine reports whether line, starting with #, reads like a daily
// limit in money: a key naming a day and a limit (see dailyLike), and an
// amount in a currency (see moneyIn) first in its value, eg: "# Daily loss
// limits: 150 BRL" or "# Limite diário: 150 BRL por dia", or ending it, eg:
// "# Daily loss limit 150 BRL"; not an amount in something else, eg: "##
// Limite diário de perda: 2 ATR", nor a currency elsewhere, eg: "## Daily
// limit: 2 trades, max 1 EUR pair".
func dailyMoneyLine(line string) bool {
	key, value, colon := headerField(line)
	if !dailyLike(key) {
		return false
	}
	return colon && moneyIn(moneyFirstRegexp, value) || moneyIn(moneyLastRegexp, strings.TrimSpace(line))
}

// headerField returns the key of a line starting with #, the text before its
// colon in lower case, its words single-spaced, eg: "take profit" for
// "# Take  profit : none", and its value, the text after the colon; the whole
// text is the key of a line without a colon, colon false.
func headerField(line string) (key, value string, colon bool) {
	text := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
	key, value, colon = strings.Cut(text, ":")
	return strings.ToLower(strings.Join(strings.Fields(key), " ")), value, colon
}

// headerValue returns the value of the first line of the header of strategy
// of key, eg: "indicators", and whether there is one.
func headerValue(strategy, key string) (string, bool) {
	lines, body := promptLines(strategy)
	for _, line := range lines[:body] {
		if k, value, colon := headerField(line); colon && k == key {
			return value, true
		}
	}
	return "", false
}

// NormalizeIndicator returns an indicator of Config.Indicators with its kind in
// lower case and its parameters, eg: "ema 20 50" for "EMA+20+50", the time
// zones kept as they are.
func NormalizeIndicator(spec string) string {
	fields := strings.FieldsFunc(strings.TrimSpace(spec), func(r rune) bool { return r == ' ' || r == '+' || r == '\t' })
	if len(fields) == 0 {
		return ""
	}
	fields[0] = strings.ToLower(fields[0])
	return strings.Join(fields, " ")
}

// stripComments returns strategy without its header (see promptLines), and
// without the blank lines around its text.
func stripComments(strategy string) string {
	lines, body := promptLines(strategy)
	return strings.TrimSpace(strings.Join(lines[body:], "\n"))
}

// decisionName is the name of the schema of a Decision, the name of the tool
// on Anthropic.
const decisionName = "trading_decision"

// decisionDescription describes the schema of a Decision.
const decisionDescription = "The trading decision for the symbol of the market context."

// decisionSchema is the JSON schema of a Decision, strict: every property
// required, none other. The reason comes first, so that the model writes the
// evidence before the action. The ranges are checked by ParseDecision, as the
// strict schemas of OpenAI don't take them.
var decisionSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"reason": {
			"type": "string",
			"description": "Evidence of the context behind the decision, in one or two short sentences, in rules.reason_language."
		},
		"action": {
			"type": "string",
			"enum": ["WAIT", "ENTER_LONG", "ENTER_SHORT", "HOLD", "EXIT", "MODIFY_SL", "MODIFY_TP"],
			"description": "One of rules.allowed_actions."
		},
		"confidence": {
			"type": "number",
			"description": "Confidence of the analysis, from 0 to 1."
		},
		"entry": {
			"type": ["number", "null"],
			"description": "Entry price of ENTER_LONG and ENTER_SHORT, the current price. Null for the other actions."
		},
		"stop_loss": {
			"type": ["number", "null"],
			"description": "Stop loss of an entry, of MODIFY_SL, or the current one on HOLD. Null otherwise."
		},
		"take_profit": {
			"type": ["number", "null"],
			"description": "Take profit of an entry (null for none only when rules.take_profit_optional is true, always null when rules.no_take_profit is true), of MODIFY_TP, or the current one on HOLD. Null otherwise."
		},
		"levels": {
			"type": "array",
			"description": "The supports and resistances the analysis relies on, the nearest to the price first, 8 at most; empty when none. Drawn on the chart, they don't trade.",
			"items": {
				"type": "object",
				"properties": {
					"kind": {"type": "string", "enum": ["support", "resistance"]},
					"price": {"type": "number"}
				},
				"required": ["kind", "price"],
				"additionalProperties": false
			}
		}
	},
	"required": ["reason", "action", "confidence", "entry", "stop_loss", "take_profit", "levels"],
	"additionalProperties": false
}`)

// The parts of the schema of a decision with the pending orders and the
// drawings, see schemaOf: the descriptions of the prices, and the properties of
// the levels and of the drawings, which take their place.
const (
	pendingEntryDescription = "Entry price of ENTER_LONG and ENTER_SHORT, the current price; the price of the " +
		"pending order of BUY_LIMIT, SELL_LIMIT, BUY_STOP and SELL_STOP, or its new one on MODIFY_PENDING. Null " +
		"for the other actions."
	pendingStopDescription = "Stop loss of an entry, at the market or pending, of MODIFY_PENDING, of " +
		"MODIFY_SL, or the current one on HOLD. Null otherwise."
	pendingProfitDescription = "Take profit of an entry, at the market or pending (null for none only when " +
		"rules.take_profit_optional is true, always null when rules.no_take_profit is true), of MODIFY_PENDING " +
		"(null only when the pending order has none), of MODIFY_TP, or the current one on HOLD. Null otherwise."
	levelsProperty = `"levels": {
			"type": "array",
			"description": "The supports and resistances the analysis relies on, the nearest to the price first, 8 at most; empty when none. Drawn on the chart, they don't trade.",
			"items": {
				"type": "object",
				"properties": {
					"kind": {"type": "string", "enum": ["support", "resistance"]},
					"price": {"type": "number"}
				},
				"required": ["kind", "price"],
				"additionalProperties": false
			}
		}`
	drawingsProperty = `"drawings": {
			"type": "array",
			"description": "The structures the analysis relies on, drawn on the chart, 20 at most; empty when none. They don't trade.",
			"items": {
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Short name, the same for the same structure from an analysis to the next."},
					"kind": {"type": "string", "enum": ["zone", "supply", "demand", "level", "marker"]},
					"label": {"type": "string", "description": "Its name on the chart, a few characters, eg: FVG, OB, BOS."},
					"bias": {"type": "string", "enum": ["bullish", "bearish", "neutral"]},
					"timeframe": {"type": "string", "description": "The timeframe it was read on."},
					"from": {"type": "string", "description": "Open time of the candle it starts at, as the context writes the times."},
					"to": {"type": ["string", "null"], "description": "Open time of the candle it ended at, null while it holds."},
					"price": {"type": ["number", "null"], "description": "Price of a level or of a marker, null for a zone."},
					"low": {"type": ["number", "null"], "description": "Low of a zone, null otherwise."},
					"high": {"type": ["number", "null"], "description": "High of a zone, null otherwise."},
					"state": {"type": ["string", "null"], "description": "State of a supply or demand zone: fresh, tested or broken; null for the other kinds."}
				},
				"required": ["id", "kind", "label", "bias", "timeframe", "from", "to", "price", "low", "high", "state"],
				"additionalProperties": false
			}
		}`
)

// schemas are the schemas of the decisions of the shapes with the pending
// orders or the drawings, see schemaOf.
var schemas = map[decisionShape]json.RawMessage{
	{pending: true}:                 extendedSchema(decisionShape{pending: true}),
	{drawings: true}:                extendedSchema(decisionShape{drawings: true}),
	{pending: true, drawings: true}: extendedSchema(decisionShape{pending: true, drawings: true}),
}

// schemaOf returns the JSON schema of a decision of shape: decisionSchema,
// with the actions of the pending orders and the drawings of the shape.
func schemaOf(shape decisionShape) json.RawMessage {
	if schema, ok := schemas[shape]; ok {
		return schema
	}
	return decisionSchema
}

// extendedSchema returns the schema of a decision of shape: the one of
// decisionSchema, the reason first, with the actions of the pending orders and
// their prices when shape.pending, and the drawings last when shape.drawings.
func extendedSchema(shape decisionShape) json.RawMessage {
	quote := func(text string) string {
		data, _ := json.Marshal(text)
		return string(data)
	}
	actions := make([]string, 0, len(shape.actions()))
	for _, action := range shape.actions() {
		actions = append(actions, quote(string(action)))
	}
	entry := "Entry price of ENTER_LONG and ENTER_SHORT, the current price. Null for the other actions."
	stop := "Stop loss of an entry, of MODIFY_SL, or the current one on HOLD. Null otherwise."
	profit := "Take profit of an entry (null for none only when rules.take_profit_optional is true, always null " +
		"when rules.no_take_profit is true), of MODIFY_TP, or the current one on HOLD. Null otherwise."
	if shape.pending {
		entry, stop, profit = pendingEntryDescription, pendingStopDescription, pendingProfitDescription
	}
	// the supports and resistances drawn, but for the shape with its own
	// drawings, the only ones drawn then
	extra, required := ",\n\t\t"+levelsProperty, `, "levels"`
	if shape.drawings {
		extra, required = ",\n\t\t"+drawingsProperty, `, "drawings"`
	}
	return json.RawMessage(fmt.Sprintf(`{
	"type": "object",
	"properties": {
		"reason": {
			"type": "string",
			"description": "Evidence of the context behind the decision, in one or two short sentences, in rules.reason_language."
		},
		"action": {
			"type": "string",
			"enum": [%s],
			"description": "One of rules.allowed_actions."
		},
		"confidence": {
			"type": "number",
			"description": "Confidence of the analysis, from 0 to 1."
		},
		"entry": {
			"type": ["number", "null"],
			"description": %s
		},
		"stop_loss": {
			"type": ["number", "null"],
			"description": %s
		},
		"take_profit": {
			"type": ["number", "null"],
			"description": %s
		}%s
	},
	"required": ["reason", "action", "confidence", "entry", "stop_loss", "take_profit"%s],
	"additionalProperties": false
}`, strings.Join(actions, ", "), quote(entry), quote(stop), quote(profit), extra, required))
}

// userMessage returns the user message of the context: its JSON.
func userMessage(in Input) (string, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("llm: context of %s: %w", in.Symbol, err)
	}
	return "Market context of the candle just closed:\n" + string(data), nil
}
