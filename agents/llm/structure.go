package llm

import (
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/raykavin/xtrader/model"
)

// The price structure of the candles is computed by the code: a model scanning
// rows of numbers misses touches and makes up levels, so it is handed the
// swings, the levels they cluster into and the shape of the last candles, to
// reason on them.
const (
	// pivotStrength is the candles on each side a swing high must top (a
	// swing low bottom): a fractal of 2*pivotStrength+1 candles.
	pivotStrength = 3
	// levelTolerance is how close the swings of a level are, in ATR.
	levelTolerance = 0.3
	// maxSwings are the last swing highs and lows sent.
	maxSwings = 4
	// levelsPerSide are the levels sent on each side of the price.
	levelsPerSide = 4
	// rangeCandles are the candles of the recent range.
	rangeCandles = 20
	// shapeCandles are the last candles described.
	shapeCandles = 3
)

// Structure is the price structure of the last Candles candles: their swing
// highs and lows, the levels the swings cluster into, the round numbers near
// the price, the range of the last candles, the shape of the last ones, and
// the percentile of the volatility and of the volume of the last one.
type Structure struct {
	Candles        int     `json:"candles"`
	SwingStructure string  `json:"swing_structure"`
	SwingHighs     []Swing `json:"swing_highs"` // the last ones, newest first
	SwingLows      []Swing `json:"swing_lows"`
	// Levels are the prices where swings cluster, within levelTolerance ATR:
	// a support below the price, a resistance above, the nearest first.
	Levels           []Zone        `json:"levels"`
	RoundLevels      []number      `json:"round_levels"` // the round numbers around the price
	RecentRange      Box           `json:"recent_range"`
	LastCandles      []CandleShape `json:"last_candles"`      // newest first
	ATRPercentile    ratio         `json:"atr_percentile"`    // of the last ATR among the ones of the candles, 0 to 100
	VolumePercentile ratio         `json:"volume_percentile"` // of the last volume among the ones of the candles
}

// Swing is a swing high or low, candles ago.
type Swing struct {
	Price      number `json:"price"`
	CandlesAgo int    `json:"candles_ago"`
}

// Zone is a level of swings: their mean price, their span from Low to High,
// how many swings touched it, the last one how many candles ago, and its
// distance from the price in ATR, positive above.
type Zone struct {
	Kind        string `json:"kind"` // support or resistance
	Price       number `json:"price"`
	Low         number `json:"low"`
	High        number `json:"high"`
	Touches     int    `json:"touches"`
	LastTouch   int    `json:"last_touch_candles_ago"`
	DistanceATR ratio  `json:"distance_atr"`
}

// MarshalJSON leaves out the span of a zone of a single swing: its price.
func (z Zone) MarshalJSON() ([]byte, error) {
	type zone Zone // without this method
	if z.Low == z.High {
		return json.Marshal(struct {
			zone
			Low  *number `json:"low,omitempty"`
			High *number `json:"high,omitempty"`
		}{zone: zone(z)})
	}
	return json.Marshal(zone(z))
}

// Box is the range of the last Candles candles, its width in ATR.
type Box struct {
	Candles  int    `json:"candles"`
	High     number `json:"high"`
	Low      number `json:"low"`
	WidthATR ratio  `json:"width_atr"`
}

// CandleShape describes a candle, CandlesAgo before the last one: its
// direction, its range in ATR, its body and wicks in % of its range, where it
// closed in its range (0 at the low, 1 at the high), and its patterns.
type CandleShape struct {
	CandlesAgo   int    `json:"candles_ago"`
	Direction    string `json:"direction"` // up, down or flat
	RangeATR     ratio  `json:"range_atr"`
	BodyPct      ratio  `json:"body_pct"`
	UpperWickPct ratio  `json:"upper_wick_pct"`
	LowerWickPct ratio  `json:"lower_wick_pct"`
	CloseInRange ratio  `json:"close_in_range"`
	Pattern      string `json:"pattern,omitempty"`
}

