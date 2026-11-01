// Package metalabel is the meta-labeling of the entries of a strategy (Marcos
// López de Prado, "Advances in Financial Machine Learning", 2018, §3.6): the
// strategy (the primary model) decides the side of an entry, and a second
// model, trained on the outcomes of its past entries, tells the probability
// that an entry reaches its target before its stop, from features of the
// market at the entry. The entries whose probability is under a threshold are
// skipped (see the entry filter of the agents, -meta-model), the side never
// changed.
//
// The training data are the samples the agents record (-meta-record, see
// Sample): one per entry attempted, its features and, once known, its label by
// the triple-barrier method (AFML §3.4): the target touched first is 1, the
// stop touched first 0, and an entry still open after its vertical barrier
// (-meta-max-bars candles), or closed by the strategy before it, the sign of
// its result in R then. Train fits a logistic regression on them (see Model).
//
// Nothing here reads the future: the features of an entry come from the
// candles up to the one it was decided at, its label from the candles after.
package metalabel

import (
	"math"
	"slices"

	"github.com/raykavin/xtrader/agents/internal/ta"
	"github.com/raykavin/xtrader/model"
)

// Window is the number of candles the features are computed from: the last
// Window candles of the dataframe, so that a feature doesn't depend on how many
// more it holds (the recursive averages, eg: the EMA 50, are seeded on the
// first ones, their seed washed out over the rest). A dataframe of fewer
// candles has no features: the strategy wrapped by the filter is handed
// Window at least.
const Window = 250

// The lookbacks of the features: the ATR, the ADX and the RSI of Wilder of 14,
// the efficiency ratio of 10, the EMA of 50 and its slope over 10 candles, the
// Bollinger bands of 20 and 2 standard deviations whose width is ranked among
// the widths of the previous 100 candles, the return over 10 candles and the
// volume over its SMA of 20.
const (
	atrLen      = 14
	adxLen      = 14
	rsiLen      = 14
	erLen       = 10
	emaLen      = 50
	slopeBars   = 10
	bbLen       = 20
	bbDev       = 2.0
	bbRankBars  = 100
	returnBars  = 10
	volumeLen   = 20
	hoursPerDay = 24
	daysPerWeek = 7
)

// featureNames are the features, sorted, see Features.
var featureNames = []string{
	"adx14",
	"atr_pct",
	"bb_width_pct",
	"dow_cos",
	"dow_sin",
	"ema50_dist_atr",
	"ema50_dist_atr_side",
	"ema50_slope_atr",
	"ema50_slope_atr_side",
	"er10",
	"hour_cos",
	"hour_sin",
	"ret10_atr",
	"ret10_atr_side",
	"rsi14",
	"rsi14_side",
	"side",
	"vol_ratio",
}

// FeatureNames returns the names of the features of Features, sorted.
func FeatureNames() []string { return slices.Clone(featureNames) }

