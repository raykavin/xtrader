package llm

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/raykavin/xtrader/indicator"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/strategy"
)

// An indicator of Config.Indicators is a group of IndicatorGroups alone, eg:
// "ema", the one of the Config with its periods (ema_9 and ema_21), or a kind
// of IndicatorKinds with its parameters, separated by spaces or by +, eg: "ema
// 20 50" or "ema+20+50" (a flag without quotes), "supertrend 10 3". A kind
// without parameters takes its defaults, eg:
// "supertrend" is "supertrend 10 3". Each sends its series to the model by the
// names of IndicatorKinds, eg: ema_20 and ema_50, and draws them.

// IndicatorKinds describes the kinds of Config.Indicators with their
// parameters, their defaults in parentheses, and the series each one sends.
var IndicatorKinds = []string{
	"ema periods... (ema_<period>)",
	"sma periods... (sma_<period>)",
	"rsi period (rsi_<period>)",
	"atr period [average] (atr_<period>, atr_<period>_sma_<average>)",
	"adx period (adx_<period>, plus_di_<period>, minus_di_<period>)",
	"macd fast slow signal (macd_<fast>_<slow>, macd_signal_<signal>, macd_histogram_<fast>_<slow>_<signal>)",
	"bb period deviations (bb_upper_<period>_<deviations>, bb_middle_<period>, bb_lower_<period>_<deviations>)",
	"volume period (volume_sma_<period>)",
	"supertrend atr factor (10 3: supertrend_<atr>_<factor>, supertrend_side_<atr>_<factor>)",
	"psar step max (0.02 0.2: psar_<step>_<max>, psar_side_<step>_<max>)",
	"ichimoku tenkan kijun senkou (9 26 52: ichimoku_tenkan_<tenkan>, ichimoku_kijun_<kijun>, ichimoku_senkou_a, " +
		"ichimoku_senkou_b_<senkou>)",
	"stoch k smoothing d (14 3 3: stoch_k_<k>_<smoothing>, stoch_d_<d>)",
	"donchian period (20: donchian_upper_<period>, donchian_lower_<period>)",
	"keltner ema atr multiplier (20 10 2: keltner_upper_<ema>_<atr>_<multiplier>, keltner_middle_<ema>, " +
		"keltner_lower_<ema>_<atr>_<multiplier>)",
	"chandelier period multiplier (22 3: chandelier_stop_<period>_<multiplier>, chandelier_side_<period>_<multiplier>)",
	"vwap anchor timezone (00:00 UTC: vwap)",
	"roc period (10: roc_<period>)",
	"range start end until timezone (range_high, range_low)",
}

// extra is an indicator of Config.Indicators other than a group of
// IndicatorGroups alone: its series, computed by compute, drawn over the
// candles or in a pane of its own, the candles its values need, and the period
// of its recursive series, if any, whose seed the warmup washes out.
type extra struct {
	spec     string // as declared
	title    string // of its pane
	series   []extraSeries
	overlay  bool
	lookback int
	seed     int
	compute  func(df *model.Dataframe)
}

// extraSeries is a series of an extra, drawn in style, none for a series only
// sent to the model, eg: the side of the Supertrend.
type extraSeries struct {
	series
	style strategy.MetricStyle
}

// indicatorColors are the colors of the series of the extras drawn, in turn.
var indicatorColors = []string{"#fd8700", "#1e88e5", "#8e44ad", "#2ecc71", "#e74c3c", "#16a085", "#f1c40f"}

// parseExtras returns the extras of the indicators of cfg, the groups of
// IndicatorGroups alone left out, and fails on an indicator unknown, of
// invalid parameters, or whose series has the name of another one.
func parseExtras(cfg Config) ([]*extra, error) {
	names := make(map[string]bool)
	for _, group := range IndicatorGroups {
		if len(cfg.Indicators) == 0 || slices.Contains(cfg.Indicators, group) {
			for _, name := range groupNames(cfg, group) {
				names[name] = true
			}
		}
	}
	var extras []*extra
	for _, spec := range cfg.Indicators {
		if slices.Contains(IndicatorGroups, spec) {
			continue // the one of the Config
		}
		e, err := parseExtra(cfg, spec)
		if err != nil {
			return nil, fmt.Errorf("the indicator %q: %w", spec, err)
		}
		for _, s := range e.series {
			if names[s.name] {
				return nil, fmt.Errorf("the indicator %q: %s is sent by another one", spec, s.name)
			}
			names[s.name] = true
		}
		extras = append(extras, e)
	}
	return extras, nil
}

