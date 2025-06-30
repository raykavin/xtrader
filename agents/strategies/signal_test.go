package strategies

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// kv are the indicators of a candle of a signal strategy test.
type kv = map[string]float64

func newSignalStrategy(t *testing.T, name string, configure func(*SignalConfig)) *SignalStrategy {
	t.Helper()
	cfg := DefaultSignalConfig(name)
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewSignal(name, cfg)
	require.NoError(t, err)
	return s
}

// newSignalRunner feeds the signal strategy name, with the ATR at 1.
func newSignalRunner(t *testing.T, name string, configure func(*SignalConfig)) (*ivRunner, *SignalStrategy) {
	t.Helper()
	s := newSignalStrategy(t, name, configure)
	r := newIVRunner(t, s, 0)
	r.fixed["sg_atr"] = 1
	return r, s
}

// The strategies asked for, each with the timeframes it trades on, see
// agents.yml; the ones of other strategies are EMA + Pullback (emapullback),
// Breakout + ATR (atrbreakout), VWAP Trend Following (vwap) and Opening Range
// Breakout (orb).
func TestSignals_Coverage(t *testing.T) {
	names := make([]string, 0, len(signals))
	for name, spec := range signals {
		names = append(names, name)
		require.NotEmpty(t, spec.about, name)
		require.NotNil(t, spec.rule, name)
		about, ok := SignalAbout(name)
		require.True(t, ok)
		require.Equal(t, spec.about, about)
	}
	slices.Sort(names)
	require.Equal(t, []string{"adxdonchian", "adxema", "atrtrailing", "bbbreakout", "channel", "donchian",
		"donchianatr", "ema200", "emacross", "emamacd", "emarsiadx", "emasupertrend", "highlow", "ichimoku",
		"ichimokuadx", "keltner", "macdtrend", "momentumbreakout", "mtfema", "mtftrend", "papullback", "psarema",
		"ribbon", "rsitrend", "stochpullback", "supertrend", "volumebreakout"}, names)
	_, ok := SignalAbout("nothing")
	require.False(t, ok)
}

func TestDefaultSignalConfig(t *testing.T) {
	cfg := DefaultSignalConfig("emacross")
	require.Equal(t, "15m", cfg.Timeframe)
	require.Equal(t, 1.0, cfg.StopATR)
	require.Equal(t, 1.5, cfg.TakeProfitATR)
	require.Zero(t, cfg.TrailingATR)
	require.Zero(t, cfg.MaxBars, "the opposite signal closes the position")

	turtle := DefaultSignalConfig("donchianatr")
	require.Equal(t, 2.0, turtle.StopATR)
	require.Zero(t, turtle.TakeProfitATR, "no take profit")
	require.Equal(t, 2.0, turtle.TrailingATR)
}