// pivot is a swing of the candles: its index, its price, high or low.
type pivot struct {
	index int
	price float64
	high  bool
}

// computeStructure returns the structure of candles, oldest first, with atr
// the ATR of each (NaN or 0 while unknown), nil with too few candles for a
// swing.
func computeStructure(candles []model.Candle, atr []float64) *Structure {
	if len(candles) < 2*pivotStrength+1 {
		return nil
	}
	atr = positiveATR(atr)
	last := len(candles) - 1
	price := candles[last].Close
	width := lastKnown(atr)
	if !(width > 0) {
		width = averageRange(candles, 14) // the ATR unknown: the average range
	}

	found := pivots(candles, pivotStrength)
	var highs, lows []pivot
	for _, p := range found {
		if p.high {
			highs = append(highs, p)
		} else {
			lows = append(lows, p)
		}
	}
	structure := &Structure{
		Candles:        len(candles),
		SwingStructure: swingStructure(highs, lows),
		SwingHighs:     swings(highs, last),
		SwingLows:      swings(lows, last),
		Levels:         levels(found, width, price, last, levelsPerSide),
		RoundLevels:    roundLevels(price),
		RecentRange:    recentRange(candles, width),
		ATRPercentile:  ratio(percentile(atr, lastKnown(atr))),
	}
	volumes := make([]float64, len(candles))
	for i, candle := range candles {
		volumes[i] = candle.Volume
	}
	structure.VolumePercentile = ratio(percentile(volumes, candles[last].Volume))
	for ago := 0; ago < shapeCandles && ago < len(candles); ago++ {
		i := last - ago
		var previous *model.Candle
		if i > 0 {
			previous = &candles[i-1]
		}
		shape := describeCandle(candles[i], previous, width)
		shape.CandlesAgo = ago
		structure.LastCandles = append(structure.LastCandles, shape)
	}
	return structure
}

// pivots returns the swing highs and lows of candles: a high above the
// strength candles before it and at least the ones after (a flat top counts
// once), a low mirrored. The last strength candles can't be confirmed yet.
func pivots(candles []model.Candle, strength int) []pivot {
	var found []pivot
	for i := strength; i < len(candles)-strength; i++ {
		high, low := true, true
		for j := i - strength; j <= i+strength && (high || low); j++ {
			switch {
			case j < i:
				high = high && candles[i].High > candles[j].High
				low = low && candles[i].Low < candles[j].Low
			case j > i:
				high = high && candles[i].High >= candles[j].High
				low = low && candles[i].Low <= candles[j].Low
			}
		}
		if high {
			found = append(found, pivot{index: i, price: candles[i].High, high: true})
		}
		if low {
			found = append(found, pivot{index: i, price: candles[i].Low})
		}
	}
	return found
}

// swingStructure names the sequence of the last two swing highs and lows.
func swingStructure(highs, lows []pivot) string {
	if len(highs) < 2 || len(lows) < 2 {
		return "unclear: too few swings"
	}
	higherHigh := highs[len(highs)-1].price > highs[len(highs)-2].price
	higherLow := lows[len(lows)-1].price > lows[len(lows)-2].price
	switch {
	case higherHigh && higherLow:
		return "higher highs and higher lows (uptrend)"
	case !higherHigh && !higherLow:
		return "lower highs and lower lows (downtrend)"
	case higherHigh:
		return "higher high and lower low (expanding)"
	default:
		return "lower high and higher low (contracting)"
	}
}

// swings returns the last maxSwings of found, newest first.
func swings(found []pivot, last int) []Swing {
	list := make([]Swing, 0, maxSwings)
	for i := len(found) - 1; i >= 0 && len(list) < maxSwings; i-- {
		list = append(list, Swing{Price: number(found[i].price), CandlesAgo: last - found[i].index})
	}
	return list
}

