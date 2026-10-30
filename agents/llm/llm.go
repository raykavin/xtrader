// Package llm trades with a language model as its analyst. On every candle
// close the Trader sends the market context of the pair to a Provider, OpenAI
// or Anthropic: its last candles, its indicators, its prices and the position
// held. The model answers with a structured Decision: WAIT, ENTER_LONG,
// ENTER_SHORT, HOLD, EXIT, MODIFY_SL or MODIFY_TP, with its entry, stop loss,
// take profit, confidence and reason.
//
// The model never trades. The Trader validates its decision against the
// context, then hands it to the broker of the bot as the other strategies do:
// an entry is a market order with its stop loss and take profit at the broker
// (service.ProtectionBroker), an exit a market order, a new level a leg moved
// with UpdateProtection. An answer that fails, or that is not a valid decision,
// trades nothing.
package llm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/indicator"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

// The setups of Config.Setup.
const (
	SetupFree          = "free"
	SetupTrendPullback = "trend_pullback"
)

// Config holds the inputs of the Trader, with its defaults in DefaultConfig.
// Periods are counted in candles of Timeframe.
type Config struct {
	Timeframe string

	// Candles is the window of the model: the last candles of the context.
	// The warmup covers it, and the periods of the indicators.
	Candles int

	// Warmup is the candles the Trader keeps at least, whatever its prompt:
	// the room of the prompts it switches to while it runs (see
	// SetPromptSource), whose indicators may need a longer history than the
	// ones of the prompt it starts with, which a bot running can't give. 0
	// keeps what its first prompt needs.
	Warmup int

	// The indicators of the context, the last History values of each: the
	// fast and slow EMAs, the SMA of the trend, the RSI, the ATR, the ADX
	// with its DIs, the MACD, the Bollinger Bands of BBLen candles BBDev
	// deviations wide, and the average of the volume.
	EMAFastLen int // must be shorter than EMASlowLen
	EMASlowLen int
	TrendLen   int
	RSILen     int
	ATRLen     int
	ADXLen     int
	MACDFast   int // must be shorter than MACDSlow
	MACDSlow   int
	MACDSignal int
	BBLen      int
	BBDev      float64
	VolumeLen  int
	History    int

	// MinConfidence ignores the decisions trading, all but WAIT and HOLD,
	// with a lower confidence, from 0 to 1: but the ones reducing the risk of
	// the position held that answer the note of a trader, who asked for them
	// (see reducesRisk).
	MinConfidence float64

	// MaxEntryATR refuses the entries whose price is further from the close
	// than that many ATR of ATRLen candles, as they are filled at the market,
	// and MaxLevelATR the stop losses and take profits further from it. 0
	// disables them.
	MaxEntryATR float64
	MaxLevelATR float64

	// WidenStop lets MODIFY_SL move the stop loss away from the price, which
	// adds risk to the position. Off, the stop loss only ever tightens.
	WidenStop bool

	// Language is the language of the reasons of the decisions, eg: pt-BR or
	// en.
	Language string

	// Timeout bounds each decision: the request to the provider.
	Timeout time.Duration

	// DailyGain and DailyLoss are the daily limits of the result of each pair,
	// in DailyCurrency, the one of the account, 0 for none: once the positions
	// of a pair closed during the operational day (from the midnight of
	// DayZone) made DailyGain, or lost DailyLoss, as the broker books them,
	// costs included, the new entries on the pair are refused until the next
	// day, in the reanalyses too; the positions held are managed as usual.
	// Read from the header of the prompt, see StrategyDailyLimits, and applied
	// with the results of SetAccountReporter only.
	DailyGain, DailyLoss float64
	DailyCurrency        string

	// DayZone is the time zone of the operational day of the daily limits,
	// LocalZone (the TZ of the agent, by its name) when nil.
	DayZone *time.Location

	// ConsultInterval is the time between the consultations of the model
	// about each pair, on a schedule of the clock (eg: 10m at :00, :10,
	// :20...), 10s at least. Live, it is required and the only schedule of
	// the model: each slot is asked on the first candle in progress of the
	// pair in it (see Input.Scheduled), a slot on the close of a candle on the
	// first one of the next candle, and the model is never asked at the close
	// of a candle (see OnCandle), besides the start and the reanalyses. In a
	// backtest, which has no candles in progress, the model is asked at the
	// closes on the schedule, at each close when 0.
	ConsultInterval time.Duration

	// Direction, MinPositionValue, PositionFraction, Leverage and PositionSize
	// work as in strategies.BracketConfig: the size of the entries, the
	// decision giving their stop loss and take profit.
	Direction        strategies.RKDirection
	MinPositionValue float64
	PositionFraction float64
	Leverage         float64
	PositionSize     map[string]float64

	// Setup is the setup the model trades. SetupTrendPullback only enters with
	// the trend of the pair, Summary.Trend up or down (the close beyond the
	// SMA of the trend and the fast EMA beyond the slow one, on the same
	// side), on a pullback confirmed by momentum, as the instructions describe:
	// the entries against the trend are left out of the allowed actions, and
	// without a trend the model isn't asked. SetupFree, the default, lets the
	// model weigh the context as it sees fit.
	Setup string

	// MinRiskReward refuses the entries whose reward is under that many times
	// their risk, both net of the spread when the exchange sends it: a long is
	// filled at the ask and closed at the bid. 0 disables it.
	MinRiskReward float64

	// MaxSpreadATR refuses the entries while the spread is wider than that
	// many ATR of ATRLen candles: the market is too costly to enter. 0
	// disables it, as does a spread unknown.
	MaxSpreadATR float64

	// MinStopATR refuses the entries whose stop loss leaves less room than
	// that many ATR before it is hit (from the bid for a long, from the ask
	// for a short): a stop inside the noise of the candles is hit by it. 0
	// disables it. The moves of a stop loss held aren't bound by it.
	MinStopATR float64

	// HigherTrendFilter leaves out of the allowed actions the entries against
	// the trend the higher timeframes of the context agree on (all of them
	// up, or all down, see Frame.Trend): only longs under higher timeframes
	// trending up. Without a trend they agree on, or without them (no feeder),
	// it filters nothing.
	HigherTrendFilter bool

	// HigherTrendRequired makes that trend a condition of every entry, the
	// filter of HigherTrendFilter the strict way: only the entry with the
	// trend the higher timeframes agree on is allowed, and without one (they
	// don't agree, their trend is mixed or unknown, or their candles are
	// missing) none: flat, the model isn't asked.
	HigherTrendRequired bool

	// Live, trading on the candles of the market as they close, refuses to
	// ask on a candle closed more than two periods ago: the history an
	// exchange hands after a restart or an outage, to keep the indicators
	// whole, would otherwise be traded at the market of now, each candle
	// billed. Its indicators are updated. Not for a backtest, all history.
	Live bool

	// Indicators are the indicators drawn on the chart and sent to the model:
	// groups of IndicatorGroups alone, eg: atr and volume for a strategy of
	// pure chart analysis, or kinds of IndicatorKinds with their parameters,
	// eg: "ema 20 50" or "supertrend 10 3" (see parseExtra); all the groups
	// when empty. The rules of the code use theirs whatever this, eg: the ATR
	// of the distances.
	Indicators []string

	// TakeProfitOptional lets the entries go without a take profit, the stop
	// loss alone protecting the position, eg: the trailing stop of the
	// Turtles: the model may answer a null take_profit, and the risk:reward
	// of such an entry isn't checked.
	TakeProfitOptional bool

	// NoTakeProfit trades without any take profit: the entries go with the
	// stop loss alone, moved with MODIFY_SL until it closes the position (or
	// EXIT does), eg: a trend followed. The model answers a null take_profit,
	// an entry with one is refused and MODIFY_TP isn't allowed; no
	// risk:reward is checked, so MinRiskReward must be 0. Exclusive with
	// TakeProfitOptional.
	NoTakeProfit bool

	// PendingOrders are the types of the pending entries the model may place,
	// of PendingTypes: limit (BUY_LIMIT and SELL_LIMIT) and stop (BUY_STOP and
	// SELL_STOP), with their stop loss and take profit at the broker (see
	// service.ProtectionBroker), none when empty. Read from the header of the
	// prompt, "# Pending orders: limit, stop" (see StrategyPendingOrders). A
	// pending entry is an entry: allowed while flat, one at a time on a pair,
	// under the rules of the entries (the daily limits, the news block, the
	// spread, the setup, the direction, the higher trend), measured from its
	// price; the model keeps it, moves it (MODIFY_PENDING) or cancels it
	// (CANCEL_PENDING) at its next analyses, and the Trader cancels it once an
	// entry of its type isn't allowed anymore, or a position is held on the
	// pair. Filled, it is the position held.
	PendingOrders []string

	// PendingExpiry cancels a pending entry once that many candles closed
	// since it was placed without its fill, 0 for never: the broker keeps it
	// until it is canceled otherwise.
	PendingExpiry int

	// Drawings asks the model to draw on the chart the structures its
	// analysis relies on (see Drawing), read from the header of the prompt,
	// "# Drawings: on" (see StrategyDrawings).
	Drawings bool

	// SMC adds to the context the structure of the candles, and of the higher
	// timeframes, in smart money concepts (see SMC), read from the header of
	// the prompt, "# Context: smc" (see StrategyContext).
	SMC bool

	// StructureCandles are the candles the price structure of the context is
	// computed on (see Structure), more than the Candles sent as rows: the
	// levels of a longer history for a few tokens. 0 leaves it out.
	StructureCandles int

	// HigherTimeframes are the higher timeframes of the context, read with a
	// feeder (see SetFeeder): nil for the ones of Timeframe (see
	// defaultHigherTimeframes), empty for none. HigherCandles of each are
	// sent as rows, their levels computed on frameCandles.
	HigherTimeframes []string
	HigherCandles    int

	// Correlated are the markets correlated with each pair, read with the
	// feeder on Timeframe, eg: USTECm, XAUUSDm and DXYm for BTCUSDm on MT5;
	// a pair among its own is left out.
	Correlated map[string][]string

	// Memory are the last decisions and trades closed of the pair sent, the
	// trades read from the order source (see SetOrderSource). 0 leaves it out.
	Memory int

	// TickVolume tells the model the volume of the candles is a tick volume,
	// the count of the changes of the price (eg: the CFDs of MT5), not the
	// volume traded.
	TickVolume bool

	// Calendar, when set, adds to the context the economic calendar of the
	// currencies of the pair (CalendarCurrencies, else the ones of the pair,
	// see calendarCurrencies): the releases of at least CalendarImportance
	// from CalendarBack before the time of the analysis to CalendarAhead after
	// it, that time now live (the candle in progress included), else the close
	// of the candle. New entries are refused from NewsBlockBefore before a
	// release of at least NewsBlockImportance to NewsBlockAfter after it,
	// whatever the model answers (both 0: never), checked again when an entry
	// is applied. A calendar that can't be read blocks nothing: the context
	// tells the model it is unavailable, and why. A NewsSwitch switches it
	// from the dashboard, see SetNews.
	Calendar            Calendar
	CalendarCurrencies  []string
	CalendarAhead       time.Duration
	CalendarBack        time.Duration
	CalendarImportance  Importance
	NewsBlockBefore     time.Duration
	NewsBlockAfter      time.Duration
	NewsBlockImportance Importance
}

// minConsultInterval is the least time between two consultations of the model
// about a pair, see Config.ConsultInterval: each request is billed.
const minConsultInterval = 10 * time.Second

// DefaultConfig returns the defaults of the Trader, on the 15m timeframe.
func DefaultConfig() Config {
	return Config{
		Timeframe: "15m",

		Candles: 100,

		EMAFastLen: 9,
		EMASlowLen: 21,
		TrendLen:   200,
		RSILen:     14,
		ATRLen:     14,
		ADXLen:     14,
		MACDFast:   12,
		MACDSlow:   26,
		MACDSignal: 9,
		BBLen:      20,
		BBDev:      2,
		VolumeLen:  20,
		History:    5,

		MinConfidence: 0.6,
		MaxEntryATR:   0.5,
		MaxLevelATR:   10,
		Language:      "pt-BR",
		Timeout:       time.Minute,

		Setup: SetupFree,

		StructureCandles: 200,
		HigherCandles:    8,
		Memory:           5,

		Direction:        strategies.RKDirectionBoth,
		MinPositionValue: 10,
		PositionFraction: 1,
		Leverage:         1,

		CalendarAhead:       4 * time.Hour,
		CalendarBack:        2 * time.Hour,
		CalendarImportance:  ImportanceMedium,
		NewsBlockBefore:     15 * time.Minute,
		NewsBlockAfter:      15 * time.Minute,
		NewsBlockImportance: ImportanceHigh,
	}
}