func TestNewSignal_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		strategy  string
		configure func(*SignalConfig)
		err       string
	}{
		{"unknown strategy", "nothing", nil, "invalid signal strategy"},
		{"timeframe", "emacross", func(c *SignalConfig) { c.Timeframe = "" }, "timeframe is required"},
		{"invalid timeframe", "emacross", func(c *SignalConfig) { c.Timeframe = "15x" }, "invalid timeframe"},
		{"negative trailing", "donchianatr", func(c *SignalConfig) { c.TrailingATR = -1 }, "trailing"},
		{"no stop loss", "emacross", func(c *SignalConfig) { c.StopATR = 0 }, "stop loss and take profit"},
		{"negative take profit", "emacross", func(c *SignalConfig) { c.TakeProfitATR = -1 }, "stop loss and take profit"},
		{"the sizing", "emacross", func(c *SignalConfig) { c.MinPositionValue = 0 }, "min position value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultSignalConfig(tt.strategy)
			if tt.configure != nil {
				tt.configure(&cfg)
			}
			_, err := NewSignal(tt.strategy, cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}
}

func TestSignalStrategy_WarmupPeriod(t *testing.T) {
	warmup := func(name, timeframe string) int {
		return newSignalStrategy(t, name, func(c *SignalConfig) { c.Timeframe = timeframe }).WarmupPeriod()
	}
	require.Equal(t, seedWarmup(21)+1, warmup("emacross", "15m"), "the EMA 21 seeded, and the candle before")
	require.Equal(t, seedWarmup(200)+1, warmup("ema200", "1h"))
	require.Equal(t, seedWarmup(14), warmup("donchian", "1h"), "the ATR of the exits seeded")
	require.Equal(t, (seedWarmup(50)+2)*4, warmup("mtftrend", "5m"), "the EMA 50 of the 4x timeframe seeded")
	require.Equal(t, (seedWarmup(50)+2)*16, warmup("mtfema", "15m"))
	require.Equal(t, 3*24*12+1, warmup("highlow", "5m"), "3 days of candles")
	for name := range signals {
		require.GreaterOrEqual(t, warmup(name, "1h"), seedWarmup(14), name)
	}
}

// mirrorCandles returns the candles of a signal test mirrored around 100, for the
// other side: the prices, their indicators and the positions, the oscillators
// mirrored around their middle, the bands and the DI swapped.
func mirrorCandles(candles []ivCandle) []ivCandle {
	price := func(v float64) float64 { return 200 - v }
	out := make([]ivCandle, len(candles))
	for i, c := range candles {
		m := ivCandle{at: c.at, open: price(c.open), close: price(c.close), volume: c.volume, position: -c.position}
		if c.open == 0 {
			m.open = 0 // the close
		}
		if c.high != 0 {
			m.low = price(c.high)
		}
		if c.low != 0 {
			m.high = price(c.low)
		}
		if c.meta != nil {
			m.meta = kv{}
		}
		for key, v := range c.meta {
			switch {
			case key == "sg_adx" || key == "sg_volume":
				m.meta[key] = v
			case key == "sg_plus":
				m.meta["sg_minus"] = v
			case key == "sg_minus":
				m.meta["sg_plus"] = v
			case key == "sg_upper":
				m.meta["sg_lower"] = price(v)
			case key == "sg_lower":
				m.meta["sg_upper"] = price(v)
			case key == "sg_day_high":
				m.meta["sg_day_low"] = price(v)
			case key == "sg_day_low":
				m.meta["sg_day_high"] = price(v)
			case key == "sg_rsi" || strings.HasPrefix(key, "sg_stoch"):
				m.meta[key] = 100 - v
			case strings.HasPrefix(key, "sg_macd") || key == "sg_chandelier":
				m.meta[key] = -v
			default:
				m.meta[key] = price(v)
			}
		}
		out[i] = m
	}
	return out
}

// Each rule, on candles closing at 100 when it fires: a BUY of 100, the whole
// balance, and a SELL of 100 on the candles mirrored; and without an entry on
// the candles of its filters.
func TestSignalStrategy_Entries(t *testing.T) {
	fill := func(n int, closePrice float64) []ivCandle {
		candles := make([]ivCandle, n)
		for i := range candles {
			candles[i] = ivCandle{close: closePrice}
		}
		return candles
	}
	tests := []struct {
		name string
		long []ivCandle   // the last candle bought
		none [][]ivCandle // without an entry
	}{
		{
			name: "emacross",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_fast": 99, "sg_slow": 100}},
				{close: 100, meta: kv{"sg_fast": 101, "sg_slow": 100}, position: 100},
			},
			none: [][]ivCandle{{ // already above
				{close: 100, meta: kv{"sg_fast": 101, "sg_slow": 100}},
				{close: 100, meta: kv{"sg_fast": 102, "sg_slow": 100}},
			}},
		},
		{
			name: "ema200",
			long: []ivCandle{
				{close: 99, meta: kv{"sg_trend": 99.5}},
				{close: 100, meta: kv{"sg_trend": 99.6}, position: 100},
			},
			none: [][]ivCandle{{ // the EMA falling
				{close: 99, meta: kv{"sg_trend": 99.7}},
				{close: 100, meta: kv{"sg_trend": 99.6}},
			}},
		},
		{
			name: "ribbon",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_ribbon0": 99, "sg_ribbon1": 98, "sg_ribbon2": 97, "sg_ribbon3": 95,
					"sg_ribbon4": 96}},
				{close: 100, meta: kv{"sg_ribbon0": 99, "sg_ribbon1": 98, "sg_ribbon2": 97, "sg_ribbon3": 96,
					"sg_ribbon4": 95}, position: 100},
			},
			none: [][]ivCandle{{ // already lined up
				{close: 100, meta: kv{"sg_ribbon0": 99, "sg_ribbon1": 98, "sg_ribbon2": 97, "sg_ribbon3": 96,
					"sg_ribbon4": 95}},
				{close: 100, meta: kv{"sg_ribbon0": 99, "sg_ribbon1": 98, "sg_ribbon2": 97, "sg_ribbon3": 96,
					"sg_ribbon4": 95}},
			}},
		},
		{
			name: "supertrend",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_supertrend": 101}},
				{close: 100, meta: kv{"sg_supertrend": 99}, position: 100},
			},
			none: [][]ivCandle{{ // no flip
				{close: 100, meta: kv{"sg_supertrend": 98}},
				{close: 100, meta: kv{"sg_supertrend": 99}},
			}},
		},
		{
			name: "emasupertrend",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_supertrend": 101, "sg_trend": 95}},
				{close: 100, meta: kv{"sg_supertrend": 99, "sg_trend": 95}, position: 100},
			},
			none: [][]ivCandle{{ // below the EMA 200
				{close: 100, meta: kv{"sg_supertrend": 101, "sg_trend": 105}},
				{close: 100, meta: kv{"sg_supertrend": 99, "sg_trend": 105}},
			}},
		},
		{
			name: "psarema",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_sar": 101, "sg_trend": 95}},
				{close: 100, meta: kv{"sg_sar": 98, "sg_trend": 95}, position: 100},
			},
			none: [][]ivCandle{
				{ // below the EMA 50
					{close: 100, meta: kv{"sg_sar": 101, "sg_trend": 105}},
					{close: 100, meta: kv{"sg_sar": 98, "sg_trend": 105}},
				},
				{ // no flip
					{close: 100, meta: kv{"sg_sar": 97, "sg_trend": 95}},
					{close: 100, meta: kv{"sg_sar": 98, "sg_trend": 95}},
				},
			},
		},
		{
			name: "atrtrailing",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_chandelier": -1}},
				{close: 100, meta: kv{"sg_chandelier": 1}, position: 100},
			},
			none: [][]ivCandle{{ // no flip
				{close: 100, meta: kv{"sg_chandelier": 1}},
				{close: 100, meta: kv{"sg_chandelier": 1}},
			}},
		},
		{
			name: "ichimoku",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_tenkan": 97, "sg_kijun": 98, "sg_span_a": 95, "sg_span_b": 96}},
				{close: 100, meta: kv{"sg_tenkan": 99, "sg_kijun": 98, "sg_span_a": 95, "sg_span_b": 96}, position: 100},
			},
			none: [][]ivCandle{{ // in the cloud
				{close: 100, meta: kv{"sg_tenkan": 97, "sg_kijun": 98, "sg_span_a": 99, "sg_span_b": 101}},
				{close: 100, meta: kv{"sg_tenkan": 99, "sg_kijun": 98, "sg_span_a": 99, "sg_span_b": 101}},
			}},
		},
		{
			name: "ichimokuadx",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_tenkan": 97, "sg_kijun": 98, "sg_span_a": 95, "sg_span_b": 96, "sg_adx": 30}},
				{close: 100, meta: kv{"sg_tenkan": 99, "sg_kijun": 98, "sg_span_a": 95, "sg_span_b": 96, "sg_adx": 30},
					position: 100},
			},
			none: [][]ivCandle{{ // a weak trend
				{close: 100, meta: kv{"sg_tenkan": 97, "sg_kijun": 98, "sg_span_a": 95, "sg_span_b": 96, "sg_adx": 20}},
				{close: 100, meta: kv{"sg_tenkan": 99, "sg_kijun": 98, "sg_span_a": 95, "sg_span_b": 96, "sg_adx": 20}},
			}},
		},
		{
			name: "mtftrend",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_fast": 99, "sg_slow": 100, "sg_htf": 98, "sg_htf_previous": 97}},
				{close: 100, meta: kv{"sg_fast": 101, "sg_slow": 100, "sg_htf": 98, "sg_htf_previous": 97},
					position: 100},
			},
			none: [][]ivCandle{{ // the higher EMA falling
				{close: 100, meta: kv{"sg_fast": 99, "sg_slow": 100, "sg_htf": 97, "sg_htf_previous": 98}},
				{close: 100, meta: kv{"sg_fast": 101, "sg_slow": 100, "sg_htf": 97, "sg_htf_previous": 98}},
			}},
		},
		{
			name: "mtfema",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_fast": 99, "sg_slow": 100, "sg_htf0_fast": 99, "sg_htf0_slow": 98,
					"sg_htf1_fast": 97, "sg_htf1_slow": 96}},
				{close: 100, meta: kv{"sg_fast": 101, "sg_slow": 100, "sg_htf0_fast": 99, "sg_htf0_slow": 98,
					"sg_htf1_fast": 97, "sg_htf1_slow": 96}, position: 100},
			},
			none: [][]ivCandle{{ // the 16x timeframe the other way
				{close: 100, meta: kv{"sg_fast": 99, "sg_slow": 100, "sg_htf0_fast": 99, "sg_htf0_slow": 98,
					"sg_htf1_fast": 96, "sg_htf1_slow": 97}},
				{close: 100, meta: kv{"sg_fast": 101, "sg_slow": 100, "sg_htf0_fast": 99, "sg_htf0_slow": 98,
					"sg_htf1_fast": 96, "sg_htf1_slow": 97}},
			}},
		},
		{
			name: "adxema",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_trend": 99, "sg_plus": 30, "sg_minus": 20, "sg_adx": 24}},
				{close: 100, meta: kv{"sg_trend": 99, "sg_plus": 30, "sg_minus": 20, "sg_adx": 26}, position: 100},
			},
			none: [][]ivCandle{
				{ // trending already
					{close: 100, meta: kv{"sg_trend": 99, "sg_plus": 30, "sg_minus": 20, "sg_adx": 26}},
					{close: 100, meta: kv{"sg_trend": 99, "sg_plus": 30, "sg_minus": 20, "sg_adx": 27}},
				},
				{ // the -DI above the +DI
					{close: 100, meta: kv{"sg_trend": 99, "sg_plus": 20, "sg_minus": 30, "sg_adx": 24}},
					{close: 100, meta: kv{"sg_trend": 99, "sg_plus": 20, "sg_minus": 30, "sg_adx": 26}},
				},
			},
		},
		{
			name: "macdtrend",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_macd": 1, "sg_macd_signal": 1.5}},
				{close: 100, meta: kv{"sg_macd": 2, "sg_macd_signal": 1.5}, position: 100},
			},
			none: [][]ivCandle{{ // under zero
				{close: 100, meta: kv{"sg_macd": -2, "sg_macd_signal": -1.5}},
				{close: 100, meta: kv{"sg_macd": -1, "sg_macd_signal": -1.5}},
			}},
		},
		{
			name: "emamacd",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_fast": 99, "sg_slow": 100, "sg_macd_hist": 0.5}},
				{close: 100, meta: kv{"sg_fast": 101, "sg_slow": 100, "sg_macd_hist": 0.5}, position: 100},
			},
			none: [][]ivCandle{{ // the histogram against it
				{close: 100, meta: kv{"sg_fast": 99, "sg_slow": 100, "sg_macd_hist": -0.5}},
				{close: 100, meta: kv{"sg_fast": 101, "sg_slow": 100, "sg_macd_hist": -0.5}},
			}},
		},
		{
			name: "emarsiadx",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_fast": 99, "sg_slow": 100, "sg_rsi": 55, "sg_adx": 25}},
				{close: 100, meta: kv{"sg_fast": 101, "sg_slow": 100, "sg_rsi": 55, "sg_adx": 25}, position: 100},
			},
			none: [][]ivCandle{
				{ // a weak trend
					{close: 100, meta: kv{"sg_fast": 99, "sg_slow": 100, "sg_rsi": 55, "sg_adx": 15}},
					{close: 100, meta: kv{"sg_fast": 101, "sg_slow": 100, "sg_rsi": 55, "sg_adx": 15}},
				},
				{ // the RSI under 50
					{close: 100, meta: kv{"sg_fast": 99, "sg_slow": 100, "sg_rsi": 45, "sg_adx": 25}},
					{close: 100, meta: kv{"sg_fast": 101, "sg_slow": 100, "sg_rsi": 45, "sg_adx": 25}},
				},
			},
		},
		{
			name: "rsitrend",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_trend": 99, "sg_rsi": 45}},
				{close: 100, meta: kv{"sg_trend": 99, "sg_rsi": 55}, position: 100},
			},
			none: [][]ivCandle{{ // below the EMA 50
				{close: 100, meta: kv{"sg_trend": 101, "sg_rsi": 45}},
				{close: 100, meta: kv{"sg_trend": 101, "sg_rsi": 55}},
			}},
		},
		{
			name: "stochpullback",
			long: []ivCandle{
				{close: 100, meta: kv{"sg_trend": 99, "sg_stoch_k": 10, "sg_stoch_d": 12}},
				{close: 100, meta: kv{"sg_trend": 99, "sg_stoch_k": 15, "sg_stoch_d": 13}, position: 100},
			},
			none: [][]ivCandle{
				{ // out of the extreme zone
					{close: 100, meta: kv{"sg_trend": 99, "sg_stoch_k": 20, "sg_stoch_d": 22}},
					{close: 100, meta: kv{"sg_trend": 99, "sg_stoch_k": 25, "sg_stoch_d": 23}},
				},
				{ // below the EMA 50
					{close: 100, meta: kv{"sg_trend": 101, "sg_stoch_k": 10, "sg_stoch_d": 12}},
					{close: 100, meta: kv{"sg_trend": 101, "sg_stoch_k": 15, "sg_stoch_d": 13}},
				},
			},
		},
		{
			name: "papullback",
			long: []ivCandle{
				{close: 100.2, meta: kv{"sg_trend": 98}},
				{open: 100.2, close: 99.9, high: 100.3, low: 99.8, meta: kv{"sg_trend": 98.1}},
				{open: 99.9, close: 99.5, high: 99.95, low: 99.4, meta: kv{"sg_trend": 98.2}},
				{open: 99.5, close: 100, high: 100.05, low: 99.5, meta: kv{"sg_trend": 98.3}, position: 100},
			},
			none: [][]ivCandle{
				{ // a single candle down
					{close: 100.2, meta: kv{"sg_trend": 98}},
					{open: 99.8, close: 99.9, high: 100.3, low: 99.8, meta: kv{"sg_trend": 98.1}},
					{open: 99.9, close: 99.5, high: 99.95, low: 99.4, meta: kv{"sg_trend": 98.2}},
					{open: 99.5, close: 100, high: 100.05, low: 99.5, meta: kv{"sg_trend": 98.3}},
				},
				{ // under the previous high
					{close: 100.2, meta: kv{"sg_trend": 98}},
					{open: 100.2, close: 99.9, high: 100.3, low: 99.8, meta: kv{"sg_trend": 98.1}},
					{open: 99.9, close: 99.5, high: 100.1, low: 99.4, meta: kv{"sg_trend": 98.2}},
					{open: 99.5, close: 100, high: 100.05, low: 99.5, meta: kv{"sg_trend": 98.3}},
				},
				{ // the EMA falling
					{close: 100.2, meta: kv{"sg_trend": 98}},
					{open: 100.2, close: 99.9, high: 100.3, low: 99.8, meta: kv{"sg_trend": 98.5}},
					{open: 99.9, close: 99.5, high: 99.95, low: 99.4, meta: kv{"sg_trend": 98.4}},
					{open: 99.5, close: 100, high: 100.05, low: 99.5, meta: kv{"sg_trend": 98.3}},
				},
			},
		},
		{
			name: "donchian",
			long: []ivCandle{
				{close: 99, meta: kv{"sg_upper": 99.5, "sg_lower": 95}},
				{close: 100, meta: kv{"sg_upper": 100, "sg_lower": 95}, position: 100},
			},
			none: [][]ivCandle{{ // within the channel
				{close: 99, meta: kv{"sg_upper": 101, "sg_lower": 95}},
				{close: 100, meta: kv{"sg_upper": 101, "sg_lower": 95}},
			}},
		},
		{
			name: "channel",
			long: []ivCandle{
				{close: 99, meta: kv{"sg_upper": 99.5, "sg_lower": 95}},
				{close: 100, meta: kv{"sg_upper": 100, "sg_lower": 95}, position: 100},
			},
		},
		{
			name: "adxdonchian",
			long: []ivCandle{
				{close: 99, meta: kv{"sg_upper": 99.5, "sg_lower": 95, "sg_adx": 30}},
				{close: 100, meta: kv{"sg_upper": 100, "sg_lower": 95, "sg_adx": 30}, position: 100},
			},
			none: [][]ivCandle{{ // a weak trend
				{close: 99, meta: kv{"sg_upper": 99.5, "sg_lower": 95, "sg_adx": 20}},
				{close: 100, meta: kv{"sg_upper": 100, "sg_lower": 95, "sg_adx": 20}},
			}},
		},
		{
			name: "donchianatr",
			long: []ivCandle{
				{close: 99, meta: kv{"sg_upper": 99.5, "sg_lower": 95}},
				{close: 100, meta: kv{"sg_upper": 100, "sg_lower": 95}, position: 100},
			},
		},
		{
			name: "bbbreakout",
			long: []ivCandle{
				{close: 99, meta: kv{"sg_upper": 99.5, "sg_middle": 98, "sg_lower": 96.5}},
				{close: 100, meta: kv{"sg_upper": 99.8, "sg_middle": 98.1, "sg_lower": 96.4}, position: 100},
			},
			none: [][]ivCandle{{ // the middle band falling
				{close: 99, meta: kv{"sg_upper": 99.5, "sg_middle": 98.2, "sg_lower": 96.5}},
				{close: 100, meta: kv{"sg_upper": 99.8, "sg_middle": 98.1, "sg_lower": 96.4}},
			}},
		},
		{
			name: "keltner",
			long: []ivCandle{
				{close: 99, meta: kv{"sg_upper": 99.5, "sg_lower": 96}},
				{close: 100, meta: kv{"sg_upper": 99.6, "sg_lower": 96}, position: 100},
			},
			none: [][]ivCandle{{ // beyond the band already
				{close: 99.9, meta: kv{"sg_upper": 99.5, "sg_lower": 96}},
				{close: 100, meta: kv{"sg_upper": 99.6, "sg_lower": 96}},
			}},
		},
		{
			name: "volumebreakout",
			long: []ivCandle{
				{close: 99, meta: kv{"sg_upper": 99.5, "sg_lower": 95, "sg_volume": 1}},
				{close: 100, volume: 1.5, meta: kv{"sg_upper": 100, "sg_lower": 95, "sg_volume": 1}, position: 100},
			},
			none: [][]ivCandle{{ // on a thin volume
				{close: 99, meta: kv{"sg_upper": 99.5, "sg_lower": 95, "sg_volume": 1}},
				{close: 100, volume: 1.4, meta: kv{"sg_upper": 100, "sg_lower": 95, "sg_volume": 1}},
			}},
		},
		{
			name: "highlow",
			long: []ivCandle{
				{close: 99, meta: kv{"sg_day_high": 99.5, "sg_day_low": 95}},
				{close: 100, meta: kv{"sg_day_high": 99.5, "sg_day_low": 95}, position: 100},
			},
			none: [][]ivCandle{{ // above it already
				{close: 99.8, meta: kv{"sg_day_high": 99.5, "sg_day_low": 95}},
				{close: 100, meta: kv{"sg_day_high": 99.5, "sg_day_low": 95}},
			}},
		},
		{
			name: "momentumbreakout",
			long: append(fill(10, 95),
				ivCandle{close: 99, meta: kv{"sg_upper": 99.5, "sg_lower": 94}},
				ivCandle{open: 98.5, close: 100, high: 100.2, low: 98.4, meta: kv{"sg_upper": 100.2, "sg_lower": 94},
					position: 100}),
			none: [][]ivCandle{
				append(fill(10, 95), // a small body
					ivCandle{close: 99, meta: kv{"sg_upper": 99.5, "sg_lower": 94}},
					ivCandle{open: 99.8, close: 100, high: 100.5, low: 99, meta: kv{"sg_upper": 100.5, "sg_lower": 94}}),
				append(fill(10, 101), // the ROC against it
					ivCandle{close: 99, meta: kv{"sg_upper": 99.5, "sg_lower": 94}},
					ivCandle{open: 98.5, close: 100, high: 100.2, low: 98.4, meta: kv{"sg_upper": 100.2, "sg_lower": 94}}),
			},
		},
	}

	tested := map[string]bool{}
	for _, tt := range tests {
		tested[tt.name] = true
		t.Run(tt.name+" buy", func(t *testing.T) {
			r, _ := newSignalRunner(t, tt.name, nil)
			r.feed(tt.long...)
		})
		t.Run(tt.name+" sell", func(t *testing.T) {
			r, _ := newSignalRunner(t, tt.name, nil)
			r.feed(mirrorCandles(tt.long)...)
		})
		for _, candles := range tt.none {
			t.Run(tt.name+" no entry", func(t *testing.T) {
				r, _ := newSignalRunner(t, tt.name, nil)
				r.feed(candles...)
				r2, _ := newSignalRunner(t, tt.name, nil)
				r2.feed(mirrorCandles(candles)...)
			})
		}
	}
	for name := range signals {
		require.True(t, tested[name], "untested: %s", name)
	}
}

