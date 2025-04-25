package exchange

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/tools/log"
)

// This file owns the market data of the MT5 adapter: the candles of /rates,
// bid prices timed at their open (UTC seconds), and the quotes of /tick.
// Every candle carries the ask and the spread of its close in
// Metadata["ask"] and Metadata["spread"]: the spread of the candle (spread of
// /rates x point), or of the last tick for the candle in progress. Every
// candle carries both keys, as the strategy controller indexes the metadata by
// the rows of the dataframe.

// mt5Timeframe is a timeframe of the terminal: its name for /rates and its
// period, 0 for the months.
type mt5Timeframe struct {
	name   string
	period time.Duration
}

// mt5Timeframes maps the xtrader timeframes to the ones of the terminal.
var mt5Timeframes = map[string]mt5Timeframe{
	"1m":  {"M1", time.Minute},
	"3m":  {"M3", 3 * time.Minute},
	"5m":  {"M5", 5 * time.Minute},
	"10m": {"M10", 10 * time.Minute},
	"15m": {"M15", 15 * time.Minute},
	"30m": {"M30", 30 * time.Minute},
	"1h":  {"H1", time.Hour},
	"2h":  {"H2", 2 * time.Hour},
	"4h":  {"H4", 4 * time.Hour},
	"1d":  {"D1", 24 * time.Hour},
	"1w":  {"W1", 7 * 24 * time.Hour},
	"1M":  {"MN1", 0},
}

func mt5Resolution(timeframe string) (mt5Timeframe, error) {
	frame, ok := mt5Timeframes[timeframe]
	if !ok {
		return mt5Timeframe{}, fmt.Errorf("mt5: unsupported timeframe %q", timeframe)
	}
	return frame, nil
}

// end returns the end of the candle opened at start.
func (f mt5Timeframe) end(start time.Time) time.Time {
	if f.period == 0 {
		return start.AddDate(0, 1, 0)
	}
	return start.Add(f.period)
}

// approx returns the period, a month counting 31 days.
func (f mt5Timeframe) approx() time.Duration {
	if f.period == 0 {
		return 31 * 24 * time.Hour
	}
	return f.period
}

// mt5Rate is a candle of GET /rates (copy_rates_*).
type mt5Rate struct {
	Time       int64   `json:"time"`
	Open       float64 `json:"open"`
	High       float64 `json:"high"`
	Low        float64 `json:"low"`
	Close      float64 `json:"close"`
	TickVolume float64 `json:"tick_volume"`
	Spread     float64 `json:"spread"`
	RealVolume float64 `json:"real_volume"`
}

// rates reads count candles, the latest ones from the position start back
// (0 for the one in progress).
func (m *MT5) rates(ctx context.Context, symbol string, frame mt5Timeframe, count, start int) ([]mt5Rate, error) {
	query := url.Values{
		"symbol":    {symbol},
		"timeframe": {frame.name},
		"count":     {strconv.Itoa(count)},
		"start":     {strconv.Itoa(start)},
	}
	var rates []mt5Rate
	if err := m.get(ctx, "/rates", query, &rates); err != nil {
		return nil, fmt.Errorf("mt5: reading the candles of %s: %w", symbol, err)
	}
	return rates, nil
}

// ratesRange reads the candles opened between from and to, included.
func (m *MT5) ratesRange(ctx context.Context, symbol string, frame mt5Timeframe, from, to time.Time) ([]mt5Rate,
	error) {
	query := url.Values{
		"symbol":    {symbol},
		"timeframe": {frame.name},
		"from":      {strconv.FormatInt(from.Unix(), 10)},
		"to":        {strconv.FormatInt(to.Unix(), 10)},
	}
	var rates []mt5Rate
	if err := m.get(ctx, "/rates", query, &rates); err != nil {
		return nil, fmt.Errorf("mt5: reading the candles of %s: %w", symbol, err)
	}
	return rates, nil
}

