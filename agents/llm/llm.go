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

	// Correlated are the markets correlated with the pairs, read with the
	// feeder on Timeframe, eg: USTECm, XAUUSDm and DXYm for BTCUSDm on MT5;
	// each pair leaves itself out.
	Correlated []string

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
	// from CalendarBack before the close of the candle to CalendarAhead after
	// it. New entries are refused from NewsBlockBefore before a release of at
	// least NewsBlockImportance to NewsBlockAfter after it, whatever the model
	// answers (both 0: never). A calendar that can't be read blocks nothing:
	// the context tells the model it is unavailable.
	Calendar            Calendar
	CalendarCurrencies  []string
	CalendarAhead       time.Duration
	CalendarBack        time.Duration
	CalendarImportance  Importance
	NewsBlockBefore     time.Duration
	NewsBlockAfter      time.Duration
	NewsBlockImportance Importance
}

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
	history  map[string]int       // the candles of history skipped in a row, by pair, see Config.Live

	orders func(pair string) ([]model.Order, error) // the stored orders of a pair, see SetOrderSource

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

	now func() time.Time // the wall clock of the rate limits

	reMtx   sync.Mutex
	paused  time.Time              // no request before it: the Retry-After of a rate limit
	pending map[string]*reanalysis // the reanalyses asked, waiting for an analysis, by pair, see Reanalyze
	running map[string]*reanalysis // the pairs analyzed now, with the reanalysis the analysis answers, nil for none

	decisionsMtx sync.RWMutex
	decisions    DecisionStore       // of the decisions with their justification, see SetDecisionStore
	listeners    []func(ui.Decision) // see ListenDecisions
	notify       func(string)        // see SetNotifier

	period        time.Duration // of the candles, to know their close
	calendarError time.Time     // last warning of a calendar that couldn't be read
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

