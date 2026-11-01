// Package entryfilter filters the entries of the strategy of a family agent,
// whatever the strategy: a decorator of strategy.Strategy (see Wrap) handing
// the strategy a broker whose entries it asks first (see broker.go), so it
// behaves the same in a backtest, in paper and live. Its filters, each off by
// default (see Config and RegisterFlags):
//
//   - the regime of the market (A1, regime.go): enter only while it trends, or
//     only while it ranges, measured by Kaufman's efficiency ratio, Wilder's
//     ADX or the Hurst exponent; and no entry while the ATR is at an extreme
//     percentile of its recent values;
//   - the equity curve (A2, tracker.go): no entry while the equity curve of the
//     theoretical trades of the strategy, every entry it attempts, is under its
//     moving average, until it gets back above it;
//   - the meta-labeling (A3, agents/metalabel): no entry a model trained on the
//     past entries of the strategy gives too low a probability of reaching its
//     target before its stop; and the samples to train it on recorded;
//   - the intermarket filter (B8, intermarket.go): buy and sell only as a
//     reference symbol not traded (eg: the dollar index against gold) stands
//     against its EMA.
//
// An entry refused returns order.ErrEntriesBlocked wrapped with the reason of
// the filter, which the strategies take as a block, as they do the blocks of
// the bot (it never touches them: BlockEntries and HoldEntries belong to the
// controller and to the backtest), told once per change of reason and pair.
// A filter not ready (too few candles, the reference missing or stale) refuses
// the entries: never taken for a favourable one. The exits always pass.
//
// Nothing reads the future: the filters of an entry decided at the close of a
// candle read the candles up to it (the ones handed to the strategy), and the
// candles of the reference closed by then.
package entryfilter

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/xhit/go-str2duration/v2"

	"github.com/raykavin/xtrader/agents/metalabel"
	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

// pairState is what the filter knows of a pair: the candles last handed to the
// strategy, why the regime refuses its entries ("" allowed), why the reference
// refuses its buys and its sells, the candle the reference was read for, and
// the reason of the last refusal told.
type pairState struct {
	df          *model.Dataframe
	regime      string
	buy, sell   string
	referenceAt time.Time
	told        string
}

// filter is a strategy filtered, see the package and Wrap.
type filter struct {
	inner     strategy.Strategy
	cfg       Config
	length    time.Duration // of the timeframe of the strategy
	warmup    int
	model     *metalabel.Model
	threshold float64
	recorder  *recorder

	mu      sync.Mutex
	source  CandleSource
	pairs   map[string]*pairState
	tracker *tracker
}

// The wrappers of a strategy filtered, by the optional interfaces of the
// strategy they keep: strategy.HighFrequencyStrategy (its partial candles,
// asked only of a strategy that has them, the engine computing the indicators
// on every tick for one) and strategies.ReleasesUser (the releases of the
// calendar forwarded, the runner giving them only to a strategy using them).
type (
	hfFilter         struct{ *filter }
	releasesFilter   struct{ *filter }
	hfReleasesFilter struct{ *filter }
)

func (f hfFilter) OnPartialCandle(df *model.Dataframe, broker service.Broker) { f.partial(df, broker) }

func (f releasesFilter) SetReleases(source strategies.Releases) { f.releases(source) }

func (f hfReleasesFilter) OnPartialCandle(df *model.Dataframe, broker service.Broker) {
	f.partial(df, broker)
}

func (f hfReleasesFilter) SetReleases(source strategies.Releases) { f.releases(source) }