// the entries of the tests of the exits: 100 bought or sold at 100 on the EMA
// 9 crossing the EMA 21
var (
	sgLong = []ivCandle{
		{close: 100, meta: kv{"sg_fast": 99, "sg_slow": 100}},
		{close: 100, meta: kv{"sg_fast": 101, "sg_slow": 100}, position: 100},
	}
	sgHold = ivCandle{close: 100.5, meta: kv{"sg_fast": 101, "sg_slow": 100}, position: 100}
)

func TestSignalStrategy_Exits(t *testing.T) {
	t.Run("the legs at the broker", func(t *testing.T) {
		r, s := newSignalRunner(t, "emacross", nil)
		r.feed(sgLong...)
		legs := s.bracket.trades["BTCUSDT"].legs
		require.Len(t, legs, 2)
		require.InDelta(t, 101.5, legs[0].Price, 1e-9, "the take profit 1.5 ATR away")
		require.InDelta(t, 99, *legs[1].Stop, 1e-9, "the stop loss 1 ATR away")
	})

	t.Run("the opposite signal closes the long and opens the short", func(t *testing.T) {
		r, s := newSignalRunner(t, "emacross", nil)
		r.feed(sgLong...)
		legs := s.bracket.trades["BTCUSDT"].legs
		r.feed(sgHold, ivCandle{close: 100, meta: kv{"sg_fast": 99, "sg_slow": 100}, position: -100})
		require.Equal(t, model.SideTypeSell, s.bracket.trades["BTCUSDT"].side)
		for _, leg := range legs {
			got, err := r.wallet.Order("BTCUSDT", leg.ExchangeID)
			require.NoError(t, err)
			require.Equal(t, model.OrderStatusTypeCanceled, got.Status, "the legs of the long, %s", leg.Type)
		}
	})

	t.Run("the opposite signal only closes the long of a buy only strategy", func(t *testing.T) {
		r, _ := newSignalRunner(t, "emacross", func(c *SignalConfig) { c.Direction = RKDirectionBuyOnly })
		r.feed(sgLong...)
		r.feed(ivCandle{close: 101, meta: kv{"sg_fast": 99, "sg_slow": 100}, position: 0})
		require.InDelta(t, 10000+100*1, r.quote(), 1e-9)
	})

	t.Run("the signal of the side held keeps it", func(t *testing.T) {
		r, _ := newSignalRunner(t, "donchian", nil)
		r.feed(ivCandle{close: 99, meta: kv{"sg_upper": 99.5, "sg_lower": 95}},
			ivCandle{close: 100, meta: kv{"sg_upper": 100, "sg_lower": 95}, position: 100},
			ivCandle{close: 100.5, meta: kv{"sg_upper": 100.5, "sg_lower": 95}, position: 100})
	})

	t.Run("the take profit closes the long", func(t *testing.T) {
		r, s := newSignalRunner(t, "emacross", nil)
		r.feed(sgLong...)
		r.feed(ivCandle{close: 101, high: 102, low: 100, meta: kv{"sg_fast": 101, "sg_slow": 100}})
		require.InDelta(t, 10000+100*1.5, r.quote(), 1e-9)
		require.Empty(t, s.bracket.trades)
	})
}

