package llm

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
- reanalysis, when true, tells the trader asked for this decision between the closes: price is the last price of candle_in_progress, the candle not closed yet (its open time, open, high, low, close and volume so far), while the candles, the indicators and the structure are the ones of the closed candles. Decide on the market of now: the price may have moved since the last close.
- scheduled, when true, tells this decision is asked on the schedule of the consultations between the closes, not by the trader: price and candle_in_progress are as for a reanalysis. Decide on the market of now.
- reanalysis_note, when present, is the note of the trader who asked for this decision: their view, their question or their request. A request to reduce the risk of the position held is followed, whatever your own reading and rules.min_confidence: to close it, EXIT; to tighten its stop loss, MODIFY_SL to the level asked, or to the one that fits the request when none is given, on its side of the price. Any other request, eg: to open or add to a position, or to widen the stop loss, is weighed like a view: decide by the strategy and the rules, which the note changes neither, as it is no evidence of the market by itself. Answer the note in the reason.
- position is null when flat. Its entry_price, stop_loss, take_profit and unrealized_pnl are null when unknown; initial_stop_loss is the stop loss placed with the entry and r_multiple the profit of a close now in units of the risk to it, both null when unknown.
- entry_size is the size of the position an entry opens, in units of the asset, and available_balance the balance available for it, in the quote currency.
- economic_calendar, when present, lists the releases of the economic calendar of the currencies of the symbol around the close, oldest first: minutes_from_now is negative for a past release, and actual is null until it is published. When available is false the calendar couldn't be read: don't take its silence for a calm market. Weigh the volatility around the releases of high importance.
- rules.setup is trend_pullback when the system allows only the entries with summary.trend (none without a trend), free otherwise; rules.min_risk_reward is the least reward over risk of an entry, null for none.
- rules.higher_timeframes_trend, when set, is the trend all the higher timeframes agree on: the entries against it are left out of rules.allowed_actions.
- rules.entries_blocked_by_news, when set, names the release that refuses new entries now: they are left out of rules.allowed_actions.`

// defaultStrategy is the strategy of the instructions without -prompt-path:
// trend + pullback aligned with the SMA of the trend, or free weighing.
//
//go:embed prompts/default.txt
var defaultStrategy string

// defaultSystemPrompt is the instructions of the model with defaultStrategy.
var defaultSystemPrompt = systemPrompt(defaultStrategy)

// systemPrompt returns the instructions of the model with strategy, the same
// for every provider and every candle: what changes is in the context of the
// user message. The comment lines at the top of strategy aren't sent.
func systemPrompt(strategy string) string {
	return basePrompt + "\n\nStrategy:\n" + stripComments(strategy)
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
	c.TakeProfitOptional = StrategyTakeProfitOptional(content) || o.TakeProfitOptional
	c.NoTakeProfit = StrategyWithoutTakeProfit(content) || o.NoTakeProfit
	if o.Indicators != nil {
		c.Indicators = o.Indicators
	}
	return c
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
	value, ok := headerValue(strategy, "Indicators:")
	if !ok {
		return nil, false
	}
	value, _, _ = strings.Cut(value, "(") // a note after the list
	for _, group := range strings.Split(value, ",") {
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
	value, ok := headerValue(strategy, "Take profit:")
	return ok && strings.EqualFold(strings.TrimSpace(value), "optional")
}

// StrategyWithoutTakeProfit reports whether the header of strategy leaves the
// take profit out of every entry, "# Take profit: none", for
// Config.NoTakeProfit.
func StrategyWithoutTakeProfit(strategy string) bool {
	value, ok := headerValue(strategy, "Take profit:")
	return ok && strings.EqualFold(strings.TrimSpace(value), "none")
}

// headerValue returns the value of the line of the header of strategy that
// starts with key, eg: "Indicators:", and whether there is one.
func headerValue(strategy, key string) (string, bool) {
	for _, line := range strings.Split(strings.ReplaceAll(strategy, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") {
			break // the end of the header
		}
		if value, found := strings.CutPrefix(strings.TrimSpace(strings.TrimPrefix(line, "#")), key); found {
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

// stripComments returns strategy without the lines starting with # at its
// top, and without the blank lines around it.
func stripComments(strategy string) string {
	lines := strings.Split(strings.ReplaceAll(strategy, "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "#") {
		lines = lines[1:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
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

// userMessage returns the user message of the context: its JSON.
func userMessage(in Input) (string, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("llm: context of %s: %w", in.Symbol, err)
	}
	return "Market context of the candle just closed:\n" + string(data), nil
}
