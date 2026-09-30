package llm

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/raykavin/xtrader/indicator"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/tools/log"
)

const (
	// frameCandles are the candles of a higher timeframe its levels and its
	// trend are computed on.
	frameCandles = 200
	// frameFast and frameSlow are the EMAs of the trend of a higher timeframe
	// and of a correlated market.
	frameFast = 20
	frameSlow = 50
	// frameLevelsPerSide are the levels of a higher timeframe sent on each
	// side of the price.
	frameLevelsPerSide = 3
	// correlationCandles are the candles of a correlated market read, and
	// minCorrelated the returns both markets must share for a correlation.
	correlationCandles = 100
	minCorrelated      = 20
	// fetchTimeout bounds a read of candles.
	fetchTimeout = 10 * time.Second
)

// CandleSource is where the Trader reads the candles of the other timeframes
// of the pair and of the correlated markets, see SetFeeder: the Feeder of the
// exchange, whose CandlesByLimit returns the last complete candles.
type CandleSource interface {
	CandlesByLimit(ctx context.Context, pair, period string, limit int) ([]model.Candle, error)
}

// Frame is a higher timeframe of the pair: its trend (EMA 20 against EMA 50,
// and the close against the EMA 50) with the last two values of both EMAs, its
// swing structure, its ATR, its levels (see Structure.Levels) around the price
// of now, and its last candles.
type Frame struct {
	Timeframe      string   `json:"timeframe"`
	Trend          string   `json:"trend"`
	EMAFast        []number `json:"ema_20,omitempty"` // frameFast
	EMASlow        []number `json:"ema_50,omitempty"` // frameSlow
	SwingStructure string   `json:"swing_structure"`
	ATR            number   `json:"atr"`
	Levels         []Zone   `json:"levels"`
	Candles        Candles  `json:"candles"`
}

// Reference holds the levels of the previous day and week, and of the day so
// far (UTC days), which the market watches.
type Reference struct {
	PreviousDayHigh  number `json:"previous_day_high"`
	PreviousDayLow   number `json:"previous_day_low"`
	PreviousDayClose number `json:"previous_day_close"`
	PreviousWeekHigh number `json:"previous_week_high"`
	PreviousWeekLow  number `json:"previous_week_low"`
	DayOpen          number `json:"day_open"`
	DayHigh          number `json:"day_high"`
	DayLow           number `json:"day_low"`
}

// Correlated is a market correlated with the pair, on its timeframe: its last
// close, its change over the candles of the context and over the last 4, its
// trend, and the correlation of its returns with the ones of the pair (-1 to
// 1) over the candles both have. MarketClosed is set when its last candle is
// older than two periods, eg: an index on a weekend.
type Correlated struct {
	Symbol       string `json:"symbol"`
	Price        number `json:"price"`
	ChangePct    ratio  `json:"change_pct"`
	RecentPct    ratio  `json:"change_last_4_pct"`
	Trend        string `json:"trend"`
	Correlation  ratio  `json:"correlation"`
	MarketClosed bool   `json:"market_closed,omitempty"`
}

// fetched are the candles read of a pair and timeframe, kept until the close
// of the next candle, when a newer one is complete.
type fetched struct {
	candles []model.Candle
	until   time.Time
}

// SetFeeder sets where the Trader reads the candles of the higher timeframes
// of the pair (Config.HigherTimeframes, and the daily and weekly levels) and of
// the correlated markets (Config.Correlated): the Feeder of the exchange, live
// or on paper. Without it, before the bot runs, the context goes without them,
// as in a backtest.
func (t *Trader) SetFeeder(feeder CandleSource) {
	t.feeder = feeder
}

// defaultHigherTimeframes returns the higher timeframes of timeframe: two
// above it, the ones a trader of it watches.
func defaultHigherTimeframes(timeframe string) []string {
	period, err := timeframeDuration(timeframe)
	if err != nil {
		return nil
	}
	switch {
	case period <= 5*time.Minute:
		return []string{"15m", "1h"}
	case period <= 30*time.Minute:
		return []string{"1h", "4h"}
	case period <= 2*time.Hour:
		return []string{"4h", "1d"}
	case period <= 4*time.Hour:
		return []string{"1d", "1w"}
	default:
		return []string{"1w"}
	}
}