// donchianatr: its stop loss 2 ATR away without a take profit, trailing 2 ATR
// behind the best price, never back.
func TestSignalStrategy_Trailing(t *testing.T) {
	breakout := []ivCandle{
		{close: 99, meta: kv{"sg_upper": 99.5, "sg_lower": 95}},
		{close: 100, meta: kv{"sg_upper": 100, "sg_lower": 95}, position: 100},
	}
	stop := func(s *SignalStrategy) float64 {
		legs := s.bracket.trades["BTCUSDT"].legs
		require.Len(t, legs, 1, "the stop loss alone")
		require.Equal(t, model.OrderTypeStopLoss, legs[0].Type)
		return *legs[0].Stop
	}

	r, s := newSignalRunner(t, "donchianatr", nil)
	r.feed(breakout...)
	require.InDelta(t, 98, stop(s), 1e-9)

	r.feed(ivCandle{open: 100, close: 102.5, high: 103, low: 100, position: 100})
	require.InDelta(t, 101, stop(s), 1e-9, "2 ATR behind the high of 103")
	r.feed(ivCandle{open: 102.5, close: 101.8, high: 102, low: 101.5, position: 100})
	require.InDelta(t, 101, stop(s), 1e-9, "never back")
	r.feed(ivCandle{open: 101.8, close: 101.2, high: 101.9, low: 100.9, position: 0})
	require.InDelta(t, 10000+100*1, r.quote(), 1e-9, "stopped out at 101")

	t.Run("adjustable", func(t *testing.T) {
		r, s := newSignalRunner(t, "donchianatr", func(c *SignalConfig) { c.TrailingATR = 1 })
		r.feed(breakout...)
		r.feed(ivCandle{open: 100, close: 102.5, high: 103, low: 100, position: 100})
		require.InDelta(t, 102, stop(s), 1e-9)
	})

	t.Run("on the losing side of the close only", func(t *testing.T) {
		r, s := newSignalRunner(t, "donchianatr", func(c *SignalConfig) { c.TrailingATR = 0.5 })
		r.feed(breakout...)
		r.feed(ivCandle{open: 100, close: 100.2, high: 103, low: 100, position: 100})
		require.InDelta(t, 98, stop(s), 1e-9, "102.5 above the close of 100.2")
	})

	t.Run("off", func(t *testing.T) {
		r, s := newSignalRunner(t, "donchian", func(c *SignalConfig) { c.MaxBars = 0 })
		r.feed(breakout...)
		r.feed(ivCandle{open: 100, close: 101, high: 101.2, low: 100, position: 100})
		require.InDelta(t, 99, *s.bracket.trades["BTCUSDT"].legs[1].Stop, 1e-9)
	})
}

