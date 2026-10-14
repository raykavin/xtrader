package strategies

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTPRunner feeds a TrendPullback, a position of 10, on 1 hour candles in an
// uptrend: the EMA 9 at 101, the EMA 21 at 100, the SMA 200 at 90, the ATR at
// 1, the ADX at 25, the RSI at 55, the MACD histogram flat at 0.
func newTPRunner(t *testing.T, configure func(*TrendPullbackConfig)) (*ivRunner, *TrendPullback) {
	t.Helper()
	cfg := DefaultTrendPullbackConfig()
	cfg.PositionSize = map[string]float64{"BTCUSDT": 10}
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewTrendPullback(cfg)
	require.NoError(t, err)
	r := newIVRunner(t, s, 0)
	r.step = time.Hour
	r.fixed = kv{"tp_fast": 101, "tp_slow": 100, "tp_trend": 90, "tp_atr": 1, "tp_adx": 25, "tp_rsi": 55,
		"tp_hist": 0}
	return r, s
}

// tpScenario returns 51 candles of an uptrend closing at 102, a high of 104
// among them, a pullback of 4 candles back to the EMA 9 (their lows at 100.9),
// then the last candle closing at entry over the EMA 9, the MACD histogram
// turning up.
func tpScenario(entry float64) []ivCandle {
	candles := make([]ivCandle, 51)
	for k := range candles {
		candles[k] = ivCandle{close: 102, high: 102.2, low: 101.8}
	}
	candles[10].high = 104
	for k := 46; k < 50; k++ {
		candles[k] = ivCandle{close: 101.3, high: 101.6, low: 100.9}
	}
	candles[50] = ivCandle{open: 101.2, close: entry, high: entry + 0.1, low: 101.1, meta: kv{"tp_hist": 0.1}}
	return candles
}

func TestNewTrendPullback_InvalidConfig(t *testing.T) {
	for name, configure := range map[string]func(*TrendPullbackConfig){
		"timeframe":   func(c *TrendPullbackConfig) { c.Timeframe = "" },
		"periods":     func(c *TrendPullbackConfig) { c.SlowLen = 5 },
		"RSI":         func(c *TrendPullbackConfig) { c.MaxRSI = 40 },
		"stop loss":   func(c *TrendPullbackConfig) { c.StopMinATR = 0 },
		"the moves":   func(c *TrendPullbackConfig) { c.TrailR = -1 },
		"swing pivot": func(c *TrendPullbackConfig) { c.SwingPivot = 0 },
	} {
		cfg := DefaultTrendPullbackConfig()
		configure(&cfg)
		_, err := NewTrendPullback(cfg)
		require.Error(t, err, name)
	}
	s, err := NewTrendPullback(DefaultTrendPullbackConfig())
	require.NoError(t, err)
	require.Equal(t, seedWarmup(35)+1, s.WarmupPeriod(), "the MACD 26 and its signal 9, seeded")
	require.Equal(t, "1h", s.Timeframe())
}

// A BUY at the end of a pullback of an uptrend: the stop loss under the low of
// the pullback, 1 ATR away at least, the take profit at the highest high of the
// last 50 candles, or 2 times the risk without one beyond the close; a SELL on
// the mirror.
func TestTrendPullback_Entries(t *testing.T) {
	candles := tpScenario(101.5)
	candles[50].position = 10
	r, s := newTPRunner(t, nil)
	r.feed(candles...)
	legs := s.bracket.trades["BTCUSDT"].legs
	require.InDelta(t, 100.5, *legs[1].Stop, 1e-9, "1 ATR under the close, the low of 100.9 nearer")
	require.InDelta(t, 104, legs[0].Price, 1e-9, "the highest high of the last 50 candles")

	candles = tpScenario(105)
	candles[50].position = 10
	r, s = newTPRunner(t, nil)
	r.feed(candles...)
	legs = s.bracket.trades["BTCUSDT"].legs
	require.InDelta(t, 100.9, *legs[1].Stop, 1e-9, "under the low of the pullback")
	require.InDelta(t, 105+2*4.1, legs[0].Price, 1e-9, "2 times the risk, no high beyond the close")

	// the mirror, a downtrend
	short := make([]ivCandle, 51)
	for k := range short {
		short[k] = ivCandle{close: 98, high: 98.2, low: 97.8}
	}
	short[10].low = 96
	for k := 46; k < 50; k++ {
		short[k] = ivCandle{close: 98.7, high: 99.1, low: 98.4}
	}
	short[50] = ivCandle{open: 98.8, close: 98.5, high: 98.9, low: 98.4, meta: kv{"tp_hist": -0.1}, position: -10}
	r, s = newTPRunner(t, nil)
	r.fixed["tp_fast"], r.fixed["tp_slow"], r.fixed["tp_trend"], r.fixed["tp_rsi"] = 99, 100, 110, 45
	r.feed(short...)
	legs = s.bracket.trades["BTCUSDT"].legs
	require.InDelta(t, 99.5, *legs[1].Stop, 1e-9)
	require.InDelta(t, 96, legs[0].Price, 1e-9)
}

