package backtest

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/xhit/go-str2duration/v2"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
)

// closedCandles reads the candles of the feed of a run closed by its simulated
// now, as exchange.BacktestFeed does (the complete candles whose open plus
// their timeframe is at or before the now, never a later one), for the
// timeframes the files were resampled to at once: a search of the candles in
// memory rather than a scan of them all on every read, eg: the reference of an
// intermarket filter read on each candle of a run of months of 1m candles.
// Another timeframe is read from the feed, the same way.
//
// A candle holding data of after its own end is never answered (see
// merged): the resample of a finer file, missing the last candle of a bucket
// (eg: the 1m candle of 10:59 of the hour of 10:00), never completes the
// bucket and merges the next one into it, kept at the open of the first (its
// close, high, low and UpdatedAt of later), which would hand the reference of
// 11:00 as soon as the candle of 10:00 closed.
type closedCandles struct {
	feed *exchange.BacktestFeed
	csv  *exchange.CSVFeed
}

// merged reports whether c, a candle of a timeframe of length, holds data of
// after its end: its last part (UpdatedAt, the open of the last candle of the
// file in it) not before its open plus its timeframe, see closedCandles.
func merged(c model.Candle, length time.Duration) bool {
	return !c.UpdatedAt.Before(c.Time.Add(length))
}

// CandlesByLimit returns the last limit candles of pair on period closed by the
// simulated now, the merged ones left out (see closedCandles),
// exchange.ErrInsufficientData with fewer.
func (c closedCandles) CandlesByLimit(ctx context.Context, pair, period string, limit int) ([]model.Candle,
	error) {
	length, err := str2duration.ParseDuration(period)
	if err != nil {
		return nil, fmt.Errorf("backtest feed: the timeframe %q: %w", period, err)
	}
	now := c.feed.Now()
	candles, ok := c.csv.CandlePairTimeFrame[fmt.Sprintf("%s--%s", pair, period)]
	if !ok {
		// resampled by the feed, the candles closed by now only
		if candles, err = c.feed.CandlesByPeriod(ctx, pair, period, time.Time{}, now); err != nil {
			return nil, err
		}
	}
	// the candles are in the order of their open, the ones resampled from a
	// finer file repeated (the parts of a candle in progress, then the
	// complete one, all opened at the same time)
	end := sort.Search(len(candles), func(i int) bool { return candles[i].Time.Add(length).After(now) })
	closed := make([]model.Candle, 0, max(limit, 0))
	for i := end - 1; i >= 0 && len(closed) < limit; i-- {
		if candles[i].Complete && !merged(candles[i], length) {
			closed = append(closed, candles[i])
		}
	}
	if len(closed) < limit {
		return nil, fmt.Errorf("%w: %s, %d of %d candles of %s closed by %s", exchange.ErrInsufficientData, pair,
			len(closed), limit, period, now.UTC().Format(time.RFC3339))
	}
	for i, j := 0, len(closed)-1; i < j; i, j = i+1, j-1 {
		closed[i], closed[j] = closed[j], closed[i]
	}
	return closed, nil
}
