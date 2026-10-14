package strategies

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newMSRunner feeds a MicroScalp of cfg, a position of 10, on 1 minute candles
// of a Monday from 09:00 UTC, London open: the ATR at 1, the ADX at 25 with the
// DI of its side on top, the volume at its average, the RSI at 55, the SMA 200
// at 90, the EMAs of the higher timeframes on the side of a long.
func newMSRunner(t *testing.T, cfg MicroScalpConfig, configure func(*MicroScalpConfig)) (*ivRunner, *MicroScalp) {
	t.Helper()
	cfg.PositionSize = map[string]float64{"BTCUSDT": 10}
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewMicroScalp(cfg)
	require.NoError(t, err)
	r := newIVRunner(t, s, 0)
	r.step = time.Minute
	r.fixed = kv{"ms_atr": 1, "ms_adx": 25, "ms_pdi": 30, "ms_mdi": 10, "ms_volume": 1, "ms_rsi": 55,
		"ms_trend": 90, "ms_hist": 0, "ms_htf_fast_0": 101, "ms_htf_slow_0": 100, "ms_htf_fast_1": 101,
		"ms_htf_slow_1": 100}
	return r, s
}

// msScenario returns the n candles of a micro trend of sign (1 up, -1 down)
// from 09:00: the fast EMA moving 0.1 a candle from 100, the slow one 0.5
// behind it, the closes 0.6 beyond the fast EMA; the pullback of the candles
// back to the fast EMA from the one of index pullback, closing 0.05 beyond it;
// the last candle resuming the trend, closing 0.5 beyond the fast EMA and
// beyond the extreme of the one before, the MACD histogram growing its way.
// The EMAs of after the last candle stay at its values, see msFixed.
func msScenario(n, pullback int, sign float64) []ivCandle {
	candles := make([]ivCandle, n)
	for k := range candles {
		fast := 100 + sign*0.1*float64(k)
		meta := kv{"ms_fast": fast, "ms_slow": fast - sign*0.5}
		c := ivCandle{meta: meta}
		switch {
		case k == n-1:
			c.open, c.close = fast+sign*0.05, fast+sign*0.5
			meta["ms_hist"] = sign * 0.2
		case k >= pullback:
			c.close = fast + sign*0.05
			c.open = c.close + sign*0.1
		default:
			c.close = fast + sign*0.6
			c.open = c.close - sign*0.05
		}
		c.high, c.low = max(c.open, c.close)+0.1, min(c.open, c.close)-0.1
		if k >= pullback && k < n-1 {
			if sign > 0 {
				c.low = fast - 0.1
			} else {
				c.high = fast + 0.1
			}
		}
		if k == 0 {
			c.at = "09:00"
		}
		candles[k] = c
	}
	return candles
}

// msFixed sets the EMAs of the candles after the scenario of n candles, the
// position held, at the values of its last one.
func msFixed(r *ivRunner, n int, sign float64) {
	fast := 100 + sign*0.1*float64(n-1)
	r.fixed["ms_fast"], r.fixed["ms_slow"] = fast, fast-sign*0.5
}

func TestNewMicroScalp_InvalidConfig(t *testing.T) {
	for name, configure := range map[string]func(*MicroScalpConfig){
		"name":             func(c *MicroScalpConfig) { c.Name = "" },
		"timeframe":        func(c *MicroScalpConfig) { c.Timeframe = "x" },
		"higher timeframe": func(c *MicroScalpConfig) { c.HigherTimeframes = []string{"90s"} },
		"periods":          func(c *MicroScalpConfig) { c.FastLen = 30 },
		"RSI":              func(c *MicroScalpConfig) { c.MaxRSI = 40 },
		"stop loss":        func(c *MicroScalpConfig) { c.StopMaxATR = 0.5 },
		"risk reward":      func(c *MicroScalpConfig) { c.MinRR = 0 },
	} {
		cfg := DefaultScalp1mConfig()
		configure(&cfg)
		_, err := NewMicroScalp(cfg)
		require.Error(t, err, name)
	}
	s, err := NewMicroScalp(DefaultScalp1mConfig())
	require.NoError(t, err)
	require.Equal(t, htfWarmup(21, 60), s.WarmupPeriod(), "the EMA 21 of 1h on 1 minute candles")
	require.Equal(t, "1m", s.Timeframe())
}