// Wrap returns s filtered as cfg says (see the package), s itself when no
// filter is on (cfg.Enabled false): the agents without a filter trade as they
// did. It fails on a config that can't filter, or a model of -meta-model that
// can't be read.
func Wrap(s strategy.Strategy, cfg Config) (strategy.Strategy, error) {
	if !cfg.Enabled() {
		return s, nil
	}
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	f := &filter{inner: s, cfg: cfg, pairs: make(map[string]*pairState)}
	length, err := str2duration.ParseDuration(s.Timeframe())
	if err != nil && cfg.IntermarketSymbol != "" {
		return nil, fmt.Errorf("-intermarket-symbol: the timeframe %q: %w", s.Timeframe(), err)
	}
	f.length = length
	if cfg.MetaModel != "" {
		if f.model, err = metalabel.Load(cfg.MetaModel); err != nil {
			return nil, fmt.Errorf("-meta-model: %w", err)
		}
		f.threshold = cfg.MetaThreshold
		if f.threshold == 0 {
			f.threshold = f.model.Threshold
		}
		if f.threshold == 0 {
			f.threshold = metalabel.DefaultThreshold
		}
	}
	var record func(metalabel.Sample)
	if cfg.MetaRecord != "" {
		f.recorder = &recorder{path: cfg.MetaRecord}
		record = f.recorder.write
	}
	equityBars, labelBars := 0, 0
	if cfg.EquityCurve > 0 {
		equityBars = cfg.EquityCurveMaxBars
	}
	if cfg.MetaRecord != "" {
		labelBars = cfg.MetaMaxBars
	}
	f.tracker = newTracker(equityBars, labelBars, record)

	f.warmup = max(s.WarmupPeriod(), regimeWarmup(cfg))
	if f.features() {
		f.warmup = max(f.warmup, metalabel.Window)
	}
	if cfg.IntermarketSymbol != "" {
		f.warmup = max(f.warmup, intermarketLimit(cfg))
	}

	_, hf := s.(strategy.HighFrequencyStrategy)
	_, releases := s.(strategies.ReleasesUser)
	switch {
	case hf && releases:
		return hfReleasesFilter{f}, nil
	case hf:
		return hfFilter{f}, nil
	case releases:
		return releasesFilter{f}, nil
	}
	return f, nil
}

// features reports whether the entries need their features: for the model or
// the samples.
func (f *filter) features() bool { return f.model != nil || f.recorder != nil }

// Unwrap returns the strategy filtered.
func (f *filter) Unwrap() strategy.Strategy { return f.inner }

// Close closes the file of the samples, if open: the samples of the trades
// still open are never written.
func (f *filter) Close() error {
	if f.recorder == nil {
		return nil
	}
	return f.recorder.close()
}

func (f *filter) Timeframe() string { return f.inner.Timeframe() }

// WarmupPeriod is the one of the strategy, or the candles the filters need
// when more: the regime (see regimeWarmup), the features of the meta-labeling
// (metalabel.Window) and the candles of the reference read (as many: they are
// read over the same period in a backtest).
func (f *filter) WarmupPeriod() int { return f.warmup }

// Indicators returns the indicators of the strategy and, with -regime on the
// efficiency ratio or the ADX, a pane of its value and threshold (none for the
// Hurst exponent, too costly to draw on every candle). Pure: the chart calls it
// at any time.
func (f *filter) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	indicators := f.inner.Indicators(df)
	if f.cfg.Regime == "" || f.cfg.RegimeMethod == MethodHurst {
		return indicators
	}
	values := regimeSeries(f.cfg, df)
	threshold := make(model.Series[float64], len(df.Close))
	for i := range threshold {
		threshold[i] = f.cfg.RegimeThreshold
	}
	warmup := f.cfg.RegimeLen
	if f.cfg.RegimeMethod == MethodADX {
		warmup = 2 * f.cfg.RegimeLen
	}
	return append(indicators, strategy.ChartIndicator{GroupName: "Regime (" + f.cfg.RegimeMethod + ")",
		Time: df.Time, Warmup: warmup, Metrics: []strategy.IndicatorMetric{
			{Name: f.cfg.RegimeMethod, Color: "#8e44ad", Style: strategy.StyleLine, Values: values},
			{Name: "threshold", Color: "#fd8700", Style: strategy.StyleLine, Values: threshold},
		}})
}

// SetCandleSource sets where the candles of the reference of the intermarket
// filter are read, see CandleSource.
func (f *filter) SetCandleSource(source CandleSource) {
	f.mu.Lock()
	f.source = source
	f.mu.Unlock()
}