// levels clusters the swings, highs and lows alike (a broken level swaps its
// role), within levelTolerance ATR of the mean of their cluster, and returns
// perSide of them on each side of price, the nearest first: the ones touched
// twice at least, completed with single swings on a side that has fewer than
// two.
func levels(found []pivot, atr, price float64, last, perSide int) []Zone {
	if len(found) == 0 || !(atr > 0) {
		return []Zone{}
	}
	sorted := slices.Clone(found)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].price < sorted[j].price })

	type cluster struct {
		sum, low, high     float64
		touches, lastIndex int
	}
	var clusters []cluster
	for _, p := range sorted {
		if n := len(clusters); n > 0 && p.price-clusters[n-1].sum/float64(clusters[n-1].touches) <= levelTolerance*atr {
			c := &clusters[n-1]
			c.sum, c.high, c.touches = c.sum+p.price, p.price, c.touches+1
			c.lastIndex = max(c.lastIndex, p.index)
			continue
		}
		clusters = append(clusters, cluster{sum: p.price, low: p.price, high: p.price, touches: 1, lastIndex: p.index})
	}

	zones := make([]Zone, 0, len(clusters))
	for _, c := range clusters {
		mean := c.sum / float64(c.touches)
		kind := LevelResistance
		if mean < price {
			kind = LevelSupport
		}
		zones = append(zones, Zone{Kind: kind, Price: number(mean), Low: number(c.low), High: number(c.high),
			Touches: c.touches, LastTouch: last - c.lastIndex, DistanceATR: ratio((mean - price) / atr)})
	}
	nearest := func(a, b Zone) bool { return math.Abs(float64(a.DistanceATR)) < math.Abs(float64(b.DistanceATR)) }
	sort.SliceStable(zones, func(i, j int) bool { return nearest(zones[i], zones[j]) })

	var chosen []Zone
	for _, kind := range []string{LevelSupport, LevelResistance} {
		var strong, weak []Zone
		for _, zone := range zones {
			switch {
			case zone.Kind != kind:
			case zone.Touches >= 2:
				strong = append(strong, zone)
			default:
				weak = append(weak, zone)
			}
		}
		side := strong
		for _, zone := range weak {
			if len(side) >= 2 {
				break
			}
			side = append(side, zone)
		}
		sort.SliceStable(side, func(i, j int) bool { return nearest(side[i], side[j]) })
		if len(side) > perSide {
			side = side[:perSide]
		}
		chosen = append(chosen, side...)
	}
	sort.SliceStable(chosen, func(i, j int) bool { return nearest(chosen[i], chosen[j]) })
	return chosen
}

// roundLevels returns the round numbers around price: the multiples of the
// power of ten a digit below its size, and of its half, eg: 84000, 84500 and
// 85000 for 84200.
func roundLevels(price float64) []number {
	if !(price > 0) {
		return []number{}
	}
	step := math.Pow(10, math.Floor(math.Log10(price))-1)
	var found []float64
	for _, s := range []float64{step, step / 2} {
		below := math.Floor(price/s) * s
		for _, level := range []float64{below, below + s} {
			level = math.Round(level/s) * s // the float error of the division
			if !slices.ContainsFunc(found, func(f float64) bool { return math.Abs(f-level) < s/1e6 }) {
				found = append(found, level)
			}
		}
	}
	slices.Sort(found)
	levels := make([]number, len(found))
	for i, level := range found {
		levels[i] = number(level)
	}
	return levels
}

// recentRange returns the range of the last rangeCandles candles.
func recentRange(candles []model.Candle, atr float64) Box {
	start := max(len(candles)-rangeCandles, 0)
	high, low := candles[start].High, candles[start].Low
	for _, candle := range candles[start+1:] {
		high, low = math.Max(high, candle.High), math.Min(low, candle.Low)
	}
	box := Box{Candles: len(candles) - start, High: number(high), Low: number(low), WidthATR: ratio(math.NaN())}
	if atr > 0 {
		box.WidthATR = ratio((high - low) / atr)
	}
	return box
}