func TestSignalStrategy_Backtest(t *testing.T) {
	for name := range signals {
		if name == "mtfema" {
			continue // its 16x timeframe needs more candles than the test data holds
		}
		t.Run(name, func(t *testing.T) {
			checkBacktest(t, newSignalStrategy(t, name, func(c *SignalConfig) { c.Timeframe = "1h" }))
		})
	}
}

func TestCrossedLevel(t *testing.T) {
	require.True(t, crossedLevel([]float64{49, 51}, 50, 1, 1))
	require.True(t, crossedLevel([]float64{50, 51}, 50, 1, 1), "from the level")
	require.False(t, crossedLevel([]float64{51, 52}, 50, 1, 1), "beyond it already")
	require.True(t, crossedLevel([]float64{51, 49}, 50, 1, -1))
	require.False(t, crossedLevel([]float64{na, 51}, 50, 1, 1))
	require.False(t, crossedLevel([]float64{51}, 50, 0, 1))
}

func TestFlipped(t *testing.T) {
	closes := []float64{100, 100, 100}
	require.True(t, flipped(closes, []float64{101, 99, 98}, 1, 1))
	require.False(t, flipped(closes, []float64{101, 99, 98}, 2, 1), "below the close already")
	require.True(t, flipped(closes, []float64{99, 101, 102}, 1, -1))
	require.False(t, flipped(closes, []float64{na, 99, 98}, 1, 1))
}