// ChartAnalysis returns the analysis the strategy draws on the chart, if it
// draws one, see ui.AnalysisSource.
func (f *filter) ChartAnalysis(pair string) (ui.Analysis, bool) {
	if source, ok := f.inner.(ui.AnalysisSource); ok {
		return source.ChartAnalysis(pair)
	}
	return ui.Analysis{}, false
}

// OnCandle follows the theoretical trades of the pair of df over its last
// candle, measures the filters on it (the reference read, a few seconds at
// most), runs the strategy with the broker filtered, then follows the
// positions it left.
func (f *filter) OnCandle(df *model.Dataframe, broker service.Broker) {
	n := len(df.Close)
	if n == 0 {
		f.inner.OnCandle(df, f.gate(broker))
		return
	}
	last := candle{time: df.Time[n-1], open: df.Open[n-1], high: df.High[n-1], low: df.Low[n-1],
		close: df.Close[n-1]}

	f.mu.Lock()
	st := f.state(df.Pair)
	read := filtersPair(f.cfg, df.Pair) && !st.referenceAt.Equal(last.time)
	source := f.source
	f.mu.Unlock()
	var refs []model.Candle
	var refErr error
	if read {
		refs, refErr = readReference(source, f.cfg, f.inner.Timeframe())
	}
	held := heldOf(broker, df.Pair)

	f.mu.Lock()
	st.df = df
	f.tracker.onCandle(df.Pair, last, held)
	st.regime = f.regime(df)
	if read {
		st.referenceAt = last.time
		st.buy, st.sell = intermarketReasons(f.cfg, refs, refErr, last.time, f.length)
	}
	f.mu.Unlock()

	f.inner.OnCandle(df, f.gate(broker))

	held = heldOf(broker, df.Pair)
	f.mu.Lock()
	f.tracker.observe(df.Pair, last, held)
	f.mu.Unlock()
}

// partial runs the partial candle of a high frequency strategy, its broker
// filtered.
func (f *filter) partial(df *model.Dataframe, broker service.Broker) {
	if hf, ok := f.inner.(strategy.HighFrequencyStrategy); ok {
		hf.OnPartialCandle(df, f.gate(broker))
	}
}

// releases gives the strategy the releases of the calendar.
func (f *filter) releases(source strategies.Releases) {
	if user, ok := f.inner.(strategies.ReleasesUser); ok {
		user.SetReleases(source)
	}
}

// state returns the state of pair, made on its first candle. Under mu.
func (f *filter) state(pair string) *pairState {
	st, ok := f.pairs[pair]
	if !ok {
		st = &pairState{}
		f.pairs[pair] = st
	}
	return st
}

// heldOf returns whether the bot holds a position of pair on a side, as broker
// tells it now: the exchange or the order controller, see holdings.
func heldOf(broker service.Broker, pair string) func(model.SideType) bool {
	return holdingsOf(broker, pair).holds
}

// regime returns why the regime and the volatility refuse the entries of df on
// its last candle, "" when they don't.
func (f *filter) regime(df *model.Dataframe) string {
	c := f.cfg
	if c.Regime != "" {
		value := regimeValue(c, df)
		relation := "under"
		if trending(c, value) {
			relation = "at or above"
			if c.RegimeMethod == MethodHurst {
				relation = "above"
			}
		}
		switch {
		case math.IsNaN(value):
			return fmt.Sprintf("regime: the %s of %d candles not known yet (%d candles handed)", c.RegimeMethod,
				c.RegimeLen, len(df.Close))
		case trending(c, value) != (c.Regime == RegimeTrend):
			return fmt.Sprintf("regime: the %s of %d at %.3f, %s %g: not a %s market", c.RegimeMethod, c.RegimeLen,
				value, relation, c.RegimeThreshold, c.Regime)
		}
	}
	if c.RegimeMaxATRPct > 0 {
		switch percentile := atrPercentile(df, c.RegimeATRWindow); {
		case math.IsNaN(percentile):
			return fmt.Sprintf("volatility: the percentile of the ATR over %d candles not known yet",
				c.RegimeATRWindow)
		case percentile > c.RegimeMaxATRPct:
			return fmt.Sprintf("volatility: the ATR at the percentile %.0f of its %d candles, above %g", percentile,
				c.RegimeATRWindow, c.RegimeMaxATRPct)
		}
	}
	return ""
}

