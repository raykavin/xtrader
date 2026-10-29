package exchange

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xhit/go-str2duration/v2"

	"github.com/raykavin/xtrader/model"
)

// BacktestFeed is the feed of a backtest: the candles of a CSVFeed handed to
// the bot in their order (CandlesSubscription), and, to whatever asks for
// candles meanwhile (CandlesByPeriod, CandlesByLimit: eg: the protection of
// the positions, the higher timeframes of an LLM), only the ones closed by
// the simulated now (SetNow), never a future one, the feed itself untouched.
// A timeframe the feed doesn't hold is resampled from the candles of the
// file. AssetsInfo returns the infos set (SetAssetsInfo), else the CSVFeed's.
type BacktestFeed struct {
	csv *CSVFeed

	mu     sync.RWMutex
	now    time.Time
	frames map[string][]model.Candle // resampled on demand, by pair and timeframe
	infos  map[string]model.AssetInfo
}

// NewBacktestFeed returns the feed of a backtest over csv, see BacktestFeed.
func NewBacktestFeed(csv *CSVFeed) *BacktestFeed {
	return &BacktestFeed{csv: csv, frames: make(map[string][]model.Candle), infos: make(map[string]model.AssetInfo)}
}

// SetNow sets the simulated now: the end of the data known, eg: the close of
// the candle being processed.
func (b *BacktestFeed) SetNow(now time.Time) {
	b.mu.Lock()
	b.now = now
	b.mu.Unlock()
}

// Now returns the simulated now, see SetNow.
func (b *BacktestFeed) Now() time.Time {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.now
}

// SetAssetsInfo sets the infos of pair, eg: the volume step and the least
// volume of its symbol.
func (b *BacktestFeed) SetAssetsInfo(pair string, info model.AssetInfo) {
	b.mu.Lock()
	b.infos[pair] = info
	b.mu.Unlock()
}

func (b *BacktestFeed) AssetsInfo(pair string) model.AssetInfo {
	b.mu.RLock()
	info, ok := b.infos[pair]
	b.mu.RUnlock()
	if ok {
		return info
	}
	return b.csv.AssetsInfo(pair)
}

func (b *BacktestFeed) LastQuote(ctx context.Context, pair string) (float64, error) {
	return b.csv.LastQuote(ctx, pair)
}

func (b *BacktestFeed) CandlesSubscription(ctx context.Context, pair, timeframe string) (chan model.Candle,
	chan error) {
	return b.csv.CandlesSubscription(ctx, pair, timeframe)
}

// closed returns the candles of pair on timeframe closed by now (their open
// plus their timeframe at or before it), complete ones only.
func (b *BacktestFeed) closed(pair, timeframe string) ([]model.Candle, error) {
	length, err := str2duration.ParseDuration(timeframe)
	if err != nil {
		return nil, fmt.Errorf("backtest feed: the timeframe %q: %w", timeframe, err)
	}
	candles, err := b.series(pair, timeframe)
	if err != nil {
		return nil, err
	}
	now := b.Now()
	var closed []model.Candle
	for _, candle := range candles {
		if !candle.Complete {
			continue
		}
		if candle.Time.Add(length).After(now) {
			break
		}
		closed = append(closed, candle)
	}
	return closed, nil
}

// series returns the candles of pair on timeframe: the CSVFeed's, else
// resampled once from the file of the pair.
func (b *BacktestFeed) series(pair, timeframe string) ([]model.Candle, error) {
	key := b.csv.feedTimeframeKey(pair, timeframe)
	if candles, ok := b.csv.CandlePairTimeFrame[key]; ok {
		return candles, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if candles, ok := b.frames[key]; ok {
		return candles, nil
	}
	feed, ok := b.csv.Feeds[pair]
	if !ok {
		return nil, fmt.Errorf("backtest feed: no candles of %s", pair)
	}
	copied := &CSVFeed{Feeds: b.csv.Feeds, CandlePairTimeFrame: map[string][]model.Candle{
		b.csv.feedTimeframeKey(pair, feed.Timeframe): b.csv.CandlePairTimeFrame[b.csv.feedTimeframeKey(pair,
			feed.Timeframe)],
	}}
	if err := copied.resample(pair, feed.Timeframe, timeframe); err != nil {
		return nil, fmt.Errorf("backtest feed: %s on %s: %w", pair, timeframe, err)
	}
	b.frames[key] = copied.CandlePairTimeFrame[key]
	return b.frames[key], nil
}

// CandlesByPeriod returns the candles of pair on timeframe from start until
// end, closed by the simulated now.
func (b *BacktestFeed) CandlesByPeriod(_ context.Context, pair, timeframe string, start,
	end time.Time) ([]model.Candle, error) {
	closed, err := b.closed(pair, timeframe)
	if err != nil {
		return nil, err
	}
	candles := make([]model.Candle, 0)
	for _, candle := range closed {
		if candle.Time.Before(start) || candle.Time.After(end) {
			continue
		}
		candles = append(candles, candle)
	}
	return candles, nil
}

// CandlesByLimit returns the last limit candles of pair on timeframe closed by
// the simulated now, ErrInsufficientData with fewer: the feed untouched.
func (b *BacktestFeed) CandlesByLimit(_ context.Context, pair, timeframe string, limit int) ([]model.Candle,
	error) {
	closed, err := b.closed(pair, timeframe)
	if err != nil {
		return nil, err
	}
	if len(closed) < limit {
		return nil, fmt.Errorf("%w: %s, %d of %d candles of %s closed by %s", ErrInsufficientData, pair,
			len(closed), limit, timeframe, b.Now().UTC().Format(time.RFC3339))
	}
	return append([]model.Candle(nil), closed[len(closed)-limit:]...), nil
}