func TestIchimoku(t *testing.T) {
	high := []float64{10, 11, 12, 13, 14, 15}
	low := []float64{8, 9, 10, 11, 12, 13}
	tenkan, kijun, spanA, spanB := ichimoku(high, low, 2, 3, 4, 3)
	require.True(t, isNa(tenkan[0]))
	require.Equal(t, 9.5, tenkan[1], "(11 + 8) / 2: the highest high and the lowest low of 2 candles")
	require.Equal(t, 11.0, kijun[3], "(13 + 9) / 2, of 3 candles")
	for i := range 2 {
		require.True(t, isNa(spanA[i]), "drawn 3 candles ahead, counting the one computed on")
		require.True(t, isNa(spanB[i]))
	}
	require.Equal(t, (tenkan[3]+kijun[3])/2, spanA[5])
	require.Equal(t, (high[3]+low[0])/2, spanB[5], "the channel of 4 candles of candle 3")
}

func TestKeltner(t *testing.T) {
	high, low, closes := make([]float64, 60), make([]float64, 60), make([]float64, 60)
	for i := range closes {
		closes[i] = 100 + math.Sin(float64(i)/3)
		high[i], low[i] = closes[i]+1, closes[i]-1
	}
	upper, middle, lower := keltner(high, low, closes, 20, 10, 2)
	atr := pineATR(high, low, closes, 10)
	ema := pineEMA(closes, 20)
	for _, i := range []int{30, 59} {
		require.InDelta(t, ema[i], middle[i], 1e-12)
		require.InDelta(t, ema[i]+2*atr[i], upper[i], 1e-12)
		require.InDelta(t, ema[i]-2*atr[i], lower[i], 1e-12)
	}
}