func (c Config) validate() error {
	for _, period := range []int{c.EMAFastLen, c.EMASlowLen, c.TrendLen, c.RSILen, c.ATRLen, c.ADXLen, c.MACDFast,
		c.MACDSlow, c.MACDSignal, c.BBLen, c.VolumeLen} {
		if period < 1 {
			return errors.New("periods must be at least 1")
		}
	}
	for pair, size := range c.PositionSize {
		if math.IsNaN(size) || math.IsInf(size, 0) || size <= 0 {
			return fmt.Errorf("the position size of %s must be positive, not %g", pair, size)
		}
	}

	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case c.Candles < 1:
		return errors.New("the candles of the context must be at least 1")
	case c.Warmup < 0:
		return errors.New("the warmup can't be negative")
	case c.History < 1 || c.History > c.Candles:
		return errors.New("the history of the indicators must be at least 1 and at most the candles")
	case c.EMAFastLen >= c.EMASlowLen:
		return errors.New("the fast EMA must be shorter than the slow one")
	case c.MACDFast >= c.MACDSlow:
		return errors.New("the fast MACD average must be shorter than the slow one")
	case c.BBDev <= 0:
		return errors.New("the Bollinger Bands deviation must be positive")
	case c.MinConfidence < 0 || c.MinConfidence > 1:
		return errors.New("min confidence must be from 0 to 1")
	case c.MaxEntryATR < 0 || c.MaxLevelATR < 0:
		return errors.New("the max distances in ATR can't be negative")
	case strings.TrimSpace(c.Language) == "":
		return errors.New("the language of the reasons is required")
	case c.Timeout <= 0:
		return errors.New("timeout must be positive")
	case c.DailyGain < 0 || c.DailyLoss < 0:
		return errors.New("the daily limits can't be negative")
	case (c.DailyGain > 0 || c.DailyLoss > 0) && !currencyRegexp.MatchString(c.DailyCurrency):
		return fmt.Errorf("the daily limits need the currency of the account, a code of 3 letters, eg: BRL, not %q",
			c.DailyCurrency)
	case c.ConsultInterval < 0 || (c.ConsultInterval > 0 && c.ConsultInterval < minConsultInterval):
		return fmt.Errorf("the consult interval must be at least %s, or 0 in a backtest (at each close)",
			minConsultInterval)
	case c.Live && c.ConsultInterval == 0:
		return errors.New("the consult interval (llm-consult-interval, eg: 5m) is required live: the model is " +
			"asked on its schedule only, never at the close of a candle")
	case c.MinPositionValue <= 0:
		return errors.New("min position value must be positive")
	case c.PositionFraction <= 0 || c.PositionFraction > 1:
		return errors.New("position fraction must be above 0 and at most 1")
	case c.Leverage < 1:
		return errors.New("leverage must be at least 1")
	case c.Leverage > 1 && c.PositionFraction >= 1 && len(c.PositionSize) == 0:
		return errors.New("with leverage, the position fraction must be below 1: a position can't commit the " +
			"whole balance")
	case c.Direction != strategies.RKDirectionBoth && c.Direction != strategies.RKDirectionBuyOnly &&
		c.Direction != strategies.RKDirectionSellOnly:
		return fmt.Errorf("invalid direction: %q", c.Direction)
	case c.Setup != "" && c.Setup != SetupFree && c.Setup != SetupTrendPullback:
		return fmt.Errorf("invalid setup %q: %s or %s", c.Setup, SetupFree, SetupTrendPullback)
	case c.StructureCandles < 0 || (c.StructureCandles > 0 && c.StructureCandles < 2*pivotStrength+1):
		return fmt.Errorf("the candles of the structure must be 0 (none) or at least %d", 2*pivotStrength+1)
	case c.HigherCandles < 0 || c.Memory < 0:
		return errors.New("the candles of the higher timeframes and the memory can't be negative")
	case c.MinRiskReward < 0 || c.MaxSpreadATR < 0 || c.MinStopATR < 0:
		return errors.New("the min risk:reward and the max spread in ATR can't be negative")
	case c.NoTakeProfit && c.TakeProfitOptional:
		return errors.New("the take profit can't be both optional and none")
	case c.NoTakeProfit && c.MinRiskReward > 0:
		return errors.New("a min risk:reward needs a take profit: without one, the reward of an entry is unknown")
	case c.PendingExpiry < 0:
		return errors.New("the expiry of the pending orders can't be negative")
	case slices.ContainsFunc(c.PendingOrders, func(kind string) bool { return !slices.Contains(PendingTypes, kind) }):
		return fmt.Errorf("invalid pending orders %v: of %s", c.PendingOrders, strings.Join(PendingTypes, ", "))
	case c.CalendarAhead < 0 || c.CalendarBack < 0 || c.NewsBlockBefore < 0 || c.NewsBlockAfter < 0:
		return errors.New("the windows of the calendar and of the news block can't be negative")
	case c.CalendarImportance < ImportanceLow || c.CalendarImportance > ImportanceHigh ||
		c.NewsBlockImportance < ImportanceLow || c.NewsBlockImportance > ImportanceHigh:
		return errors.New("the importance of the calendar and of the news block must be low, medium or high")
	}
	if _, err := parseExtras(c); err != nil {
		return err
	}
	if c.Calendar != nil {
		if _, err := timeframeDuration(c.Timeframe); err != nil {
			return fmt.Errorf("the calendar needs the period of the candles: %w", err)
		}
	}
	base, _ := timeframeDuration(c.Timeframe)
	for _, timeframe := range c.HigherTimeframes {
		period, err := timeframeDuration(timeframe)
		if err != nil {
			return fmt.Errorf("the higher timeframes: %w", err)
		}
		if period <= base {
			return fmt.Errorf("the higher timeframe %s isn't above %s", timeframe, c.Timeframe)
		}
	}
	return nil
}

// Trader trades the decisions of a Provider, one position at a time per pair,
// see the package documentation and Config. Positions opened before a restart,
// or by someone else, are followed without their entry nor their legs, so the
// model may hold or exit them, but not move their levels.
type Trader struct {
	cfg      Config
	provider Provider
	name     string               // of the provider, for the logs
	series   []series             // of the indicators of the context, the groups of IndicatorGroups
	warmup   int                  // the candles kept, the room of the prompts, see Config.Warmup
	trades   map[string]*trade    // the positions followed, by pair
	handled  map[string]time.Time // the last candle handled, by pair: one request per candle at most
	// the slot of ConsultInterval consulted last, by pair: one consultation per slot
	consulted map[string]time.Time
	starting  map[string]*starting // the decisions of the start asked apart, by pair, see startApart
	history   map[string]int       // the candles of history skipped in a row, by pair, see Config.Live

	orders func(pair string) ([]model.Order, error) // the stored orders of a pair, see SetOrderSource

	reporter    service.AccountReporter // the results the broker books, see SetAccountReporter
	currencyMtx sync.Mutex
	currency    string               // of the account, once read, see accountCurrency
	dailyWarned ui.DecisionPrompt    // the prompt whose daily limits were told unapplied
	orphans     map[int64]bool       // the fills closing a position unknown, logged, see pairTrades
	overdue     map[int64]bool       // the closes whose result is late, told, see pendingWhy
	awaitedLate map[string]time.Time // the close at the broker of each pair told late, see awaitedWhy
	// the prompt told refused for the currency of its daily limits, see tellCurrency
	currencyTold *promptState

	feeder     CandleSource            // of the higher timeframes and the correlated markets, see SetFeeder
	fetched    map[string]fetched      // the candles read of the feeder, see fetch
	higher     []string                // the higher timeframes of the context
	remembered map[string][]remembered // the last decisions of each pair, for the memory

	state     atomic.Pointer[promptState] // the prompt analyzed with, see refreshPrompt
	overrides PromptOverrides             // of the flags, over the headers of the prompts
	prompts   PromptSource                // of the prompts to switch to, see SetPromptSource
	refused   ui.DecisionPrompt           // the last prompt of prompts refused, not tried again

	journal Journal // of the calls to the model, see SetJournal

	levelsMtx sync.Mutex
	levels    map[string][]Level // of the last decision, by pair: drawn on the chart, see ChartLevels

	pendings map[string]*pendingOrder // the pending entries followed, by pair, see Config.PendingOrders

	analysisMtx sync.Mutex
	analyses    map[string]ui.Decision // the last decision answered of each pair, drawn on the chart, see ChartAnalysis
	restored    map[string]bool        // the pairs whose analysis was read back from the store, see restoreAnalysis

	now func() time.Time // the wall clock of the rate limits

	reMtx   sync.Mutex
	paused  time.Time              // no request before it: the Retry-After of a rate limit
	pending map[string]*reanalysis // the reanalyses asked, waiting for an analysis, by pair, see Reanalyze
	running map[string]*reanalysis // the pairs analyzed now, with the reanalysis the analysis answers, nil for none

	decisionsMtx sync.RWMutex
	decisions    DecisionStore       // of the decisions with their justification, see SetDecisionStore
	listeners    []func(ui.Decision) // see ListenDecisions
	notify       func(string)        // see SetNotifier

	skipsMtx sync.Mutex
	skips    map[string]*skipRun    // the analyses skipped in a row for the same reason, by pair, see grouped
	keepMtx  map[string]*sync.Mutex // the records of each pair kept one at a time, see keep

	period        time.Duration // of the candles, to know their close
	calendarError time.Time     // last warning of a calendar that couldn't be read

	// the context of the requests to the model, done by Stop; nil for none
	quit context.Context
	stop context.CancelFunc
}

// series is an indicator of the context: its key in the Metadata of the
// dataframe, and its name for the model, eg: ema_9.
type series struct {
	key, name, group string
}

// IndicatorGroups are the groups of Config.Indicators: the fast and the slow
// EMAs, the SMA of the trend, the RSI, the ATR, the ADX with its DIs, the MACD
// with its signal and histogram, the Bollinger Bands, and the average volume.
var IndicatorGroups = []string{"ema", "sma", "rsi", "atr", "adx", "macd", "bb", "volume"}

// trade is a position of the Trader: its side, its entry, 0 when unknown, its
// opening, or the time it is followed from, and the candles closed since (see
// heldCandles), the legs of its stop loss and take profit at the broker, and
// whether its exit was decided, its close retried on the next ticks while it
// fails, the position protected meanwhile, see close.
type trade struct {
	side    model.SideType
	entry   float64
	stop    float64 // the stop loss placed with the entry, the unit of the risk; 0 when unknown
	since   time.Time
	bars    int
	legs    []model.Order
	exiting bool

	attempts int         // of its close
	retryAt  time.Time   // the next attempt of its close on a tick, see retryClose
	canceled *protection // the levels of its legs canceled for its close, nil once placed again
	failed   int         // the attempts of its close failed
	bare     bool        // notified left without its legs
}

// protection is the stop loss and the take profit of a position, 0 for none.
type protection struct {
	stop, profit float64
}

// The attempts to close a position on the ticks, once its close failed: the
// first one after closeRetry, then twice as late each time, up to
// closeRetryMax, so that the broker isn't flooded with orders it refuses, eg:
// the market closed. The close of each candle is retried at once.
const (
	closeRetry    = 30 * time.Second
	closeRetryMax = 15 * time.Minute
)

// Option configures the Trader, see New.
type Option func(*options)

type options struct {
	prompt    *Prompt
	overrides PromptOverrides
}

// WithPrompt sets the prompt the Trader starts with, rather than the
// instructions of its provider with the indicators and the take profit of its
// Config: its strategy, sent with each request (see SystemPromptProvider), its
// indicators and its take profit read from its header, the overrides taking
// precedence over it. The prompts of SetPromptSource take the same overrides.
func WithPrompt(p Prompt, overrides PromptOverrides) Option {
	return func(o *options) {
		o.prompt, o.overrides = &p, overrides
	}
}

// New validates the configuration and creates the Trader of the decisions of
// provider.
func New(cfg Config, provider Provider, opts ...Option) (*Trader, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("llm: %w", err)
	}
	if provider == nil {
		return nil, errors.New("llm: a provider is required")
	}
	var o options
	for _, option := range opts {
		option(&o)
	}

	name := fmt.Sprintf("%T", provider)
	if stringer, ok := provider.(fmt.Stringer); ok {
		name = stringer.String()
	}

	period, _ := timeframeDuration(cfg.Timeframe) // needed and checked with a calendar only
	higher := cfg.HigherTimeframes
	if higher == nil {
		higher = defaultHigherTimeframes(cfg.Timeframe)
	}
	if cfg.HigherTrendRequired && len(higher) == 0 {
		return nil, fmt.Errorf("llm: the trend of the higher timeframes is required for an entry, without any "+
			"above %s", cfg.Timeframe)
	}

	quit, stop := context.WithCancel(context.Background())
	t := &Trader{
		quit:      quit,
		stop:      stop,
		period:    period,
		decisions: NewMemoryDecisions(memoryDecisions(cfg)),
		skips:     make(map[string]*skipRun),
		pending:   make(map[string]*reanalysis),
		running:   make(map[string]*reanalysis),
		cfg:       cfg,
		provider:  provider,
		name:      name,
		overrides: o.overrides,
		series: []series{
			{"llm_ema_fast", fmt.Sprintf("ema_%d", cfg.EMAFastLen), "ema"},
			{"llm_ema_slow", fmt.Sprintf("ema_%d", cfg.EMASlowLen), "ema"},
			{"llm_sma", fmt.Sprintf("sma_%d", cfg.TrendLen), "sma"},
			{"llm_rsi", fmt.Sprintf("rsi_%d", cfg.RSILen), "rsi"},
			{"llm_atr", fmt.Sprintf("atr_%d", cfg.ATRLen), "atr"},
			{"llm_adx", fmt.Sprintf("adx_%d", cfg.ADXLen), "adx"},
			{"llm_plus_di", fmt.Sprintf("plus_di_%d", cfg.ADXLen), "adx"},
			{"llm_minus_di", fmt.Sprintf("minus_di_%d", cfg.ADXLen), "adx"},
			{"llm_macd", fmt.Sprintf("macd_%d_%d", cfg.MACDFast, cfg.MACDSlow), "macd"},
			{"llm_macd_signal", fmt.Sprintf("macd_signal_%d", cfg.MACDSignal), "macd"},
			{"llm_macd_hist", "macd_histogram", "macd"},
			{"llm_bb_upper", fmt.Sprintf("bb_upper_%d_%g", cfg.BBLen, cfg.BBDev), "bb"},
			{"llm_bb_middle", fmt.Sprintf("bb_middle_%d", cfg.BBLen), "bb"},
			{"llm_bb_lower", fmt.Sprintf("bb_lower_%d_%g", cfg.BBLen, cfg.BBDev), "bb"},
			{"llm_volume_sma", fmt.Sprintf("volume_sma_%d", cfg.VolumeLen), "volume"},
		},
		trades:    make(map[string]*trade),
		handled:   make(map[string]time.Time),
		consulted: make(map[string]time.Time),
		starting:  make(map[string]*starting),
		history:   make(map[string]int),
		levels:    make(map[string][]Level),
		pendings:  make(map[string]*pendingOrder),
		now:       time.Now,

		fetched:    make(map[string]fetched),
		higher:     higher,
		remembered: make(map[string][]remembered),
	}

	state, err := newPromptState(cfg, ui.DecisionPrompt{}, "")
	if o.prompt != nil {
		if _, ok := provider.(SystemPromptProvider); !ok {
			return nil, fmt.Errorf("llm: %s takes no prompt with its requests", name)
		}
		state, err = t.promptState(*o.prompt)
		if err != nil {
			err = fmt.Errorf("the prompt %s: %w", describePrompt(o.prompt.Ref), err)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("llm: %w", err)
	}
	t.state.Store(state)
	t.warmup = max(state.need, cfg.Warmup)
	return t, nil
}

