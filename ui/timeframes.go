package ui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/raykavin/xtrader/model"
)

// ViewTimeframes are the timeframes the dashboard shows the chart of a pair
// on, besides the one of the strategy: the ones the trader selects, D the
// daily one. Showing one changes what is drawn only: the strategy, its
// language model included, trades on its own timeframe, see
// CandlesResponse.ExecutionTimeframe.
var ViewTimeframes = []string{"1m", "5m", "15m", "30m", "4h", "1d"}

// CandleSource is where the chart reads the candles of the timeframes other
// than the one of the strategy, see WithCandleSource: the Feeder of the
// exchange, whose CandlesByLimit returns the last complete candles of a
// timeframe, eg: MT5. It is never a feed consuming its candles, eg: the CSV
// files of a backtest.
type CandleSource interface {
	CandlesByLimit(ctx context.Context, pair, period string, limit int) ([]model.Candle, error)
}

// WithCandleSource sets where the chart reads the candles of the timeframes
// shown other than the one of the strategy, see GET /api/{pair}/candles:
// without it, only the timeframes above the one of the strategy, multiples of
// it, are shown, built from its candles.
func WithCandleSource(source CandleSource) Option {
	return func(c *Chart) { c.source = source }
}

// The candles of the timeframes shown: how many by default and at most, how
// long the candles read from the source are kept (the dashboards refresh them
// every few seconds, the bridge read once for all of them), how long a read
// may take, and the prices of the strategy kept for the candles in progress
// of the timeframes below its own, see candlesInProgress.
const (
	viewCandles     = 1000
	maxViewCandles  = ChartCandles
	viewCacheTTL    = 2 * time.Second
	viewReadTimeout = 15 * time.Second
	ticksKept       = 25 * time.Hour
	maxTicks        = 100000
)

// The sources of the candles of CandlesResponse.
const (
	SourceStrategy  = "strategy"  // the candles of the strategy, its timeframe
	SourceBroker    = "broker"    // read from the broker, the candle in progress built by the chart
	SourceResampled = "resampled" // built from the candles of the strategy
)

// ErrTimeframeUnavailable is returned for a timeframe the chart can't show,
// eg: below the one of the strategy without a CandleSource.
var ErrTimeframeUnavailable = errors.New("timeframe unavailable")

// tick is a price of the strategy the chart received, at the time it did.
type tick struct {
	at    time.Time
	price float64
}

// viewCached is the candles read from the source for a pair, a timeframe and a
// limit, until its expiry.
type viewCached struct {
	candles []model.Candle
	until   time.Time
}