func TestChandelier(t *testing.T) {
	var high, low, closes []float64
	add := func(c float64) {
		closes, high, low = append(closes, c), append(high, c+0.5), append(low, c-0.5)
	}
	for i := range 30 {
		add(100 + float64(i))
	}
	add(110) // a drop of 19
	add(111)

	side, stop := chandelier(high, low, closes, 3, 1)
	require.True(t, isNa(side[0]), "warming up")
	require.Equal(t, 1.0, side[29], "up along the rise")
	for i := 10; i < 30; i++ {
		require.GreaterOrEqual(t, stop[i], stop[i-1], "the stop of the longs only tightens")
		require.Less(t, stop[i], closes[i])
	}
	require.Equal(t, -1.0, side[30], "the drop below the stop of the longs")
	require.Equal(t, -1.0, side[31])
	require.Greater(t, stop[30], closes[30], "the stop of the shorts above")
}

func TestHTFEMA(t *testing.T) {
	// 15m candles closing at their index, the bars of 1h closing at 3, 7, 11 and
	// 15
	start := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	times, closes := make([]time.Time, 17), make([]float64, 17)
	for i := range closes {
		times[i], closes[i] = start.Add(time.Duration(i)*15*time.Minute), float64(i)
	}
	last, previous := htfEMA(times, closes, time.Hour, 2)
	require.True(t, isNa(last[7]), "a single bar closed")
	require.Equal(t, 5.0, last[8], "the SMA of 3 and 7 seeding it")
	require.True(t, isNa(previous[8]))
	require.Equal(t, 9.0, last[12], "2/3 of 11 and 1/3 of 5")
	require.Equal(t, 5.0, previous[12])
	require.Equal(t, 9.0, last[15], "the bar of the candle not closed yet: no look ahead")
	require.Equal(t, 13.0, last[16])
}

func TestPreviousDay(t *testing.T) {
	start := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) // a partial day first
	var times []time.Time
	var high, low []float64
	for i, h := range []float64{50, 60, 10, 12, 11, 13, 20, 21} {
		times = append(times, start.Add(time.Duration(i)*6*time.Hour))
		high, low = append(high, h), append(low, h-1)
	}
	highs, lows := previousDay(times, high, low)
	for i := range 6 {
		require.True(t, isNa(highs[i]), "the day before the second one is partial")
	}
	require.Equal(t, 13.0, highs[6], "the high of the 27th")
	require.Equal(t, 9.0, lows[6])
	require.Equal(t, 13.0, highs[7])
}