// toCandles converts candles of the terminal into xtrader ones, oldest first,
// rounded to the digits of the symbol: a candle is complete once a newer one
// started, or mt5CloseDelay after its end. tick, when known, sets the ask and
// the spread of the last candle when in progress.
func (m *MT5) toCandles(pair string, rates []mt5Rate, frame mt5Timeframe, now time.Time,
	tick *mt5Tick) []model.Candle {
	symbol := m.instrument(pair)
	digits := -1
	if symbol.Name != "" {
		digits = symbol.Digits
	}
	point := symbol.Point
	if point <= 0 && digits >= 0 {
		point = math.Pow10(-digits)
	}

	sort.SliceStable(rates, func(i, j int) bool { return rates[i].Time < rates[j].Time })
	candles := make([]model.Candle, 0, len(rates))
	for i, rate := range rates {
		start := time.Unix(rate.Time, 0).UTC()
		volume := rate.RealVolume
		if volume <= 0 {
			volume = rate.TickVolume
		}
		candle := model.Candle{
			Pair:      pair,
			Time:      start,
			UpdatedAt: start,
			Open:      mt5Round(rate.Open, digits),
			High:      mt5Round(rate.High, digits),
			Low:       mt5Round(rate.Low, digits),
			Close:     mt5Round(rate.Close, digits),
			Volume:    volume,
			Complete:  i < len(rates)-1 || !frame.end(start).Add(mt5CloseDelay).After(now),
		}
		ask := candle.Close + rate.Spread*point
		if tick != nil && !candle.Complete && i == len(rates)-1 && tick.Ask > 0 {
			ask = tick.Ask
			candle.UpdatedAt = now.UTC()
		}
		ask = mt5Round(ask, digits)
		candle.Metadata = map[string]float64{"ask": ask, "spread": mt5Round(ask-candle.Close, digits)}
		candles = append(candles, candle)
	}
	return candles
}

// LastQuote returns the bid of pair, as the candles, from its last tick.
func (m *MT5) LastQuote(ctx context.Context, pair string) (float64, error) {
	tick, err := m.quote(ctx, pair)
	if err != nil {
		return 0, err
	}
	if tick.Bid <= 0 {
		return 0, fmt.Errorf("mt5: no price for %s", pair)
	}
	return m.instrument(pair).price(tick.Bid), nil
}

// CandlesByLimit returns the last limit complete candles, oldest first: the
// latest limit+1 (the candle in progress dropped), read backwards by pages of
// mt5MaxRates from the positions of /rates, until enough candles or the start
// of the history. It records the last complete candle, where
// CandlesSubscription starts.
func (m *MT5) CandlesByLimit(ctx context.Context, pair, period string, limit int) ([]model.Candle, error) {
	frame, err := mt5Resolution(period)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []model.Candle{}, nil
	}
	m.awaitHistory(ctx, pair, frame)

	var rates []mt5Rate
	for start := 0; len(rates) < limit+1; {
		count := min(limit+1-len(rates), mt5MaxRates)
		page, err := m.rates(ctx, pair, frame, count, start)
		if err != nil {
			return nil, err
		}
		kept := page[:0]
		for _, rate := range page {
			if len(rates) == 0 || rate.Time < rates[0].Time {
				kept = append(kept, rate)
			}
		}
		rates = append(kept, rates...)
		if len(page) < count || len(kept) == 0 {
			break // the start of the history
		}
		start += len(page)
	}

	candles := m.toCandles(pair, rates, frame, time.Now(), nil)
	complete := candles[:0]
	for _, candle := range candles {
		if candle.Complete {
			complete = append(complete, candle)
		}
	}
	if len(complete) > limit {
		complete = complete[len(complete)-limit:]
	}
	if len(complete) > 0 {
		m.loadedMtx.Lock()
		m.loaded[pair+"--"+period] = complete[len(complete)-1].Time
		m.loadedMtx.Unlock()
	}
	return complete, nil
}

// awaitHistory waits, syncWait at most, for the last candle of the terminal
// to reach its last tick. Just opened, the terminal serves the history it had
// cached, eg: hours old, while it downloads the rest: preloaded, it would be
// followed by all the candles in between, handed to the strategies as new
// ones. A market closed has its last tick with its last candle: no wait. The
// errors are left to the requests of the caller.
func (m *MT5) awaitHistory(ctx context.Context, pair string, frame mt5Timeframe) {
	if m.syncWait <= 0 {
		return
	}
	deadline := time.Now().Add(m.syncWait)
	for {
		rates, err := m.rates(ctx, pair, frame, 1, 0)
		if err != nil || len(rates) == 0 {
			return
		}
		tick, err := m.tick(ctx, pair)
		if err != nil || tick.Time == 0 {
			return
		}
		last, at := time.Unix(rates[len(rates)-1].Time, 0), time.Unix(tick.Time, 0)
		if !at.After(frame.end(last).Add(frame.approx())) {
			return // the last candle is within a period of the last tick
		}
		if !time.Now().Before(deadline) {
			log.Warnf("[MT5] the candles of %s end at %s, its last tick is of %s: the history of the terminal "+
				"isn't synced after %s", pair, frame.end(last).UTC().Format(time.DateTime),
				at.UTC().Format(time.DateTime), m.syncWait)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(mt5SyncPoll):
		}
	}
}

