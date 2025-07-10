package strategies

import (
	"fmt"
	"math"
	"time"

	"github.com/xhit/go-str2duration/v2"

	"github.com/raykavin/xtrader/model"
)

// The sessions of the intraday strategies are times of day, HH:MM, in a time
// zone given by its IANA name, eg: Europe/London: a session follows the daylight
// saving time of its market. The candles are placed in them by their open
// time.

// dayTime is a time of day, the wall clock time from midnight.
type dayTime time.Duration

// parseDayTime parses a time of day written HH:MM.
func parseDayTime(value string) (dayTime, error) {
	t, err := time.Parse("15:04", value)
	if err != nil {
		return 0, fmt.Errorf("invalid time of day %q, expected HH:MM", value)
	}
	return dayTime(time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute), nil
}

// on returns the time of day d on the day of t, in loc.
func (d dayTime) on(t time.Time, loc *time.Location, days int) time.Time {
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day()+days, 0, 0, int(time.Duration(d)/time.Second), 0,
		loc)
}

// last returns the last time of day d at or before t, in loc.
func (d dayTime) last(t time.Time, loc *time.Location) time.Time {
	at := d.on(t, loc, 0)
	if at.After(t) {
		at = d.on(t, loc, -1)
	}
	return at
}

// next returns the first time of day d after t, in loc.
func (d dayTime) next(t time.Time, loc *time.Location) time.Time {
	at := d.on(t, loc, 0)
	if !at.After(t) {
		at = d.on(t, loc, 1)
	}
	return at
}

// until returns the time from d to the next time of day end, a day for the
// same time: a session may go over midnight.
func (d dayTime) until(end dayTime) time.Duration {
	span := time.Duration(end-d) % rkDay
	if span <= 0 {
		span += rkDay
	}
	return span
}

// session is a daily window of time, from start to end.
type session struct {
	start, end dayTime
	loc        *time.Location
}

// newSession parses a session from its times of day and the name of its time
// zone.
func newSession(start, end, timezone string) (session, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return session{}, fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}
	s := session{loc: loc}
	if s.start, err = parseDayTime(start); err != nil {
		return session{}, err
	}
	if s.end, err = parseDayTime(end); err != nil {
		return session{}, err
	}
	return s, nil
}

// bounds returns the start and the end of the last session started at or
// before t.
func (s session) bounds(t time.Time) (start, end time.Time) {
	start = s.start.last(t, s.loc)
	return start, s.end.next(start, s.loc)
}

// length is the duration of the session, as the wall clock goes: a day with a
// change of the daylight saving time makes it an hour longer or shorter.
func (s session) length() time.Duration {
	return s.start.until(s.end)
}

// rangeBreakout lays out the sessions of a range breakout: the range is the
// high and the low of the candles opened over the range session, and its
// breakouts are traded over the trade session that follows it.
type rangeBreakout struct {
	rng, trade session
}

// sessions returns the bounds of the trade session of a candle opened at t,
// the last one started at or before it, and of its range session, the last one
// ended at or before the trade session started.
func (r rangeBreakout) sessions(t time.Time) (rangeStart, rangeEnd, tradeStart, tradeEnd time.Time) {
	tradeStart, tradeEnd = r.trade.bounds(t)
	rangeEnd = r.rng.end.last(tradeStart, r.rng.loc)
	rangeStart = r.rng.start.last(rangeEnd.Add(-time.Nanosecond), r.rng.loc)
	return rangeStart, rangeEnd, tradeStart, tradeEnd
}

// trading reports whether a candle of timeframe tf opened at t is traded: it
// opened in a trade session, which is still open at its close.
func (r rangeBreakout) trading(t time.Time, tf time.Duration) bool {
	_, end := r.trade.bounds(t)
	return t.Add(tf).Before(end)
}

// span is the longest time from the start of a range session to the end of its
// trade session, an hour of daylight saving time included.
func (r rangeBreakout) span() time.Duration {
	return r.rng.length() + r.rng.end.until(r.trade.start)%rkDay + r.trade.length() + time.Hour
}

// levels returns the high and the low of the range of each candle of df, NaN
// out of the trade sessions and when the window doesn't reach back to the start
// of the range session, or holds no candle of it.
func (r rangeBreakout) levels(df *model.Dataframe) (high, low []float64) {
	type extremes struct{ high, low float64 }
	ranges := make(map[int64]extremes) // by the start of the range session

	high, low = make([]float64, len(df.Time)), make([]float64, len(df.Time))
	for i, t := range df.Time {
		high[i], low[i] = na, na
		rangeStart, rangeEnd, tradeStart, tradeEnd := r.sessions(t)
		if t.Before(tradeStart) || !t.Before(tradeEnd) || df.Time[0].After(rangeStart) {
			continue
		}

		levels, ok := ranges[rangeStart.Unix()]
		if !ok {
			levels = extremes{high: na, low: na}
			for j, at := range df.Time {
				if at.Before(rangeStart) || !at.Before(rangeEnd) {
					continue
				}
				if isNa(levels.high) {
					levels = extremes{high: df.High[j], low: df.Low[j]}
				}
				levels.high, levels.low = math.Max(levels.high, df.High[j]), math.Min(levels.low, df.Low[j])
			}
			ranges[rangeStart.Unix()] = levels
		}
		high[i], low[i] = levels.high, levels.low
	}
	return high, low
}

// anchoredVWAP is the VWAP of the typical price, hlc3, from the last anchor
// time of day at or before each candle, as ta.vwap anchored to a session: NaN
// when the window doesn't reach back to the anchor. A session without volume,
// eg: from a feed without it, weights its candles equally.
func anchoredVWAP(df *model.Dataframe, anchor dayTime, loc *time.Location) []float64 {
	out := make([]float64, len(df.Time))
	var (
		start           time.Time
		priceVolume     float64
		volume, prices  float64
		candles         int
		covered, inited bool
	)
	for i, t := range df.Time {
		if at := anchor.last(t, loc); !inited || !at.Equal(start) {
			start, inited = at, true
			covered = !df.Time[0].After(at)
			priceVolume, volume, prices, candles = 0, 0, 0, 0
		}

		out[i] = na
		if !covered {
			continue
		}

		price := (df.High[i] + df.Low[i] + df.Close[i]) / 3
		if i < len(df.Volume) && df.Volume[i] > 0 {
			priceVolume += price * df.Volume[i]
			volume += df.Volume[i]
		}
		prices += price
		candles++

		if volume > 0 {
			out[i] = priceVolume / volume
		} else {
			out[i] = prices / float64(candles)
		}
	}
	return out
}

// timeframeCandles returns the number of candles of timeframe covering span,
// and the duration of a candle.
func timeframeCandles(timeframe string, span time.Duration) (int, time.Duration, error) {
	tf, err := str2duration.ParseDuration(timeframe)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid timeframe %q: %w", timeframe, err)
	}
	if tf <= 0 {
		return 0, 0, fmt.Errorf("invalid timeframe %q", timeframe)
	}
	return int((span + tf - 1) / tf), tf, nil
}