// liveDecisionsHeld are the decisions a Trader trading live keeps in memory
// without SetDecisionStore, eg: on a paper wallet: the last ones, as a bot runs
// for months, the dashboard listing 500 at most; a backtest keeps them all.
const liveDecisionsHeld = 2000

// memoryDecisions returns the capacity of the decisions kept in memory by
// default, see liveDecisionsHeld.
func memoryDecisions(cfg Config) int {
	if cfg.Live {
		return liveDecisionsHeld
	}
	return 0
}

func (t *Trader) Timeframe() string {
	return t.cfg.Timeframe
}

func (t *Trader) WarmupPeriod() int {
	return t.warmup
}

// Indicators computes the indicators of the prompt analyzed with on df, and
// returns the ones drawn. Called by the chart too, whose candles are its own,
// it reads the prompt once: the one of the last analysis.
func (t *Trader) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	st := t.state.Load()
	t.compute(df, st)
	return t.chart(df, st)
}

// compute computes the indicators of st on df: the groups of IndicatorGroups
// and the extras of the prompt.
func (t *Trader) compute(df *model.Dataframe, st *promptState) {
	c := t.cfg
	if len(df.Close) < st.lookback {
		// talib fails on fewer candles than its periods: the chart may have
		// fewer than the warmup
		for _, s := range t.series {
			df.Metadata[s.key] = nans(len(df.Close))
		}
		for _, e := range st.extras {
			for _, s := range e.series {
				df.Metadata[s.key] = nans(len(df.Close))
			}
		}
	} else {
		for _, e := range st.extras {
			e.compute(df)
		}
		df.Metadata["llm_ema_fast"] = indicator.EMA(df.Close, c.EMAFastLen)
		df.Metadata["llm_ema_slow"] = indicator.EMA(df.Close, c.EMASlowLen)
		df.Metadata["llm_sma"] = indicator.SMA(df.Close, c.TrendLen)
		df.Metadata["llm_rsi"] = indicator.RSI(df.Close, c.RSILen)
		df.Metadata["llm_atr"] = indicator.ATR(df.High, df.Low, df.Close, c.ATRLen)
		df.Metadata["llm_adx"] = indicator.ADX(df.High, df.Low, df.Close, c.ADXLen)
		df.Metadata["llm_plus_di"] = indicator.PlusDI(df.High, df.Low, df.Close, c.ADXLen)
		df.Metadata["llm_minus_di"] = indicator.MinusDI(df.High, df.Low, df.Close, c.ADXLen)
		macd, signal, hist := indicator.MACD(df.Close, c.MACDFast, c.MACDSlow, c.MACDSignal)
		df.Metadata["llm_macd"], df.Metadata["llm_macd_signal"], df.Metadata["llm_macd_hist"] = macd, signal, hist
		upper, middle, lower := indicator.BB(df.Close, c.BBLen, c.BBDev, indicator.TypeSMA)
		df.Metadata["llm_bb_upper"], df.Metadata["llm_bb_middle"], df.Metadata["llm_bb_lower"] = upper, middle, lower
		df.Metadata["llm_volume_sma"] = indicator.SMA(df.Volume, c.VolumeLen)
	}
}

// chart returns the indicators of st the model is sent, drawn: the averages
// and the bands on the price, the others in panes of their own.
func (t *Trader) chart(df *model.Dataframe, st *promptState) []strategy.ChartIndicator {
	c := t.cfg
	line := func(key, name, color string) strategy.IndicatorMetric {
		return strategy.IndicatorMetric{Values: df.Metadata[key], Name: name, Color: color, Style: strategy.StyleLine}
	}
	pane := func(group string, metrics ...strategy.IndicatorMetric) strategy.ChartIndicator {
		return strategy.ChartIndicator{GroupName: group, Time: df.Time, Warmup: st.lookback, Metrics: metrics}
	}
	var metrics []strategy.IndicatorMetric
	if st.shows("ema") {
		metrics = append(metrics,
			strategy.IndicatorMetric{Values: df.Metadata["llm_ema_fast"], Name: fmt.Sprintf("EMA %d", c.EMAFastLen),
				Color: "#fd8700", Style: strategy.StyleLine},
			strategy.IndicatorMetric{Values: df.Metadata["llm_ema_slow"], Name: fmt.Sprintf("EMA %d", c.EMASlowLen),
				Color: "#1e88e5", Style: strategy.StyleLine})
	}
	if st.shows("sma") {
		metrics = append(metrics, strategy.IndicatorMetric{Values: df.Metadata["llm_sma"],
			Name: fmt.Sprintf("SMA %d", c.TrendLen), Color: "#8e44ad", Style: strategy.StyleLine})
	}
	if st.shows("bb") {
		metrics = append(metrics,
			line("llm_bb_upper", fmt.Sprintf("BB %d upper", c.BBLen), "#7f8c8d"),
			line("llm_bb_middle", fmt.Sprintf("BB %d", c.BBLen), "#95a5a6"),
			line("llm_bb_lower", fmt.Sprintf("BB %d lower", c.BBLen), "#7f8c8d"))
	}
	overlay, panes := chartExtras(df, st.extras, st.lookback)
	metrics = append(metrics, overlay...)
	var indicators []strategy.ChartIndicator
	if len(metrics) > 0 {
		indicators = append(indicators,
			strategy.ChartIndicator{Overlay: true, GroupName: "LLM", Time: df.Time, Warmup: st.lookback, Metrics: metrics})
	}
	if st.shows("atr") {
		indicators = append(indicators, pane(fmt.Sprintf("ATR %d", c.ATRLen),
			line("llm_atr", "", "#f39c12"))) // named by its pane, once in the legend
	}
	if st.shows("rsi") {
		indicators = append(indicators, pane(fmt.Sprintf("RSI %d", c.RSILen),
			line("llm_rsi", "", "#9b59b6")))
	}
	if st.shows("adx") {
		indicators = append(indicators, pane(fmt.Sprintf("ADX %d", c.ADXLen),
			line("llm_adx", "ADX", "#ecf0f1"), line("llm_plus_di", "+DI", "#2ecc71"), line("llm_minus_di", "-DI", "#e74c3c")))
	}
	if st.shows("macd") {
		hist := line("llm_macd_hist", "Histogram", "#95a5a6")
		hist.Style = strategy.StyleHistogram
		indicators = append(indicators, pane(fmt.Sprintf("MACD %d %d %d", c.MACDFast, c.MACDSlow, c.MACDSignal),
			line("llm_macd", "MACD", "#3498db"), line("llm_macd_signal", "Signal", "#e67e22"), hist))
	}
	if st.shows("volume") {
		indicators = append(indicators, pane(fmt.Sprintf("Volume SMA %d", c.VolumeLen),
			line("llm_volume_sma", "", "#1abc9c")))
	}
	return append(indicators, panes...)
}

// OnCandle follows the candle just closed of df: live, the model isn't asked
// at the close (see Config.ConsultInterval), only the exit that failed retried
// and the pending order followed; in a backtest, the model is asked about it
// on the schedule, see onClose.
func (t *Trader) OnCandle(df *model.Dataframe, broker service.Broker) {
	t.finishStart(df, broker, true) // the decision of the start first, asked before the close
	if t.cfg.Live || !t.onClose(df, broker, TriggerCandle) {
		t.retryExit(df, broker)    // a close left out of the analyses: the exit retried all the same
		t.guardPending(df, broker) // and the pending order followed
	}
}

// onClose asks the model about the candle just closed of df, once per candle,
// trigger telling why: at its close in a backtest, or on the first candle in
// progress after the start. It reports whether the candle is analyzed, false
// when it is left out.
func (t *Trader) onClose(df *model.Dataframe, broker service.Broker, trigger string) bool {
	if len(df.Close) == 0 {
		return false
	}
	if len(df.Time) > 0 {
		// a candle handled already, handed again: never asked twice, as each
		// request is billed
		at := df.Time[len(df.Time)-1]
		if last, ok := t.handled[df.Pair]; ok && !at.After(last) {
			log.Warnf("[LLM] %s %s | the candle of %s again, after the one of %s: skipped", df.Pair,
				t.cfg.Timeframe, at.UTC().Format(time.DateTime), last.UTC().Format(time.DateTime))
			return false
		}
		t.handled[df.Pair] = at
		if t.cfg.Live && t.period > 0 {
			if closed := at.Add(t.period); t.now().Sub(closed) > 2*t.period {
				if t.history[df.Pair] == 0 {
					log.Warnf("[LLM] %s %s | the candle of %s closed %s ago: history, not asked until the "+
						"live candles", df.Pair, t.cfg.Timeframe, at.UTC().Format(time.DateTime),
						t.now().Sub(closed).Round(time.Second))
				}
				t.history[df.Pair]++
				return false
			}
			if skipped := t.history[df.Pair]; skipped > 0 {
				log.Infof("[LLM] %s %s | live again, after %d candles of history not asked", df.Pair,
					t.cfg.Timeframe, skipped)
				delete(t.history, df.Pair)
			}
		}
		if interval := t.cfg.ConsultInterval; interval > 0 && trigger == TriggerCandle && t.period > 0 {
			// the close consulted when it falls on the schedule, else left out
			closed := at.Add(t.period)
			if !closed.Truncate(interval).Equal(closed) {
				log.Debugf("[LLM] %s %s | the close of %s off the consult interval of %s: not asked", df.Pair,
					t.cfg.Timeframe, closed.UTC().Format(time.DateTime), interval)
				return false
			}
			t.consulted[df.Pair] = closed
		}
	}
	if trigger == TriggerStart {
		t.startApart(df, broker)
		return true
	}
	t.analyze(df, broker, analysis{trigger: trigger, market: df})
	return true
}

// analysis is a decision to ask: why (TriggerCandle, TriggerStart or
// TriggerReanalysis), and the dataframe of the market of now, whose last candle
// gives the price: the one analyzed at the close of a candle, and with the
// candle in progress for a reanalysis, see Reanalyze.
type analysis struct {
	trigger    string
	market     *model.Dataframe
	inProgress bool // the last candle of market is in progress
}

// analyze asks the model about the closed candles of df, at the market of run,
// and applies its decision, kept with its justification (see SetDecisionStore)
// whatever comes of it. A reanalysis asked before it, or while it runs, is
// answered by it, its result notified, see SetNotifier.
func (t *Trader) analyze(df *model.Dataframe, broker service.Broker, run analysis) {
	p, result := t.prepare(df, broker, run)
	if p != nil {
		result = t.finish(p, broker, t.request(p.st, p.input, t.now()))
	}
	t.endAnalysis(df.Pair, result)
}

// prepared is an analysis ready to ask the model, see prepare: its candles,
// its market, its prompt, the position followed, its context, why it is
// asked, its time (see dayClock), the one its decision is remembered at, and
// what happened to the pending order of the pair, told with its decision.
type prepared struct {
	df, market  *model.Dataframe
	st          *promptState
	trade       *trade
	input       Input
	trigger     string
	requestedBy string
	at          time.Time
	pending     *pendingEvent
}

