package llm

import (
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/raykavin/xtrader/model"
)

// Input is the market context of a decision, as sent to the provider, its JSON
// the user message of the request: the pair and its prices, its last candles
// and indicators, the position held, and the rules of the decision, which
// Decision.Validate enforces. A number unknown is NaN, null in JSON.
type Input struct {
	Symbol    string    `json:"symbol"`
	Timeframe string    `json:"timeframe"`
	Time      time.Time `json:"time"`  // open time of the candle just closed, the last one
	Price     number    `json:"price"` // its close, the current price

	// Bid, Ask and Spread at the close, when the exchange sends them in the
	// Metadata of the candles, eg: Exness, whose candles are bid prices.
	Bid    number `json:"bid"`
	Ask    number `json:"ask"`
	Spread number `json:"spread"`

	Candles    Candles             `json:"candles"`
	Indicators map[string][]number `json:"indicators"` // the last values of each one, oldest first
	Summary    Summary             `json:"summary"`
	Market     map[string]number   `json:"market_data,omitempty"` // the other Metadata of the last candle

	// Calendar is the economic calendar around now, nil when the
	// Trader has none, see Config.Calendar.
	Calendar *CalendarContext `json:"economic_calendar,omitempty"`

	// The context computed by the code: the price structure of the last
	// Config.StructureCandles candles, the higher timeframes and the levels
	// of the previous day and week (with a feeder, see SetFeeder), the
	// session, the instrument, the correlated markets and the memory of the
	// last decisions and trades. Each absent when unknown or disabled.
	Structure        *Structure   `json:"structure,omitempty"`
	HigherTimeframes []Frame      `json:"higher_timeframes,omitempty"`
	Reference        *Reference   `json:"reference_levels,omitempty"`
	Session          *Session     `json:"session,omitempty"`
	Instrument       *Instrument  `json:"instrument,omitempty"`
	Correlated       []Correlated `json:"correlated_markets,omitempty"`
	Memory           *Memory      `json:"memory,omitempty"`

	// Daily is the operational day of the symbol so far: its positions, its
	// orders and its result, with the daily limits of the prompt, see
	// DailyHistory. Absent without the orders of the bot.
	Daily *DailyHistory `json:"daily_history,omitempty"`

	// Reanalysis tells that the decision is asked on demand between the
	// closes, see Trader.Reanalyze: Price is the last price of
	// CandleInProgress, the candle not closed yet, and the other data are the
	// ones of the closed candles.
	Reanalysis       bool              `json:"reanalysis,omitempty"`
	CandleInProgress *CandleInProgress `json:"candle_in_progress,omitempty"`

	// Scheduled tells that the decision is asked on the consult interval
	// between the closes, see Config.ConsultInterval, CandleInProgress as for
	// a reanalysis, which the trader didn't ask.
	Scheduled bool `json:"scheduled,omitempty"`

	// ReanalysisNote is the note the trader sent with the reanalysis the
	// decision answers, if any: their view, their question or their request;
	// the requests reducing the risk of the position held are followed, the
	// others weighed, see Config.MinConfidence.
	ReanalysisNote string `json:"reanalysis_note,omitempty"`

	Position  *Position `json:"position"`          // nil when flat
	EntrySize number    `json:"entry_size"`        // of the position an entry opens, in units of the asset
	Balance   number    `json:"available_balance"` // quote available for new positions

	// PendingOrder is the pending entry waiting on the pair for its fill,
	// while flat, see Config.PendingOrders: nil without one.
	PendingOrder *PendingOrder `json:"pending_order,omitempty"`

	// SMC is the structure of the candles in smart money concepts, for a
	// prompt that asks for it, see Config.SMC: nil otherwise.
	SMC *SMC `json:"smc,omitempty"`

	// Drawings are the drawings on the chart of the last analysis of the
	// pair, for a prompt that draws (see Config.Drawings): the model keeps the
	// ones still valid. Absent without any.
	Drawings []Drawing `json:"chart_drawings,omitempty"`

	Rules Rules `json:"rules"`

	// drawings tells the decision of the context holds drawings, see
	// Config.Drawings: the schema of the decision asked, not sent.
	drawings bool
}