// groupNames returns the names of the series of a group of IndicatorGroups.
func groupNames(cfg Config, group string) []string {
	switch group {
	case "ema":
		return []string{fmt.Sprintf("ema_%d", cfg.EMAFastLen), fmt.Sprintf("ema_%d", cfg.EMASlowLen)}
	case "sma":
		return []string{fmt.Sprintf("sma_%d", cfg.TrendLen)}
	case "rsi":
		return []string{fmt.Sprintf("rsi_%d", cfg.RSILen)}
	case "atr":
		return []string{fmt.Sprintf("atr_%d", cfg.ATRLen)}
	case "adx":
		return []string{fmt.Sprintf("adx_%d", cfg.ADXLen), fmt.Sprintf("plus_di_%d", cfg.ADXLen),
			fmt.Sprintf("minus_di_%d", cfg.ADXLen)}
	case "macd":
		return []string{fmt.Sprintf("macd_%d_%d", cfg.MACDFast, cfg.MACDSlow),
			fmt.Sprintf("macd_signal_%d", cfg.MACDSignal), "macd_histogram"}
	case "bb":
		return []string{fmt.Sprintf("bb_upper_%d_%g", cfg.BBLen, cfg.BBDev), fmt.Sprintf("bb_middle_%d", cfg.BBLen),
			fmt.Sprintf("bb_lower_%d_%g", cfg.BBLen, cfg.BBDev)}
	case "volume":
		return []string{fmt.Sprintf("volume_sma_%d", cfg.VolumeLen)}
	}
	return nil
}

// params are the parameters of an indicator, read by index.
type params []string

func (p params) period(i int) (int, error) {
	n, err := strconv.Atoi(p[i])
	if err != nil || n < 1 {
		return 0, fmt.Errorf("the period %q must be a whole number of 1 at least", p[i])
	}
	return n, nil
}

func (p params) positive(i int) (float64, error) {
	v, err := strconv.ParseFloat(p[i], 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0, fmt.Errorf("the parameter %q must be a positive number", p[i])
	}
	return v, nil
}

// clock reads a time of day, HH:MM.
func (p params) clock(i int) (time.Duration, error) {
	at, err := time.Parse("15:04", p[i])
	if err != nil {
		return 0, fmt.Errorf("the time %q must be HH:MM", p[i])
	}
	return time.Duration(at.Hour())*time.Hour + time.Duration(at.Minute())*time.Minute, nil
}