// prepare begins the analysis of the closed candles of df, at the market of
// run (see analyze), up to the context the model is asked with, and returns
// it; nil, with the result of the analysis, when the model isn't asked: flat
// without any entry to open, a close that failed retried first, or the provider
// rate limited, the analysis kept then with why (see OutcomeSkipped). The
// caller ends the analysis, see endAnalysis.
func (t *Trader) prepare(df *model.Dataframe, broker service.Broker, run analysis) (*prepared, string) {
	st := t.refreshPrompt(df) // the analysis goes on with it, whatever the switches meanwhile
	t.restoreAnalysis(df.Pair)
	requested := t.beginAnalysis(df.Pair)

	price := run.market.Close.Last(0)
	var trade *trade
	ev := &pendingEvent{}
	skipped := func(s skip) (*prepared, string) {
		record := t.skipRecord(df, st, run.trigger, requested, price, trade, s)
		pendingRecord(&record, ev)
		t.keep(record, nil)
		return nil, "the model wasn't asked: " + s.detail
	}
	asset, quote, err := broker.Position(df.Pair)
	if err != nil {
		log.Error(err)
		return skipped(skip{SkipPosition, "the position: " + err.Error()})
	}
	point, err := pointValue(broker, df.Pair)
	if err != nil {
		log.Error(err)
		return skipped(skip{SkipPosition, err.Error()})
	}
	cash := money{free: quote, point: point}

	at := t.dayClock(df)
	held := heldSide(asset*price*point, t.cfg.MinPositionValue)
	t.refreshPending(df, broker, st, held, at, ev) // filled, it is the position followed
	trade = t.follow(df, held, at)
	if trade != nil && trade.exiting {
		// a close that failed: retried at the close of a candle, and on the
		// ticks once due
		if !t.retryClose(run.market, broker, trade, !run.inProgress) {
			return skipped(skip{SkipCloseRetry, "a close that failed is retried on the ticks, next at " +
				trade.retryAt.UTC().Format(time.TimeOnly)})
		}
		return skipped(skip{SkipCloseRetry, "a close that failed was retried first"})
	}

	pending := t.pendingOf(df.Pair)
	if trade != nil {
		pending = nil // a position held: its cancel is retried, see refreshPending
	}
	input, why := t.context(df, run.market, broker, st, trade, pending, asset, cash, price)
	enriched, kept := false, false
	if pending != nil {
		// kept while an entry of its type is allowed now, as if flat without
		// it: the higher trend of the context included
		if why.code == "" {
			t.enrich(df, &input, st, at)
			enriched = true
			why = t.higherTrend(df.Pair, &input)
		}
		if t.checkPending(df, broker, pending, &input, why, at, ev) {
			if pending.canceling {
				return skipped(skip{SkipPendingCancel, "the cancel of " + pending.describe() + " is awaited"})
			}
			kept, why = true, skip{}
			input.PendingOrder = pending.context(t, df, at)
			input.Rules.Actions = []Action{ActionWait, ActionModifyPending, ActionCancelPending}
		} else {
			// canceled: the analysis goes on flat, afresh
			input, why = t.context(df, run.market, broker, st, nil, nil, asset, cash, price)
			enriched = false
		}
	}
	if why.code != "" {
		return skipped(why) // flat without any entry to open: nothing to ask
	}
	if now, until := t.now(), t.pausedUntil(); now.Before(until) {
		log.Debugf("[LLM] %s %s | %s | rate limited for %s", df.Pair, t.cfg.Timeframe, t.name,
			until.Sub(now).Round(time.Second))
		return skipped(skip{SkipRateLimited, fmt.Sprintf("%s is rate limited for %s", t.name,
			until.Sub(now).Round(time.Second))})
	}

	if !enriched {
		t.enrich(df, &input, st, at)
	}
	if requested != nil {
		input.ReanalysisNote = requested.note
	}
	if run.inProgress {
		last := len(run.market.Close) - 1
		input.Reanalysis = run.trigger == TriggerReanalysis
		input.Scheduled = run.trigger == TriggerInterval
		input.CandleInProgress = &CandleInProgress{Open: number(run.market.Open[last]),
			High: number(run.market.High[last]), Low: number(run.market.Low[last]), Close: number(price),
			Volume: number(run.market.Volume[last])}
		if last < len(run.market.Time) {
			input.CandleInProgress.Time = run.market.Time[last].UTC()
		}
	}
	if !kept {
		if why := t.higherTrend(df.Pair, &input); why.code != "" {
			return skipped(why) // only WAIT left: nothing to ask
		}
	}

	p := &prepared{df: df, market: run.market, st: st, trade: trade, input: input, trigger: run.trigger, at: at,
		pending: ev}
	if requested != nil {
		p.requestedBy = string(requested.channel)
	}
	return p, ""
}

// finish concludes the analysis p with the reply of the model: its decision,
// validated and kept, applied to the broker. It returns the result of the
// analysis, see endAnalysis. A request that failed once Stop was called, eg:
// the one in progress it ended, is no failure of the model: nothing is kept,
// neither a decision nor an entry of the journal.
func (t *Trader) finish(p *prepared, broker service.Broker, r reply) string {
	if r.err != nil && t.stopped() {
		log.Infof("[LLM] %s %s | %s | the agent stops: the request to the model left without its reply (%v), "+
			"nothing kept", p.input.Symbol, p.input.Timeframe, t.name, r.err)
		return "no decision: the agent stopped before the model answered"
	}
	decision, kept, err := t.conclude(p, r)
	pendingRecord(&kept, p.pending)
	if err != nil {
		t.keep(kept, decision.Attachments)
		return describeDecision(kept) // no decision, no trade
	}
	t.levelsMtx.Lock()
	t.levels[p.df.Pair] = decision.Levels
	t.levelsMtx.Unlock()
	orders, outcome, detail := t.apply(p.market, broker, p.st, p.input, p.trade, decision)
	kept.Outcome = outcome
	switch {
	case outcome == OutcomeRefused:
		kept.Error = detail
	case detail != "":
		kept.Notes = append(kept.Notes, ui.DecisionNote{Kind: "text", Text: detail})
	}
	for _, order := range orders {
		if order.ID > 0 { // numbered by the order controller of the bot, the broker of the strategies
			kept.OrderIDs = append(kept.OrderIDs, order.ID)
		}
	}
	t.keep(kept, decision.Attachments)
	return describeDecision(kept)
}

// starting is the decision of the start of a pair, the model asked apart (see
// startApart): its analysis, and the reply of the model, set once done is
// closed.
type starting struct {
	prepared *prepared
	reply    reply
	done     chan struct{}
}

// startApart begins the decision of the start on the closed candles of df, as
// any analysis (see prepare), the model asked apart: the pairs of the agent
// are asked at once, the first candle in progress of each of them coming
// within seconds, rather than one after another. Its decision is applied in
// the flow of the strategy, on the next candle of the pair, see finishStart.
func (t *Trader) startApart(df *model.Dataframe, broker service.Broker) {
	if interval := t.cfg.ConsultInterval; interval > 0 {
		t.consulted[df.Pair] = t.now().Truncate(interval) // the slot of the start, consulted
	}
	p, result := t.prepare(df, broker, analysis{trigger: TriggerStart, market: df})
	if p == nil {
		t.endAnalysis(df.Pair, result)
		return
	}
	s := &starting{prepared: p, done: make(chan struct{})}
	t.starting[df.Pair] = s
	at := t.now()
	go func() {
		defer close(s.done)
		s.reply = t.request(p.st, p.input, at)
	}()
}

// finishStart applies the decision of the start of the pair of df, once the
// model answered, waiting for it with wait, at the market of df, the one of
// now, and reports whether none is left to apply. The slot of the consult
// interval it ends in is consulted.
func (t *Trader) finishStart(df *model.Dataframe, broker service.Broker, wait bool) bool {
	pair := df.Pair
	s, ok := t.starting[pair]
	if !ok {
		return true
	}
	if wait {
		<-s.done
	} else {
		select {
		case <-s.done:
		default:
			return false
		}
	}
	delete(t.starting, pair)
	if len(df.Close) > 0 {
		s.prepared.market = df // applied a while after its context was read
	}
	t.endAnalysis(pair, t.finish(s.prepared, broker, s.reply))
	if interval := t.cfg.ConsultInterval; interval > 0 {
		t.consulted[pair] = t.now().Truncate(interval)
	}
	return true
}

// OnPartialCandle asks the model on the candles in progress, live only, the
// only place it is asked live: once on the first one after the start, about
// the candle closed before it; on the one after a reanalysis is asked, at its
// price, see Reanalyze; and on the first one of each slot of ConsultInterval
// from the open of the candle in progress to its close, a slot on the close
// of a candle asked on the first one of the next candle, the candle closed
// with the others (never on a candle still handed in progress past its close,
// the close late, complete but not confirmed). The decision of the start,
// asked apart for the pairs to be asked at once (see startApart), is applied
// on the next one, and takes the slots it is asked in. On each of them, the
// close of an exit that failed is retried once due, see retryClose.
func (t *Trader) OnPartialCandle(df *model.Dataframe, broker service.Broker) {
	if len(df.Close) == 0 {
		return
	}
	t.retryExit(df, broker)    // at the price of now
	t.guardPending(df, broker) // the pending order followed, at the price of now
	if !t.cfg.Live || len(df.Close) < 2 {
		return
	}
	if !t.finishStart(df, broker, false) {
		return // the model still asked about the start of the pair
	}
	if _, started := t.handled[df.Pair]; !started {
		t.onClose(closed(df), broker, TriggerStart)
		return
	}
	if t.reanalysisPending(df.Pair) {
		if interval := t.cfg.ConsultInterval; interval > 0 {
			t.consulted[df.Pair] = t.now().Truncate(interval) // the slot it is asked in, answered by it
		}
		t.analyze(closed(df), broker, analysis{trigger: TriggerReanalysis, market: df, inProgress: true})
		return
	}
	if interval := t.cfg.ConsultInterval; interval > 0 && len(df.Time) == len(df.Close) {
		slot := t.now().Truncate(interval)
		opened := df.Time[len(df.Time)-1]
		// within the candle in progress only, from its open: a slot at or past
		// its close is asked on the next candle, once this one is closed
		within := !slot.Before(opened) && (t.period <= 0 || slot.Before(opened.Add(t.period)))
		if within && t.consulted[df.Pair].Before(slot) {
			t.consulted[df.Pair] = slot
			t.analyze(closed(df), broker, analysis{trigger: TriggerInterval, market: df, inProgress: true})
		}
	}
}

// closed returns df without its last candle, the one in progress: its rows
// and its indicators, which only look back, until the candle closed before.
func closed(df *model.Dataframe) *model.Dataframe {
	n := len(df.Close) - 1
	view := &model.Dataframe{
		Pair:     df.Pair,
		Close:    df.Close[:n],
		Open:     df.Open[:n],
		High:     df.High[:n],
		Low:      df.Low[:n],
		Volume:   df.Volume[:n],
		Metadata: make(map[string]model.Series[float64], len(df.Metadata)),
	}
	if len(df.Time) > n {
		view.Time = df.Time[:n]
		view.LastUpdate = df.Time[n-1]
	}
	for name, values := range df.Metadata {
		if len(values) > n {
			view.Metadata[name] = values[:n]
		} else {
			view.Metadata[name] = values
		}
	}
	return view
}

// ChartLevels returns the supports and resistances of the last decision on
// pair, drawn on the chart: what the model saw. It implements
// ui.LevelsSource.
func (t *Trader) ChartLevels(pair string) []ui.Level {
	t.levelsMtx.Lock()
	defer t.levelsMtx.Unlock()
	levels := make([]ui.Level, 0, len(t.levels[pair]))
	for _, level := range t.levels[pair] {
		levels = append(levels, ui.Level{Kind: level.Kind, Price: level.Price})
	}
	return levels
}

// follow returns the position followed on the pair of df, the closed candles
// of the analysis at at (see dayClock), once held is the side of the one held,
// none when flat, with its candles held, see heldCandles.
func (t *Trader) follow(df *model.Dataframe, held model.SideType, at time.Time) *trade {
	pair := df.Pair
	if held == "" {
		delete(t.trades, pair) // its stop loss or take profit closed it, if any
		return nil
	}

	tr, ok := t.trades[pair]
	if !ok || tr.side != held {
		// a position opened before a restart, or by someone else: followed
		// from now on, with its entry and its legs when they are stored, see
		// SetOrderSource
		tr = &trade{side: held, since: at}
		t.recover(pair, tr)
		t.trades[pair] = tr
	}
	tr.bars = t.heldCandles(df, tr.since, at)
	return tr
}

// heldCandles returns the candles closed since since, the opening of a
// position or the time it is followed from, df the closed candles of the
// analysis at at, by their times rather than by the analyses, which may leave
// closes out (see Config.ConsultInterval): the candles of df closing after it,
// as a market closed (eg: on a weekend) closes none, the ones before the
// window of df counted by the time; by the time up to at without the times of
// the candles.
func (t *Trader) heldCandles(df *model.Dataframe, since, at time.Time) int {
	if t.period <= 0 || since.IsZero() {
		return 0
	}
	n := len(df.Time)
	if n == 0 {
		return max(int(at.Sub(since)/t.period), 0)
	}
	first := sort.Search(n, func(i int) bool { return df.Time[i].Add(t.period).After(since) })
	held := n - first
	if first == 0 {
		held += max(int(df.Time[0].Sub(since)/t.period), 0)
	}
	return held
}

// SetOrderSource sets where the Trader finds the stored orders of a pair, eg:
// the storage of the bot, before it runs: a position found at the start,
// opened before a restart, is followed with its entry and the legs of its
// stop loss and take profit, which it may move, rather than without them.
func (t *Trader) SetOrderSource(orders func(pair string) ([]model.Order, error)) {
	t.orders = orders
}

