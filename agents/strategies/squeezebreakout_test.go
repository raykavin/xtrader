package strategies

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newSQRunner feeds a SqueezeBreakout, a position of 10, on 15 minute candles:
// the ATR at 2 (the candles of the box and after it set it at 1, see sqATR),
// the SMA 200 at 90, the average volume at 1, the ADX at 20 with the DI of a
// long on top.
func newSQRunner(t *testing.T, configure func(*SqueezeBreakoutConfig)) (*ivRunner, *SqueezeBreakout) {
	t.Helper()
	cfg := DefaultSqueezeBreakoutConfig()
	cfg.PositionSize = map[string]float64{"BTCUSDT": 10}
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewSqueezeBreakout(cfg)
	require.NoError(t, err)
	r := newIVRunner(t, s, 0)
	r.step = 15 * time.Minute
	r.fixed = kv{"sq_atr": 2, "sq_trend": 90, "sq_volume": 1, "sq_adx": 20, "sq_pdi": 30, "sq_mdi": 10}
	return r, s
}

// sqBox returns the 110 candles before a breakout: the box of the last 10,
// from 97 to 100, the 100 before them wider.
func sqBox() []ivCandle {
	candles := make([]ivCandle, 110)
	for k := range candles {
		candles[k] = ivCandle{close: 98.5, high: 102, low: 95}
	}
	for k := 100; k < 110; k++ {
		candles[k] = ivCandle{close: 98.5, high: 99.5, low: 97.5, meta: sqATR()}
	}
	candles[100].high, candles[101].low = 100, 97
	return candles
}

// the breakout of the box: a close at 100.2, 0.2 over the box, the body 82% of
// the candle, twice the average volume, the ADX rising
var sqBreakout = ivCandle{open: 99.3, close: 100.2, high: 100.3, low: 99.2, volume: 2,
	meta: kv{"sq_adx": 21, "sq_atr": 1}}

// sqATR is the meta of a candle of the squeeze: the ATR at 1.
func sqATR() kv {
	return kv{"sq_atr": 1}
}

func TestNewSqueezeBreakout_InvalidConfig(t *testing.T) {
	for name, configure := range map[string]func(*SqueezeBreakoutConfig){
		"timeframe":  func(c *SqueezeBreakoutConfig) { c.Timeframe = "" },
		"periods":    func(c *SqueezeBreakoutConfig) { c.BoxBars = 0 },
		"percentile": func(c *SqueezeBreakoutConfig) { c.MaxATRPercentile = 120 },
		"volume":     func(c *SqueezeBreakoutConfig) { c.AgainstTrendVolume = 1 },
		"retest":     func(c *SqueezeBreakoutConfig) { c.RetestBars = -1 },
		"stop loss":  func(c *SqueezeBreakoutConfig) { c.StopMinATR = 0 },
	} {
		cfg := DefaultSqueezeBreakoutConfig()
		configure(&cfg)
		_, err := NewSqueezeBreakout(cfg)
		require.Error(t, err, name)
	}
	s, err := NewSqueezeBreakout(DefaultSqueezeBreakoutConfig())
	require.NoError(t, err)
	require.Equal(t, seedWarmup(28)+100+1, s.WarmupPeriod(), "the ADX seeded and the percentile of the ATR")
	require.Equal(t, "15m", s.Timeframe())
}

// A BUY on the close of the breakout near the box: the stop loss under the
// candle of the breakout, nearer than the middle of the box and 1 ATR away,
// the take profit the height of the box over its top.
func TestSqueezeBreakout_Entry(t *testing.T) {
	r, s := newSQRunner(t, nil)
	r.feed(sqBox()...)
	entry := sqBreakout
	entry.position = 10
	r.feed(entry)
	legs := s.bracket.trades["BTCUSDT"].legs
	require.InDelta(t, 99.2, *legs[1].Stop, 1e-9, "under the candle of the breakout")
	require.InDelta(t, 103, legs[0].Price, 1e-9, "the height of 3 over the top of 100")
}

