package agentapi

// The backtests of the agents, as the controller asks for them and as the
// agents report them, see agents/internal/backtest.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/raykavin/xtrader/ui"
)

// BacktestConfig is the backtest a run of an agent makes, read from the file of
// -backtest-config: its period (the candles before From warm the indicators
// up, no entry then), its money, its costs, its symbols, the protection and
// the daily limits of the agent, and where its report and progress go.
type BacktestConfig struct {
	// From and To bound the trading period, Unix seconds: the entries are
	// refused before From, the run ends with the last candle before To.
	From int64 `json:"from"`
	To   int64 `json:"to"`

	// Balance is the starting balance, in the quote currency of the pairs
	// (a simulation: the money of the account isn't converted).
	Balance float64 `json:"balance"`
	// Leverage, 1 for none, is the leverage of the margin: a position commits
	// its notional over it, see wallet.
	Leverage float64 `json:"leverage"`
	// Fee is charged on the notional of every fill, a share of it.
	Fee float64 `json:"fee"`
	// SlippagePoints worsens the fills at the market and of the stops, in
	// points of each symbol; SpreadPoints is the spread of the candles without
	// one of their own (their spread column), in points too.
	SlippagePoints float64 `json:"slippage_points"`
	SpreadPoints   float64 `json:"spread_points"`
	// Conservative assumes the worst of what a candle can't tell, see
	// exchange.PaperExecution; GapFills fills the stops at the open of a
	// candle opening beyond them.
	Conservative bool `json:"conservative"`
	GapFills     bool `json:"gap_fills"`

	// Symbols are the specs of the symbols, by pair, see BacktestSymbol.
	Symbols map[string]BacktestSymbol `json:"symbols"`

	// Protection is the breakeven and trailing stop of the agent, nil for
	// none; DailyLoss and DailyGain its daily limits on the result of the day
	// (realized plus open, from midnight of America/Belem), in the quote
	// currency, 0 for none.
	Protection *ui.ProtectionSettings `json:"protection,omitempty"`
	DailyLoss  float64                `json:"daily_loss,omitempty"`
	DailyGain  float64                `json:"daily_gain,omitempty"`
	// Hours are the trading hours of the run, nil for none, see
	// ui.BacktestHours and TradingWindow.
	Hours *ui.BacktestHours `json:"hours,omitempty"`

	// LLMMaxCalls and LLMMaxCost bound the calls of the language model of an
	// LLM agent and their cost, in dollars, estimated with LLMPriceInput and
	// LLMPriceOutput, dollars per million tokens (0: the cost unknown, the
	// calls bound it): reached, the run stops, budget_exhausted.
	LLMMaxCalls    int     `json:"llm_max_calls,omitempty"`
	LLMMaxCost     float64 `json:"llm_max_cost,omitempty"`
	LLMPriceInput  float64 `json:"llm_price_input,omitempty"`
	LLMPriceOutput float64 `json:"llm_price_output,omitempty"`

	// Agent is the name of the agent backtested, for what the run records,
	// eg: the samples of its -meta-record; "" for none. Run is the id of the
	// run at the controller (0 for none) and Segment its segment of the
	// period (fit or validation, "" for none), written in those samples too,
	// so that a training tells the runs and their segments apart.
	Agent   string `json:"agent,omitempty"`
	Run     int64  `json:"run,omitempty"`
	Segment string `json:"segment,omitempty"`
	// References are the candles of the symbols the strategy reads but never
	// trades (see Warmup.References), by symbol: a CSV of the same timeframe
	// and period as the pairs, loaded in the feed of the run, never
	// subscribed nor traded.
	References map[string]string `json:"references,omitempty"`
	// Releases are the scheduled releases of the economic calendar of the
	// period, by pair (the ones of its currencies), for a strategy trading
	// around them (see Warmup.Releases): their times and importance, never a
	// value released; nil for none (the news off or unreadable).
	Releases map[string][]BacktestRelease `json:"releases,omitempty"`

	// Report is the file the report is written to; Progress prints the
	// progress on the standard error (PROGRESS done total).
	Report   string `json:"report"`
	Progress bool   `json:"progress"`
}