// timeframePeriod returns the period of timeframe, eg: 4 hours for 4h, 24 for
// 1d; false for one the chart doesn't know, eg: of months.
func timeframePeriod(timeframe string) (time.Duration, bool) {
	if len(timeframe) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(timeframe[:len(timeframe)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	switch timeframe[len(timeframe)-1] {
	case 'm':
		return time.Duration(n) * time.Minute, true
	case 'h':
		return time.Duration(n) * time.Hour, true
	case 'd':
		return time.Duration(n) * 24 * time.Hour, true
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, true
	}
	return 0, false
}

// normalizeTimeframe returns the timeframe of the query, the daily one as 1d
// whatever it is written, eg: D or 1D.
func normalizeTimeframe(value string) string {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "d", "1d", "d1":
		return "1d"
	}
	if n := len(value); n > 1 && value[n-1] != 'M' { // 1M are months
		return strings.ToLower(value)
	}
	return value
}

// executionTimeframe returns the timeframe of the strategy, the one it trades
// on, empty without a strategy.
func (c *Chart) executionTimeframe() string {
	if c.strategy == nil {
		return ""
	}
	return c.strategy.Timeframe()
}

// resamples reports whether the candles of timeframe are built from the ones
// of the strategy, of execution: a period above its own, a multiple of it.
func resamples(timeframe, execution string) bool {
	period, ok := timeframePeriod(timeframe)
	base, known := timeframePeriod(execution)
	return ok && known && period > base && period%base == 0
}

// viewTimeframes returns the timeframes the chart shows a pair on: the ones of
// ViewTimeframes, and the one of the strategy, by their period, each
// available when its candles can be read: the one of the strategy always, the
// others with a CandleSource, or built from the candles of the strategy.
func (c *Chart) viewTimeframes() []ViewTimeframe {
	execution := c.executionTimeframe()
	list := slices.Clone(ViewTimeframes)
	if execution != "" && !slices.Contains(list, execution) {
		list = append(list, execution)
	}
	slices.SortStableFunc(list, func(a, b string) int {
		pa, _ := timeframePeriod(a)
		pb, _ := timeframePeriod(b)
		return int((pa - pb) / time.Second)
	})
	views := make([]ViewTimeframe, 0, len(list))
	for _, timeframe := range list {
		views = append(views, ViewTimeframe{Timeframe: timeframe, Execution: timeframe == execution,
			Available: c.viewable(timeframe, execution)})
	}
	return views
}

// viewable reports whether the chart can show the candles of timeframe, the
// strategy trading on execution.
func (c *Chart) viewable(timeframe, execution string) bool {
	if timeframe == execution {
		return true
	}
	if !slices.Contains(ViewTimeframes, timeframe) {
		return false
	}
	return c.source != nil || resamples(timeframe, execution)
}

// recordTick keeps the price of the strategy received now on pair, for the
// candles in progress of the timeframes below its own, see candlesInProgress:
// ticksKept at most, maxTicks of them. Only with a CandleSource, which the
// other timeframes are read from. The caller holds c.mu.
func (c *Chart) recordTick(pair string, price float64) {
	if c.source == nil || !finite(price) || price <= 0 {
		return
	}
	now := c.clock()
	ticks := append(c.ticks[pair], tick{at: now, price: price})
	drop := 0
	for drop < len(ticks) && (now.Sub(ticks[drop].at) > ticksKept || len(ticks)-drop > maxTicks) {
		drop++
	}
	c.ticks[pair] = ticks[drop:]
}

// handleCandles replies the candles of the pair of the request on the
// timeframe of its query (the one of the strategy without it), limit of its
// query at most (viewCandles by default), see CandlesResponse.
func (c *Chart) handleCandles(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	pair, known := c.resolvePair(chi.URLParam(r, "pair"))
	execution := c.executionTimeframe()
	c.mu.Unlock()
	if !known {
		writeJSONError(w, http.StatusNotFound, "unknown pair")
		return
	}
	timeframe := normalizeTimeframe(r.URL.Query().Get("timeframe"))
	if timeframe == "" {
		timeframe = execution
	}
	limit := viewCandles
	if value := r.URL.Query().Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			writeJSONError(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = min(n, maxViewCandles)
	}
	if timeframe != execution && !slices.Contains(ViewTimeframes, timeframe) {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("timeframe %q: one of %s, or the one of the strategy, %s",
			timeframe, strings.Join(ViewTimeframes, ", "), execution))
		return
	}

	candles, source, err := c.viewCandles(r.Context(), pair, timeframe, execution, limit)
	switch {
	case errors.Is(err, ErrTimeframeUnavailable):
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, CandlesResponse{Pair: pair, Timeframe: timeframe, ExecutionTimeframe: execution, Source: source,
		Candles: candles})
}

// viewCandles returns the candles of pair on timeframe, the strategy trading on
// execution, oldest first, limit at most, the last one in progress, and where
// they come from: the ones of the strategy on its own timeframe; else the
// complete ones of the CandleSource, followed by the ones in progress since
// (see candlesInProgress); else the ones of the strategy resampled, see
// resample.
func (c *Chart) viewCandles(ctx context.Context, pair, timeframe, execution string, limit int) ([]Candle,
	string, error) {

	if timeframe == execution {
		c.mu.Lock()
		candles := c.withPartial(pair)
		c.mu.Unlock()
		return candles[max(len(candles)-limit, 0):], SourceStrategy, nil
	}
	period, _ := timeframePeriod(timeframe)
	if c.source == nil {
		if !resamples(timeframe, execution) {
			return nil, "", fmt.Errorf("%w: %s is below the timeframe of the strategy, %s, and the agent reads no "+
				"other one in this mode", ErrTimeframeUnavailable, timeframe, execution)
		}
		c.mu.Lock()
		candles := resample(c.withPartial(pair), period, time.Time{})
		c.mu.Unlock()
		return candles[max(len(candles)-limit, 0):], SourceResampled, nil
	}

	read, err := c.readView(ctx, pair, timeframe, limit)
	if err != nil {
		return nil, "", fmt.Errorf("the candles %s of %s: %w", timeframe, pair, err)
	}
	candles := make([]Candle, 0, len(read)+1)
	for _, candle := range read {
		candles = append(candles, Candle{Time: candle.Time.Unix(), Open: candle.Open, High: candle.High,
			Low: candle.Low, Close: candle.Close, Volume: candle.Volume})
	}
	c.mu.Lock()
	candles = append(candles, c.candlesInProgress(pair, candles, period, execution)...)
	c.mu.Unlock()
	return candles[max(len(candles)-limit, 0):], SourceBroker, nil
}