// CandlesByPeriod returns the complete candles opened between start and end,
// oldest first, read in ranges of mt5RangeRates periods.
func (m *MT5) CandlesByPeriod(ctx context.Context, pair, period string, start, end time.Time) ([]model.Candle,
	error) {
	frame, err := mt5Resolution(period)
	if err != nil {
		return nil, err
	}
	if start.IsZero() {
		return nil, errors.New("mt5: the candles of a period need its start")
	}
	now := time.Now()
	if end.IsZero() || end.After(now) {
		end = now
	}

	candles := make([]model.Candle, 0)
	for from := start; !from.After(end); {
		to := from.Add(time.Duration(mt5RangeRates-1) * frame.approx())
		if to.After(end) {
			to = end
		}
		rates, err := m.ratesRange(ctx, pair, frame, from, to)
		if err != nil {
			return nil, err
		}
		for _, candle := range m.toCandles(pair, rates, frame, now, nil) {
			newer := len(candles) == 0 || candle.Time.After(candles[len(candles)-1].Time)
			if candle.Complete && newer && !candle.Time.Before(start) && !candle.Time.After(end) {
				candles = append(candles, candle)
			}
		}
		from = to.Add(time.Second)
	}
	return candles, nil
}

// CandlesSubscription polls the latest candles (and the tick, for the ask of
// the candle in progress) every poll interval: the candle in progress is sent
// as partial each time it changes, and every candle once complete, the ones
// missed during an outage included. The stream starts after the last candle
// returned by CandlesByLimit, which the bot preloads; without one, after the
// candles already complete at the first poll. Errors go to the error channel
// without stopping the stream; both channels are closed when ctx is done.
func (m *MT5) CandlesSubscription(ctx context.Context, pair, period string) (chan model.Candle, chan error) {
	ccandle := make(chan model.Candle)
	cerr := make(chan error)

	go func() {
		defer close(cerr)
		defer close(ccandle)

		frame, err := mt5Resolution(period)
		if err != nil {
			select {
			case cerr <- err:
			case <-ctx.Done():
			}
			return
		}

		m.loadedMtx.Lock()
		lastComplete, started := m.loaded[pair+"--"+period]
		m.loadedMtx.Unlock()
		var partial model.Candle

		ticker := time.NewTicker(m.pollInterval)
		defer ticker.Stop()

		var polled time.Time // start of the last successful poll
		for {
			start := time.Now()
			candles, err := m.latest(ctx, pair, frame, lastComplete, polled)
			switch {
			case err == nil:
				polled = start
			case ctx.Err() != nil:
				return
			default:
				select {
				case cerr <- err:
				case <-ctx.Done():
					return
				}
			}

			for _, candle := range candles {
				if !started && candle.Complete {
					lastComplete = candle.Time
					continue
				}
				if !candle.Time.After(lastComplete) {
					continue
				}
				if candle.Complete {
					lastComplete = candle.Time
				} else if samePrices(candle, partial) && candle.Metadata["ask"] == partial.Metadata["ask"] {
					continue
				} else {
					partial = candle
				}

				select {
				case ccandle <- candle:
				case <-ctx.Done():
					return
				}
			}
			started = started || err == nil

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	return ccandle, cerr
}

// latest returns the latest candles: enough for those since the poll at polled,
// and since the complete candle at last, eg: the ones missed while the bridge
// was down; the ones missed beyond a request are read by period.
func (m *MT5) latest(ctx context.Context, pair string, frame mt5Timeframe, last, polled time.Time) ([]model.Candle,
	error) {
	if polled.IsZero() {
		polled = last
	}
	count := 3
	if !polled.IsZero() {
		count = min(max(int(time.Since(polled)/frame.approx())+3, count), mt5MaxRates)
	}

	rates, err := m.rates(ctx, pair, frame, count, 0)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var tick *mt5Tick
	if candles := m.toCandles(pair, rates, frame, now, nil); len(candles) > 0 && !candles[len(candles)-1].Complete {
		if t, err := m.tick(ctx, pair); err == nil {
			tick = &t
		}
	}
	candles := m.toCandles(pair, rates, frame, now, tick)

	// more candles missed than requested, eg: after an outage longer than a
	// request or while the host was suspended, which the monotonic clock of
	// time.Since skips: the ones in between
	if !last.IsZero() && len(candles) > 0 && candles[0].Time.After(frame.end(last)) {
		missed, err := m.CandlesByPeriod(ctx, pair, mt5PeriodOf(frame), frame.end(last), candles[0].Time.Add(-time.Second))
		if err != nil {
			return nil, err
		}
		candles = append(missed, candles...)
	}
	return candles, nil
}

// periodOf returns the xtrader timeframe of a timeframe of the terminal.
func mt5PeriodOf(frame mt5Timeframe) string {
	for period, candidate := range mt5Timeframes {
		if candidate.name == frame.name {
			return period
		}
	}
	return ""
}