// describeCandle returns the shape of candle, previous the one before it (nil
// for the first), with atr the ATR.
func describeCandle(candle model.Candle, previous *model.Candle, atr float64) CandleShape {
	nan := ratio(math.NaN())
	shape := CandleShape{Direction: "flat", RangeATR: nan, BodyPct: nan, UpperWickPct: nan, LowerWickPct: nan,
		CloseInRange: nan}
	switch {
	case candle.Close > candle.Open:
		shape.Direction = "up"
	case candle.Close < candle.Open:
		shape.Direction = "down"
	}
	span := candle.High - candle.Low
	body := math.Abs(candle.Close - candle.Open)
	upper := candle.High - math.Max(candle.Open, candle.Close)
	lower := math.Min(candle.Open, candle.Close) - candle.Low
	if atr > 0 {
		shape.RangeATR = ratio(span / atr)
	}
	if !(span > 0) {
		return shape
	}
	shape.BodyPct = ratio(body / span * 100)
	shape.UpperWickPct = ratio(upper / span * 100)
	shape.LowerWickPct = ratio(lower / span * 100)
	shape.CloseInRange = ratio((candle.Close - candle.Low) / span)

	var patterns []string
	switch {
	case body <= 0.1*span:
		patterns = append(patterns, "doji")
	case lower >= 0.6*span && body <= 0.35*span:
		patterns = append(patterns, "bullish pin bar (long lower wick)")
	case upper >= 0.6*span && body <= 0.35*span:
		patterns = append(patterns, "bearish pin bar (long upper wick)")
	case body >= 0.7*span && atr > 0 && span >= 1.2*atr:
		patterns = append(patterns, "strong "+map[bool]string{true: "bullish", false: "bearish"}[shape.Direction == "up"]+
			" candle")
	}
	if previous != nil {
		bullish, bearish := candle.Close > candle.Open, candle.Close < candle.Open
		previousUp, previousDown := previous.Close > previous.Open, previous.Close < previous.Open
		switch {
		case bullish && previousDown && candle.Close >= previous.Open && candle.Open <= previous.Close:
			patterns = append(patterns, "bullish engulfing")
		case bearish && previousUp && candle.Close <= previous.Open && candle.Open >= previous.Close:
			patterns = append(patterns, "bearish engulfing")
		case candle.High <= previous.High && candle.Low >= previous.Low:
			patterns = append(patterns, "inside bar")
		case candle.High > previous.High && candle.Low < previous.Low:
			patterns = append(patterns, "outside bar")
		}
	}
	shape.Pattern = strings.Join(patterns, ", ")
	return shape
}

// percentile returns the share of the known values of series at most v, in %,
// NaN without any or with v unknown.
func percentile(series []float64, v float64) float64 {
	if !known(v) {
		return math.NaN()
	}
	var count, below int
	for _, value := range series {
		if !known(value) {
			continue
		}
		count++
		if value <= v {
			below++
		}
	}
	if count == 0 {
		return math.NaN()
	}
	return float64(below) / float64(count) * 100
}

// positiveATR returns a copy of atr with the values at or below 0 unknown,
// NaN: the ones talib writes while it warms up, eg: 0 over the first ATRLen
// candles, which would weigh in the percentile as the quietest ones.
func positiveATR(atr []float64) []float64 {
	out := make([]float64, len(atr))
	for i, v := range atr {
		out[i] = v
		if !(v > 0) {
			out[i] = math.NaN()
		}
	}
	return out
}

// lastKnown returns the last known value of series, NaN without any.
func lastKnown(series []float64) float64 {
	for i := len(series) - 1; i >= 0; i-- {
		if known(series[i]) {
			return series[i]
		}
	}
	return math.NaN()
}

// averageRange returns the average range, high to low, of the last n candles.
func averageRange(candles []model.Candle, n int) float64 {
	start := max(len(candles)-n, 0)
	var sum float64
	for _, candle := range candles[start:] {
		sum += candle.High - candle.Low
	}
	if count := len(candles) - start; count > 0 {
		return sum / float64(count)
	}
	return math.NaN()
}