// recover sets the entry and the legs of tr, a position found held, from the
// stored orders of pair: its last fill on its side, its opening the creation
// of that fill, and its open legs (grouped orders on the other side) of that
// entry (see legOf), the last stop loss and take profit. The initial stop,
// unknown, is taken as the stop loss of now.
func (t *Trader) recover(pair string, tr *trade) {
	if t.orders == nil {
		return
	}
	orders, err := t.orders(pair)
	if err != nil {
		log.Warnf("[LLM] %s | the stored orders: %v: the position followed without its entry nor its legs", pair, err)
		return
	}

	var entry, stop, profit *model.Order
	for i := range orders {
		order := &orders[i]
		if order.Side == tr.side && order.Status == model.OrderStatusTypeFilled && order.GroupID == nil &&
			(entry == nil || order.ID > entry.ID) {
			entry = order
		}
	}
	stale := 0 // open legs of another entry
	for i := range orders {
		order := &orders[i]
		if order.Side == tr.side || order.GroupID == nil || order.Status != model.OrderStatusTypeNew &&
			order.Status != model.OrderStatusTypePartiallyFilled {
			continue
		}
		if !t.legOf(*order, entry) {
			stale++
			continue
		}
		isStop := order.Type == model.OrderTypeStopLoss || order.Type == model.OrderTypeStopLossLimit
		if isStop && (stop == nil || order.ID > stop.ID) {
			stop = order
		} else if !isStop && (profit == nil || order.ID > profit.ID) {
			profit = order
		}
	}
	if stale > 0 {
		log.Warnf("[LLM] %s %s | %d open legs of the stored orders not taken for the ones of the %s position found: "+
			"of another entry, eg: of a position closed whose exit the broker hasn't booked yet", pair,
			t.cfg.Timeframe, stale, tr.side)
	}
	if entry != nil && entry.Price > 0 {
		tr.entry = entry.Price
		if !entry.CreatedAt.IsZero() {
			tr.since = entry.CreatedAt
		}
	}
	for _, leg := range []*model.Order{profit, stop} {
		if leg != nil {
			tr.legs = append(tr.legs, *leg)
		}
	}
	if i := tr.leg(true); i >= 0 {
		tr.stop = legLevel(tr.legs[i])
	}
	if tr.entry > 0 || len(tr.legs) > 0 {
		log.Infof("[LLM] %s %s | the %s position found followed with its entry %g and %d legs, from the stored orders",
			pair, t.cfg.Timeframe, tr.side, tr.entry, len(tr.legs))
	}
}

// legOf reports whether leg, an open leg of the stored orders, protects the
// position entry opened, the fill recover found: as the broker tells it (see
// fillPositions), when it knows both; else placed after it (see placedAfter),
// never one of an older entry, eg: of a position closed by its stop loss whose
// exit the broker hasn't booked yet, its legs open meanwhile. Any without an
// entry stored, eg: a pending entry filled while the bot was off, its fill
// not stored yet.
func (t *Trader) legOf(leg model.Order, entry *model.Order) bool {
	if entry == nil {
		return true
	}
	if positions, ok := t.reporter.(fillPositions); ok {
		opened, _, known := positions.FillPositions(*entry)
		_, protected, legKnown := positions.FillPositions(leg)
		if known && opened != 0 && legKnown {
			return slices.Contains(protected, opened)
		}
	}
	return placedAfter(leg, *entry)
}

// reply is the answer of the model to a context, see request: its decision or
// its error, when it was asked, and how long it took.
type reply struct {
	decision Decision
	err      error
	at       time.Time
	latency  time.Duration
}

// request asks the provider for the decision of input with the instructions of
// st, asked at at, bounded by the timeout. It touches no state of the Trader:
// it may run apart, see startApart.
func (t *Trader) request(st *promptState, input Input, at time.Time) reply {
	base := t.quit
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, t.cfg.Timeout)
	defer cancel()
	r := reply{at: at}
	start := time.Now()
	r.decision, r.err = t.ask(ctx, st, input)
	r.latency = time.Since(start).Round(time.Millisecond)
	return r
}

// Stop ends the requests to the model in progress, and fails the ones asked
// after at once, as the bot exits: its orders refused anyway (see
// xtrader.XTrader.Run), the candle in progress ends without waiting for the
// reply, and nothing is kept of those requests (see finish).
func (t *Trader) Stop() {
	if t.stop != nil {
		t.stop()
	}
}

// stopped reports whether Stop was called.
func (t *Trader) stopped() bool {
	return t.quit != nil && t.quit.Err() != nil
}

// conclude validates the reply r of the provider against the context of the
// analysis p, whatever the provider, as any Provider may be plugged in, and
// returns its decision with its record (see ui.Decision), its trigger telling
// why it was asked and its requestedBy the channel of the reanalysis it
// answers. A decision refused, or an answer failed, returns an error, with the
// record of the refusal, or of the failure, the justification of the decision
// refused included. A valid decision is remembered at the time of p, see
// memory.
func (t *Trader) conclude(p *prepared, r reply) (Decision, ui.Decision, error) {
	st, input, trigger, requestedBy := p.st, p.input, p.trigger, p.requestedBy
	decision, err, latency := r.decision, r.err, r.latency
	if err == nil {
		err = decision.Validate(input)
	}
	switch {
	case !input.drawings:
		decision.Drawings = nil // a provider plugged in may draw all the same
	case decision.Action != "":
		// drawn only once valid, whatever the decision: they only inform
		var dropped []string
		decision.Drawings, dropped = cleanDrawings(decision.Drawings, input)
		if len(dropped) > 0 {
			decision.Notes = append(decision.Notes, Note{Kind: "text",
				Text: "drawings left out: " + strings.Join(dropped, "; ")})
		}
	}
	kept := t.decisionRecord(st, input, trigger, requestedBy, r.at, latency, decision, err)
	t.record(st, input, latency, decision, err, kept.ID, trigger, requestedBy)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
			t.pause(t.now().Add(apiErr.RetryAfter))
		}
		log.Errorf("[LLM] %s %s | %s | %s | no decision: %v", input.Symbol, input.Timeframe, t.name, latency, err)
		return decision, kept, err
	}

	log.Infof("[LLM] %s %s %s | %s | %s | %s %.2f | %s", input.Symbol, input.Timeframe,
		input.Time.UTC().Format("15:04"), t.name, latency, decision.Action, decision.Confidence, decision.Reason)
	t.remember(input.Symbol, p.at, decision)
	return decision, kept, nil
}

// ask asks the provider for the decision of the context with the instructions
// of st, its own when st has none.
func (t *Trader) ask(ctx context.Context, st *promptState, input Input) (Decision, error) {
	if provider, ok := t.provider.(SystemPromptProvider); ok && st.system != "" {
		return provider.AnalyzeWith(ctx, st.system, input)
	}
	return t.provider.Analyze(ctx, input)
}

// apply hands the decision to the broker, at the market of df, and returns the
// orders it placed or moved with its outcome, and its detail: why it was
// refused, or the rule waived to apply it. WAIT and HOLD keep things as they
// are, and the decisions trading under MinConfidence are ignored, but the ones
// reducing the risk on a note of the trader (see reducesRisk), the minimum
// waived told in the detail.
func (t *Trader) apply(df *model.Dataframe, broker service.Broker, st *promptState, input Input, tr *trade,
	d Decision) ([]model.Order, string, string) {

	var waived string
	if d.Action.trades() && d.Confidence < t.cfg.MinConfidence {
		if input.ReanalysisNote == "" || !reducesRisk(input, d) {
			log.Infof("[LLM] %s %s | %s ignored: confidence %.2f under %.2f", df.Pair, t.cfg.Timeframe, d.Action,
				d.Confidence, t.cfg.MinConfidence)
			return nil, OutcomeIgnored, ""
		}
		log.Infof("[LLM] %s %s | %s asked by the trader: applied, its confidence %.2f under %.2f", df.Pair,
			t.cfg.Timeframe, d.Action, d.Confidence, t.cfg.MinConfidence)
		waived = fmt.Sprintf("the min confidence %.2f waived for this %s of confidence %.2f: it reduces the risk "+
			"of the position held, decided on a reanalysis with a note of the trader", t.cfg.MinConfidence, d.Action,
			d.Confidence)
	}

	refused := func(why string) ([]model.Order, string, string) {
		log.Infof("[LLM] %s %s | %s refused: %s", df.Pair, t.cfg.Timeframe, d.Action, why)
		return nil, OutcomeRefused, why
	}
	profit := 0.0 // none: the stop loss alone, see Config.TakeProfitOptional
	if d.TakeProfit != nil {
		profit = *d.TakeProfit
	}
	failed := func(err error) string { // the decision applied, the broker failing
		if err == nil {
			return waived
		}
		why := "the broker: " + err.Error()
		if waived != "" {
			why = waived + "; " + why
		}
		return why
	}
	var orders []model.Order
	switch d.Action {
	case ActionEnterLong, ActionEnterShort, ActionBuyLimit, ActionSellLimit, ActionBuyStop, ActionSellStop:
		// the entries of the agent blocked by the supervisor or the dashboard:
		// the broker would refuse the order anyway
		if why := entriesBlockedBy(broker); why != "" {
			return refused(why)
		}
		// the daily limits again, whatever the decision: a close booked since
		// the context was read
		now := t.dayClock(df)
		if _, limited := t.daily(df.Pair, st, now); limited != "" {
			return refused(limited)
		}
		// and the news block, at the time of now: the model may answer late
		if blocked := t.newsBlocked(df.Pair, now); blocked != "" {
			return refused("no entry around the release " + blocked)
		}
		// and the position and the price of now, last
		if why := t.recheck(df, broker, input, d); why != "" {
			return refused(why)
		}
		if d.Action.pendingKind() != "" {
			placed, err := t.place(df, broker, d.Action, float64(input.EntrySize), *d.Entry, *d.StopLoss, profit)
			return placed, OutcomeApplied, failed(err)
		}
		orders = t.open(df, broker, d.Action.side(), float64(input.EntrySize), *d.StopLoss, profit)
	case ActionModifyPending:
		p := t.pendingOf(df.Pair)
		if p == nil {
			return refused("the pending order isn't waiting anymore: filled or canceled since the context was read")
		}
		if why := t.recheck(df, broker, input, d); why != "" {
			return refused(why)
		}
		moved, err := t.modifyPending(df, broker, p, *d.Entry, *d.StopLoss, profit)
		return moved, OutcomeApplied, failed(err)
	case ActionCancelPending:
		p := t.pendingOf(df.Pair)
		if p == nil {
			return nil, OutcomeApplied, "the pending order wasn't waiting anymore: filled or canceled since the " +
				"context was read"
		}
		ev := &pendingEvent{}
		if !t.cancelPending(df.Pair, broker, p, "decided by the model", ev) {
			return nil, OutcomeApplied, failed(errors.New(ev.notes[len(ev.notes)-1]))
		}
		return ev.orders, OutcomeApplied, waived
	case ActionExit:
		log.Infof("[LLM] %s %s | EXIT | close %f", df.Pair, t.cfg.Timeframe, df.Close.Last(0))
		if _, order := t.close(df, broker, tr); order != nil {
			orders = append(orders, *order)
		}
	case ActionModifySL, ActionModifyTP:
		level := d.TakeProfit
		if d.Action == ActionModifySL {
			level = d.StopLoss
		}
		if moved, ok := t.move(df, broker, tr, d.Action == ActionModifySL, *level); ok {
			orders = append(orders, moved)
		}
	}
	return orders, OutcomeApplied, waived
}

// quoter is a broker that reads the last price of a pair, eg: the order
// controller of the bot, the bid on MT5, as the candles.
type quoter interface {
	LastQuote(pair string) (float64, error)
}

// recheck returns why the entry d, decided on the context input, can't be
// applied now at the market of df, empty when it can. The model answers a
// while after the context was read (up to Config.Timeout; the decision of the
// start on a later tick), so the position is read again, an order of someone
// else (eg: the order ticket of the dashboard) may have opened one on the pair
// meanwhile, and the decision is validated again at the price of now (see
// priceNow), by the rules of Decision.Validate: its stop loss and take profit
// on their sides of the price, within their distances, with the risk:reward
// net of the spread.
func (t *Trader) recheck(df *model.Dataframe, broker service.Broker, input Input, d Decision) string {
	now, err := t.priceNow(df, broker, input)
	if err != nil {
		return "the price of now unread: " + err.Error()
	}
	asset, _, err := broker.Position(df.Pair)
	if err != nil {
		return "the position unread: " + err.Error()
	}
	if held := heldSide(worth(broker, df.Pair, asset, float64(now.Price)), t.cfg.MinPositionValue); held != "" {
		return fmt.Sprintf("a %s position of %g is held on the pair now, opened since the context was read",
			describeSide(held), math.Abs(asset))
	}
	if p := t.pendingOf(df.Pair); p != nil {
		if d.Action != ActionModifyPending {
			return p.describe() + " is waiting on the pair now, placed since the context was read"
		}
		now.PendingOrder = p.context(t, df, t.dayClock(df)) // its levels of now
	}
	if err := d.Validate(now); err != nil {
		return fmt.Sprintf("at the price of now, %g: %v", float64(now.Price), err)
	}
	return ""
}

