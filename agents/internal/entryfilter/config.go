package entryfilter

import (
	"errors"
	"flag"
	"fmt"
	"github.com/raykavin/xtrader/agentapi"
	"math"
	"slices"
	"strings"
	"time"
)

// The regimes an entry may be asked to be in (Config.Regime).
const (
	RegimeTrend = "trend" // the market trending: the value of the method at or above the threshold
	RegimeRange = "range" // the market ranging: below it
)

// The methods measuring the regime (Config.RegimeMethod), see regime.go.
const (
	MethodER    = "er"    // Kaufman's efficiency ratio
	MethodADX   = "adx"   // Wilder's ADX
	MethodHurst = "hurst" // the Hurst exponent, by rescaled range
)

// The modes of the intermarket filter (Config.IntermarketMode).
const (
	IntermarketInverse = "inverse" // buy while the reference is below its EMA, sell while above
	IntermarketDirect  = "direct"  // buy while above, sell while below
)

// The defaults of the filters: the lookback and the threshold of each method
// of the regime when 0 (er: 10 bars, the period of Kaufman's adaptive average,
// and 0.30, a threshold commonly read into his ratio, ours; adx: 14 bars,
// Wilder's period, and 25, the usual reading of his index, Wilder speaking of
// the ADXR; hurst: 100 returns, ours, and 0.5, the random walk), the
// window of the ATR percentile, the horizon of the theoretical trades of the
// equity curve and of the labels of the samples, the EMA of the reference and
// the mode of the intermarket filter.
const (
	DefaultERLen            = 10
	DefaultERThreshold      = 0.30
	DefaultADXLen           = 14
	DefaultADXThreshold     = 25
	DefaultHurstLen         = 100
	DefaultHurstThreshold   = 0.50
	DefaultATRWindow        = 100
	DefaultEquityMaxBars    = 100
	DefaultMetaMaxBars      = 100
	DefaultIntermarketEMA   = 50
	DefaultIntermarketMode  = IntermarketInverse
	defaultRegimeMethod     = MethodER
	minHurstLen             = 32 // two sizes of sub-periods at least: 8 and 16
	atrLen                  = 14 // the ATR of the percentile of the volatility, Wilder's
	intermarketFreshBars    = 4  // the reference candle no older than that many candles of the pair
	intermarketCandlesExtra = 10 // the candles read beyond three EMA lengths
)

// Config is the entry filter of an agent, shared by the strategies of every
// family agent (see RegisterFlags), each filter off at its zero value, see
// Enabled:
//
//   - Regime ("", RegimeTrend, RegimeRange): enter only while the market is in
//     that regime, measured by RegimeMethod over RegimeLen candles, trending at
//     or above RegimeThreshold (above it for the Hurst exponent);
//   - RegimeMaxATRPct (0 off, up to 100): no entry while the percentile of the
//     ATR of 14 over the RegimeATRWindow candles before is above it, an extreme
//     volatility, with or without Regime;
//   - EquityCurve (0 off, 2 or more): no entry while the equity curve of the
//     theoretical trades of the strategy (see tracker.go) is under its SMA of
//     its last EquityCurve points, judged once that many trades ended, each
//     trade ended after EquityCurveMaxBars candles at most;
//   - MetaModel (a file of metalabel.Model): an entry only when the model
//     gives it a probability of its target first at or above MetaThreshold
//     (0: the model's own);
//   - MetaRecord (a JSON lines file): every entry attempted appended to it as
//     a metalabel.Sample once its label is known, MetaMaxBars candles at most
//     after it (never a filter);
//   - IntermarketSymbol (a symbol not traded, eg: DXYm): buy and sell only as
//     the reference stands against its EMA of IntermarketEMA candles, in the
//     IntermarketMode, on the pairs of IntermarketPairs (none: all of them).
//
// Agent and Strategy name the agent and its strategy in the samples, Source
// where they were recorded (metalabel.SourceLive, SourcePaper or
// SourceBacktest), Run the backtest run of the controller (0 for none) and
// Segment its segment (metalabel.SegmentFit or SegmentValidation, "" for
// none), so that the training tells them apart. An entry decided before
// RecordFrom (zero for none), the warm-up of a backtest, is neither recorded
// (the run of the segment before covers it, its samples recorded once) nor
// counted by the equity curve (which starts the period empty, as it starts
// live).
type Config struct {
	Regime          string
	RegimeMethod    string
	RegimeLen       int
	RegimeThreshold float64
	RegimeMaxATRPct float64
	RegimeATRWindow int

	EquityCurve        int
	EquityCurveMaxBars int

	MetaModel     string
	MetaThreshold float64
	MetaRecord    string
	MetaMaxBars   int

	IntermarketSymbol string
	IntermarketMode   string
	IntermarketEMA    int
	IntermarketPairs  []string

	Agent      string
	Strategy   string
	Source     string
	Run        int64
	Segment    string
	RecordFrom time.Time

	// Gate, when set, tells the assets the strategy may open positions on now
	// (the window of the trading hours open), asked before any filter
	Gate *agentapi.PairGate
}