// trade is a position of the Trader: its side, its entry, 0 when unknown, the
// candles closed since, the legs of its stop loss and take profit at the
// broker, and whether its exit was decided, retried on each candle once its
// legs are canceled.
type trade struct {
	side    model.SideType
	entry   float64
	stop    float64 // the stop loss placed with the entry, the unit of the risk; 0 when unknown
	bars    int
	legs    []model.Order
	exiting bool
}

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

	t := &Trader{
		period:    period,
		decisions: NewMemoryDecisions(0),
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
		trades:  make(map[string]*trade),
		handled: make(map[string]time.Time),
		history: make(map[string]int),
		levels:  make(map[string][]Level),
		now:     time.Now,

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

func (t *Trader) OnCandle(df *model.Dataframe, broker service.Broker) {
	t.onClose(df, broker, TriggerCandle)
}

// onClose asks the model about the candle just closed of df, once per candle,
// trigger telling why: at its close, or on the first candle after the start.
func (t *Trader) onClose(df *model.Dataframe, broker service.Broker, trigger string) {
	if len(df.Close) == 0 {
		return
	}
	if len(df.Time) > 0 {
		// a candle handled already, handed again: never asked twice, as each
		// request is billed
		at := df.Time[len(df.Time)-1]
		if last, ok := t.handled[df.Pair]; ok && !at.After(last) {
			log.Warnf("[LLM] %s %s | the candle of %s again, after the one of %s: skipped", df.Pair,
				t.cfg.Timeframe, at.UTC().Format(time.DateTime), last.UTC().Format(time.DateTime))
			return
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
				return
			}
			if skipped := t.history[df.Pair]; skipped > 0 {
				log.Infof("[LLM] %s %s | live again, after %d candles of history not asked", df.Pair,
					t.cfg.Timeframe, skipped)
				delete(t.history, df.Pair)
			}
		}
	}
	t.analyze(df, broker, analysis{trigger: trigger, market: df})
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
	st := t.refreshPrompt(df) // the analysis goes on with it, whatever the switches meanwhile
	requested := t.beginAnalysis(df.Pair)
	result := ""
	defer func() { t.endAnalysis(df.Pair, result) }()

	price := run.market.Close.Last(0)
	asset, quote, err := broker.Position(df.Pair)
	if err != nil {
		log.Error(err)
		result = "the model wasn't asked: the position: " + err.Error()
		return
	}

	trade := t.follow(df.Pair, heldSide(asset*price, t.cfg.MinPositionValue), run.trigger != TriggerReanalysis)
	if trade != nil && trade.exiting {
		// a close that failed, the position left without its legs
		log.Infof("[LLM] %s %s | RETRY | close %f", df.Pair, t.cfg.Timeframe, price)
		t.close(run.market, broker, trade)
		result = "the model wasn't asked: a close that failed was retried first"
		return
	}

	input, why := t.context(df, run.market, broker, st, trade, asset, quote, price)
	if why != "" {
		result = "the model wasn't asked: " + why
		return // flat without any entry to open: nothing to ask
	}
	if now, until := t.now(), t.pausedUntil(); now.Before(until) {
		log.Debugf("[LLM] %s %s | %s | rate limited for %s", df.Pair, t.cfg.Timeframe, t.name,
			until.Sub(now).Round(time.Second))
		result = fmt.Sprintf("the model wasn't asked: %s is rate limited for %s", t.name,
			until.Sub(now).Round(time.Second))
		return
	}

	t.enrich(df, &input, st)
	if requested != nil {
		input.ReanalysisNote = requested.note
	}
	if run.inProgress {
		last := len(run.market.Close) - 1
		input.Reanalysis = true
		input.CandleInProgress = &CandleInProgress{Open: number(run.market.Open[last]),
			High: number(run.market.High[last]), Low: number(run.market.Low[last]), Close: number(price),
			Volume: number(run.market.Volume[last])}
		if last < len(run.market.Time) {
			input.CandleInProgress.Time = run.market.Time[last].UTC()
		}
	}
	if why := t.higherTrend(df.Pair, &input); why != "" {
		result = "the model wasn't asked: " + why
		return // only WAIT left: nothing to ask
	}

	requestedBy := ""
	if requested != nil {
		requestedBy = string(requested.channel)
	}
	decision, kept, err := t.decide(st, input, run.trigger, requestedBy)
	if err != nil {
		t.keep(kept, decision.Attachments)
		result = describeDecision(kept)
		return // no decision, no trade
	}
	t.levelsMtx.Lock()
	t.levels[df.Pair] = decision.Levels
	t.levelsMtx.Unlock()
	orders, outcome := t.apply(run.market, broker, input, trade, decision)
	kept.Outcome = outcome
	for _, order := range orders {
		if order.ID > 0 { // numbered by the order controller of the bot, the broker of the strategies
			kept.OrderIDs = append(kept.OrderIDs, order.ID)
		}
	}
	t.keep(kept, decision.Attachments)
	result = describeDecision(kept)
}

