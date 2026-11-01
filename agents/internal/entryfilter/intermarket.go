package entryfilter

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/raykavin/xtrader/agents/internal/ta"
	"github.com/raykavin/xtrader/model"
)

// CandleSource reads the last candles of a symbol, complete ones only, eg:
// the market data of MT5 (paper), its broker (live), or the feed of a backtest,
// which answers only the candles closed by its simulated now.
type CandleSource interface {
	CandlesByLimit(ctx context.Context, pair, period string, limit int) ([]model.Candle, error)
}

// intermarketRead bounds a read of the reference: the strategy waits for it.
const intermarketRead = 5 * time.Second

// The intermarket filter: on each candle of a pair it filters, the reference
// symbol (eg: DXYm, the dollar index, against gold), read on the timeframe of
// the strategy, IntermarketEMA times 3 candles and 10 more, from the source the
// runner gives; only the reference candles closed when the candle of the pair
// closed count (their open at or before its open, the timeframes the same,
// and nothing in them of after its close: their UpdatedAt before it, which
// leaves out a candle of a backtest file merged with the next one, see
// agents/internal/backtest; a candle of MT5 is updated at its open), the
// freshest of them no older than 4 candles of the pair (the markets keep
// different hours), and its close is set against the EMA of the reference
// (Pine's ta.ema, seeded with an SMA). Inverse: a buy only while the reference
// closes below its EMA, a sell only while above; direct: the other way; at the
// EMA, neither. Without the source, the reference, enough of its candles or a
// fresh one, both sides are refused: the filter is never taken for a
// favourable one.

// intermarketLimit returns the candles of the reference read for c.
func intermarketLimit(c Config) int {
	return 3*c.IntermarketEMA + intermarketCandlesExtra
}

// readReference reads the reference of c from source for the candle of the
// pair opened at at, on timeframe.
func readReference(source CandleSource, c Config, timeframe string) ([]model.Candle, error) {
	if source == nil {
		return nil, fmt.Errorf("no source of the candles of %s", c.IntermarketSymbol)
	}
	ctx, cancel := context.WithTimeout(context.Background(), intermarketRead)
	defer cancel()
	return source.CandlesByLimit(ctx, c.IntermarketSymbol, timeframe, intermarketLimit(c))
}

// intermarketReasons returns why the buys and the sells of the pair whose
// candle opened at at are refused by the reference read (refs, or err), ""
// for a side allowed; length is the timeframe.
func intermarketReasons(c Config, refs []model.Candle, err error, at time.Time,
	length time.Duration) (buy, sell string) {
	refused := func(why string) (string, string) {
		reason := fmt.Sprintf("intermarket: %s, both sides refused", why)
		return reason, reason
	}
	if err != nil {
		return refused(fmt.Sprintf("the reference %s not read: %v", c.IntermarketSymbol, err))
	}
	usable := make([]model.Candle, 0, len(refs))
	for _, r := range refs {
		if r.Complete && !r.Time.After(at) && r.UpdatedAt.Before(at.Add(length)) {
			usable = append(usable, r)
		}
	}
	slices.SortStableFunc(usable, func(a, b model.Candle) int { return a.Time.Compare(b.Time) })
	if len(usable) < c.IntermarketEMA {
		return refused(fmt.Sprintf("%d candles of %s closed, %d needed for its EMA", len(usable),
			c.IntermarketSymbol, c.IntermarketEMA))
	}
	last := usable[len(usable)-1]
	if at.Sub(last.Time) > intermarketFreshBars*length {
		return refused(fmt.Sprintf("the last candle of %s is of %s, older than %d candles", c.IntermarketSymbol,
			last.Time.UTC().Format("2006-01-02 15:04"), intermarketFreshBars))
	}
	closes := make([]float64, len(usable))
	for i, r := range usable {
		closes[i] = r.Close
	}
	ema := ta.EMA(closes, c.IntermarketEMA)[len(closes)-1]
	if math.IsNaN(ema) {
		return refused(fmt.Sprintf("no EMA of %s", c.IntermarketSymbol))
	}
	above, below := last.Close > ema, last.Close < ema
	describe := fmt.Sprintf("%s %g against its EMA %d %g (%s)", c.IntermarketSymbol, last.Close,
		c.IntermarketEMA, ema, c.IntermarketMode)
	buyOK, sellOK := below, above
	if c.IntermarketMode == IntermarketDirect {
		buyOK, sellOK = above, below
	}
	if !buyOK {
		buy = "intermarket: " + describe + ": no buy"
	}
	if !sellOK {
		sell = "intermarket: " + describe + ": no sell"
	}
	return buy, sell
}

// filtersPair reports whether the intermarket filter of c applies to pair.
func filtersPair(c Config, pair string) bool {
	return c.IntermarketSymbol != "" && (len(c.IntermarketPairs) == 0 || slices.Contains(c.IntermarketPairs, pair))
}
