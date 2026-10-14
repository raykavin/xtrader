package strategies

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newMRRunner feeds a MeanReversion, a position of 10, in a range: the middle
// band at 100, the bands at 98 and 102, the EMAs flat at 100, the ATR at 0.5,
// the ADX at 15 with its DI even, the RSI at 50.
func newMRRunner(t *testing.T, configure func(*MeanReversionConfig)) (*ivRunner, *MeanReversion) {
	t.Helper()
	cfg := DefaultMeanReversionConfig()
	cfg.PositionSize = map[string]float64{"BTCUSDT": 10}
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewMeanReversion(cfg)
	require.NoError(t, err)
	r := newIVRunner(t, s, 0)
	r.step = 15 * time.Minute
	r.fixed = kv{"mr_mid": 100, "mr_upper": 102, "mr_lower": 98, "mr_fast": 100, "mr_slow": 100, "mr_atr": 0.5,
		"mr_adx": 15, "mr_pdi": 20, "mr_mdi": 20, "mr_rsi": 50}
	return r, s
}

// mrRange is a range of 20 candles, the close crossing the middle band on each.
func mrRange() []ivCandle {
	candles := make([]ivCandle, 20)
	for i := range candles {
		candles[i] = ivCandle{close: 99 + 2*float64(i%2)}
	}
	return candles
}

// the rejection of the lower band: its low at 97.6, back inside at 98.4,
// bullish, a lower wick of 53% of the candle, the RSI oversold
var mrLong = ivCandle{open: 98.05, close: 98.4, high: 98.45, low: 97.6, meta: kv{"mr_rsi": 28}, position: 10}

func TestNewMeanReversion_InvalidConfig(t *testing.T) {
	for name, configure := range map[string]func(*MeanReversionConfig){
		"timeframe":   func(c *MeanReversionConfig) { c.Timeframe = "" },
		"period":      func(c *MeanReversionConfig) { c.BBLen = 0 },
		"ADX":         func(c *MeanReversionConfig) { c.MaxADX = 30 },
		"band":        func(c *MeanReversionConfig) { c.BBMult = 0 },
		"crosses":     func(c *MeanReversionConfig) { c.MinCrosses = 21 },
		"RSI":         func(c *MeanReversionConfig) { c.Oversold = 80 },
		"wick":        func(c *MeanReversionConfig) { c.WickPct = 1.5 },
		"risk reward": func(c *MeanReversionConfig) { c.MinRR = 0 },
		"sizing":      func(c *MeanReversionConfig) { c.PositionFraction = 2 },
	} {
		cfg := DefaultMeanReversionConfig()
		configure(&cfg)
		_, err := NewMeanReversion(cfg)
		require.Error(t, err, name)
	}
	s, err := NewMeanReversion(DefaultMeanReversionConfig())
	require.NoError(t, err)
	require.Equal(t, seedWarmup(28)+1, s.WarmupPeriod(), "the ADX smoothed twice, seeded")
	require.Equal(t, "15m", s.Timeframe())
}

// A BUY on the rejection of the lower band in a range: the stop loss 0.5 ATR
// under the lowest low of the last 5 candles, the take profit at the middle
// band; a SELL on the mirror.
func TestMeanReversion_Entries(t *testing.T) {
	r, s := newMRRunner(t, nil)
	r.feed(mrRange()...)
	r.feed(mrLong)
	legs := s.bracket.trades["BTCUSDT"].legs
	require.Len(t, legs, 2)
	require.InDelta(t, 100, legs[0].Price, 1e-9, "the take profit at the middle band")
	require.InDelta(t, 97.35, *legs[1].Stop, 1e-9, "0.5 ATR under the low of 97.6")

	r, s = newMRRunner(t, nil)
	r.feed(mrRange()...)
	r.feed(ivCandle{open: 101.95, close: 101.6, high: 102.4, low: 101.55, meta: kv{"mr_rsi": 72}, position: -10})
	legs = s.bracket.trades["BTCUSDT"].legs
	require.InDelta(t, 100, legs[0].Price, 1e-9)
	require.InDelta(t, 102.65, *legs[1].Stop, 1e-9, "0.5 ATR over the high of 102.4")
}