// OnPartialCandle asks the model on the candles in progress, live only: once
// on the first one after the start, about the candle closed before it, the
// decision (and the levels on the chart) of the start rather than at the next
// close, a whole period later; and on the one after a reanalysis is asked, at
// its price, see Reanalyze. The candles in progress are otherwise left to their
// close.
func (t *Trader) OnPartialCandle(df *model.Dataframe, broker service.Broker) {
	if !t.cfg.Live || len(df.Close) < 2 {
		return
	}
	if _, started := t.handled[df.Pair]; !started {
		t.onClose(closed(df), broker, TriggerStart)
		return
	}
	if t.reanalysisPending(df.Pair) {
		t.analyze(closed(df), broker, analysis{trigger: TriggerReanalysis, market: df, inProgress: true})
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

// follow returns the position followed on the pair once held is the side of
// the one held, none when flat, counting a candle held with bar.
func (t *Trader) follow(pair string, held model.SideType, bar bool) *trade {
	if held == "" {
		delete(t.trades, pair) // its stop loss or take profit closed it, if any
		return nil
	}

	tr, ok := t.trades[pair]
	if !ok || tr.side != held {
		// a position opened before a restart, or by someone else: followed
		// from this candle on, with its entry and its legs when they are
		// stored, see SetOrderSource
		tr = &trade{side: held}
		t.recover(pair, tr)
		t.trades[pair] = tr
	}
	if bar {
		tr.bars++
	}
	return tr
}

// SetOrderSource sets where the Trader finds the stored orders of a pair, eg:
// the storage of the bot, before it runs: a position found at the start,
// opened before a restart, is followed with its entry and the legs of its
// stop loss and take profit, which it may move, rather than without them.
func (t *Trader) SetOrderSource(orders func(pair string) ([]model.Order, error)) {
	t.orders = orders
}

// recover sets the entry and the legs of tr, a position found held, from the
// stored orders of pair: its last fill on its side, and its open legs (grouped
// orders on the other side), the last stop loss and take profit. The initial
// stop, unknown, is taken as the stop loss of now.
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
		switch {
		case order.Side == tr.side && order.Status == model.OrderStatusTypeFilled && order.GroupID == nil:
			if entry == nil || order.ID > entry.ID {
				entry = order
			}
		case order.Side != tr.side && order.GroupID != nil && (order.Status == model.OrderStatusTypeNew ||
			order.Status == model.OrderStatusTypePartiallyFilled):
			isStop := order.Type == model.OrderTypeStopLoss || order.Type == model.OrderTypeStopLossLimit
			if isStop && (stop == nil || order.ID > stop.ID) {
				stop = order
			} else if !isStop && (profit == nil || order.ID > profit.ID) {
				profit = order
			}
		}
	}
	if entry != nil && entry.Price > 0 {
		tr.entry = entry.Price
		if t.period > 0 && !entry.CreatedAt.IsZero() {
			tr.bars = max(int(t.now().Sub(entry.CreatedAt)/t.period)-1, 0)
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

// decide asks the provider for the decision of the context, validated against
// it whatever the provider, as any Provider may be plugged in, and returns it
// with its record (see ui.Decision), trigger telling why it was asked and
// requestedBy the channel of the reanalysis it answers. A decision refused, or
// an answer failed, returns an error, with the record of the refusal, or of the
// failure, the justification of the decision refused included.
func (t *Trader) decide(st *promptState, input Input, trigger, requestedBy string) (Decision, ui.Decision, error) {
	ctx, cancel := context.WithTimeout(context.Background(), t.cfg.Timeout)
	defer cancel()

	at := t.now()
	start := time.Now()
	decision, err := t.ask(ctx, st, input)
	if err == nil {
		err = decision.Validate(input)
	}
	latency := time.Since(start).Round(time.Millisecond)
	kept := t.decisionRecord(st, input, trigger, requestedBy, at, latency, decision, err)
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
	t.remember(input.Symbol, input.Time, decision)
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

// apply hands the decision to the broker, and returns the orders it placed or
// moved with its outcome: WAIT and HOLD keep things as they are, and the
// decisions trading under MinConfidence are ignored.
func (t *Trader) apply(df *model.Dataframe, broker service.Broker, input Input, tr *trade,
	d Decision) ([]model.Order, string) {

	if d.Action.trades() && d.Confidence < t.cfg.MinConfidence {
		if input.ReanalysisNote == "" || !reducesRisk(input, d) {
			log.Infof("[LLM] %s %s | %s ignored: confidence %.2f under %.2f", df.Pair, t.cfg.Timeframe, d.Action,
				d.Confidence, t.cfg.MinConfidence)
			return nil, OutcomeIgnored
		}
		log.Infof("[LLM] %s %s | %s asked by the trader: applied, its confidence %.2f under %.2f", df.Pair,
			t.cfg.Timeframe, d.Action, d.Confidence, t.cfg.MinConfidence)
	}

	var orders []model.Order
	switch d.Action {
	case ActionEnterLong, ActionEnterShort:
		profit := 0.0 // none: the stop loss alone, see Config.TakeProfitOptional
		if d.TakeProfit != nil {
			profit = *d.TakeProfit
		}
		orders = t.open(df, broker, d.Action.side(), float64(input.EntrySize), *d.StopLoss, profit)
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
	return orders, OutcomeApplied
}

// reducesRisk reports whether the decision reduces the risk of the position
// held of its context: EXIT closes it, and MODIFY_SL moves its stop loss toward
// the price. Such a decision, asked by the trader in the note of a
// reanalysis, is applied whatever its confidence.
func reducesRisk(in Input, d Decision) bool {
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
// to decide, empty when there is: flat without any entry to open, eg: the
// balance too low for a position, or a broker that can't place the stop loss
// and take profit with the order.
func (t *Trader) context(df, market *model.Dataframe, broker service.Broker, st *promptState, tr *trade, asset,
	quote, closePrice float64) (Input, string) {

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
		Balance:    number(quote),
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
	blocked := t.calendar(df.Pair, &input)

	_, canProtect := broker.(service.ProtectionBroker)
	if tr == nil {
		input.Rules.Actions = []Action{ActionWait}
		if !canProtect {
			log.Errorf("llm: the broker can't place the stop loss and take profit with the order")
			return input, "the broker can't place the stop loss and take profit with the order"
		}
		if blocked != "" {
			// only WAIT left: nothing to ask
			log.Infof("[LLM] %s %s | no entry: %s", df.Pair, t.cfg.Timeframe, blocked)
			return input, "no entry around the release " + blocked
		}
		size, ok := t.size(df.Pair, quote, closePrice)
		if !ok {
			return input, "no position fits the balance and the sizing"
		}
		input.EntrySize = number(size)
		entries, why := t.entries(df, input)
		for _, entry := range entries {
			if opens(t.cfg.Direction, entry.side()) {
				input.Rules.Actions = append(input.Rules.Actions, entry)
			}
		}
		if len(input.Rules.Actions) == 1 {
			// only WAIT left: nothing to ask
			if why == "" {
				why = "no entry the direction of the positions allows"
			} else {
				log.Infof("[LLM] %s %s | no entry: %s", df.Pair, t.cfg.Timeframe, why)
			}
			return input, why
		}
		return input, ""
	}

	position := &Position{Side: tr.side, Size: number(math.Abs(asset)), Entry: nan, StopLoss: nan, TakeProfit: nan,
		PnL: nan, Candles: tr.bars}
	if tr.entry > 0 {
		position.Entry = number(tr.entry)
		position.PnL = number(sideSign(tr.side) * (closePrice - tr.entry) * math.Abs(asset))
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
	return input, ""
}

// setup returns the setup of the Trader, SetupFree when unset.
func (t *Trader) setup() string {
	if t.cfg.Setup == "" {
		return SetupFree
	}
	return t.cfg.Setup
}

// entries returns the entries the model may choose from when flat, and why
// there is none: SetupTrendPullback leaves only the one with the trend of
// Summary.Trend, and a spread wider than MaxSpreadATR none.
func (t *Trader) entries(df *model.Dataframe, input Input) ([]Action, string) {
	spread, atr := float64(input.Spread), last(df, "llm_atr")
	if t.cfg.MaxSpreadATR > 0 && known(spread) && known(atr) && atr > 0 && spread > t.cfg.MaxSpreadATR*atr {
		return nil, fmt.Sprintf("the spread %g is wider than %g ATR (%g)", spread, t.cfg.MaxSpreadATR, atr)
	}
	if t.setup() != SetupTrendPullback {
		return []Action{ActionEnterLong, ActionEnterShort}, ""
	}
	switch input.Summary.Trend {
	case "up":
		return []Action{ActionEnterLong}, ""
	case "down":
		return []Action{ActionEnterShort}, ""
	}
	return nil, "no trend for the trend pullback setup (the close and the EMAs not on the same side of the SMA)"
}

// calendar adds the economic calendar to the context of the candle, when the
// Trader has one, and returns the release blocking new entries, if any, as
// Rules.EntriesBlockedBy tells it to the model.
func (t *Trader) calendar(pair string, input *Input) string {
	if t.cfg.Calendar == nil {
		return ""
	}
	currencies := t.cfg.CalendarCurrencies
	if len(currencies) == 0 {
		currencies = calendarCurrencies(exchange.SplitAssetQuote(pair))
	}
	if len(currencies) == 0 {
		return "" // no currency of the calendar, eg: a crypto pair quoted in USDT
	}

	at := input.Time.Add(t.period) // the close of the candle just closed
	from := at.Add(-max(t.cfg.CalendarBack, t.cfg.NewsBlockAfter))
	to := at.Add(max(t.cfg.CalendarAhead, t.cfg.NewsBlockBefore))
	ctx, cancel := context.WithTimeout(context.Background(), calendarTimeout)
	defer cancel()
	events, err := t.cfg.Calendar.Events(ctx, currencies, from, to)
	if err != nil {
		input.Calendar = &CalendarContext{Available: false, Events: []calendarEvent{}}
		if now := t.now(); now.Sub(t.calendarError) > calendarWarnEvery {
			t.calendarError = now
			log.Warnf("[LLM] %s %s | economic calendar unavailable, entries not blocked by news: %v", pair,
				t.cfg.Timeframe, err)
		}
		return ""
	}

	window := make([]Event, 0, len(events))
	for _, event := range events {
		if !event.Time.Before(at.Add(-t.cfg.CalendarBack)) && !event.Time.After(at.Add(t.cfg.CalendarAhead)) {
			window = append(window, event)
		}
	}
	input.Calendar = calendarContext(window, at, t.cfg.CalendarImportance, calendarMaxEvents)

	if t.cfg.NewsBlockBefore == 0 && t.cfg.NewsBlockAfter == 0 {
		return ""
	}
	event, ok := newsBlock(events, at, t.cfg.NewsBlockBefore, t.cfg.NewsBlockAfter, t.cfg.NewsBlockImportance)
	if !ok {
		return ""
	}
	blocked := fmt.Sprintf("%s %s (%s importance) at %s UTC", event.Currency, event.Name, event.Importance,
		event.Time.UTC().Format("2006-01-02 15:04"))
	input.Rules.EntriesBlockedBy = blocked
	return blocked
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
// and the requirement none. It returns why the model isn't to be asked, empty
// when it is: not with WAIT left alone.
func (t *Trader) higherTrend(pair string, input *Input) string {
	required := t.cfg.HigherTrendRequired
	if (!t.cfg.HigherTrendFilter && !required) || input.Position != nil {
		return "" // a position held: no filter
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
			return "" // no trend to filter with
		}
		why += ", and their trend is required for an entry"
		log.Infof("[LLM] %s %s | no entry: %s", pair, t.cfg.Timeframe, why)
		return why
	}

	against := ActionEnterShort
	if trend == "down" {
		against = ActionEnterLong
	}
	input.Rules.HigherTrend = trend
	input.Rules.Actions = slices.DeleteFunc(slices.Clone(input.Rules.Actions), func(a Action) bool {
		return a == against
	})
	if len(input.Rules.Actions) <= 1 {
		log.Infof("[LLM] %s %s | no entry: the higher timeframes trend %s, against the entries allowed", pair,
			t.cfg.Timeframe, trend)
		return "only WAIT is left, against the trend of the higher timeframes"
	}
	return ""
}

// The volumes of Instrument.Volume.
const (
	tickVolume   = "tick volume: the count of the changes of the price in the candle, not the volume traded; compare it with its own average only"
	tradedVolume = "the volume traded in the candle, in units of the asset"
)

// enrich adds to the context of a request what the code computes: the price
// structure, the higher timeframes and the levels of the day and the week
// (with a feeder), the session, the instrument, the correlated markets and the
// memory. For the requests sent only: its reads are skipped when the model
// isn't asked.
func (t *Trader) enrich(df *model.Dataframe, input *Input, st *promptState) {
	if n := t.cfg.StructureCandles; n > 0 {
		candles := window(df, n)
		atr := nans(len(candles))
		if series, ok := df.Metadata["llm_atr"]; ok {
			atr = series[max(len(series)-len(candles), 0):]
		}
		input.Structure = computeStructure(candles, atr)
	}
	closeAt := input.Time.Add(t.period) // the close of the candle just closed
	if t.feeder != nil {
		input.HigherTimeframes = t.frames(df.Pair, float64(input.Price))
		input.Reference = t.reference(df, closeAt)
		input.Correlated = t.correlated(df)
	}
	input.Session = t.session(df, closeAt)

	size := float64(input.EntrySize)
	if input.Position != nil {
		size = float64(input.Position.Size)
	}
	_, quote := exchange.SplitAssetQuote(df.Pair)
	input.Instrument = &Instrument{Volume: tradedVolume, ValuePerPriceUnit: number(size), QuoteCurrency: quote}
	if t.cfg.TickVolume {
		input.Instrument.Volume = tickVolume
	}
	if t.cfg.Memory > 0 {
		input.Memory = t.memory(df.Pair, input.Time)
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
	bid, ask, spread := last(df, "bid"), last(df, "ask"), last(df, "spread")
	switch {
	case known(bid) && known(ask) && !known(spread):
		spread = ask - bid
	case !known(bid) && known(ask) && known(spread):
		bid = ask - spread
	case known(bid) && !known(ask) && known(spread):
		ask = bid + spread
	}
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
	if heldSide(asset*closePrice, t.cfg.MinPositionValue) != side {
		return orders
	}
	tr := &trade{side: side, entry: closePrice, stop: stop}
	if len(orders) > 0 && orders[0].Price > 0 {
		tr.entry = orders[0].Price
	}
	if len(orders) > 1 {
		tr.legs = orders[1:]
	}
	t.trades[df.Pair] = tr
	return orders
}

// close closes the position of tr at the market once its legs are canceled,
// and reports whether the pair is flat now, with the order at the market, nil
// when none was placed. The legs reserve the position, so
// the close would otherwise open the other side, eg: in the paper wallet. A
// close that fails leaves the position without them, so the trade is marked as
// exiting and the close retried on the next candles.
func (t *Trader) close(df *model.Dataframe, broker service.Broker, tr *trade) (bool, *model.Order) {
	tr.exiting = true
	for _, leg := range tr.legs {
		if err := broker.Cancel(leg); err != nil {
			log.Debugf("[LLM] %s | %s: %v", df.Pair, leg.Type, err) // already filled or canceled
		}
	}
	tr.legs = nil

	// the position is read again: a leg filled while its cancel was in flight
	// may have closed it meanwhile, and the order would open the other side
	asset, _, err := broker.Position(df.Pair)
	if err != nil {
		log.Error(err)
		return false, nil
	}
	if held := heldSide(asset*df.Close.Last(0), t.cfg.MinPositionValue); held != tr.side {
		if held == "" {
			delete(t.trades, df.Pair) // closed by a leg meanwhile
		}
		return held == "", nil
	}

	order, err := broker.CreateOrderMarket(oppositeSide(tr.side), df.Pair, math.Abs(asset))
	if err != nil {
		log.Error(err)
		return false, nil
	}
	delete(t.trades, df.Pair)
	return true, &order
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
	if err != nil {
		log.Error(err)
		return model.Order{}, false
	}
	tr.legs[i] = moved
	return moved, true
}

// size returns the size of the position an entry opens on pair at price, with
// quote available, sized as the intraday strategies size theirs: PositionSize,
// or else PositionFraction of quote times Leverage. ok is false when there is
// none: a share of the balance worth less than MinPositionValue, or a fixed
// size missing, worth less than MinPositionValue, it would be taken for dust,
// or more than quote times the leverage, all the balance can take.
func (t *Trader) size(pair string, quote, price float64) (float64, bool) {
	c := t.cfg
	if price <= 0 || !known(price) {
		return 0, false
	}
	if len(c.PositionSize) == 0 {
		value := quote * c.PositionFraction * c.Leverage
		return value / price, value >= c.MinPositionValue
	}

	size, ok := c.PositionSize[pair]
	value := size * price
	switch {
	case !ok:
		log.Errorf("%s: no position size set for the pair", pair)
	case value < c.MinPositionValue:
		log.Errorf("%s: a position of %g is worth %f, under the min position value of %g: it would be taken "+
			"for dust", pair, size, value, c.MinPositionValue)
	case value > quote*c.Leverage:
		log.Errorf("%s: a position of %g is worth %f, more than the %f available times the leverage of %g",
			pair, size, value, quote, c.Leverage)
	default:
		return size, true
	}
	return 0, false
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