// Enabled reports whether a filter is on, or the samples recorded: off, the
// strategy is left as it is (see Wrap).
func (c Config) Enabled() bool {
	return c.Regime != "" || c.RegimeMaxATRPct > 0 || c.EquityCurve > 0 || c.MetaModel != "" ||
		c.MetaRecord != "" || c.IntermarketSymbol != ""
}

// References returns the symbols the filter reads but never trades: the one of
// the intermarket filter, if any.
func (c Config) References() []string {
	if c.IntermarketSymbol == "" {
		return nil
	}
	return []string{c.IntermarketSymbol}
}

// withDefaults returns c with the defaults of the fields left at 0, see the
// constants.
func (c Config) withDefaults() Config {
	if c.RegimeMethod == "" {
		c.RegimeMethod = defaultRegimeMethod
	}
	if c.RegimeLen == 0 {
		c.RegimeLen = map[string]int{MethodER: DefaultERLen, MethodADX: DefaultADXLen,
			MethodHurst: DefaultHurstLen}[c.RegimeMethod]
	}
	if c.RegimeThreshold == 0 {
		c.RegimeThreshold = map[string]float64{MethodER: DefaultERThreshold, MethodADX: DefaultADXThreshold,
			MethodHurst: DefaultHurstThreshold}[c.RegimeMethod]
	}
	if c.RegimeATRWindow == 0 {
		c.RegimeATRWindow = DefaultATRWindow
	}
	if c.EquityCurveMaxBars == 0 {
		c.EquityCurveMaxBars = DefaultEquityMaxBars
	}
	if c.MetaMaxBars == 0 {
		c.MetaMaxBars = DefaultMetaMaxBars
	}
	if c.IntermarketMode == "" {
		c.IntermarketMode = DefaultIntermarketMode
	}
	if c.IntermarketEMA == 0 {
		c.IntermarketEMA = DefaultIntermarketEMA
	}
	return c
}