// No entry missing any of the conditions.
func TestMeanReversion_NoEntry(t *testing.T) {
	flat := mrLong
	flat.position = 0
	for name, tc := range map[string]struct {
		configure func(*MeanReversionConfig)
		fixed     kv
		candles   []ivCandle
		entry     ivCandle
	}{
		"a trend, the ADX at 20 at least": {fixed: kv{"mr_adx": 22}},
		"the ADX rising": {entry: ivCandle{open: 98.05, close: 98.4, high: 98.45, low: 97.6,
			meta: kv{"mr_rsi": 28, "mr_adx": 16}}},
		"the EMAs apart": {fixed: kv{"mr_fast": 100.2}},
		"the close never crossing the mean": {candles: func() []ivCandle {
			candles := mrRange()
			for i := range candles {
				candles[i].close = 99 + 0.1*float64(i%2) // under the middle band
			}
			return candles
		}()},
		"the RSI not oversold": {entry: ivCandle{open: 98.05, close: 98.4, high: 98.45, low: 97.6,
			meta: kv{"mr_rsi": 35}}},
		"the close beyond the band": {entry: ivCandle{open: 97.95, close: 97.9, high: 98.1, low: 97.6,
			meta: kv{"mr_rsi": 28}}},
		"a short wick": {entry: ivCandle{open: 98.05, close: 98.4, high: 98.45, low: 97.98,
			meta: kv{"mr_rsi": 28}}},
		"not enough reward": {configure: func(c *MeanReversionConfig) { c.MinRR = 2 }},
		"too quiet for the costs": {configure: func(c *MeanReversionConfig) {
			c.MinATRPct = 1
		}},
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := newMRRunner(t, tc.configure)
			for key, value := range tc.fixed {
				r.fixed[key] = value
			}
			candles := tc.candles
			if candles == nil {
				candles = mrRange()
			}
			r.feed(candles...)
			entry := tc.entry
			if entry.close == 0 {
				entry = flat
			}
			entry.position = 0
			r.feed(entry)
		})
	}
}

// Closed at the middle band, on a trend against it, on a close beyond the band
// again, and going nowhere for 8 candles; its stop loss to the entry from 0.7 R.
func TestMeanReversion_Exits(t *testing.T) {
	entered := func(t *testing.T) (*ivRunner, *MeanReversion) {
		r, s := newMRRunner(t, nil)
		r.feed(mrRange()...)
		r.feed(mrLong)
		return r, s
	}

	t.Run("the mean", func(t *testing.T) {
		r, _ := entered(t)
		r.feed(ivCandle{open: 99.8, close: 100.1, high: 100.2, low: 99.7})
	})
	t.Run("a trend against it", func(t *testing.T) {
		r, _ := entered(t)
		r.feed(ivCandle{close: 98.6, meta: kv{"mr_adx": 26, "mr_mdi": 30, "mr_pdi": 10}})
	})
	t.Run("beyond the band again", func(t *testing.T) {
		r, _ := entered(t)
		r.feed(ivCandle{close: 97.9})
	})
	t.Run("the break-even", func(t *testing.T) {
		r, s := entered(t)
		r.feed(ivCandle{close: 99.1, position: 10}) // 0.67 R
		require.InDelta(t, 97.35, *s.bracket.trades["BTCUSDT"].legs[1].Stop, 1e-9)
		r.feed(ivCandle{close: 99.2, position: 10}) // 0.76 R
		require.InDelta(t, 98.4, *s.bracket.trades["BTCUSDT"].legs[1].Stop, 1e-9, "to the entry")
	})
	t.Run("going nowhere", func(t *testing.T) {
		r, _ := entered(t)
		for range 7 {
			r.feed(ivCandle{close: 98.5, position: 10})
		}
		r.feed(ivCandle{close: 98.5}) // 0.1 R after 8 candles
	})
}