// No entry missing any of the conditions, nor offering 2 R: the stop loss at
// the middle of the box never does, with the take profit the height of the box.
func TestSqueezeBreakout_NoEntry(t *testing.T) {
	for name, tc := range map[string]struct {
		configure func(*SqueezeBreakoutConfig)
		fixed     kv
		edit      func([]ivCandle, *ivCandle)
	}{
		"a box too tall":     {edit: func(c []ivCandle, _ *ivCandle) { c[105].low = 96 }},
		"a market not quiet": {fixed: kv{"sq_atr": 0.5}}, // the ATR of the box over 90% of the ones before
		"a small body":       {edit: func(_ []ivCandle, b *ivCandle) { b.open = 100 }},
		"the volume low": {edit: func(_ []ivCandle, b *ivCandle) {
			b.volume = 1.4
		}},
		"against the SMA 200 without twice the volume": {fixed: kv{"sq_trend": 101},
			edit: func(_ []ivCandle, b *ivCandle) { b.volume = 1.8 }},
		"the ADX not rising":  {edit: func(_ []ivCandle, b *ivCandle) { b.meta = kv{"sq_adx": 20, "sq_atr": 1} }},
		"the other DI on top": {fixed: kv{"sq_pdi": 10, "sq_mdi": 30}},
		"the stop loss at the middle of the box": {edit: func(_ []ivCandle, b *ivCandle) {
			b.low = 99.5 // the candle of the breakout 0.7 ATR away: not far enough
		}},
	} {
		t.Run(name, func(t *testing.T) {
			candles, breakout := sqBox(), sqBreakout
			if tc.edit != nil {
				tc.edit(candles, &breakout)
			}
			r, _ := newSQRunner(t, tc.configure)
			for key, value := range tc.fixed {
				r.fixed[key] = value
			}
			r.feed(candles...)
			breakout.position = 0
			r.feed(breakout)
		})
	}
}

// A breakout farther than 0.5 ATR from its box waits for its retest: a candle
// back near the top holding it, then a candle closing over its high; a close
// back inside the box drops it.
func TestSqueezeBreakout_Retest(t *testing.T) {
	far := ivCandle{open: 99.4, close: 100.8, high: 100.9, low: 99.6, volume: 2, meta: kv{"sq_adx": 21, "sq_atr": 1}}

	r, s := newSQRunner(t, nil)
	r.feed(sqBox()...)
	r.feed(far)                                                                         // 0.8 over the box: no entry
	r.feed(ivCandle{open: 100.5, close: 100.4, high: 100.6, low: 100.1, meta: sqATR()}) // the retest
	r.feed(ivCandle{open: 100.45, close: 100.7, high: 100.75, low: 100.4, position: 10, meta: sqATR()})
	legs := s.bracket.trades["BTCUSDT"].legs
	require.InDelta(t, 99.6, *legs[1].Stop, 1e-9, "under the candle of the breakout")
	require.InDelta(t, 103, legs[0].Price, 1e-9)

	r, _ = newSQRunner(t, nil)
	r.feed(sqBox()...)
	r.feed(far)
	r.feed(ivCandle{open: 100.2, close: 99.8, high: 100.3, low: 99.7, meta: sqATR()}) // back inside: failed
	r.feed(ivCandle{open: 99.9, close: 100.7, high: 100.75, low: 99.9, meta: sqATR()})

	r, _ = newSQRunner(t, nil)
	r.feed(sqBox()...)
	r.feed(far)
	for range 5 { // no retest within 5 candles
		r.feed(ivCandle{open: 100.9, close: 101, high: 101.1, low: 100.8, meta: sqATR()})
	}
	r.feed(ivCandle{open: 100.5, close: 100.4, high: 100.6, low: 100.1, meta: sqATR()})
	r.feed(ivCandle{open: 100.45, close: 100.7, high: 100.75, low: 100.4, meta: sqATR()})
}

// Closed on a close back inside the box; its stop loss to the entry from 1 R,
// from 1.5 R behind the EMA 9 or the low of the last 2 candles, the nearer.
func TestSqueezeBreakout_Exits(t *testing.T) {
	entered := func(t *testing.T, configure func(*SqueezeBreakoutConfig)) (*ivRunner, *SqueezeBreakout) {
		r, s := newSQRunner(t, configure)
		r.feed(sqBox()...)
		entry := sqBreakout
		entry.position = 10
		r.feed(entry)
		return r, s
	}
	stop := func(s *SqueezeBreakout) float64 { return *s.bracket.trades["BTCUSDT"].legs[1].Stop }

	t.Run("back inside the box", func(t *testing.T) {
		r, _ := entered(t, nil)
		r.feed(ivCandle{open: 100.1, close: 99.9, high: 100.2, low: 99.8, meta: sqATR()})
	})
	t.Run("the break-even and the trailing", func(t *testing.T) {
		r, s := entered(t, nil)
		r.fixed["sq_fast"] = 100.9
		r.feed(ivCandle{open: 100.2, close: 101.3, high: 101.4, low: 100.6, position: 10, meta: sqATR()}) // 1.1 R
		require.InDelta(t, 100.2, stop(s), 1e-9, "to the entry")
		r.feed(ivCandle{open: 101.3, close: 101.8, high: 101.9, low: 101.2, position: 10, meta: sqATR()}) // 1.6 R
		require.InDelta(t, 100.9, stop(s), 1e-9, "behind the EMA 9, nearer than the low of 100.6")
	})
}