// validate returns why c can't filter, its defaults applied.
func (c Config) validate() error {
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	switch {
	case c.Regime != "" && c.Regime != RegimeTrend && c.Regime != RegimeRange:
		return fmt.Errorf("-regime: %q, trend or range", c.Regime)
	case !slices.Contains([]string{MethodER, MethodADX, MethodHurst}, c.RegimeMethod):
		return fmt.Errorf("-regime-method: %q, er, adx or hurst", c.RegimeMethod)
	case c.RegimeMethod == MethodER && c.RegimeLen < 1:
		return errors.New("-regime-len: 1 candle at least for the efficiency ratio")
	case c.RegimeMethod == MethodADX && c.RegimeLen < 2:
		return errors.New("-regime-len: 2 candles at least for the ADX")
	case c.RegimeMethod == MethodHurst && c.RegimeLen < minHurstLen:
		return fmt.Errorf("-regime-len: %d returns at least for the Hurst exponent (sub-periods of 8 and 16)",
			minHurstLen)
	case !finite(c.RegimeThreshold) || c.RegimeThreshold <= 0:
		return errors.New("-regime-threshold must be above 0")
	case c.RegimeMethod == MethodER && c.RegimeThreshold > 1:
		return errors.New("-regime-threshold: the efficiency ratio is from 0 to 1")
	case c.RegimeMethod == MethodHurst && c.RegimeThreshold >= 1:
		return errors.New("-regime-threshold: the Hurst exponent is from 0 to 1")
	case c.RegimeMethod == MethodADX && c.RegimeThreshold > 100:
		return errors.New("-regime-threshold: the ADX is from 0 to 100")
	case !finite(c.RegimeMaxATRPct) || c.RegimeMaxATRPct < 0 || c.RegimeMaxATRPct > 100:
		return errors.New("-regime-max-atr-pct: a percentile from 0 (off) to 100")
	case c.RegimeATRWindow < 2:
		return errors.New("-regime-atr-window: 2 candles at least")
	case c.EquityCurve < 0 || c.EquityCurve == 1:
		return errors.New("-equity-curve: 0 (off), or the 2 trades at least of its SMA")
	case c.EquityCurveMaxBars < 1:
		return errors.New("-equity-curve-max-bars: 1 candle at least")
	case !finite(c.MetaThreshold) || c.MetaThreshold < 0 || c.MetaThreshold >= 1:
		return errors.New("-meta-threshold: a probability from 0 (the model's) to below 1")
	case c.MetaMaxBars < 1:
		return errors.New("-meta-max-bars: 1 candle at least")
	case c.IntermarketMode != IntermarketInverse && c.IntermarketMode != IntermarketDirect:
		return fmt.Errorf("-intermarket-mode: %q, inverse or direct", c.IntermarketMode)
	case c.IntermarketEMA < 2:
		return errors.New("-intermarket-ema: 2 candles at least")
	case c.IntermarketSymbol != "" && slices.Contains(c.IntermarketPairs, c.IntermarketSymbol):
		return fmt.Errorf("-intermarket-pairs: the reference %s isn't filtered by itself", c.IntermarketSymbol)
	}
	return nil
}

// The names of the flags, see RegisterFlags.
const (
	flagRegime          = "regime"
	flagRegimeMethod    = "regime-method"
	flagRegimeLen       = "regime-len"
	flagRegimeThreshold = "regime-threshold"
	flagRegimeMaxATRPct = "regime-max-atr-pct"
	flagRegimeATRWindow = "regime-atr-window"
	flagEquity          = "equity-curve"
	flagEquityMaxBars   = "equity-curve-max-bars"
	flagMetaModel       = "meta-model"
	flagMetaThreshold   = "meta-threshold"
	flagMetaRecord      = "meta-record"
	flagMetaMaxBars     = "meta-max-bars"
	flagIMSymbol        = "intermarket-symbol"
	flagIMMode          = "intermarket-mode"
	flagIMEMA           = "intermarket-ema"
	flagIMPairs         = "intermarket-pairs"
)

// Flags are the names of the flags of RegisterFlags, eg: for a list of the
// shared inputs.
var Flags = []string{flagRegime, flagRegimeMethod, flagRegimeLen, flagRegimeThreshold, flagRegimeMaxATRPct,
	flagRegimeATRWindow, flagEquity, flagEquityMaxBars, flagMetaModel, flagMetaThreshold, flagMetaRecord,
	flagMetaMaxBars, flagIMSymbol, flagIMMode, flagIMEMA, flagIMPairs}