// No entry missing any of the conditions.
func TestTrendPullback_NoEntry(t *testing.T) {
	for name, tc := range map[string]struct {
		configure func(*TrendPullbackConfig)
		fixed     kv
		edit      func([]ivCandle)
	}{
		"the EMAs against the trend": {fixed: kv{"tp_fast": 99}},
		"under the SMA 200":          {fixed: kv{"tp_trend": 103}},
		"the ADX low":                {fixed: kv{"tp_adx": 15}},
		"no pullback": {edit: func(c []ivCandle) {
			for k := 46; k < 50; k++ {
				c[k].low = 101.2
			}
		}},
		"a close under the SMA 200 in the pullback": {fixed: kv{"tp_trend": 101.4}},
		"the momentum not turning": {edit: func(c []ivCandle) {
			c[48].meta, c[49].meta = kv{"tp_hist": -0.2}, kv{"tp_hist": -0.1} // rising before
		}},
		"the RSI overbought": {edit: func(c []ivCandle) { c[50].meta["tp_rsi"] = 75 }},
		"not enough reward":  {configure: func(c *TrendPullbackConfig) { c.MinRR = 3 }},
	} {
		t.Run(name, func(t *testing.T) {
			candles := tpScenario(101.5)
			if tc.edit != nil {
				tc.edit(candles)
			}
			r, _ := newTPRunner(t, tc.configure)
			for key, value := range tc.fixed {
				r.fixed[key] = value
			}
			r.feed(candles...)
		})
	}
}

// Its stop loss to the entry from 1 R, from 2 R behind the last swing low or 1
// ATR behind the close, the nearer; closed on a close under the EMA 21.
func TestTrendPullback_Exits(t *testing.T) {
	entered := func(t *testing.T) (*ivRunner, *TrendPullback) {
		candles := tpScenario(101.5)
		candles[50].position = 10
		r, s := newTPRunner(t, func(c *TrendPullbackConfig) { c.TargetBars = 50 })
		r.feed(candles...)
		return r, s
	}
	stop := func(s *TrendPullback) float64 { return *s.bracket.trades["BTCUSDT"].legs[1].Stop }

	t.Run("the trend broken", func(t *testing.T) {
		r, _ := entered(t)
		r.feed(ivCandle{close: 99.9, low: 99.8})
	})
	t.Run("the break-even and the trailing", func(t *testing.T) {
		r, s := entered(t)
		r.feed(ivCandle{close: 102.4, high: 102.5, low: 102.3, position: 10}) // 0.9 R
		require.InDelta(t, 100.5, stop(s), 1e-9)
		r.feed(ivCandle{close: 102.6, high: 102.7, low: 102.5, position: 10}) // 1.1 R
		require.InDelta(t, 101.5, stop(s), 1e-9, "to the entry")
		r.feed(ivCandle{close: 103.6, high: 103.7, low: 103.5, position: 10}) // 2.1 R
		require.InDelta(t, 102.6, stop(s), 1e-9, "1 ATR behind the close, nearer than the last swing low")
	})
}