// priceNow returns input at the price of now: the last close of df, the market
// of the decision applied, with the bid, the ask and the spread of its
// Metadata, and live, the last price of the broker when it reads one
// (quoter), the market of df moved to it, its spread kept.
func (t *Trader) priceNow(df *model.Dataframe, broker service.Broker, input Input) (Input, error) {
	price := df.Close.Last(0)
	bid, ask, spread := quotesOf(df)
	if q, ok := broker.(quoter); ok && t.cfg.Live {
		last, err := q.LastQuote(df.Pair)
		if err != nil {
			return input, err
		}
		if !known(last) || last <= 0 {
			return input, fmt.Errorf("no price of %s: %g", df.Pair, last)
		}
		bid, ask = bid+last-price, ask+last-price // NaN when unknown
		price = last
	}
	now := input
	now.Price, now.Bid, now.Ask, now.Spread = number(price), number(bid), number(ask), number(spread)
	return now, nil
}

// reducesRisk reports whether the decision reduces the risk of the position
// held of its context: EXIT closes it, and MODIFY_SL moves its stop loss toward
// the price; or the risk of its pending order: CANCEL_PENDING cancels it. Such
// a decision, asked by the trader in the note of a reanalysis, is applied
// whatever its confidence.
func reducesRisk(in Input, d Decision) bool {
	if d.Action == ActionCancelPending {
		return in.PendingOrder != nil
	}
	if in.Position == nil {
		return false
	}
	switch d.Action {
	case ActionExit:
		return true
	case ActionModifySL:
		current := float64(in.Position.StopLoss)
		return d.StopLoss != nil && known(current) && sideSign(in.Position.Side)*(*d.StopLoss-current) > 0
	}
	return false
}

// context returns the context of the decision on the closed candles of df, at
// closePrice, the price of now, the quotes of market, and why there is nothing
// to decide (see skip), none when there is: flat without any entry to open,
// eg: the balance too low for a position, or a broker that can't place the
// stop loss and take profit with the order. Flat with the pending order p
// waiting, the entries are the ones allowed as if without it, the size its
// own: see checkPending.
func (t *Trader) context(df, market *model.Dataframe, broker service.Broker, st *promptState, tr *trade,
	p *pendingOrder, asset float64, cash money, closePrice float64) (Input, skip) {

	nan := number(math.NaN())
	candles := t.candles(df)
	input := Input{
		Symbol:     df.Pair,
		Timeframe:  t.cfg.Timeframe,
		Price:      number(closePrice),
		Bid:        nan,
		Ask:        nan,
		Spread:     nan,
		Candles:    candles,
		Indicators: make(map[string][]number, len(t.series)),
		Summary:    t.summary(df, candles),
		EntrySize:  nan,
		Balance:    number(cash.free),
		point:      cash.point,
		Rules: Rules{
			MinConfidence:    number(t.cfg.MinConfidence),
			MaxEntryDistance: nan,
			MaxLevelDistance: nan,
			StopCanWiden:     t.cfg.WidenStop,
			ReasonLanguage:   t.cfg.Language,
			Setup:            t.setup(),
			MinRiskReward:    nan,
			MinStopDistance:  nan,

			TakeProfitOptional: st.takeProfitOptional,
			NoTakeProfit:       st.noTakeProfit,
		},
		drawings: st.drawings,
	}
	if len(st.pendingOrders) > 0 {
		input.Rules.PendingOrders = slices.Clone(st.pendingOrders)
		input.Rules.PendingExpiry = t.cfg.PendingExpiry
	}
	if t.cfg.MinRiskReward > 0 {
		input.Rules.MinRiskReward = number(t.cfg.MinRiskReward)
	}
	if len(df.Time) > 0 {
		input.Time = df.Time[len(df.Time)-1].UTC()
	}
	for _, s := range t.series {
		if st.shows(s.group) {
			input.Indicators[s.name] = lastNumbers(df.Metadata[s.key], t.cfg.History)
		}
	}
	for _, e := range st.extras {
		for _, s := range e.series {
			input.Indicators[s.name] = lastNumbers(df.Metadata[s.key], t.cfg.History)
		}
	}
	if atr := last(df, "llm_atr"); known(atr) && atr > 0 {
		if t.cfg.MaxEntryATR > 0 {
			input.Rules.MaxEntryDistance = number(t.cfg.MaxEntryATR * atr)
		}
		if t.cfg.MaxLevelATR > 0 {
			input.Rules.MaxLevelDistance = number(t.cfg.MaxLevelATR * atr)
		}
		if t.cfg.MinStopATR > 0 {
			input.Rules.MinStopDistance = number(t.cfg.MinStopATR * atr)
		}
	}
	t.quotes(market, &input)
	now := t.dayClock(df)
	blocked := t.calendar(df.Pair, now, &input)
	day, limited := t.daily(df.Pair, st, now)
	input.Daily = day
	input.Rules.EntriesBlockedByDailyLimit = limited

	_, canProtect := broker.(service.ProtectionBroker)
	if tr == nil {
		input.Rules.Actions = []Action{ActionWait}
		if !canProtect {
			log.Errorf("llm: the broker can't place the stop loss and take profit with the order")
			return input, skip{SkipProtection, "the broker can't place the stop loss and take profit with the order"}
		}
		if limited != "" {
			// only WAIT left until the next day: nothing to ask
			log.Infof("[LLM] %s %s | no entry: %s", df.Pair, t.cfg.Timeframe, limited)
			return input, skip{SkipDailyLimit, "no entry: " + limited}
		}
		if blocked != "" {
			// only WAIT left: nothing to ask
			log.Infof("[LLM] %s %s | no entry: %s", df.Pair, t.cfg.Timeframe, blocked)
			return input, skip{SkipNews, "no entry around the release " + blocked}
		}
		if p == nil {
			size, ok := t.size(df.Pair, cash, closePrice)
			if !ok {
				return input, skip{SkipSize, "no position fits the balance and the sizing"}
			}
			input.EntrySize = number(size)
		} else {
			input.EntrySize = number(p.entry.Quantity) // its funds reserved: the size its own
		}
		entries, why := t.entries(df, input, st)
		for _, entry := range entries {
			if opens(t.cfg.Direction, entry.side()) {
				input.Rules.Actions = append(input.Rules.Actions, entry)
			}
		}
		if len(input.Rules.Actions) == 1 {
			// only WAIT left: nothing to ask
			if why.code == "" {
				why = skip{SkipDirection, "no entry the direction of the positions allows"}
			} else {
				log.Infof("[LLM] %s %s | no entry: %s", df.Pair, t.cfg.Timeframe, why.detail)
			}
			return input, why
		}
		return input, skip{}
	}

	position := &Position{Side: tr.side, Size: number(math.Abs(asset)), Entry: nan, StopLoss: nan, TakeProfit: nan,
		PnL: nan, Candles: tr.bars}
	if tr.entry > 0 {
		position.Entry = number(tr.entry)
		position.PnL = number(sideSign(tr.side) * (closePrice - tr.entry) * math.Abs(asset) * cash.point)
	}
	position.InitialStop, position.RMultiple = nan, nan
	if tr.stop > 0 {
		position.InitialStop = number(tr.stop)
		if risk := sideSign(tr.side) * (tr.entry - tr.stop); tr.entry > 0 && risk > 0 {
			// closed at the bid for a long, at the ask for a short
			exit := closePrice
			if spread := float64(input.Spread); tr.side == model.SideTypeSell && known(spread) && spread > 0 {
				exit += spread
			}
			position.RMultiple = number(sideSign(tr.side) * (exit - tr.entry) / risk)
		}
	}
	input.Position = position
	input.Rules.Actions = []Action{ActionHold, ActionExit}
	refreshLegs(broker, df.Pair, tr)
	if i := tr.leg(true); i >= 0 {
		position.StopLoss = number(legLevel(tr.legs[i]))
		if canProtect {
			input.Rules.Actions = append(input.Rules.Actions, ActionModifySL)
		}
	}
	if i := tr.leg(false); i >= 0 {
		// eg: a position of another strategy, recovered: its take profit is
		// shown, and never moved without any
		position.TakeProfit = number(legLevel(tr.legs[i]))
		if canProtect && !st.noTakeProfit {
			input.Rules.Actions = append(input.Rules.Actions, ActionModifyTP)
		}
	}
	return input, skip{}
}

// setup returns the setup of the Trader, SetupFree when unset.
func (t *Trader) setup() string {
	if t.cfg.Setup == "" {
		return SetupFree
	}
	return t.cfg.Setup
}

// entries returns the entries the model may choose from when flat, at the
// market and, with the prompt st placing them, the pending orders of its
// types (see Config.PendingOrders), and why there is none: SetupTrendPullback
// leaves only the ones with the trend of Summary.Trend, and a spread wider
// than MaxSpreadATR none.
func (t *Trader) entries(df *model.Dataframe, input Input, st *promptState) ([]Action, skip) {
	spread, atr := float64(input.Spread), last(df, "llm_atr")
	if t.cfg.MaxSpreadATR > 0 && known(spread) && known(atr) && atr > 0 && spread > t.cfg.MaxSpreadATR*atr {
		return nil, skip{SkipSpread, fmt.Sprintf("the spread %g is wider than %g ATR (%g)", spread,
			t.cfg.MaxSpreadATR, atr)}
	}
	market := []Action{ActionEnterLong, ActionEnterShort}
	if t.setup() == SetupTrendPullback {
		switch input.Summary.Trend {
		case "up":
			market = []Action{ActionEnterLong}
		case "down":
			market = []Action{ActionEnterShort}
		default:
			return nil, skip{SkipSetup,
				"no trend for the trend pullback setup (the close and the EMAs not on the same side of the SMA)"}
		}
	}
	entries := slices.Clone(market)
	for _, kind := range st.pendingOrders {
		for _, entry := range market {
			entries = append(entries, pendingAction(entry.side(), kind))
		}
	}
	return entries, skip{}
}

// calendar adds the economic calendar to the context of the analysis at at
// (see dayClock: now, live, even on the candle in progress), when the Trader
// has one, and returns the release blocking new entries, if any, as
// Rules.EntriesBlockedBy tells it to the model.
func (t *Trader) calendar(pair string, at time.Time, input *Input) string {
	currencies := t.calendarCurrencies(pair)
	if len(currencies) == 0 {
		return "" // no calendar, or no currency of it, eg: a crypto pair quoted in USDT
	}

	blockBefore, blockAfter, _ := t.newsWindow()
	from := at.Add(-max(t.cfg.CalendarBack, blockAfter))
	to := at.Add(max(t.cfg.CalendarAhead, blockBefore))
	events, err := t.readCalendar(pair, currencies, from, to)
	if err != nil {
		input.Calendar = &CalendarContext{Available: false, Status: calendarStatus(err), Events: []calendarEvent{}}
		return ""
	}

	window := make([]Event, 0, len(events))
	for _, event := range events {
		if event.within(at.Add(-t.cfg.CalendarBack), at.Add(t.cfg.CalendarAhead)) {
			window = append(window, event)
		}
	}
	input.Calendar = calendarContext(window, at, t.cfg.CalendarImportance, calendarMaxEvents)

	blocked := t.newsBlock(events, at)
	input.Rules.EntriesBlockedBy = blocked
	return blocked
}

// newsBlocked returns the release of the calendar blocking new entries on pair
// at at, if any: the news block checked again when an entry is applied, the
// model answering after the context was read, eg: the decision of the start
// applied on a later tick. A calendar that can't be read blocks nothing, as
// in the context.
func (t *Trader) newsBlocked(pair string, at time.Time) string {
	currencies := t.calendarCurrencies(pair)
	before, after, _ := t.newsWindow()
	if len(currencies) == 0 || before == 0 && after == 0 {
		return ""
	}
	events, err := t.readCalendar(pair, currencies, at.Add(-after), at.Add(before))
	if err != nil {
		return ""
	}
	return t.newsBlock(events, at)
}

// nextNewsBlock returns when the window of the news block of the next release
// of pair after at starts, among the ones of the calendar up to
// Config.CalendarAhead after it: zero without one, without a news block, or
// with a calendar that can't be read. A pending order waiting is canceled
// then, see guardPending.
func (t *Trader) nextNewsBlock(pair string, at time.Time) time.Time {
	currencies := t.calendarCurrencies(pair)
	before, after, importance := t.newsWindow()
	if len(currencies) == 0 || before == 0 && after == 0 {
		return time.Time{}
	}
	events, err := t.readCalendar(pair, currencies, at, at.Add(max(t.cfg.CalendarAhead, before)))
	if err != nil {
		return time.Time{}
	}
	var next time.Time
	for _, event := range events {
		if event.Importance < importance || !event.Timed() {
			continue
		}
		if start := event.Time.Add(-before); start.After(at) && (next.IsZero() || start.Before(next)) {
			next = start
		}
	}
	return next
}

// newsBlock returns the release of events blocking new entries at at, if any,
// see Config.NewsBlockBefore.
func (t *Trader) newsBlock(events []Event, at time.Time) string {
	before, after, importance := t.newsWindow()
	if before == 0 && after == 0 {
		return ""
	}
	event, ok := newsBlock(events, at, before, after, importance)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s %s (%s importance) at %s UTC", event.Currency, event.Name, event.Importance,
		event.Time.UTC().Format("2006-01-02 15:04"))
}