// fetch returns the last limit complete candles of pair on timeframe, read
// from the feeder once per candle: kept until the close of the next one, or a
// few minutes when the market is closed, a minute after a failure. Nil
// without a feeder or on a failure, logged.
func (t *Trader) fetch(pair, timeframe string, limit int) []model.Candle {
	if t.feeder == nil {
		return nil
	}
	key := fmt.Sprintf("%s|%s|%d", pair, timeframe, limit)
	now := t.now()
	if cached, ok := t.fetched[key]; ok && now.Before(cached.until) {
		return cached.candles
	}
	period, err := timeframeDuration(timeframe)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	candles, err := t.feeder.CandlesByLimit(ctx, pair, timeframe, limit)
	if err != nil {
		log.Warnf("[LLM] %s %s: the candles of the context: %v", pair, timeframe, err)
		t.fetched[key] = fetched{until: now.Add(time.Minute)}
		return nil
	}
	until := now.Add(min(period, 5*time.Minute)) // a market closed: read again in a while
	if n := len(candles); n > 0 {
		if next := candles[n-1].Time.Add(2 * period); next.After(now) {
			until = next // the close of the next candle
		}
	}
	t.fetched[key] = fetched{candles: candles, until: until}
	return candles
}

// frames returns the higher timeframes of pair, their levels around price.
func (t *Trader) frames(pair string, price float64) []Frame {
	var frames []Frame
	for _, timeframe := range t.higher {
		candles := t.fetch(pair, timeframe, frameCandles)
		if len(candles) < 2*pivotStrength+1 {
			continue
		}
		frames = append(frames, t.frame(timeframe, candles, price))
	}
	return frames
}

// frame returns the summary of the candles of timeframe, oldest first.
func (t *Trader) frame(timeframe string, candles []model.Candle, price float64) Frame {
	closes, highs, lows := columns(candles)
	atr := nans(len(candles))
	if len(candles) > t.cfg.ATRLen {
		atr = indicator.ATR(highs, lows, closes, t.cfg.ATRLen)
	}
	width := lastKnown(atr)
	if !(width > 0) {
		width = averageRange(candles, t.cfg.ATRLen)
	}
	found := pivots(candles, pivotStrength)
	var swingHighs, swingLows []pivot
	for _, p := range found {
		if p.high {
			swingHighs = append(swingHighs, p)
		} else {
			swingLows = append(swingLows, p)
		}
	}
	sent := candles[max(len(candles)-t.cfg.HigherCandles, 0):]
	var fast, slow []number
	if len(closes) > frameSlow {
		fast, slow = lastNumbers(indicator.EMA(closes, frameFast), 2), lastNumbers(indicator.EMA(closes, frameSlow), 2)
	}
	return Frame{
		Timeframe:      timeframe,
		Trend:          trendOf(closes),
		EMAFast:        fast,
		EMASlow:        slow,
		SwingStructure: swingStructure(swingHighs, swingLows),
		ATR:            number(width),
		Levels:         levels(found, width, price, len(candles)-1, frameLevelsPerSide),
		Candles:        Candles(sent),
	}
}

// reference returns the levels of the previous day and week of pair, and of
// the day of at so far, from the daily, weekly and hourly candles, and the
// candles of df of the day after the last hourly one. Nil when none is known.
func (t *Trader) reference(df *model.Dataframe, at time.Time) *Reference {
	days := t.fetch(df.Pair, "1d", 3)
	weeks := t.fetch(df.Pair, "1w", 2)
	hours := t.fetch(df.Pair, "1h", 30)
	nan := number(math.NaN())
	ref := &Reference{PreviousDayHigh: nan, PreviousDayLow: nan, PreviousDayClose: nan, PreviousWeekHigh: nan,
		PreviousWeekLow: nan, DayOpen: nan, DayHigh: nan, DayLow: nan}
	found := false

	at = at.UTC()
	today := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	for i := len(days) - 1; i >= 0; i-- {
		if days[i].Time.Before(today) {
			ref.PreviousDayHigh, ref.PreviousDayLow = number(days[i].High), number(days[i].Low)
			ref.PreviousDayClose = number(days[i].Close)
			found = true
			break
		}
	}
	if n := len(weeks); n > 0 {
		ref.PreviousWeekHigh, ref.PreviousWeekLow = number(weeks[n-1].High), number(weeks[n-1].Low)
		found = true
	}

	// the day so far: its hourly candles, then the ones of df after them
	open, high, low := math.NaN(), math.Inf(-1), math.Inf(1)
	covered := today
	add := func(o, h, l float64) {
		if math.IsNaN(open) {
			open = o
		}
		high, low = math.Max(high, h), math.Min(low, l)
	}
	for _, candle := range hours {
		if !candle.Time.Before(today) {
			add(candle.Open, candle.High, candle.Low)
			covered = candle.Time.Add(time.Hour)
		}
	}
	for i := range df.Close {
		if i < len(df.Time) && !df.Time[i].Before(covered) && i < len(df.Open) && i < len(df.High) &&
			i < len(df.Low) {
			add(df.Open[i], df.High[i], df.Low[i])
		}
	}
	if !math.IsNaN(open) {
		ref.DayOpen, ref.DayHigh, ref.DayLow = number(open), number(high), number(low)
		found = true
	}
	if !found {
		return nil
	}
	return ref
}

