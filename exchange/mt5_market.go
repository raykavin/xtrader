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
// started, or, the last one, mt5CloseDelay after its end when confirmed: the
// terminal had all of it (see confirmsClose), always for a range of the
// history with newer candles after it. tick, when known, sets the ask and the
// spread of the last candle when in progress.
func (m *MT5) toCandles(pair string, rates []mt5Rate, frame mt5Timeframe, now time.Time,
	tick *mt5Tick, confirmed bool) []model.Candle {
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
		complete := i < len(rates)-1 || confirmed && !frame.end(start).Add(mt5CloseDelay).After(now)
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
			Complete:  complete,
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

// mt5LastEnd returns the end of the latest candle of rates, zero without any.
func mt5LastEnd(rates []mt5Rate, frame mt5Timeframe) time.Time {
	if len(rates) == 0 {
		return time.Time{}
	}
	latest := rates[0].Time
	for _, rate := range rates[1:] {
		latest = max(latest, rate.Time)
	}
	return frame.end(time.Unix(latest, 0).UTC())
}

// confirmsClose reports whether the terminal had the whole candle of pair
// ended at end when its candles were read, at read, which the clock alone
// can't tell: a terminal disconnected from its server serves the candle it
// was building, partial, with no newer one after it, and the clock of the bot
// may be ahead of the server's. Read before read, either a tick of the symbol
// at or after end, on the clock of the server, or the terminal connected to
// its server mt5CloseDelay after end, on the bot's (GET /health, see
// askHealth), later by as much as the clock of the bot looks ahead of the
// server's past mt5CloseDelay (see clockAhead).
func (m *MT5) confirmsClose(pair string, end, read time.Time) bool {
	m.quotesMtx.Lock()
	quote, ok := m.quotes[pair]
	m.quotesMtx.Unlock()
	if ok && !quote.at.After(read) && !quote.tick.at().Before(end) {
		return true
	}
	m.healthMtx.Lock()
	seen := m.health
	m.healthMtx.Unlock()
	return seen.connected && !seen.asked.Before(end.Add(mt5CloseDelay+m.clockAhead())) &&
		!seen.answered.After(read)
}

// askHealth reads GET /health for confirmsClose, unless one was asked at or
// after since or is in flight: the polls of every pair share it. A failure
// leaves the previous read.
func (m *MT5) askHealth(ctx context.Context, since time.Time) {
	m.healthMtx.Lock()
	skip := m.healthBusy || !m.health.asked.Before(since)
	m.healthBusy = m.healthBusy || !skip
	m.healthMtx.Unlock()
	if skip {
		return
	}

	var health mt5Health
	asked := time.Now()
	err := m.get(ctx, "/health", nil, &health)
	answered := time.Now()

	m.healthMtx.Lock()
	defer m.healthMtx.Unlock()
	m.healthBusy = false
	if err != nil {
		log.Debugf("[MT5] reading the state of the terminal: %v", err)
		return
	}
	m.health = mt5Seen{connected: health.Connected, asked: asked, answered: answered}
}

// at returns the time of the tick, on the clock of the trade server.
func (t mt5Tick) at() time.Time {
	if t.TimeMsc > 0 {
		return mt5Time(t.TimeMsc)
	}
	return time.Unix(t.Time, 0).UTC()
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
// of the history. The latest one, over on the clock only, is complete when
// the terminal confirms it (confirmsClose, the state of the terminal read
// first); otherwise it is dropped as in progress. It records the last complete
// candle, where CandlesSubscription starts.
func (m *MT5) CandlesByLimit(ctx context.Context, pair, period string, limit int) ([]model.Candle, error) {
	frame, err := mt5Resolution(period)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []model.Candle{}, nil
	}
	m.awaitHistory(ctx, pair, frame)
	m.askHealth(ctx, time.Now().Add(-m.pollInterval))

	read := time.Now()
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

	confirmed := m.confirmsClose(pair, mt5LastEnd(rates, frame), read)
	candles := m.toCandles(pair, rates, frame, time.Now(), nil, confirmed)
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
// oldest first, read in ranges of mt5RangeRates periods. Up to now, the latest
// one is complete as for CandlesByLimit.
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
	present := end.IsZero() || end.After(now)
	if present {
		end = now
		m.askHealth(ctx, now.Add(-m.pollInterval))
	}

	read := time.Now()
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
		// the last candle of a range has newer ones after it, but up to now
		confirmed := !present || to.Before(end) || m.confirmsClose(pair, mt5LastEnd(rates, frame), read)
		for _, candle := range m.toCandles(pair, rates, frame, now, nil, confirmed) {
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
// missed during an outage included. The latest candle over on the clock only
// stays in progress until the terminal confirms it (confirmsClose), eg: while
// it is disconnected from its server, logged after mt5HeldWarn. The stream
// starts after the last candle returned by CandlesByLimit, which the bot
// preloads; without one, after the candles already complete at the first
// poll. A candle the terminal changes after it was sent complete is logged,
// never sent again: the strategies ran on it. Errors go to the error channel
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
		sent := make(map[time.Time]model.Candle) // the complete candles sent, while the polls read them
		var held time.Time                       // the candle in progress over on the clock, logged

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
					if before, ok := sent[candle.Time]; ok && !samePrices(before, candle) {
						log.Warnf("[MT5] %s %s: the terminal changed the candle of %s after it was sent closed, "+
							"%s, now %s: the strategies keep the one sent", pair, period,
							candle.Time.Format(time.DateTime), mt5Prices(before), mt5Prices(candle))
						sent[candle.Time] = candle
					}
					continue
				}
				if candle.Complete {
					lastComplete = candle.Time
					sent[candle.Time] = candle
					if candle.Time.Equal(held) {
						log.Infof("[MT5] %s %s: the candle of %s is closed, the terminal confirmed it", pair, period,
							candle.Time.Format(time.DateTime))
					}
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
			if n := len(candles); n > 0 {
				for at := range sent {
					if at.Before(candles[0].Time) {
						delete(sent, at) // no longer read
					}
				}
				last := candles[n-1]
				over := frame.end(last.Time).Add(mt5CloseDelay + m.clockAhead() + max(mt5HeldWarn, 3*m.pollInterval))
				if !last.Complete && last.Time.After(lastComplete) && !last.Time.Equal(held) && time.Now().After(over) {
					held = last.Time
					log.Warnf("[MT5] %s %s: the candle of %s ended at %s, but the terminal doesn't confirm it "+
						"closed (no tick since, not connected to its server, or GET /health failing): it stays in "+
						"progress until it does, or has the next candle", pair, period,
						last.Time.Format(time.DateTime), frame.end(last.Time).Format(time.DateTime))
				}
			}

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

	read := time.Now()
	rates, err := m.rates(ctx, pair, frame, count, 0)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	end := mt5LastEnd(rates, frame)
	confirmed := m.confirmsClose(pair, end, read)
	var tick *mt5Tick
	candles := m.toCandles(pair, rates, frame, now, nil, confirmed)
	if n := len(candles); n > 0 && !candles[n-1].Complete && candles[n-1].Time.After(last) {
		if closing := end.Add(mt5CloseDelay + m.clockAhead()); !closing.After(now) {
			// over on the clock, not confirmed: the state of the terminal for
			// the next poll
			m.askHealth(ctx, mt5Later(closing, now.Add(-m.pollInterval)))
		}
		if t, err := m.tick(ctx, pair); err == nil {
			tick = &t
		}
	}
	candles = m.toCandles(pair, rates, frame, now, tick, confirmed)

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

// mt5Prices formats the prices and the volume of a candle for the logs.
func mt5Prices(candle model.Candle) string {
	return fmt.Sprintf("open %v high %v low %v close %v volume %v", candle.Open, candle.High, candle.Low,
		candle.Close, candle.Volume)
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