// scalp1m: a BUY on the micro pullback of a micro uptrend: the stop loss under
// the low of the pullback, 1 to 1.5 ATR away, the take profit 1.5 times the
// risk; a SELL on the mirror.
func TestMicroScalp_Scalp1mEntries(t *testing.T) {
	candles := msScenario(22, 18, 1)
	candles[21].position = 10
	r, s := newMSRunner(t, DefaultScalp1mConfig(), nil)
	r.feed(candles...)
	legs := s.bracket.trades["BTCUSDT"].legs
	require.Len(t, legs, 2)
	require.InDelta(t, 101.6, *legs[1].Stop, 1e-9, "1 ATR under the close of 102.6, the low of 101.7 nearer")
	require.InDelta(t, 104.1, legs[0].Price, 1e-9, "1.5 times the risk")

	candles = msScenario(22, 18, -1)
	candles[21].position = -10
	r, s = newMSRunner(t, DefaultScalp1mConfig(), nil)
	r.fixed["ms_pdi"], r.fixed["ms_mdi"] = 10, 30
	r.fixed["ms_htf_fast_0"], r.fixed["ms_htf_fast_1"] = 99, 99
	r.feed(candles...)
	legs = s.bracket.trades["BTCUSDT"].legs
	require.InDelta(t, 98.4, *legs[1].Stop, 1e-9)
	require.InDelta(t, 95.9, legs[0].Price, 1e-9)
}

// scalp1m: no entry missing any of the conditions.
func TestMicroScalp_Scalp1mNoEntry(t *testing.T) {
	for name, tc := range map[string]struct {
		configure func(*MicroScalpConfig)
		fixed     kv
		edit      func([]ivCandle)
	}{
		"a quiet hour":                  {edit: func(c []ivCandle) { c[0].at = "06:00" }},
		"the ADX low":                   {fixed: kv{"ms_adx": 15}},
		"the volume low":                {fixed: kv{"ms_volume": 2}},
		"a higher timeframe against it": {fixed: kv{"ms_htf_fast_1": 99}},
		"the EMAs too close": {edit: func(c []ivCandle) {
			for k := range c {
				c[k].meta["ms_slow"] = c[k].meta["ms_fast"] - 0.05
			}
		}},
		"no pullback": {edit: func(c []ivCandle) {
			for k := 18; k < 21; k++ {
				c[k].low = c[k].meta["ms_fast"] + 0.01
			}
		}},
		"a close under the slow EMA": {edit: func(c []ivCandle) {
			c[19].close, c[19].low = c[19].meta["ms_slow"]-0.1, c[19].meta["ms_slow"]-0.2
		}},
		"no momentum": {edit: func(c []ivCandle) { c[21].meta["ms_hist"] = -0.1 }},
		"the RSI overbought": {edit: func(c []ivCandle) {
			c[21].meta["ms_rsi"] = 80
		}},
		"too far from the fast EMA": {edit: func(c []ivCandle) {
			c[21].close, c[21].high = c[21].meta["ms_fast"]+1.2, c[21].meta["ms_fast"]+1.3
		}},
		"after 3 strong candles": {edit: func(c []ivCandle) {
			for k := 19; k <= 21; k++ {
				fast := c[k].meta["ms_fast"]
				c[k].open, c[k].close, c[k].low, c[k].high = fast-0.05, fast+0.5, fast-0.1, fast+0.55
			}
		}},
		"too quiet for the costs": {configure: func(c *MicroScalpConfig) { c.MinATRPct = 2 }},
	} {
		t.Run(name, func(t *testing.T) {
			candles := msScenario(22, 18, 1)
			if tc.edit != nil {
				tc.edit(candles)
			}
			r, _ := newMSRunner(t, DefaultScalp1mConfig(), tc.configure)
			for key, value := range tc.fixed {
				r.fixed[key] = value
			}
			r.feed(candles...)
		})
	}
}