// PendingOrder is the pending entry waiting on the pair for its fill: Type the
// action that places it, eg: BUY_LIMIT, its entry, its stop loss and take
// profit at the broker (null without one), its size in units of the asset, the
// candles closed since it was placed, and who placed it: model, the model
// itself, or trader, someone else, eg: from the dashboard.
type PendingOrder struct {
	Type       Action `json:"type"`
	Entry      number `json:"entry_price"`
	StopLoss   number `json:"stop_loss"`
	TakeProfit number `json:"take_profit"`
	Size       number `json:"size"`
	Candles    int    `json:"candles_pending"`
	PlacedBy   string `json:"placed_by"`
}

// CandleInProgress is the candle not closed yet of a reanalysis: its open time,
// and its prices and volume so far.
type CandleInProgress struct {
	Time   time.Time `json:"time"`
	Open   number    `json:"open"`
	High   number    `json:"high"`
	Low    number    `json:"low"`
	Close  number    `json:"close"`
	Volume number    `json:"volume"`
}

// candleColumns are the values of a row of Candles.
var candleColumns = []string{"time", "open", "high", "low", "close", "volume"}

// Candles are the last candles of the context, oldest first, written as rows
// of candleColumns to save tokens.
type Candles []model.Candle

func (c Candles) MarshalJSON() ([]byte, error) {
	rows := make([][]any, len(c))
	for i, candle := range c {
		rows[i] = []any{candle.Time.UTC().Format(time.RFC3339), number(candle.Open), number(candle.High),
			number(candle.Low), number(candle.Close), number(candle.Volume)}
	}
	return json.Marshal(struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}{candleColumns, rows})
}

// Summary sums the candles of the context up.
type Summary struct {
	// Trend is up with the fast EMA above the slow one and the close above
	// the SMA of the trend, down with both below, else mixed. Left out for a
	// strategy that doesn't use the averages (Config.Indicators).
	Trend       string `json:"trend,omitempty"`
	ATRPct      number `json:"atr_pct"`      // ATR in % of the price
	VolumeRatio number `json:"volume_ratio"` // volume of the last candle over its average
	High        number `json:"high"`         // highest high of the candles
	Low         number `json:"low"`          // lowest low of the candles
	ChangePct   number `json:"change_pct"`   // change of the close over the candles, in %
}

// Instrument tells what the numbers of the pair mean: its volume (tick volume,
// the count of the changes of the price, or the volume traded), and the profit
// or the loss of the position (or of entry_size when flat) for a move of 1 of
// the price, in the quote currency.
type Instrument struct {
	Volume            string `json:"volume"`
	ValuePerPriceUnit number `json:"value_per_price_unit"`
	QuoteCurrency     string `json:"quote_currency"`
}

// Position is the position held on the pair. Entry is unknown for a position
// opened before a restart, or by someone else, as its levels without their
// legs, and PnL without the entry.
type Position struct {
	Side       model.SideType `json:"-"`
	Size       number         `json:"size"` // in units of the asset
	Entry      number         `json:"entry_price"`
	StopLoss   number         `json:"stop_loss"`
	TakeProfit number         `json:"take_profit"`
	PnL        number         `json:"unrealized_pnl"` // at the price, in quote currency, before the costs
	Candles    int            `json:"candles_held"`   // closed since the entry, or since it is followed

	// InitialStop is the stop loss placed with the entry, whose distance to
	// it is the risk of the trade, and RMultiple the profit of a close at the
	// market now, net of the spread, in units of that risk: 1 is a profit of
	// the initial risk, -1 the loss at the initial stop. Both are null for a
	// position the Trader didn't open.
	InitialStop number `json:"initial_stop_loss"`
	RMultiple   number `json:"r_multiple"`
}