// periods reads the parameters from index i on as periods.
func (p params) periods(i int) ([]int, error) {
	out := make([]int, 0, len(p)-i)
	for ; i < len(p); i++ {
		n, err := p.period(i)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// format writes a number of a name, eg: 0.02.
func format(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// parseExtra returns the extra of spec, see IndicatorKinds.
func parseExtra(cfg Config, spec string) (*extra, error) {
	fields := strings.FieldsFunc(strings.TrimSpace(spec), func(r rune) bool { return r == ' ' || r == '+' || r == '\t' })
	if len(fields) == 0 {
		return nil, fmt.Errorf("no kind")
	}
	kind, p := strings.ToLower(fields[0]), params(fields[1:])
	defaults := map[string]params{
		"ema": {strconv.Itoa(cfg.EMAFastLen), strconv.Itoa(cfg.EMASlowLen)}, "sma": {strconv.Itoa(cfg.TrendLen)},
		"rsi": {strconv.Itoa(cfg.RSILen)}, "atr": {strconv.Itoa(cfg.ATRLen)}, "adx": {strconv.Itoa(cfg.ADXLen)},
		"macd": {strconv.Itoa(cfg.MACDFast), strconv.Itoa(cfg.MACDSlow), strconv.Itoa(cfg.MACDSignal)},
		"bb":   {strconv.Itoa(cfg.BBLen), format(cfg.BBDev)}, "volume": {strconv.Itoa(cfg.VolumeLen)},
		"supertrend": {"10", "3"}, "psar": {"0.02", "0.2"}, "ichimoku": {"9", "26", "52"}, "stoch": {"14", "3", "3"},
		"donchian": {"20"}, "keltner": {"20", "10", "2"}, "chandelier": {"22", "3"}, "vwap": {"00:00", "UTC"},
		"roc": {"10"},
	}
	// the counts of parameters of each kind, a range for the optional ones
	arity := map[string][2]int{
		"ema": {1, 100}, "sma": {1, 100}, "rsi": {1, 1}, "atr": {1, 2}, "adx": {1, 1}, "macd": {3, 3}, "bb": {2, 2},
		"volume": {1, 1}, "supertrend": {2, 2}, "psar": {2, 2}, "ichimoku": {3, 3}, "stoch": {3, 3},
		"donchian": {1, 1}, "keltner": {3, 3}, "chandelier": {2, 2}, "vwap": {1, 2}, "roc": {1, 1}, "range": {4, 4},
	}
	counts, ok := arity[kind]
	if !ok {
		return nil, fmt.Errorf("unknown kind %q: of %s, or %s alone", kind, strings.Join(kindNames(), ", "),
			strings.Join(IndicatorGroups, ", "))
	}
	if len(p) == 0 {
		p = defaults[kind]
	}
	if len(p) < counts[0] || len(p) > counts[1] {
		return nil, fmt.Errorf("%s takes %d to %d parameters, not %d", kind, counts[0], counts[1], len(p))
	}
	e, err := buildExtra(cfg, kind, p)
	if err != nil {
		return nil, err
	}
	e.spec = spec
	return e, nil
}

// kindNames returns the kinds of IndicatorKinds.
func kindNames() []string {
	names := make([]string, len(IndicatorKinds))
	for i, kind := range IndicatorKinds {
		names[i], _, _ = strings.Cut(kind, " ")
	}
	return names
}

// buildExtra returns the extra of kind with the parameters p, their count
// checked.
func buildExtra(cfg Config, kind string, p params) (*extra, error) {
	line := func(key, name string) extraSeries {
		return extraSeries{series: series{key: "llm_x_" + key, name: name, group: kind}, style: strategy.StyleLine}
	}
	hidden := func(key, name string) extraSeries {
		return extraSeries{series: series{key: "llm_x_" + key, name: name, group: kind}}
	}
	period, _ := timeframeDuration(cfg.Timeframe)
	day := 1 // the candles of a day, for the indicators of the time of day
	if period > 0 {
		day = int((24*time.Hour + period - 1) / period)
	}

	switch kind {
	case "ema", "sma":
		lengths, err := p.periods(0)
		if err != nil {
			return nil, err
		}
		e := &extra{overlay: true, lookback: slices.Max(lengths) + 1}
		for _, n := range lengths {
			e.series = append(e.series, line(fmt.Sprintf("%s_%d", kind, n), fmt.Sprintf("%s_%d", kind, n)))
		}
		if kind == "ema" {
			e.seed = slices.Max(lengths)
		}
		e.compute = func(df *model.Dataframe) {
			for i, n := range lengths {
				if kind == "ema" {
					df.Metadata[e.series[i].key] = indicator.EMA(df.Close, n)
				} else {
					df.Metadata[e.series[i].key] = indicator.SMA(df.Close, n)
				}
			}
		}
		return e, nil

	case "rsi", "roc", "donchian", "volume", "adx":
		n, err := p.period(0)
		if err != nil {
			return nil, err
		}
		switch kind {
		case "rsi":
			e := &extra{title: fmt.Sprintf("RSI %d", n), lookback: n + 2, seed: n,
				series: []extraSeries{line(fmt.Sprintf("rsi_%d", n), fmt.Sprintf("rsi_%d", n))}}
			e.compute = func(df *model.Dataframe) { df.Metadata[e.series[0].key] = indicator.RSI(df.Close, n) }
			return e, nil
		case "roc":
			e := &extra{title: fmt.Sprintf("ROC %d", n), lookback: n + 2,
				series: []extraSeries{line(fmt.Sprintf("roc_%d", n), fmt.Sprintf("roc_%d", n))}}
			e.compute = func(df *model.Dataframe) { df.Metadata[e.series[0].key] = rateOfChange(df.Close, n) }
			return e, nil
		case "volume":
			e := &extra{title: fmt.Sprintf("Volume SMA %d", n), lookback: n + 1,
				series: []extraSeries{line(fmt.Sprintf("volume_sma_%d", n), fmt.Sprintf("volume_sma_%d", n))}}
			e.compute = func(df *model.Dataframe) { df.Metadata[e.series[0].key] = indicator.SMA(df.Volume, n) }
			return e, nil
		case "adx":
			e := &extra{title: fmt.Sprintf("ADX %d", n), lookback: 2*n + 1, seed: 2 * n, series: []extraSeries{
				line(fmt.Sprintf("adx_%d", n), fmt.Sprintf("adx_%d", n)),
				line(fmt.Sprintf("plus_di_%d", n), fmt.Sprintf("plus_di_%d", n)),
				line(fmt.Sprintf("minus_di_%d", n), fmt.Sprintf("minus_di_%d", n)),
			}}
			e.compute = func(df *model.Dataframe) {
				df.Metadata[e.series[0].key] = indicator.ADX(df.High, df.Low, df.Close, n)
				df.Metadata[e.series[1].key] = indicator.PlusDI(df.High, df.Low, df.Close, n)
				df.Metadata[e.series[2].key] = indicator.MinusDI(df.High, df.Low, df.Close, n)
			}
			return e, nil
		}
		e := &extra{overlay: true, lookback: n + 2, series: []extraSeries{
			line(fmt.Sprintf("donchian_upper_%d", n), fmt.Sprintf("donchian_upper_%d", n)),
			line(fmt.Sprintf("donchian_lower_%d", n), fmt.Sprintf("donchian_lower_%d", n)),
		}}
		e.compute = func(df *model.Dataframe) {
			df.Metadata[e.series[0].key], df.Metadata[e.series[1].key] = donchian(df.High, df.Low, n)
		}
		return e, nil

	case "atr":
		n, err := p.period(0)
		if err != nil {
			return nil, err
		}
		e := &extra{title: fmt.Sprintf("ATR %d", n), lookback: n + 1, seed: n,
			series: []extraSeries{line(fmt.Sprintf("atr_%d", n), fmt.Sprintf("atr_%d", n))}}
		average := 0
		if len(p) > 1 {
			if average, err = p.period(1); err != nil {
				return nil, err
			}
			e.title = fmt.Sprintf("ATR %d, SMA %d", n, average)
			e.lookback = n + average + 1
			name := fmt.Sprintf("atr_%d_sma_%d", n, average)
			e.series = append(e.series, line(name, name))
		}
		e.compute = func(df *model.Dataframe) {
			atr := indicator.ATR(df.High, df.Low, df.Close, n)
			df.Metadata[e.series[0].key] = atr
			if average > 0 {
				df.Metadata[e.series[1].key] = indicator.SMA(atr, average)
			}
		}
		return e, nil

	case "macd":
		lengths, err := p.periods(0)
		if err != nil {
			return nil, err
		}
		fast, slow, signal := lengths[0], lengths[1], lengths[2]
		if fast >= slow {
			return nil, fmt.Errorf("the fast average must be shorter than the slow one")
		}
		e := &extra{title: fmt.Sprintf("MACD %d %d %d", fast, slow, signal), lookback: slow + signal + 1,
			seed: slow + signal, series: []extraSeries{
				line(fmt.Sprintf("macd_%d_%d", fast, slow), fmt.Sprintf("macd_%d_%d", fast, slow)),
				line(fmt.Sprintf("macd_signal_%d", signal), fmt.Sprintf("macd_signal_%d", signal)),
				line(fmt.Sprintf("macd_histogram_%d_%d_%d", fast, slow, signal),
					fmt.Sprintf("macd_histogram_%d_%d_%d", fast, slow, signal)),
			}}
		e.series[2].style = strategy.StyleHistogram
		e.compute = func(df *model.Dataframe) {
			value, signalLine, hist := indicator.MACD(df.Close, fast, slow, signal)
			df.Metadata[e.series[0].key], df.Metadata[e.series[1].key] = value, signalLine
			df.Metadata[e.series[2].key] = hist
		}
		return e, nil

	case "bb", "keltner":
		lengths := []int{}
		last := len(p) - 1 // the deviations, the multiplier
		for i := range last {
			n, err := p.period(i)
			if err != nil {
				return nil, err
			}
			lengths = append(lengths, n)
		}
		width, err := p.positive(last)
		if err != nil {
			return nil, err
		}
		if kind == "bb" {
			n := lengths[0]
			e := &extra{overlay: true, lookback: n + 1, series: []extraSeries{
				line(fmt.Sprintf("bb_upper_%d_%s", n, format(width)), fmt.Sprintf("bb_upper_%d_%s", n, format(width))),
				line(fmt.Sprintf("bb_middle_%d", n), fmt.Sprintf("bb_middle_%d", n)),
				line(fmt.Sprintf("bb_lower_%d_%s", n, format(width)), fmt.Sprintf("bb_lower_%d_%s", n, format(width))),
			}}
			e.compute = func(df *model.Dataframe) {
				upper, middle, lower := indicator.BB(df.Close, n, width, indicator.TypeSMA)
				df.Metadata[e.series[0].key], df.Metadata[e.series[1].key] = upper, middle
				df.Metadata[e.series[2].key] = lower
			}
			return e, nil
		}
		emaLen, atrLen := lengths[0], lengths[1]
		suffix := fmt.Sprintf("%d_%d_%s", emaLen, atrLen, format(width))
		e := &extra{overlay: true, lookback: max(emaLen, atrLen) + 1, seed: max(emaLen, atrLen),
			series: []extraSeries{
				line("keltner_upper_"+suffix, "keltner_upper_"+suffix),
				line(fmt.Sprintf("keltner_middle_%d", emaLen), fmt.Sprintf("keltner_middle_%d", emaLen)),
				line("keltner_lower_"+suffix, "keltner_lower_"+suffix),
			}}
		e.compute = func(df *model.Dataframe) {
			middle, atr := indicator.EMA(df.Close, emaLen), indicator.ATR(df.High, df.Low, df.Close, atrLen)
			upper, lower := make([]float64, len(middle)), make([]float64, len(middle))
			for i := range middle {
				upper[i], lower[i] = middle[i]+width*atr[i], middle[i]-width*atr[i]
			}
			df.Metadata[e.series[0].key], df.Metadata[e.series[1].key] = upper, middle
			df.Metadata[e.series[2].key] = lower
		}
		return e, nil

	case "supertrend", "chandelier":
		n, err := p.period(0)
		if err != nil {
			return nil, err
		}
		factor, err := p.positive(1)
		if err != nil {
			return nil, err
		}
		suffix := fmt.Sprintf("%d_%s", n, format(factor))
		if kind == "supertrend" {
			e := &extra{overlay: true, lookback: n + 2, seed: n, series: []extraSeries{
				line("supertrend_"+suffix, "supertrend_"+suffix), hidden("supertrend_side_"+suffix, "supertrend_side_"+suffix),
			}}
			e.compute = func(df *model.Dataframe) {
				st := indicator.SuperTrend(df.High, df.Low, df.Close, n, factor)
				df.Metadata[e.series[0].key], df.Metadata[e.series[1].key] = st, sideOf(df.Close, st)
			}
			return e, nil
		}
		e := &extra{overlay: true, lookback: n + 2, seed: n, series: []extraSeries{
			line("chandelier_stop_"+suffix, "chandelier_stop_"+suffix),
			hidden("chandelier_side_"+suffix, "chandelier_side_"+suffix),
		}}
		e.compute = func(df *model.Dataframe) {
			side, stop := chandelier(df.High, df.Low, df.Close, n, factor)
			df.Metadata[e.series[0].key], df.Metadata[e.series[1].key] = stop, side
		}
		return e, nil

	case "psar":
		step, err := p.positive(0)
		if err != nil {
			return nil, err
		}
		limit, err := p.positive(1)
		if err != nil {
			return nil, err
		}
		if step > limit {
			return nil, fmt.Errorf("the step %g is above the max %g", step, limit)
		}
		suffix := format(step) + "_" + format(limit)
		e := &extra{overlay: true, lookback: 3, series: []extraSeries{
			line("psar_"+suffix, "psar_"+suffix), hidden("psar_side_"+suffix, "psar_side_"+suffix),
		}}
		e.series[0].style = strategy.StyleScatter
		e.compute = func(df *model.Dataframe) {
			sar := indicator.SAR(df.High, df.Low, step, limit)
			df.Metadata[e.series[0].key], df.Metadata[e.series[1].key] = sar, sideOf(df.Close, sar)
		}
		return e, nil

	case "ichimoku":
		lengths, err := p.periods(0)
		if err != nil {
			return nil, err
		}
		tenkan, kijun, senkou := lengths[0], lengths[1], lengths[2]
		e := &extra{overlay: true, lookback: senkou + kijun, series: []extraSeries{
			line(fmt.Sprintf("ichimoku_tenkan_%d", tenkan), fmt.Sprintf("ichimoku_tenkan_%d", tenkan)),
			line(fmt.Sprintf("ichimoku_kijun_%d", kijun), fmt.Sprintf("ichimoku_kijun_%d", kijun)),
			line("ichimoku_senkou_a", "ichimoku_senkou_a"),
			line(fmt.Sprintf("ichimoku_senkou_b_%d", senkou), fmt.Sprintf("ichimoku_senkou_b_%d", senkou)),
		}}
		e.compute = func(df *model.Dataframe) {
			t, k, a, b := ichimoku(df.High, df.Low, tenkan, kijun, senkou)
			df.Metadata[e.series[0].key], df.Metadata[e.series[1].key] = t, k
			df.Metadata[e.series[2].key], df.Metadata[e.series[3].key] = a, b
		}
		return e, nil

	case "stoch":
		lengths, err := p.periods(0)
		if err != nil {
			return nil, err
		}
		k, smoothing, d := lengths[0], lengths[1], lengths[2]
		e := &extra{title: fmt.Sprintf("Stochastic %d %d %d", k, smoothing, d), lookback: k + smoothing + d,
			series: []extraSeries{
				line(fmt.Sprintf("stoch_k_%d_%d", k, smoothing), fmt.Sprintf("stoch_k_%d_%d", k, smoothing)),
				line(fmt.Sprintf("stoch_d_%d", d), fmt.Sprintf("stoch_d_%d", d)),
			}}
		e.compute = func(df *model.Dataframe) {
			slowK, slowD := indicator.Stoch(df.High, df.Low, df.Close, k, smoothing, indicator.TypeSMA, d,
				indicator.TypeSMA)
			df.Metadata[e.series[0].key], df.Metadata[e.series[1].key] = slowK, slowD
		}
		return e, nil

	case "vwap":
		anchor, err := p.clock(0)
		if err != nil {
			return nil, err
		}
		zone := "UTC"
		if len(p) > 1 {
			zone = p[1]
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return nil, fmt.Errorf("the time zone %q: %w", zone, err)
		}
		e := &extra{overlay: true, lookback: day + 1, series: []extraSeries{line("vwap", "vwap")}}
		e.compute = func(df *model.Dataframe) {
			df.Metadata[e.series[0].key] = anchoredVWAP(df.Time, df.High, df.Low, df.Close, df.Volume, anchor, loc)
		}
		return e, nil

	case "range":
		var clocks [3]time.Duration
		for i := range clocks {
			at, err := p.clock(i)
			if err != nil {
				return nil, err
			}
			clocks[i] = at
		}
		if clocks[0] >= clocks[1] || clocks[1] >= clocks[2] {
			return nil, fmt.Errorf("the range starts before it ends, and ends before the trading ends, the same day")
		}
		loc, err := time.LoadLocation(p[3])
		if err != nil {
			return nil, fmt.Errorf("the time zone %q: %w", p[3], err)
		}
		e := &extra{overlay: true, lookback: day + 1, series: []extraSeries{
			line("range_high", "range_high"), line("range_low", "range_low"),
		}}
		e.compute = func(df *model.Dataframe) {
			df.Metadata[e.series[0].key], df.Metadata[e.series[1].key] = sessionRange(df.Time, df.High, df.Low,
				clocks[0], clocks[1], clocks[2], loc)
		}
		return e, nil
	}
	return nil, fmt.Errorf("unknown kind %q", kind)
}

// chart returns the series of the extras drawn: the ones over the candles as
// metrics of the overlay, the others each in a pane of its own.
func chartExtras(df *model.Dataframe, extras []*extra, warmup int) (overlay []strategy.IndicatorMetric,
	panes []strategy.ChartIndicator) {
	color := 0
	for _, e := range extras {
		var metrics []strategy.IndicatorMetric
		for _, s := range e.series {
			if s.style == "" {
				continue // sent, not drawn
			}
			metrics = append(metrics, strategy.IndicatorMetric{Values: df.Metadata[s.key], Name: s.name,
				Color: indicatorColors[color%len(indicatorColors)], Style: s.style})
			color++
		}
		if e.overlay {
			overlay = append(overlay, metrics...)
		} else if len(metrics) > 0 {
			panes = append(panes, strategy.ChartIndicator{GroupName: e.title, Time: df.Time, Warmup: warmup,
				Metrics: metrics})
		}
	}
	return overlay, panes
}

// highest returns the highest of the last length values, NaN until the
// window is full.
func highest(src []float64, length int) []float64 {
	return extreme(src, length, math.Max)
}

// lowest returns the lowest of the last length values, NaN until the window is
// full.
func lowest(src []float64, length int) []float64 {
	return extreme(src, length, math.Min)
}

func extreme(src []float64, length int, pick func(a, b float64) float64) []float64 {
	out := nans(len(src))
	for i := length - 1; i < len(src); i++ {
		value := src[i]
		for _, v := range src[i-length+1 : i] {
			value = pick(value, v)
		}
		out[i] = value
	}
	return out
}

// sideOf returns the side of the close to line for every candle: 1 above it,
// -1 below it, the side before on a tie, NaN before a line known: the trend of
// the indicators that flip to the other side of the price, eg: the Supertrend.
func sideOf(close, line []float64) []float64 {
	out := nans(len(close))
	side := math.NaN()
	for i := range close {
		if i >= len(line) || !known(line[i]) || line[i] == 0 {
			continue // talib writes 0 for the candles it lacks
		}
		switch {
		case close[i] > line[i]:
			side = 1
		case close[i] < line[i]:
			side = -1
		}
		out[i] = side
	}
	return out
}

// donchian returns the Donchian channel of length for every candle: the
// highest high and the lowest low of the length candles before it, not
// counting it, so that a close beyond them breaks the channel.
func donchian(high, low []float64, length int) (upper, lower []float64) {
	highs, lows := highest(high, length), lowest(low, length)
	upper, lower = nans(len(high)), nans(len(high))
	for i := 1; i < len(high); i++ {
		upper[i], lower[i] = highs[i-1], lows[i-1]
	}
	return upper, lower
}

// ichimoku returns the conversion line (Tenkan), the base line (Kijun), and the
// two spans of the cloud where they are drawn, kijun-1 candles after the one
// they are computed on, as TradingView does.
func ichimoku(high, low []float64, tenkanLen, kijunLen, senkouLen int) (tenkan, kijun, spanA, spanB []float64) {
	middle := func(length int) []float64 {
		highs, lows := highest(high, length), lowest(low, length)
		out := make([]float64, len(high))
		for i := range out {
			out[i] = (highs[i] + lows[i]) / 2
		}
		return out
	}
	tenkan, kijun = middle(tenkanLen), middle(kijunLen)
	b := middle(senkouLen)
	spanA, spanB = nans(len(high)), nans(len(high))
	for i := range high {
		if k := i - kijunLen + 1; k >= 0 {
			spanA[i], spanB[i] = (tenkan[k]+kijun[k])/2, b[k]
		}
	}
	return tenkan, kijun, spanA, spanB
}

// chandelier returns the side of the Chandelier Exit for every candle, 1 once
// the close crosses above the stop of the shorts (the lowest low of length
// candles plus mult ATR of length candles), -1 once it crosses below the stop
// of the longs (the highest high minus mult ATR), NaN before; and the stop of
// its side. Each stop only tightens while the close holds beyond it.
func chandelier(high, low, close []float64, length int, mult float64) (side, stop []float64) {
	atr := indicator.ATR(high, low, close, length)
	highs, lows := highest(high, length), lowest(low, length)
	side, stop = nans(len(close)), nans(len(close))
	longStop, shortStop, dir := math.NaN(), math.NaN(), math.NaN()
	for i := range close {
		if i < length || !known(highs[i]) || !known(lows[i]) || !(atr[i] > 0) {
			continue // talib writes 0 for the candles it lacks
		}
		long, short := highs[i]-mult*atr[i], lows[i]+mult*atr[i]
		if known(longStop) {
			if close[i-1] > longStop {
				long = math.Max(long, longStop)
			}
			if close[i-1] < shortStop {
				short = math.Min(short, shortStop)
			}
			switch {
			case close[i] > shortStop:
				dir = 1
			case close[i] < longStop:
				dir = -1
			}
		}
		longStop, shortStop = long, short
		side[i] = dir
		switch dir {
		case 1:
			stop[i] = longStop
		case -1:
			stop[i] = shortStop
		}
	}
	return side, stop
}

// rateOfChange returns the change of the close over length candles, in %.
func rateOfChange(close []float64, length int) []float64 {
	out := nans(len(close))
	for i := length; i < len(close); i++ {
		if close[i-length] != 0 {
			out[i] = (close[i]/close[i-length] - 1) * 100
		}
	}
	return out
}

// lastAnchor returns the last time of day at, in loc, at or before t.
func lastAnchor(t time.Time, at time.Duration, loc *time.Location) time.Time {
	local := t.In(loc)
	anchor := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).Add(at)
	if anchor.After(t) {
		anchor = time.Date(local.Year(), local.Month(), local.Day()-1, 0, 0, 0, 0, loc).Add(at)
	}
	return anchor
}

// anchoredVWAP returns the VWAP of the typical price, hlc3, from the last
// anchor, a time of day in loc, at or before each candle, as ta.vwap anchored
// to a session: NaN when the candles don't reach back to the anchor. A
// session without volume weighs its candles equally.
func anchoredVWAP(times []time.Time, high, low, close, volume []float64, anchor time.Duration,
	loc *time.Location) []float64 {
	out := nans(len(times))
	var (
		start              time.Time
		priceVolume, total float64
		prices             float64
		candles            int
		covered, started   bool
	)
	for i, t := range times {
		if at := lastAnchor(t, anchor, loc); !started || !at.Equal(start) {
			start, started = at, true
			covered = !times[0].After(at)
			priceVolume, total, prices, candles = 0, 0, 0, 0
		}
		if !covered {
			continue
		}
		price := (high[i] + low[i] + close[i]) / 3
		if i < len(volume) && volume[i] > 0 {
			priceVolume += price * volume[i]
			total += volume[i]
		}
		prices += price
		candles++
		if total > 0 {
			out[i] = priceVolume / total
		} else {
			out[i] = prices / float64(candles)
		}
	}
	return out
}

// sessionRange returns, for every candle opened from end to until of its day
// in loc, the high and the low of the candles of that day opened from start to
// end, and NaN for the other candles, or when the candles don't reach back to
// the start of the range: eg: the opening range of New York, 09:30 to 09:45,
// traded until 12:00.
func sessionRange(times []time.Time, high, low []float64, start, end, until time.Duration,
	loc *time.Location) (highs, lows []float64) {
	highs, lows = nans(len(times)), nans(len(times))
	var (
		day           time.Time
		rangeHigh     float64
		rangeLow      float64
		covered, seen bool
	)
	for i, t := range times {
		local := t.In(loc)
		midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		if !midnight.Equal(day) {
			day, seen = midnight, false
			covered = !times[0].After(midnight.Add(start))
		}
		clock := local.Sub(midnight)
		switch {
		case clock >= start && clock < end:
			if !seen {
				rangeHigh, rangeLow, seen = high[i], low[i], true
			} else {
				rangeHigh, rangeLow = math.Max(rangeHigh, high[i]), math.Min(rangeLow, low[i])
			}
		case clock >= end && clock < until && covered && seen:
			highs[i], lows[i] = rangeHigh, rangeLow
		}
	}
	return highs, lows
}