// calendarCurrencies returns the currencies of the calendar of pair, none
// without a calendar.
func (t *Trader) calendarCurrencies(pair string) []string {
	if t.cfg.Calendar == nil {
		return nil
	}
	if len(t.cfg.CalendarCurrencies) > 0 {
		return t.cfg.CalendarCurrencies
	}
	return calendarCurrencies(exchange.SplitAssetQuote(pair))
}

// readCalendar reads the releases of currencies between from and to, a
// calendar that can't be read warned about every calendarWarnEvery.
func (t *Trader) readCalendar(pair string, currencies []string, from, to time.Time) ([]Event, error) {
	ctx, cancel := context.WithTimeout(context.Background(), calendarTimeout)
	defer cancel()
	events, err := t.cfg.Calendar.Events(ctx, currencies, from, to)
	// the news off is told once, as they are switched (see SetNews), never
	// as a calendar failing
	if err != nil && !errors.Is(err, ErrNewsDisabled) {
		if now := t.now(); now.Sub(t.calendarError) > calendarWarnEvery {
			t.calendarError = now
			log.Warnf("[LLM] %s %s | economic calendar not read (%s), entries not blocked by news: %v", pair,
				t.cfg.Timeframe, calendarStatus(err), err)
		}
	}
	return events, err
}

// The reads of the calendar: how long one may take, how often a calendar that
// can't be read is warned about, and how many releases the context holds.
const (
	calendarTimeout   = 10 * time.Second
	calendarWarnEvery = 15 * time.Minute
	calendarMaxEvents = 20
)

// candles returns the last candles of the dataframe, the window of the model.
func (t *Trader) candles(df *model.Dataframe) Candles {
	return window(df, t.cfg.Candles)
}

// window returns the last n candles of df.
func window(df *model.Dataframe, n int) Candles {
	start := max(len(df.Close)-n, 0)
	candles := make(Candles, 0, len(df.Close)-start)
	for i := start; i < len(df.Close); i++ {
		candle := model.Candle{Pair: df.Pair, Close: df.Close[i]}
		if i < len(df.Time) {
			candle.Time = df.Time[i]
		}
		if i < len(df.Open) {
			candle.Open = df.Open[i]
		}
		if i < len(df.High) {
			candle.High = df.High[i]
		}
		if i < len(df.Low) {
			candle.Low = df.Low[i]
		}
		if i < len(df.Volume) {
			candle.Volume = df.Volume[i]
		}
		candles = append(candles, candle)
	}
	return candles
}

// higherTrend applies Config.HigherTrendFilter and Config.HigherTrendRequired
// to the entries of input, flat: when all the higher timeframes of the context
// trend the same way, up or down, the entry against it is left out of the
// allowed actions; without a trend they agree on, the filter leaves them all
// and the requirement none. It returns why the model isn't to be asked (see
// skip), none when it is: not with WAIT left alone.
func (t *Trader) higherTrend(pair string, input *Input) skip {
	required := t.cfg.HigherTrendRequired
	if (!t.cfg.HigherTrendFilter && !required) || input.Position != nil {
		return skip{} // a position held: no filter
	}
	frames := input.HigherTimeframes
	var trend, why string
	if len(frames) == 0 || len(frames) < len(t.higher) {
		why = "the candles of the higher timeframes are missing"
	} else {
		trend = frames[0].Trend
		for _, frame := range frames[1:] {
			if frame.Trend != trend {
				trend, why = "", "the higher timeframes don't agree on a trend"
				break
			}
		}
		if why == "" && trend != "up" && trend != "down" {
			trend, why = "", "the trend of the higher timeframes is "+trend // mixed or unknown
		}
	}
	if trend == "" {
		if !required {
			return skip{} // no trend to filter with
		}
		why += ", and their trend is required for an entry"
		log.Infof("[LLM] %s %s | no entry: %s", pair, t.cfg.Timeframe, why)
		return skip{SkipHigherTrend, why}
	}

	against := model.SideTypeSell
	if trend == "down" {
		against = model.SideTypeBuy
	}
	input.Rules.HigherTrend = trend
	input.Rules.Actions = slices.DeleteFunc(slices.Clone(input.Rules.Actions), func(a Action) bool {
		return a.side() == against // the entries at the market and pending against the trend
	})
	if len(input.Rules.Actions) <= 1 {
		log.Infof("[LLM] %s %s | no entry: the higher timeframes trend %s, against the entries allowed", pair,
			t.cfg.Timeframe, trend)
		return skip{SkipAgainstTrend, "only WAIT is left, against the trend of the higher timeframes"}
	}
	return skip{}
}

// The volumes of Instrument.Volume.
const (
	tickVolume   = "tick volume: the count of the changes of the price in the candle, not the volume traded; compare it with its own average only"
	tradedVolume = "the volume traded in the candle, in units of the asset"
)

// enrich adds to the context of a request what the code computes: the price
// structure, the higher timeframes and the levels of the day and the week
// (with a feeder), the session, the instrument, the correlated markets and the
// memory at at, the time of the analysis. For the requests sent only: its
// reads are skipped when the model isn't asked.
func (t *Trader) enrich(df *model.Dataframe, input *Input, st *promptState, at time.Time) {
	if n := t.cfg.StructureCandles; n > 0 {
		candles := window(df, n)
		atr := nans(len(candles))
		if series, ok := df.Metadata["llm_atr"]; ok {
			atr = series[max(len(series)-len(candles), 0):]
		}
		input.Structure = computeStructure(candles, atr)
		if st.smc {
			input.SMC = computeSMC(candles, atr, float64(input.Price))
		}
	}
	if st.drawings {
		input.Drawings = t.chartDrawings(df.Pair)
	}
	closeAt := input.Time.Add(t.period) // the close of the candle just closed
	if t.feeder != nil {
		input.HigherTimeframes = t.frames(df.Pair, float64(input.Price), st.smc)
		input.Reference = t.reference(df, closeAt)
		input.Correlated = t.correlated(df)
	}
	input.Session = t.session(df, closeAt)

	size := float64(input.EntrySize)
	if input.Position != nil {
		size = float64(input.Position.Size)
	}
	_, quote := exchange.SplitAssetQuote(df.Pair)
	currency := t.accountCurrency()
	if currency == "" {
		currency = quote // the paper wallet, simulated in the quote
	}
	input.Instrument = &Instrument{Volume: tradedVolume, ValuePerPriceUnit: number(size * input.point),
		QuoteCurrency: quote, Currency: currency}
	if t.cfg.TickVolume {
		input.Instrument.Volume = tickVolume
	}
	if t.cfg.Memory > 0 {
		input.Memory = t.memory(df.Pair, at)
	}
	if !st.shows("ema") && !st.shows("sma") {
		input.Summary.Trend = "" // the strategy ignores the averages: no noise
	}
}

// summary sums the candles of the context up, see Summary.
func (t *Trader) summary(df *model.Dataframe, candles Candles) Summary {
	closePrice := df.Close.Last(0)
	nan := number(math.NaN())
	summary := Summary{Trend: "mixed", ATRPct: nan, VolumeRatio: nan, High: nan, Low: nan, ChangePct: nan}

	fast, slow, sma := last(df, "llm_ema_fast"), last(df, "llm_ema_slow"), last(df, "llm_sma")
	switch {
	case fast > slow && closePrice > sma:
		summary.Trend = "up"
	case fast < slow && closePrice < sma:
		summary.Trend = "down"
	}
	if atr := last(df, "llm_atr"); known(atr) && closePrice > 0 {
		summary.ATRPct = number(atr / closePrice * 100)
	}
	if average := last(df, "llm_volume_sma"); known(average) && average > 0 && len(candles) > 0 {
		summary.VolumeRatio = number(candles[len(candles)-1].Volume / average)
	}
	if len(candles) > 0 {
		high, low := candles[0].High, candles[0].Low
		for _, candle := range candles[1:] {
			high, low = math.Max(high, candle.High), math.Min(low, candle.Low)
		}
		summary.High, summary.Low = number(high), number(low)
		if first := candles[0].Close; first > 0 {
			summary.ChangePct = number((closePrice/first - 1) * 100)
		}
	}
	return summary
}

// quotes sets the bid, the ask and the spread of the context from the Metadata
// of the last candle, when the exchange sends them, eg: the ask and the spread
// of Exness, whose candles are bid prices, and the rest of its Metadata as
// market data.
func (t *Trader) quotes(df *model.Dataframe, input *Input) {
	bid, ask, spread := quotesOf(df)
	input.Bid, input.Ask, input.Spread = number(bid), number(ask), number(spread)

	for key := range df.Metadata {
		if strings.HasPrefix(key, "llm_") || key == "bid" || key == "ask" || key == "spread" {
			continue
		}
		if value := last(df, key); known(value) {
			if input.Market == nil {
				input.Market = make(map[string]number)
			}
			input.Market[key] = number(value)
		}
	}
}

// quotesOf returns the bid, the ask and the spread of the last candle of df,
// see quotes: NaN when unknown, the one missing of the three computed.
func quotesOf(df *model.Dataframe) (bid, ask, spread float64) {
	bid, ask, spread = last(df, "bid"), last(df, "ask"), last(df, "spread")
	switch {
	case known(bid) && known(ask) && !known(spread):
		spread = ask - bid
	case !known(bid) && known(ask) && known(spread):
		bid = ask - spread
	case known(bid) && !known(ask) && known(spread):
		ask = bid + spread
	}
	return bid, ask, spread
}

// open opens a position of side at the market with its stop loss and take
// profit at the broker, profit 0 for none, follows it once held, and returns
// the orders placed: the entry, then its legs.
func (t *Trader) open(df *model.Dataframe, broker service.Broker, side model.SideType, size, stop,
	profit float64) []model.Order {

	protected, ok := broker.(service.ProtectionBroker)
	if !ok {
		log.Errorf("llm: the broker can't place the stop loss and take profit with the order")
		return nil
	}
	closePrice := df.Close.Last(0)
	log.Infof("[LLM] %s %s | %s | close %f | size %g | stop %f | take profit %f", df.Pair, t.cfg.Timeframe, side,
		closePrice, size, stop, profit)

	orders, err := protected.CreateOrderMarketProtected(side, df.Pair, size, stop, profit)
	if err != nil {
		log.Error(err)
	}

	// an order reported as failed may have been filled even so, eg: a request
	// that timed out, so whatever position is held now is followed
	asset, _, err := broker.Position(df.Pair)
	if err != nil {
		log.Error(err)
		return orders
	}
	if heldSide(worth(broker, df.Pair, asset, closePrice), t.cfg.MinPositionValue) != side {
		return orders
	}
	tr := &trade{side: side, entry: closePrice, stop: stop, since: t.dayClock(df)}
	if len(orders) > 0 && orders[0].Price > 0 {
		tr.entry = orders[0].Price
	}
	if len(orders) > 1 {
		tr.legs = orders[1:]
	}
	t.trades[df.Pair] = tr
	return orders
}

// close closes the position of tr at the market, and reports whether the pair
// is flat now, with the order at the market, nil when none was placed. The
// position held is never left without its stop loss and take profit: where
// the order closes it with its legs in place (service.ProtectedCloser, eg:
// MT5), they stay until it is flat; elsewhere they reserve the position, eg:
// in the paper wallet, and the order would open the other side, so they are
// canceled first, and placed again at their levels when the close fails
// (service.PositionProtector). A close that fails, or fills a part only,
// leaves the trade exiting, its close retried on the next ticks, see
// retryClose.
func (t *Trader) close(df *model.Dataframe, broker service.Broker, tr *trade) (bool, *model.Order) {
	tr.exiting = true
	tr.attempts++
	tr.retryAt = t.now().Add(closeDelay(tr.attempts))
	if !closesProtected(broker) {
		t.unprotect(df.Pair, broker, tr)
	}

	// the position is read again: a leg filled meanwhile, eg: while its cancel
	// was in flight, may have closed it, and the order would open the other side
	asset, held, err := t.held(df, broker)
	switch {
	case err != nil:
		return t.settleClose(df, broker, tr, err), nil
	case held != tr.side:
		t.closed(df.Pair, broker, tr) // by a leg meanwhile, or by someone else
		return held == "", nil
	}

	order, err := broker.CreateOrderMarket(oppositeSide(tr.side), df.Pair, math.Abs(asset))
	flat := t.settleClose(df, broker, tr, err)
	if err != nil {
		return flat, nil
	}
	return flat, &order
}

// closeDelay returns the delay of the next attempt to close a position on the
// ticks, after attempts of it, see closeRetry.
func closeDelay(attempts int) time.Duration {
	delay := closeRetry
	for i := 1; i < attempts && delay < closeRetryMax; i++ {
		delay *= 2
	}
	return min(delay, closeRetryMax)
}

// retryExit retries the close of the position of df.Pair whose exit failed,
// once due, see retryClose.
func (t *Trader) retryExit(df *model.Dataframe, broker service.Broker) {
	if tr, ok := t.trades[df.Pair]; ok && tr.exiting {
		t.retryClose(df, broker, tr, false)
	}
}

// retryClose closes again the position of tr, whose close failed, at the price
// of df: at once when now, eg: at the close of a candle, else once its next
// attempt is due, the attempts on the ticks spaced out, see closeRetry. It
// reports whether the close was retried.
func (t *Trader) retryClose(df *model.Dataframe, broker service.Broker, tr *trade, now bool) bool {
	if !now && t.now().Before(tr.retryAt) {
		return false
	}
	log.Infof("[LLM] %s %s | RETRY | close %f", df.Pair, t.cfg.Timeframe, df.Close.Last(0))
	t.close(df, broker, tr)
	return true
}