// MarshalJSON writes the side of the position as LONG or SHORT, as the actions
// name them.
func (p Position) MarshalJSON() ([]byte, error) {
	type position Position // without this method
	return json.Marshal(struct {
		Side string `json:"side"`
		position
	}{describeSide(p.Side), position(p)})
}

// Rules are the rules of the decision, sent to the model so that it follows
// them, and enforced by Decision.Validate.
type Rules struct {
	Actions          []Action `json:"allowed_actions"`
	MinConfidence    number   `json:"min_confidence"`     // the actions trading under it are ignored
	MaxEntryDistance number   `json:"max_entry_distance"` // of the entry from the price, null: no limit
	MaxLevelDistance number   `json:"max_level_distance"` // of the stop loss and take profit from the price
	StopCanWiden     bool     `json:"stop_loss_can_widen"`
	ReasonLanguage   string   `json:"reason_language"`

	// Setup is the setup to trade, see Config.Setup, and MinRiskReward the
	// least reward over risk of an entry, net of the spread, null for none.
	Setup         string `json:"setup"`
	MinRiskReward number `json:"min_risk_reward"`

	// MinStopDistance is the least room between the stop loss of an entry and
	// the price that hits it (the bid for a long, the ask for a short), null
	// for none, see Config.MinStopATR.
	MinStopDistance number `json:"min_stop_distance"`

	// TakeProfitOptional lets an entry go without a take profit, see
	// Config.TakeProfitOptional, and NoTakeProfit leaves it out: every entry
	// goes without one, see Config.NoTakeProfit.
	TakeProfitOptional bool `json:"take_profit_optional,omitempty"`
	NoTakeProfit       bool `json:"no_take_profit,omitempty"`

	// HigherTrend, when set, is the trend the higher timeframes agree on: the
	// entries against it are left out of Actions, see
	// Config.HigherTrendFilter.
	HigherTrend string `json:"higher_timeframes_trend,omitempty"`

	// EntriesBlockedBy names the release of the calendar that refuses new
	// entries now, left out of Actions, see Config.NewsBlockBefore.
	EntriesBlockedBy string `json:"entries_blocked_by_news,omitempty"`

	// EntriesBlockedByDailyLimit tells why the new entries on the symbol are
	// refused until the next operational day, see Config.DailyGain: a daily
	// limit reached, or a result unknown yet; a position held is managed as
	// usual.
	EntriesBlockedByDailyLimit string `json:"entries_blocked_by_daily_limit,omitempty"`

	// PendingOrders are the types of the pending entries the model may place,
	// limit and stop, see Config.PendingOrders, and PendingExpiry the candles
	// after which one not filled is canceled, 0 for never, see
	// Config.PendingExpiry.
	PendingOrders []string `json:"pending_orders,omitempty"`
	PendingExpiry int      `json:"pending_expiry_candles,omitempty"`
}

// number is a number of the context, written with 8 significant digits, and
// null when unknown: NaN, eg: the entry of a position opened by someone else.
type number float64

func (n number) MarshalJSON() ([]byte, error) {
	if !known(float64(n)) {
		return []byte("null"), nil
	}
	return strconv.AppendFloat(nil, float64(n), 'g', 8, 64), nil
}

// ratio is a number the system derives for the context (a distance in ATR, a
// percentage, a correlation), written with 4 significant digits: more would
// be noise, and tokens. Null when unknown.
type ratio float64

func (r ratio) MarshalJSON() ([]byte, error) {
	if !known(float64(r)) {
		return []byte("null"), nil
	}
	return strconv.AppendFloat(nil, float64(r), 'g', 4, 64), nil
}

// known reports whether v is a number, neither NaN nor infinite.
func known(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// lastNumbers returns the last n values of series, fewer when it is shorter.
func lastNumbers(series []float64, n int) []number {
	series = model.Series[float64](series).LastValues(n)
	values := make([]number, len(series))
	for i, v := range series {
		values[i] = number(v)
	}
	return values
}