// Features returns the features of an entry of side decided on the last candle
// of df, from its last Window candles only (never a later one), false when df
// holds fewer, the side is neither buy nor sell, or a feature can't be
// computed (eg: a flat market without an ATR). Fixed and sorted (see
// FeatureNames), each of them:
//
//   - adx14: the ADX of Wilder of 14 (ta.DMI(14, 14));
//   - atr_pct: the ATR of 14 over the close, in % (100·ATR/close);
//   - bb_width_pct: the percentile, 0 to 100, of the width of the Bollinger
//     bands of 20 and 2 standard deviations (their distance over their middle)
//     among the widths of the 100 candles before: the share of them narrower;
//   - dow_sin, dow_cos: the day of the week of the open of the last candle,
//     UTC, on a circle (Sunday 0 to Saturday 6, over 7);
//   - ema50_dist_atr: the distance of the close from the EMA of 50, in ATR,
//     (close-EMA50)/ATR;
//   - ema50_slope_atr: the move of the EMA of 50 over the last 10 candles, in
//     ATR, (EMA50-EMA50[10 candles before])/ATR;
//   - er10: the efficiency ratio of Kaufman of 10, 0 to 1 (see
//     ta.EfficiencyRatio);
//   - hour_sin, hour_cos: the hour of the open of the last candle, UTC, its
//     minutes as a fraction, on a circle (over 24);
//   - ret10_atr: the return of the last 10 candles, in ATR,
//     (close-close[10 candles before])/ATR;
//   - rsi14: the RSI of Wilder of 14, 0 to 100;
//   - side: 1 for a buy, -1 for a sell;
//   - vol_ratio: the volume of the last candle over the SMA of 20 of the
//     volume, 1 without a volume (the SMA 0);
//   - ema50_dist_atr_side, ema50_slope_atr_side, ret10_atr_side: the three
//     above times side, positive in the direction of the entry; rsi14_side:
//     (rsi14-50) times side. A logistic regression is linear: without them, a
//     move up couldn't tell a buy from a sell.
func Features(df *model.Dataframe, side model.SideType) (map[string]float64, bool) {
	n := len(df.Close)
	if n < Window || len(df.High) != n || len(df.Low) != n || len(df.Time) != n {
		return nil, false
	}
	var sign float64
	switch side {
	case model.SideTypeBuy:
		sign = 1
	case model.SideTypeSell:
		sign = -1
	default:
		return nil, false
	}
	start := n - Window
	closes, high, low := df.Close[start:], df.High[start:], df.Low[start:]
	volume := make([]float64, Window)
	if len(df.Volume) == n {
		copy(volume, df.Volume[start:])
	}
	i := Window - 1
	price := closes[i]

	atr := ta.ATR(high, low, closes, atrLen)[i]
	if !(atr > 0) || !(price > 0) {
		return nil, false
	}
	_, _, adx := ta.DMI(high, low, closes, adxLen, adxLen)
	ema := ta.EMA(closes, emaLen)
	mid, deviation := ta.SMA(closes, bbLen), ta.Stdev(closes, bbLen)
	width := func(k int) float64 { return 2 * bbDev * deviation[k] / mid[k] }
	narrower := 0
	for k := i - bbRankBars; k <= i; k++ {
		if math.IsNaN(width(k)) || math.IsInf(width(k), 0) {
			return nil, false
		}
		if k < i && width(k) < width(i) {
			narrower++
		}
	}
	ratio := 1.0
	if average := ta.SMA(volume, volumeLen)[i]; average > 0 {
		ratio = volume[i] / average
	}
	at := df.Time[n-1].UTC()
	hour := float64(at.Hour()) + float64(at.Minute())/60
	day := float64(at.Weekday())
	rsi := ta.RSI(closes, rsiLen)[i]
	distance := (price - ema[i]) / atr
	slope := (ema[i] - ema[i-slopeBars]) / atr
	ret := (price - closes[i-returnBars]) / atr

	features := map[string]float64{
		"adx14":                adx[i],
		"atr_pct":              100 * atr / price,
		"bb_width_pct":         float64(narrower) / bbRankBars * 100,
		"dow_cos":              math.Cos(2 * math.Pi * day / daysPerWeek),
		"dow_sin":              math.Sin(2 * math.Pi * day / daysPerWeek),
		"ema50_dist_atr":       distance,
		"ema50_dist_atr_side":  distance * sign,
		"ema50_slope_atr":      slope,
		"ema50_slope_atr_side": slope * sign,
		"er10":                 ta.EfficiencyRatio(closes, erLen)[i],
		"hour_cos":             math.Cos(2 * math.Pi * hour / hoursPerDay),
		"hour_sin":             math.Sin(2 * math.Pi * hour / hoursPerDay),
		"ret10_atr":            ret,
		"ret10_atr_side":       ret * sign,
		"rsi14":                rsi,
		"rsi14_side":           (rsi - 50) * sign,
		"side":                 sign,
		"vol_ratio":            ratio,
	}
	for _, value := range features {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, false
		}
	}
	return features, true
}