// closesProtected reports whether a market order of broker closes a protected
// position with its legs in place, see service.ProtectedCloser.
func closesProtected(broker service.Broker) bool {
	closer, ok := broker.(service.ProtectedCloser)
	return ok && closer.ClosesProtected()
}

// held reads the position of df.Pair: the asset held, and its side at the last
// price of df, none for dust.
func (t *Trader) held(df *model.Dataframe, broker service.Broker) (float64, model.SideType, error) {
	asset, _, err := broker.Position(df.Pair)
	if err != nil {
		return 0, "", err
	}
	return asset, heldSide(worth(broker, df.Pair, asset, df.Close.Last(0)), t.cfg.MinPositionValue), nil
}

// unprotect cancels the legs of tr before its close, as they reserve its
// position, and keeps their levels to place them again with should the close
// fail, see protectAgain.
func (t *Trader) unprotect(pair string, broker service.Broker, tr *trade) {
	if len(tr.legs) == 0 {
		return
	}
	levels := tr.protection()
	for _, leg := range tr.legs {
		if err := broker.Cancel(leg); err != nil {
			log.Debugf("[LLM] %s | %s: %v", pair, leg.Type, err) // already filled or canceled
		}
	}
	tr.legs, tr.canceled = nil, &levels
}

// settleClose checks the position of tr once its order at the market is done,
// failed with failure, nil when filled, and reports whether the pair is flat
// now: the close may be done whatever the error says, eg: a request that
// timed out, and an order filled may close a part only. Still held, the
// position keeps its legs, or gets them back at their levels when they were
// canceled for the close, and its close is retried on the next ticks: the
// first failure is notified, and the position left without its legs.
func (t *Trader) settleClose(df *model.Dataframe, broker service.Broker, tr *trade, failure error) bool {
	pair := df.Pair
	asset, held, err := t.held(df, broker)
	if err == nil && held != tr.side {
		t.closed(pair, broker, tr)
		return held == ""
	}

	switch {
	case failure != nil:
	case err != nil:
		failure = fmt.Errorf("the order filled, the position unread: %w", err)
	default:
		failure = fmt.Errorf("the order filled a part of the position only, %g left", math.Abs(asset))
	}
	tr.failed++
	log.Errorf("[LLM] %s %s | the close failed: %v: retried on the next ticks, from %s", pair, t.cfg.Timeframe,
		failure, tr.retryAt.UTC().Format(time.TimeOnly))

	var bare error // why the position is left without its legs
	var state string
	switch {
	case tr.canceled != nil && err != nil:
		bare = fmt.Errorf("the position: %w", err)
	case tr.canceled != nil:
		levels := *tr.canceled
		if bare = t.protectAgain(pair, broker, tr, math.Abs(asset)); bare == nil {
			state = fmt.Sprintf("its %s placed again", levels)
		}
	case len(tr.legs) > 0:
		state = fmt.Sprintf("it keeps its %s", tr.protection())
	default:
		state = "it has no stop loss nor take profit known to the bot"
	}

	message := fmt.Sprintf("EXIT of %s %s failed: %v", pair, t.cfg.Timeframe, failure)
	if bare != nil {
		log.Errorf("[LLM] %s %s | the position is left without its %s: %v", pair, t.cfg.Timeframe,
			*tr.canceled, bare)
		if !tr.bare {
			tr.bare = true
			t.tell(fmt.Sprintf("🛑 %s. The position is left WITHOUT its %s: %v. Its close is retried on the "+
				"next ticks.", message, *tr.canceled, bare))
		}
		return false
	}
	if tr.failed == 1 || tr.bare {
		t.tell(fmt.Sprintf("⚠️ %s. The position is held, %s. Its close is retried on the next ticks.", message,
			state))
	}
	tr.bare = false
	return false
}

// protectAgain places the legs of tr canceled for its close again, at their
// levels, on the size units held: the close failed.
func (t *Trader) protectAgain(pair string, broker service.Broker, tr *trade, size float64) error {
	protector, ok := broker.(service.PositionProtector)
	if !ok {
		return errors.New("the broker can't protect a position held")
	}
	levels := *tr.canceled
	legs, err := protector.Protect(tr.side, pair, size, levels.stop, levels.profit)
	if len(legs) == 0 {
		if err == nil {
			err = errors.New("no leg placed")
		}
		return err
	}
	if err != nil {
		log.Errorf("[LLM] %s %s | %v", pair, t.cfg.Timeframe, err) // placed, a part of them unstored
	}
	log.Warnf("[LLM] %s %s | the %s placed again on the %s position of %g", pair, t.cfg.Timeframe, levels,
		tr.side, size)
	tr.legs, tr.canceled = legs, nil
	return nil
}

// closed ends tr, its position closed: the legs a close leaves in place
// (service.ProtectedCloser) are canceled, so that none outlives it, eg: gone
// with the position already at MT5.
func (t *Trader) closed(pair string, broker service.Broker, tr *trade) {
	for _, leg := range tr.legs {
		if err := broker.Cancel(leg); err != nil {
			log.Debugf("[LLM] %s | %s: %v", pair, leg.Type, err) // gone with the position
		}
	}
	tr.legs = nil
	delete(t.trades, pair)
	if tr.failed > 0 {
		t.tell(fmt.Sprintf("✅ EXIT of %s %s: the position is closed, after %d failed attempts.", pair,
			t.cfg.Timeframe, tr.failed))
	}
}

// tell logs a message for the trader and notifies it, see SetNotifier.
func (t *Trader) tell(message string) {
	log.Infof("[LLM] %s", message)
	if notify := t.notifier(); notify != nil {
		notify(message)
	}
}

// move moves the stop loss leg of tr to level, or its take profit leg, and
// returns the leg moved, false when none was.
func (t *Trader) move(df *model.Dataframe, broker service.Broker, tr *trade, stop bool,
	level float64) (model.Order, bool) {
	name := "take profit"
	if stop {
		name = "stop loss"
	}
	i := tr.leg(stop)
	protected, ok := broker.(service.ProtectionBroker)
	if i < 0 || !ok {
		log.Errorf("llm: %s: no %s to move", df.Pair, name)
		return model.Order{}, false
	}
	current := legLevel(tr.legs[i])
	if level == current {
		return model.Order{}, false // unchanged: no request
	}

	log.Infof("[LLM] %s %s | %s %f -> %f | close %f", df.Pair, t.cfg.Timeframe, name, current, level,
		df.Close.Last(0))
	moved, err := protected.UpdateProtection(tr.legs[i], level)
	var tighter *service.StopNotTighterError
	if errors.As(err, &tighter) {
		// the stop guard kept a more protective stop: followed as it is, the
		// decision not applied
		tr.legs[i] = tighter.Leg
		log.Infof("[LLM] %s %s | %s kept at %f, more protective than %f", df.Pair, t.cfg.Timeframe, name,
			tighter.Current, level)
		return model.Order{}, false
	}
	if err != nil {
		log.Error(err)
		return model.Order{}, false
	}
	tr.legs[i] = moved
	return moved, true
}

// money is the cash available for a new position on a pair and the point
// value of the pair: the value of a move of 1 of its price for 1 unit, both in
// the currency of the account (see service.PointValuer), in the quote in the
// paper wallet, its point value 1.
type money struct {
	free, point float64
}

// pointValue returns the point value of pair at broker, see money, an error
// when it isn't positive.
func pointValue(broker service.Broker, pair string) (float64, error) {
	point, err := service.PointValue(broker, pair)
	switch {
	case err != nil:
		return 0, fmt.Errorf("the point value: %w", err)
	case !known(point) || point <= 0:
		return 0, fmt.Errorf("no point value of %s, %g", pair, point)
	}
	return point, nil
}

// worth returns the value of asset units of pair at price in the currency of
// the account, to tell dust apart: by the point value of the pair, or 1 while
// it can't be read.
func worth(broker service.Broker, pair string, asset, price float64) float64 {
	point, err := pointValue(broker, pair)
	if err != nil {
		log.Debug(err)
		point = 1
	}
	return asset * price * point
}

// size returns the size of the position an entry opens on pair at price, with
// cash available, sized as the intraday strategies size theirs: PositionSize,
// or else PositionFraction of the cash times Leverage, in the currency of the
// account. ok is false when there is none: a share of the balance worth less
// than MinPositionValue, or a fixed size missing, worth less than
// MinPositionValue, it would be taken for dust, or more than the cash times the
// leverage, all the balance can take.
func (t *Trader) size(pair string, cash money, price float64) (float64, bool) {
	c := t.cfg
	if price <= 0 || !known(price) {
		return 0, false
	}
	if len(c.PositionSize) == 0 {
		value := cash.free * c.PositionFraction * c.Leverage
		return value / (price * cash.point), value >= c.MinPositionValue
	}

	size, ok := c.PositionSize[pair]
	value := size * price * cash.point
	switch {
	case !ok:
		log.Errorf("%s: no position size set for the pair", pair)
	case value < c.MinPositionValue:
		log.Errorf("%s: a position of %g is worth %f, under the min position value of %g: it would be taken "+
			"for dust", pair, size, value, c.MinPositionValue)
	case value > cash.free*c.Leverage:
		log.Errorf("%s: a position of %g is worth %f, more than the %f available times the leverage of %g",
			pair, size, value, cash.free, c.Leverage)
	default:
		return size, true
	}
	return 0, false
}

// protection returns the levels of the legs of the trade.
func (tr *trade) protection() protection {
	var p protection
	if i := tr.leg(true); i >= 0 {
		p.stop = legLevel(tr.legs[i])
	}
	if i := tr.leg(false); i >= 0 {
		p.profit = legLevel(tr.legs[i])
	}
	return p
}

func (p protection) String() string {
	switch {
	case p.stop > 0 && p.profit > 0:
		return fmt.Sprintf("stop loss %g and take profit %g", p.stop, p.profit)
	case p.stop > 0:
		return fmt.Sprintf("stop loss %g", p.stop)
	case p.profit > 0:
		return fmt.Sprintf("take profit %g", p.profit)
	}
	return "stop loss and take profit"
}

// leg returns the index of the stop loss leg of the trade, or of its take
// profit leg, -1 without it.
func (tr *trade) leg(stop bool) int {
	for i, leg := range tr.legs {
		if isStop := leg.Type == model.OrderTypeStopLoss || leg.Type == model.OrderTypeStopLossLimit; isStop == stop {
			return i
		}
	}
	return -1
}

// legLevel returns the level of a stop loss or take profit leg.
// stopGuard is implemented by the brokers telling whether the stop losses of
// the open positions of a pair only tighten, eg: order.Controller with the
// protection of the positions on (breakeven, trailing stop).
type stopGuard interface {
	StopGuarded(pair string) bool
}

// refreshLegs reads the legs of the trade tr on pair again from broker when
// its stop guard is on: the protection of the positions moves them apart from
// the Trader, which then sees the stop loss as it is, never an older one.
func refreshLegs(broker service.Broker, pair string, tr *trade) {
	guard, guarded := broker.(stopGuard)
	held, holds := broker.(service.PositionLegs)
	if !guarded || !holds || !guard.StopGuarded(pair) {
		return
	}
	if legs, _, err := held.HeldLegs(pair, tr.side); err == nil && len(legs) > 0 {
		tr.legs = legs
	}
}

func legLevel(leg model.Order) float64 {
	if leg.Stop != nil && (leg.Type == model.OrderTypeStopLoss || leg.Type == model.OrderTypeStopLossLimit) {
		return *leg.Stop
	}
	return leg.Price
}

// last returns the value of the last candle of the Metadata key, NaN without
// it.
func last(df *model.Dataframe, key string) float64 {
	values, ok := df.Metadata[key]
	if !ok || len(values) == 0 || len(values) != len(df.Close) {
		return math.NaN()
	}
	return values.Last(0)
}

// heldSide returns the side of a position worth value, negative for a short:
// none when it is worth less than minValue, dust.
func heldSide(value, minValue float64) model.SideType {
	switch {
	case value >= minValue:
		return model.SideTypeBuy
	case value <= -minValue:
		return model.SideTypeSell
	}
	return ""
}

// opens reports whether direction lets a position of side open.
func opens(direction strategies.RKDirection, side model.SideType) bool {
	switch direction {
	case strategies.RKDirectionBuyOnly:
		return side == model.SideTypeBuy
	case strategies.RKDirectionSellOnly:
		return side == model.SideTypeSell
	}
	return true
}

func oppositeSide(side model.SideType) model.SideType {
	if side == model.SideTypeSell {
		return model.SideTypeBuy
	}
	return model.SideTypeSell
}

// entriesBlocker is a broker whose entries may be blocked, eg: the order
// controller, see order.Controller.BlockEntries.
type entriesBlocker interface {
	EntriesBlocked() (blocked bool, reason string, revision int64)
}

// entriesBlockedBy returns why the entries of broker are blocked, "" when they
// aren't, or the broker doesn't tell.
func entriesBlockedBy(broker service.Broker) string {
	blocker, ok := broker.(entriesBlocker)
	if !ok {
		return ""
	}
	blocked, reason, _ := blocker.EntriesBlocked()
	if !blocked {
		return ""
	}
	if reason == "" {
		return "the entries of the agent are blocked"
	}
	return "the entries of the agent are blocked: " + reason
}