// correlated returns the correlated markets of the pair of df, on its
// timeframe, the pair itself left out: one list for all the pairs traded.
func (t *Trader) correlated(df *model.Dataframe) []Correlated {
	symbols := t.cfg.Correlated[df.Pair]
	if len(symbols) == 0 {
		return nil
	}
	period, err := timeframeDuration(t.cfg.Timeframe)
	if err != nil {
		return nil
	}
	pair := returnsByTime(df.Time, df.Close)
	var list []Correlated
	for _, symbol := range symbols {
		if symbol == df.Pair {
			continue
		}
		candles := t.fetch(symbol, t.cfg.Timeframe, correlationCandles)
		if len(candles) < 2 {
			continue
		}
		closes, _, _ := columns(candles)
		last := candles[len(candles)-1]
		window := closes[max(len(closes)-t.cfg.Candles, 0):]
		recent := closes[max(len(closes)-5, 0):]
		c := Correlated{
			Symbol:       symbol,
			Price:        number(last.Close),
			ChangePct:    ratio(changePct(window)),
			RecentPct:    ratio(changePct(recent)),
			Trend:        trendOf(closes),
			Correlation:  ratio(math.NaN()),
			MarketClosed: t.now().Sub(last.Time.Add(period)) > 2*period,
		}
		times := make([]time.Time, len(candles))
		for i, candle := range candles {
			times[i] = candle.Time
		}
		other := returnsByTime(times, closes)
		var a, b []float64
		for at, r := range other {
			if s, ok := pair[at]; ok {
				a, b = append(a, s), append(b, r)
			}
		}
		if len(a) >= minCorrelated {
			c.Correlation = ratio(pearson(a, b))
		}
		list = append(list, c)
	}
	return list
}

// trendOf returns the trend of closes: up with the EMA 20 above the EMA 50
// and the close above the EMA 50, down with both below, else mixed; unknown
// with fewer closes than the EMA 50 needs.
func trendOf(closes []float64) string {
	if len(closes) <= frameSlow {
		return "unknown"
	}
	fast, slow := indicator.EMA(closes, frameFast), indicator.EMA(closes, frameSlow)
	f, s, c := fast[len(fast)-1], slow[len(slow)-1], closes[len(closes)-1]
	switch {
	case !known(f) || !known(s):
		return "unknown"
	case f > s && c > s:
		return "up"
	case f < s && c < s:
		return "down"
	}
	return "mixed"
}

// columns returns the closes, highs and lows of candles.
func columns(candles []model.Candle) (closes, highs, lows []float64) {
	closes, highs, lows = make([]float64, len(candles)), make([]float64, len(candles)), make([]float64, len(candles))
	for i, candle := range candles {
		closes[i], highs[i], lows[i] = candle.Close, candle.High, candle.Low
	}
	return closes, highs, lows
}

// nans returns n NaNs.
func nans(n int) []float64 {
	values := make([]float64, n)
	for i := range values {
		values[i] = math.NaN()
	}
	return values
}

// changePct returns the change of closes from the first to the last, in %.
func changePct(closes []float64) float64 {
	if len(closes) < 2 || !(closes[0] > 0) {
		return math.NaN()
	}
	return (closes[len(closes)-1]/closes[0] - 1) * 100
}

// returnsByTime returns the log returns of closes by the time of their candle.
func returnsByTime(times []time.Time, closes []float64) map[time.Time]float64 {
	returns := make(map[time.Time]float64, len(closes))
	for i := 1; i < len(closes) && i < len(times); i++ {
		if closes[i-1] > 0 && closes[i] > 0 {
			returns[times[i].UTC()] = math.Log(closes[i] / closes[i-1])
		}
	}
	return returns
}

// pearson returns the correlation of a and b, of the same length, NaN when one
// doesn't vary.
func pearson(a, b []float64) float64 {
	n := float64(len(a))
	var sa, sb float64
	for i := range a {
		sa, sb = sa+a[i], sb+b[i]
	}
	ma, mb := sa/n, sb/n
	var cov, va, vb float64
	for i := range a {
		da, db := a[i]-ma, b[i]-mb
		cov, va, vb = cov+da*db, va+da*da, vb+db*db
	}
	if va == 0 || vb == 0 {
		return math.NaN()
	}
	return cov / math.Sqrt(va*vb)
}