// entry asks the filters for an entry of side on pair of kind, at level for a
// pending one, with its stop loss and take profit (0 for none): its
// theoretical trade opened (see tracker.go), and place called when every
// filter allows it. It reports whether the filter refused it (place not
// called), and the error returned: ErrEntriesBlocked wrapped with the reason
// then, else the one of place.
func (f *filter) entry(pair string, side model.SideType, kind entryKind, level, stop, target float64,
	place func() error) (bool, error) {
	f.mu.Lock()
	st := f.pairs[pair]
	var trade *theoretical
	var sample *metalabel.Sample
	var features map[string]float64
	var computed bool
	if st != nil && st.df != nil && len(st.df.Close) > 0 {
		n := len(st.df.Close)
		price := level
		if kind == kindMarket {
			price = st.df.Close[n-1]
		}
		at := st.df.Time[n-1]
		if f.features() {
			features, computed = metalabel.Features(st.df, side)
		}
		// decided before RecordFrom, in the warm-up of a backtest: neither a
		// sample nor a point of the equity curve, which starts empty at the
		// start of the period as it does live
		warm := at.Add(f.length).Before(f.cfg.RecordFrom)
		trade = &theoretical{pair: pair, side: side, kind: kind, price: price, stop: stop, target: target, at: at,
			warm: warm}
		if f.recorder != nil && computed && !warm {
			sample = &metalabel.Sample{Agent: f.cfg.Agent, Strategy: f.cfg.Strategy, Pair: pair, Side: string(side),
				Time: at.Unix(), Entry: price, Stop: stop, Target: target, Features: features,
				Source: f.cfg.Source, Run: f.cfg.Run, Segment: f.cfg.Segment}
		}
	}
	reason := f.refusal(pair, st, side, features, computed)
	f.mu.Unlock()

	var err error
	if reason == "" {
		err = place()
	} else {
		err = fmt.Errorf("%w: %s", order.ErrEntriesBlocked, reason)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if trade != nil {
		trade.real = reason == "" && err == nil
		f.tracker.add(trade, sample)
	}
	if st != nil && st.told != reason {
		if reason != "" {
			log.Warnf("[FILTER] %s: entries refused: %s", pair, reason)
		} else {
			log.Infof("[FILTER] %s: entries allowed by the filter again", pair)
		}
		st.told = reason
	}
	return reason != "", err
}

// refusal returns why the filters refuse an entry of side on pair, "" when
// they allow it: the regime and the volatility, the equity curve, the
// reference, the meta-labeling, in that order. Under mu.
func (f *filter) refusal(pair string, st *pairState, side model.SideType, features map[string]float64,
	computed bool) string {
	if st == nil || st.df == nil {
		return fmt.Sprintf("no candle of %s handed to the strategy yet", pair)
	}
	if st.regime != "" {
		return st.regime
	}
	if f.cfg.EquityCurve > 0 {
		if paused, equity, sma := f.tracker.paused(f.cfg.EquityCurve); paused {
			return fmt.Sprintf("equity curve: the theoretical trades at %.2f R, under their SMA %d at %.2f R",
				equity, f.cfg.EquityCurve, sma)
		}
	}
	if filtersPair(f.cfg, pair) {
		switch side {
		case model.SideTypeBuy:
			if st.buy != "" {
				return st.buy
			}
		case model.SideTypeSell:
			if st.sell != "" {
				return st.sell
			}
		}
	}
	if f.model != nil {
		if !computed {
			return "meta-labeling: the features of the entry can't be computed"
		}
		p, err := f.model.Probability(features)
		switch {
		case err != nil:
			return "meta-labeling: " + err.Error()
		case p < f.threshold:
			return fmt.Sprintf("meta-labeling: a probability of %.2f of the target first, under %.2f", p,
				f.threshold)
		}
	}
	return ""
}