// readView reads the last limit complete candles of pair on timeframe from the
// source, the ones read less than viewCacheTTL ago kept.
func (c *Chart) readView(ctx context.Context, pair, timeframe string, limit int) ([]model.Candle, error) {
	key := pair + "|" + timeframe + "|" + strconv.Itoa(limit)
	now := c.clock()
	c.viewMu.Lock()
	cached, ok := c.viewCache[key]
	c.viewMu.Unlock()
	if ok && now.Before(cached.until) {
		return cached.candles, nil
	}
	ctx, cancel := context.WithTimeout(ctx, viewReadTimeout)
	defer cancel()
	candles, err := c.source.CandlesByLimit(ctx, pair, timeframe, limit)
	if err != nil {
		return nil, err
	}
	c.viewMu.Lock()
	c.viewCache[key] = viewCached{candles: candles, until: now.Add(viewCacheTTL)}
	c.viewMu.Unlock()
	return candles, nil
}

// withPartial returns the candles of the strategy on pair with the one in
// progress, last. The caller holds c.mu.
func (c *Chart) withPartial(pair string) []Candle {
	candles := make([]Candle, len(c.candles[pair]), len(c.candles[pair])+1)
	copy(candles, c.candles[pair])
	if partial, ok := c.partial[pair]; ok && (len(candles) == 0 || partial.Time > candles[len(candles)-1].Time) {
		candles = append(candles, partial)
	}
	return candles
}

// candlesInProgress returns the candles of pair of period after the complete
// ones read, which the source doesn't send, in the grid of their times: above
// the timeframe of the strategy, execution, its candles of each (the one in
// progress included) merged, exact; below it, the prices of the strategy the
// chart received during each (see recordTick), the open the close before,
// their high and low the ones of the prices received (the moves between two
// of them unseen) and no volume. The caller holds c.mu.
func (c *Chart) candlesInProgress(pair string, read []Candle, period time.Duration, execution string) []Candle {
	base, _ := timeframePeriod(execution)
	var after time.Time // the start of the first period after the ones read
	if n := len(read); n > 0 {
		after = time.Unix(read[n-1].Time, 0).Add(period)
	}
	if period > base {
		return resample(c.withPartial(pair), period, after)
	}

	var candles []Candle
	previous := math.NaN() // the close before, the open of the next one
	if n := len(read); n > 0 {
		previous = read[n-1].Close
	}
	for _, t := range c.ticks[pair] {
		if t.at.Before(after) {
			continue
		}
		start := bucket(t.at, period, after)
		if n := len(candles); n == 0 || candles[n-1].Time != start.Unix() {
			open := t.price
			if !math.IsNaN(previous) {
				open = previous
			}
			candles = append(candles, Candle{Time: start.Unix(), Open: open, High: math.Max(open, t.price),
				Low: math.Min(open, t.price), Close: t.price})
		}
		last := &candles[len(candles)-1]
		last.High, last.Low, last.Close = math.Max(last.High, t.price), math.Min(last.Low, t.price), t.price
		previous = t.price
	}
	return candles
}

// bucket returns the start of the candle of period holding at: in the grid of
// the candles read, from origin, when known; else in the one of the Unix
// time, the days from midnight UTC.
func bucket(at time.Time, period time.Duration, origin time.Time) time.Time {
	if origin.IsZero() {
		return at.Truncate(period)
	}
	return origin.Add(at.Sub(origin) / period * period)
}

// resample returns the candles of period built from candles, oldest first, of
// a period below it: the ones starting at after, or all of them when zero,
// each with the open of its first candle, the close of its last, the high and
// the low of all of them and the sum of their volumes. The candles of a period
// start in the grid of after when set, else in the one of the Unix time.
func resample(candles []Candle, period time.Duration, after time.Time) []Candle {
	var out []Candle
	for _, candle := range candles {
		at := time.Unix(candle.Time, 0)
		if !after.IsZero() && at.Before(after) {
			continue
		}
		start := bucket(at, period, after).Unix()
		if n := len(out); n == 0 || out[n-1].Time != start {
			out = append(out, Candle{Time: start, Open: candle.Open, High: candle.High, Low: candle.Low,
				Close: candle.Close, Volume: candle.Volume})
			continue
		}
		last := &out[len(out)-1]
		last.High, last.Low = math.Max(last.High, candle.High), math.Min(last.Low, candle.Low)
		last.Close, last.Volume = candle.Close, last.Volume+candle.Volume
	}
	return out
}