// RegisterFlags defines the flags of the entry filter on fs (see Config), each
// off by default, and returns what builds the Config of them once fs is
// parsed: its defaults applied, validated, and a flag of a filter set without
// the filter (eg: -regime-len without -regime) refused rather than ignored.
func RegisterFlags(fs *flag.FlagSet) func() (Config, error) {
	var (
		regime          = fs.String(flagRegime, "", "entry filter: enter only in a regime of the market, trend or range, measured by -regime-method (default: off)")
		regimeMethod    = fs.String(flagRegimeMethod, defaultRegimeMethod, "entry filter, with -regime: what measures the regime: er (Kaufman's efficiency ratio), adx (Wilder's ADX) or hurst (the Hurst exponent by rescaled range)")
		regimeLen       = fs.Int(flagRegimeLen, 0, "entry filter, with -regime: the candles of -regime-method (default: er 10, adx 14, hurst 100)")
		regimeThreshold = fs.Float64(flagRegimeThreshold, 0, "entry filter, with -regime: trending at or above it (hurst: above it, persistent) (default: er 0.30, adx 25, hurst 0.50)")
		regimeMaxATR    = fs.Float64(flagRegimeMaxATRPct, 0, "entry filter: no entry while the percentile of the ATR 14 over -regime-atr-window candles is above it, from 0 to 100, eg: 90 (default: off)")
		regimeWindow    = fs.Int(flagRegimeATRWindow, DefaultATRWindow, "entry filter, with -regime-max-atr-pct: the candles the ATR is ranked among")
		equity          = fs.Int(flagEquity, 0, "entry filter: N, no entry while the equity curve of the theoretical trades of the strategy (in R) is under its SMA of its last N trades, once N trades ended (default: off)")
		equityMaxBars   = fs.Int(flagEquityMaxBars, DefaultEquityMaxBars, "entry filter, with -equity-curve: a theoretical trade neither stopped nor at its target ends after that many candles, at the close")
		metaModel       = fs.String(flagMetaModel, "", "entry filter: the JSON file of a meta-labeling model (agents/metalabel): an entry only when it gives its target first a probability at or above -meta-threshold (default: off)")
		metaThreshold   = fs.Float64(flagMetaThreshold, 0, "entry filter, with -meta-model: the least probability of an entry (default: the model's)")
		metaRecord      = fs.String(flagMetaRecord, "", "the JSON lines file every entry attempted is appended to, its features and, once known, its label by the triple barrier: the training data of -meta-model (default: off)")
		metaMaxBars     = fs.Int(flagMetaMaxBars, DefaultMetaMaxBars, "with -meta-record: the vertical barrier of the labels, in candles")
		imSymbol        = fs.String(flagIMSymbol, "", "entry filter: a reference symbol read, never traded, eg: DXYm, its close against its EMA allowing the buys or the sells, see -intermarket-mode (default: off)")
		imMode          = fs.String(flagIMMode, DefaultIntermarketMode, "entry filter, with -intermarket-symbol: inverse (buy while the reference closes below its EMA, sell while above: the dollar against gold) or direct (the other way)")
		imEMA           = fs.Int(flagIMEMA, DefaultIntermarketEMA, "entry filter, with -intermarket-symbol: the EMA of the reference, in candles of the timeframe of the strategy")
		imPairs         = fs.String(flagIMPairs, "", "entry filter, with -intermarket-symbol: the pairs filtered, separated by ;, eg: XAUUSDm (default: all)")
	)
	return func() (Config, error) {
		set := make(map[string]bool)
		fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
		c := Config{Regime: strings.TrimSpace(*regime), RegimeMethod: strings.TrimSpace(*regimeMethod),
			RegimeLen: *regimeLen, RegimeThreshold: *regimeThreshold, RegimeMaxATRPct: *regimeMaxATR,
			RegimeATRWindow: *regimeWindow, EquityCurve: *equity, EquityCurveMaxBars: *equityMaxBars,
			MetaModel: strings.TrimSpace(*metaModel), MetaThreshold: *metaThreshold,
			MetaRecord: strings.TrimSpace(*metaRecord), MetaMaxBars: *metaMaxBars,
			IntermarketSymbol: strings.TrimSpace(*imSymbol), IntermarketMode: strings.TrimSpace(*imMode),
			IntermarketEMA: *imEMA, IntermarketPairs: split(*imPairs)}
		for _, needs := range []struct {
			flags []string
			on    bool
			what  string
		}{
			{[]string{flagRegimeMethod, flagRegimeLen, flagRegimeThreshold}, c.Regime != "", "-regime"},
			{[]string{flagRegimeATRWindow}, c.RegimeMaxATRPct > 0, "-regime-max-atr-pct"},
			{[]string{flagEquityMaxBars}, c.EquityCurve > 0, "-equity-curve"},
			{[]string{flagMetaThreshold}, c.MetaModel != "", "-meta-model"},
			{[]string{flagMetaMaxBars}, c.MetaRecord != "", "-meta-record"},
			{[]string{flagIMMode, flagIMEMA, flagIMPairs}, c.IntermarketSymbol != "", "-intermarket-symbol"},
		} {
			for _, name := range needs.flags {
				if set[name] && !needs.on {
					return Config{}, fmt.Errorf("-%s is for %s, off", name, needs.what)
				}
			}
		}
		c = c.withDefaults()
		return c, c.validate()
	}
}

// split splits a list of pairs, separated by ";" (or ","), the blanks dropped.
func split(value string) []string {
	var items []string
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == ',' }) {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}