// scalp1m: its stop loss to the entry from 0.8 R, 1 ATR behind the close from
// 1.5 R; closed on a close under the slow EMA, and after 10 candles without
// reaching 0.5 R.
func TestMicroScalp_Scalp1mExits(t *testing.T) {
	entered := func(t *testing.T, configure func(*MicroScalpConfig)) (*ivRunner, *MicroScalp) {
		candles := msScenario(22, 18, 1)
		candles[21].position = 10
		r, s := newMSRunner(t, DefaultScalp1mConfig(), configure)
		r.feed(candles...)
		msFixed(r, 22, 1)
		return r, s
	}
	stop := func(s *MicroScalp) float64 { return *s.bracket.trades["BTCUSDT"].legs[1].Stop }

	t.Run("the slow EMA", func(t *testing.T) {
		r, _ := entered(t, nil)
		r.feed(ivCandle{close: 101.5}) // the slow EMA at 101.6
	})
	t.Run("the break-even and the trailing", func(t *testing.T) {
		r, s := entered(t, func(c *MicroScalpConfig) { c.TargetR = 3 })
		r.feed(ivCandle{close: 103.3, position: 10}) // 0.7 R
		require.InDelta(t, 101.6, stop(s), 1e-9)
		r.feed(ivCandle{close: 103.5, position: 10}) // 0.9 R
		require.InDelta(t, 102.6, stop(s), 1e-9, "to the entry")
		r.feed(ivCandle{close: 104.2, position: 10}) // 1.6 R
		require.InDelta(t, 103.2, stop(s), 1e-9, "1 ATR behind the close")
		r.feed(ivCandle{close: 103.9, position: 10})
		require.InDelta(t, 103.2, stop(s), 1e-9, "never back")
	})
	t.Run("going nowhere", func(t *testing.T) {
		r, _ := entered(t, nil)
		for range 9 {
			r.feed(ivCandle{close: 102.8, position: 10}) // 0.2 R
		}
		r.feed(ivCandle{close: 102.8})
	})
}

// scalp1mbtc: only the strong bursts: the ADX rising with the DI of its side on
// top, the volume 1.2 times its average, beyond the SMA 200, room ahead; the
// stop loss 1.5 to 2 ATR away, the take profit 2 times the risk, 3 ATR away at
// least; closed on the momentum falling with the other DI on top.
func TestMicroScalp_Scalp1mBTC(t *testing.T) {
	burst := func(t *testing.T, configure func(*MicroScalpConfig), edit func([]ivCandle)) (*ivRunner, *MicroScalp) {
		candles := msScenario(62, 58, 1)
		candles[61].meta["ms_adx"] = 26 // rising from 25
		if edit != nil {
			edit(candles)
		}
		r, s := newMSRunner(t, DefaultScalp1mBTCConfig(), configure)
		r.fixed["ms_volume"] = 0.8
		return r, s
	}
	candles := msScenario(62, 58, 1)
	candles[61].meta["ms_adx"] = 26
	candles[61].position = 10
	r, s := burst(t, nil, nil)
	r.feed(candles...)
	legs := s.bracket.trades["BTCUSDT"].legs
	require.InDelta(t, 106.6-1.5, *legs[1].Stop, 1e-9, "1.5 ATR under the close of 106.6, the low nearer")
	require.InDelta(t, 106.6+3, legs[0].Price, 1e-9, "2 times the risk, 3 ATR")

	msFixed(r, 62, 1)
	r.feed(ivCandle{close: 106.8, meta: kv{"ms_adx": 24, "ms_pdi": 10, "ms_mdi": 30}}) // the momentum turned

	for name, tc := range map[string]struct {
		fixed kv
		edit  func([]ivCandle)
	}{
		"the ADX not rising":  {edit: func(c []ivCandle) { c[61].meta["ms_adx"] = 25 }},
		"the other DI on top": {fixed: kv{"ms_pdi": 10, "ms_mdi": 30}},
		"under the SMA 200":   {fixed: kv{"ms_trend": 110}},
		"the volume low":      {fixed: kv{"ms_volume": 1}},
		"a close under the fast EMA": {edit: func(c []ivCandle) {
			c[59].close = c[59].meta["ms_fast"] - 0.02
		}},
		"a high in the way": {edit: func(c []ivCandle) { c[10].high = 108 }},
	} {
		t.Run(name, func(t *testing.T) {
			candles := msScenario(62, 58, 1)
			candles[61].meta["ms_adx"] = 26
			if tc.edit != nil {
				tc.edit(candles)
			}
			r, _ := burst(t, nil, nil)
			for key, value := range tc.fixed {
				r.fixed[key] = value
			}
			r.feed(candles...)
		})
	}
}