// BacktestRelease is a scheduled release of the economic calendar a backtest
// hands its strategy: its time (Unix seconds, UTC), the currency of the event,
// its code (the MQL5 event_code) and name, its importance (0 none, 1 low, 2
// medium, 3 high), and whether its time is exact (Timed: an all-day release
// has none). Tentative tells a release whose time the calendar knows only once
// it is released (tentative, or published without a time): the run tells it
// untimed, at the start of its day, until then, as the calendar did live.
type BacktestRelease struct {
	Time       int64  `json:"time"`
	Currency   string `json:"currency"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Importance int    `json:"importance"`
	Timed      bool   `json:"timed"`
	Tentative  bool   `json:"tentative,omitempty"`
}

// BacktestSymbol is what a backtest knows of a symbol: the price of a point (the unit
// of the spread and of the slippage), the volume step and the least volume of
// its orders, in units of the asset (a lot times the contract size), 0 for
// none.
type BacktestSymbol struct {
	Point       float64 `json:"point"`
	VolumeStep  float64 `json:"volume_step"`
	VolumeMin   float64 `json:"volume_min"`
	SpreadPoint float64 `json:"spread_points,omitempty"` // the fallback spread of the symbol, else BacktestConfig.SpreadPoints
}

// PrintWarmupFlag is the flag of the agents printing the warmup of their
// strategy, see Warmup, and exiting: what the controller reads before the
// period of a backtest.
const PrintWarmupFlag = "print-warmup"

// Warmup is the warmup of the strategy of an agent, as -print-warmup prints
// it: the candles of Timeframe its indicators need before it trades (the
// engine hands it a candle only once it holds that many); the symbols it reads
// but never trades (References, eg: the one of -intermarket-symbol), whose
// candles a backtest reads too; and whether it trades around the releases of
// the economic calendar (Releases), which a backtest reads then.
type Warmup struct {
	Timeframe  string   `json:"timeframe"`
	Candles    int      `json:"candles"`
	References []string `json:"references,omitempty"`
	Releases   bool     `json:"releases,omitempty"`
}

// ReadBacktestConfig reads the config of a backtest from path, validated.
func ReadBacktestConfig(path string) (BacktestConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return BacktestConfig{}, fmt.Errorf("backtest: the config: %w", err)
	}
	var c BacktestConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return BacktestConfig{}, fmt.Errorf("backtest: the config %s: %w", path, err)
	}
	return c, c.Validate()
}

// Validate returns why c can't run.
func (c *BacktestConfig) Validate() error {
	positive := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }
	if c.Leverage == 0 {
		c.Leverage = 1
	}
	switch {
	case c.From <= 0 || c.To <= c.From:
		return errors.New("backtest: the period: from before to, Unix seconds")
	case !(c.Balance > 0) || math.IsInf(c.Balance, 0):
		return errors.New("backtest: the balance must be above 0")
	case c.Leverage < 1 || math.IsInf(c.Leverage, 0) || math.IsNaN(c.Leverage):
		return errors.New("backtest: the leverage must be 1 or more")
	case !positive(c.Fee) || c.Fee >= 0.1:
		return errors.New("backtest: the fee: a share of the notional, from 0 to 0.1")
	case !positive(c.SlippagePoints) || !positive(c.SpreadPoints):
		return errors.New("backtest: the slippage and the spread must be 0 or more")
	case !positive(c.DailyLoss) || !positive(c.DailyGain):
		return errors.New("backtest: the daily limits must be 0 or more")
	case c.LLMMaxCalls < 0 || !positive(c.LLMMaxCost) || !positive(c.LLMPriceInput) || !positive(c.LLMPriceOutput):
		return errors.New("backtest: the ceilings and the prices of the language model must be 0 or more")
	case c.Report == "":
		return errors.New("backtest: the file of the report")
	case c.Hours != nil && len(HoursWindows(c.Hours.Windows, c.Hours.Start, c.Hours.End, false, false)) == 0:
		return errors.New("backtest: the trading hours: a window at least")
	case c.Hours != nil && len(c.Hours.Windows) > 0 && WindowsProblem(c.Hours.Windows) != "":
		return fmt.Errorf("backtest: the trading hours: %s", WindowsProblem(c.Hours.Windows))
	case c.Hours != nil && len(c.Hours.Windows) == 0 && (!ValidClock(c.Hours.Start) || !ValidClock(c.Hours.End) ||
		c.Hours.Start == c.Hours.End):
		return errors.New("backtest: the trading hours: a start and an end HH:MM, apart")
	}
	for pair, s := range c.Symbols {
		if !positive(s.Point) || !positive(s.VolumeStep) || !positive(s.VolumeMin) || !positive(s.SpreadPoint) {
			return fmt.Errorf("backtest: the symbol %s: its point, volume step and least volume must be 0 or more",
				pair)
		}
	}
	for symbol, file := range c.References {
		if symbol == "" || file == "" {
			return errors.New("backtest: a reference: its symbol and the file of its candles")
		}
	}
	for pair, releases := range c.Releases {
		for _, r := range releases {
			if pair == "" || r.Time <= 0 || r.Importance < 0 || r.Importance > 3 {
				return fmt.Errorf("backtest: a release of %q: its time and an importance from 0 to 3", pair)
			}
		}
	}
	return nil
}

// The status of a report: the run ended with its last candle, was canceled
// (its report partial), failed, or stopped at its budget (an LLM).
const (
	BacktestStatusSucceeded       = "succeeded"
	BacktestStatusCanceled        = "canceled"
	BacktestStatusFailed          = "failed"
	BacktestStatusBudgetExhausted = "budget_exhausted"
)

// The reasons of the entries and the exits of the trades, see Trade.
const (
	BacktestReasonMarket     = "market"        // an order at the market of the strategy
	BacktestReasonLimit      = "limit"         // a limit order
	BacktestReasonStopEntry  = "stop_entry"    // a stop entry order
	BacktestReasonStopLoss   = "stop_loss"     // the stop loss, maybe moved by the breakeven or the trailing stop
	BacktestReasonTakeProfit = "take_profit"   // the take profit
	BacktestReasonDailyLimit = "daily_limit"   // closed by the daily limit, see the backtest of the agents
	BacktestReasonStopOut    = "stop_out"      // closed as the equity reached 0
	BacktestReasonHours      = "trading_hours" // closed at the end of the trading hours
)

// BacktestReport is the result of a backtest: its status (see BacktestStatusSucceeded) and
// error, the money (in Currency, the quote of the pairs: a simulation), the
// period traded and the one of the candles read, the assumptions effective,
// the summary, the trades closed in the period, the positions open at its end
// (valued at the market, apart from the realized result), the equity, the
// warnings (eg: data missing, an order refused) and what the strategy adds
// (Extra, eg: the calls of an LLM).
type BacktestReport struct {
	Status      string          `json:"status"`
	Error       string          `json:"error,omitempty"`
	Currency    string          `json:"currency"`
	Pairs       []string        `json:"pairs"`
	Timeframe   string          `json:"timeframe"`
	From        int64           `json:"from"`
	To          int64           `json:"to"`
	DataFrom    int64           `json:"data_from,omitempty"`
	DataTo      int64           `json:"data_to,omitempty"`
	Candles     int             `json:"candles"`
	Processed   int             `json:"processed"`
	Assumptions BacktestConfig  `json:"assumptions"`
	Summary     BacktestSummary `json:"summary"`
	Trades      []BacktestTrade `json:"trades"`
	Open        []BacktestOpen  `json:"open"`
	Equity      []BacktestPoint `json:"equity"`
	Warnings    []string        `json:"warnings"`
	Extra       json.RawMessage `json:"extra,omitempty"`
}

// BacktestSummary sums a report up: the starting balance, the final equity (the
// open positions valued at the market), the result of the trades closed
// (Realized, after their fees; Gross before them; Fees), the result of the
// positions open at the end (OpenProfit), the most the equity fell from a
// peak (MaxDrawdown, and its share of the peak), the moves of the
// protection, the entries refused.
type BacktestSummary struct {
	InitialBalance  float64 `json:"initial_balance"`
	FinalEquity     float64 `json:"final_equity"`
	Realized        float64 `json:"realized"`
	Gross           float64 `json:"gross"`
	Fees            float64 `json:"fees"`
	OpenProfit      float64 `json:"open_profit"`
	Trades          int     `json:"trades"`
	MaxDrawdown     float64 `json:"max_drawdown"`
	MaxDrawdownPct  float64 `json:"max_drawdown_pct"`
	ProtectionMoves int     `json:"protection_moves"`
	Refused         int     `json:"refused"`
}

// BacktestTrade is a position opened and closed in the period of a backtest, from
// flat back to flat (a reversal closes it, opening another): its side, its
// most units held, the average prices of its entries and of its exits (the
// spread and the slippage in them), its result before the fees (Gross), the
// fees and Net, the reasons of its entry and of its exit (see BacktestReasonMarket).
type BacktestTrade struct {
	Pair        string  `json:"pair"`
	Side        string  `json:"side"`
	Quantity    float64 `json:"quantity"`
	OpenedAt    int64   `json:"opened_at"`
	ClosedAt    int64   `json:"closed_at"`
	Entry       float64 `json:"entry"`
	Exit        float64 `json:"exit"`
	Gross       float64 `json:"gross"`
	Fees        float64 `json:"fees"`
	Net         float64 `json:"net"`
	EntryReason string  `json:"entry_reason"`
	ExitReason  string  `json:"exit_reason"`
}

// BacktestOpen is a position open at the end of a backtest: valued at Mark,
// its exit at the market then (the bid less the slippage for a long, the ask
// plus it for a short), its Profit after the fees paid and the one of that
// exit; never in the realized result.
type BacktestOpen struct {
	Pair     string  `json:"pair"`
	Side     string  `json:"side"`
	Quantity float64 `json:"quantity"`
	OpenedAt int64   `json:"opened_at"`
	Entry    float64 `json:"entry"`
	Mark     float64 `json:"mark"`
	Profit   float64 `json:"profit"`
}

// BacktestPoint is the equity at a time, Unix seconds.
type BacktestPoint struct {
	Time  int64   `json:"time"`
	Value float64 `json:"value"`
}
