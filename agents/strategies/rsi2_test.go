package strategies

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/strategy"
)

func newRSI2(t *testing.T, configure func(*RSI2Config)) *RSI2 {
	t.Helper()
	cfg := DefaultRSI2Config()
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewRSI2(cfg)
	require.NoError(t, err)
	return s
}

// newR2Runner feeds an RSI2 on daily candles, a position of 10, the close of
// 100 above the SMA 200 at 90 and under the SMA 5 at 105, the RSI at 50 and
// the ATR at 1: no entry and no exit until a candle sets them otherwise.
func newR2Runner(t *testing.T, configure func(*RSI2Config)) (*ivRunner, *RSI2) {
	t.Helper()
	s := newRSI2(t, func(c *RSI2Config) {
		c.PositionSize = map[string]float64{"BTCUSDT": 10}
		if configure != nil {
			configure(c)
		}
	})
	r := newIVRunner(t, s, 0)
	r.step = 24 * time.Hour
	r.fixed = kv{"r2_trend": 90, "r2_exit": 105, "r2_rsi": 50, "r2_atr": 1}
	return r, s
}

// r2 is a candle at 100 with the indicators of meta, holding position after
// it.
func r2(meta kv, position float64) ivCandle {
	return ivCandle{close: 100, meta: meta, position: position}
}

// the entries of the tests: 10 bought at 100 above the SMA 200 with the RSI 2
// at 5, 10 sold at 100 under the SMA 200 with the RSI 2 at 95
var (
	r2Long  = r2(kv{"r2_rsi": 5}, 10)
	r2Short = r2(kv{"r2_trend": 110, "r2_rsi": 95}, -10)
)

func TestNewRSI2_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*RSI2Config)
		err       string
	}{
		{"timeframe", func(c *RSI2Config) { c.Timeframe = "" }, "timeframe is required"},
		{"invalid timeframe", func(c *RSI2Config) { c.Timeframe = "daily" }, "invalid timeframe"},
		{"RSI period", func(c *RSI2Config) { c.RSILen = 0 }, "RSI period"},
		{"trend period", func(c *RSI2Config) { c.TrendLen = 1 }, "SMAs of the trend and of the exit"},
		{"exit period", func(c *RSI2Config) { c.ExitLen = 0 }, "SMAs of the trend and of the exit"},
		{"oversold at 0", func(c *RSI2Config) { c.Oversold = 0 }, "RSI levels"},
		{"overbought at 100", func(c *RSI2Config) { c.Overbought = 100 }, "RSI levels"},
		{"oversold above overbought", func(c *RSI2Config) { c.Oversold, c.Overbought = 60, 40 }, "RSI levels"},
		{"oversold at overbought", func(c *RSI2Config) { c.Oversold, c.Overbought = 50, 50 }, "RSI levels"},
		{"negative trailing", func(c *RSI2Config) { c.TrailingATR = -1 }, "trailing ATR"},
		{"no stop loss", func(c *RSI2Config) { c.StopATR = 0 }, "stop loss and take profit"},
		{"negative take profit", func(c *RSI2Config) { c.TakeProfitATR = -1 }, "stop loss and take profit"},
		{"ATR period", func(c *RSI2Config) { c.ATRLen = 0 }, "at least 1"},
		{"sizing", func(c *RSI2Config) { c.PositionFraction = 2 }, "position fraction"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultRSI2Config()
			tt.configure(&cfg)
			_, err := NewRSI2(cfg)
			require.ErrorContains(t, err, tt.err)
			require.ErrorContains(t, err, "rsi2: ")
		})
	}

	// no take profit, the exit at the SMA alone, as the source
	newRSI2(t, func(c *RSI2Config) { c.TakeProfitATR = 0 })
}

// The defaults of the source: the SMA 200, the RSI 2 under 10 or over 90, the
// SMA 5, daily candles; the safety exits of DefaultBracketConfig.
func TestDefaultRSI2Config(t *testing.T) {
	cfg := DefaultRSI2Config()
	require.Equal(t, "1d", cfg.Timeframe)
	require.Equal(t, 200, cfg.TrendLen)
	require.Equal(t, 2, cfg.RSILen)
	require.Equal(t, 10.0, cfg.Oversold)
	require.Equal(t, 90.0, cfg.Overbought)
	require.Equal(t, 5, cfg.ExitLen)
	require.Zero(t, cfg.TrailingATR)
	require.Equal(t, DefaultBracketConfig(), cfg.BracketConfig)

	s := newRSI2(t, nil)
	require.Equal(t, "1d", s.Timeframe())
}

func TestRSI2_WarmupPeriod(t *testing.T) {
	// the SMA 200 and its margin, the seed of the ATR 14 (154, longer than
	// the one of the RSI 2) washed out inside it: the two overlapped, not
	// summed
	warmup := newRSI2(t, nil).WarmupPeriod()
	require.Equal(t, 200+rsi2Margin, warmup)
	require.Equal(t, 210, warmup)
	require.Greater(t, 200, seedWarmup(14), "the seed inside the window of the SMA")
	// held by the warm-up of a backtest of the controller, a year at most:
	// about 250 daily candles of gold (the market closed at the weekends)
	require.LessOrEqual(t, warmup, 250)

	require.Equal(t, seedWarmup(14), newRSI2(t, func(c *RSI2Config) { c.TrendLen = 50 }).WarmupPeriod(),
		"the ATR seed when it is the longer")
	require.Equal(t, seedWarmup(25), newRSI2(t, func(c *RSI2Config) { c.RSILen = 25 }).WarmupPeriod(),
		"the RSI seed when it is the longer")
	require.Equal(t, 50+rsi2Margin, newRSI2(t, func(c *RSI2Config) {
		c.TrendLen, c.RSILen, c.ATRLen = 50, 3, 2
	}).WarmupPeriod(), "the SMA when the seeds are shorter")
	require.Equal(t, 300+rsi2Margin, newRSI2(t, func(c *RSI2Config) { c.ExitLen = 300 }).WarmupPeriod(),
		"the longer SMA")
}

func TestRSI2_Entries(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*RSI2Config)
		candles   []ivCandle
	}{
		{name: "buy on the RSI under the oversold level above the SMA 200", candles: []ivCandle{r2(nil, 0), r2Long}},
		{name: "sell on the RSI over the overbought level under the SMA 200",
			candles: []ivCandle{r2(nil, 0), r2Short}},
		{name: "no buy at the oversold level", candles: []ivCandle{r2(kv{"r2_rsi": 10}, 0)}},
		{name: "no sell at the overbought level", candles: []ivCandle{r2(kv{"r2_trend": 110, "r2_rsi": 90}, 0)}},
		{name: "no buy under the SMA 200", candles: []ivCandle{r2(kv{"r2_trend": 110, "r2_rsi": 5}, 0)}},
		{name: "no sell above the SMA 200", candles: []ivCandle{r2(kv{"r2_rsi": 95}, 0)}},
		{name: "no entry at the SMA 200", candles: []ivCandle{
			r2(kv{"r2_trend": 100, "r2_rsi": 5}, 0), r2(kv{"r2_trend": 100, "r2_rsi": 95}, 0)}},
		{name: "no entry with the RSI in between", candles: []ivCandle{r2(kv{"r2_rsi": 30}, 0),
			r2(kv{"r2_trend": 110, "r2_rsi": 70}, 0)}},
		{name: "a level, not a cross: the RSI already under it", candles: []ivCandle{
			r2(kv{"r2_trend": 110, "r2_rsi": 5}, 0), r2Long}},
		{name: "no entry without the RSI", candles: []ivCandle{r2(kv{"r2_rsi": math.NaN()}, 0)}},
		{name: "no entry without the SMA 200", candles: []ivCandle{r2(kv{"r2_trend": math.NaN(), "r2_rsi": 5}, 0)}},
		{name: "no entry without the ATR", candles: []ivCandle{r2(kv{"r2_rsi": 5, "r2_atr": math.NaN()}, 0)}},
		{
			name:      "the oversold level is configurable",
			configure: func(c *RSI2Config) { c.Oversold = 20 },
			candles:   []ivCandle{r2(kv{"r2_rsi": 15}, 10)},
		},
		{
			name:      "the overbought level is configurable",
			configure: func(c *RSI2Config) { c.Overbought = 80 },
			candles:   []ivCandle{r2(kv{"r2_trend": 110, "r2_rsi": 85}, -10)},
		},
		{
			name:      "the direction keeps the other side out",
			configure: func(c *RSI2Config) { c.Direction = RKDirectionBuyOnly },
			candles:   []ivCandle{r2(kv{"r2_trend": 110, "r2_rsi": 95}, 0), r2Long},
		},
		{
			name:      "no entry while the ATR is too small for the costs",
			configure: func(c *RSI2Config) { c.MinATRPct = 2 },
			candles:   []ivCandle{r2(kv{"r2_rsi": 5}, 0)},
		},
		{name: "one position at a time", candles: []ivCandle{r2Long, r2Long, r2(kv{"r2_rsi": 5}, 10)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newR2Runner(t, tt.configure)
			r.feed(tt.candles...)
		})
	}
}

// Each entry is placed with the stop loss and the take profit of the bracket
// at the broker, a safety: 1 and 1.5 ATR from the entry close by default.
func TestRSI2_Legs(t *testing.T) {
	for _, tc := range []struct {
		name         string
		configure    func(*RSI2Config)
		entry        ivCandle
		stop, profit float64
	}{
		{name: "long", entry: r2Long, stop: 99, profit: 101.5},
		{name: "short", entry: r2Short, stop: 101, profit: 98.5},
		{name: "configurable", configure: func(c *RSI2Config) { c.StopATR, c.TakeProfitATR = 3, 2 }, entry: r2Long,
			stop: 97, profit: 102},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := newR2Runner(t, tc.configure)
			r.feed(tc.entry)
			legs := s.bracket.trades["BTCUSDT"].legs
			require.Len(t, legs, 2)
			require.Equal(t, model.OrderTypeLimitMaker, legs[0].Type)
			require.InDelta(t, tc.profit, legs[0].Price, 1e-9)
			require.Equal(t, model.OrderTypeStopLoss, legs[1].Type)
			require.InDelta(t, tc.stop, *legs[1].Stop, 1e-9)
		})
	}

	t.Run("no take profit", func(t *testing.T) {
		r, s := newR2Runner(t, func(c *RSI2Config) { c.TakeProfitATR = 0 })
		r.feed(r2Long)
		legs := s.bracket.trades["BTCUSDT"].legs
		require.Len(t, legs, 1, "the stop loss alone")
		require.Equal(t, model.OrderTypeStopLoss, legs[0].Type)
		require.InDelta(t, 99, *legs[0].Stop, 1e-9)
	})
}

func TestRSI2_Exits(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*RSI2Config)
		candles   []ivCandle
		quote     float64 // after the candles, 0 unchecked
	}{
		{
			name: "the long closes on a close above the SMA 5",
			candles: []ivCandle{r2Long, r2(kv{"r2_exit": 100.5}, 10),
				{close: 100.6, meta: kv{"r2_exit": 100.5}, position: 0}},
			quote: 10000 + 10*0.6,
		},
		{
			name: "the short closes on a close under the SMA 5",
			candles: []ivCandle{r2Short, r2(kv{"r2_trend": 110, "r2_exit": 99.5}, -10),
				{close: 99.4, meta: kv{"r2_trend": 110, "r2_exit": 99.5}, position: 0}},
			quote: 10000 + 10*0.6,
		},
		{
			name:    "the long holds at the SMA 5",
			candles: []ivCandle{r2Long, r2(kv{"r2_exit": 100}, 10)},
		},
		{
			name:    "the long holds under the SMA 5, whatever the RSI",
			candles: []ivCandle{r2Long, r2(kv{"r2_rsi": 99}, 10), r2(kv{"r2_rsi": 1}, 10)},
		},
		{
			name: "the short holds above the SMA 5",
			candles: []ivCandle{r2Short, r2(kv{"r2_trend": 110, "r2_exit": 99.9}, -10),
				r2(kv{"r2_trend": 110, "r2_exit": 100}, -10)},
		},
		{
			name:    "the long holds without the SMA 5",
			candles: []ivCandle{r2Long, {close: 100.6, meta: kv{"r2_exit": math.NaN()}, position: 10}},
		},
		{
			name: "the safety stop loss",
			candles: []ivCandle{r2Long, {open: 100, close: 99.2, low: 98.9, meta: kv{"r2_exit": 105},
				position: 0}},
			quote: 10000 - 10*1,
		},
		{
			name: "the safety take profit",
			candles: []ivCandle{r2Long, {open: 100, close: 100.2, high: 101.6, meta: kv{"r2_exit": 105},
				position: 0}},
			quote: 10000 + 10*1.5,
		},
		{
			name:      "max bars",
			configure: func(c *RSI2Config) { c.MaxBars = 2 },
			candles:   []ivCandle{r2Long, r2(nil, 10), r2(nil, 0)},
		},
		{
			// the long closed on its exit, the rule of a short holding on the
			// same close: the short opened, as the rule of each side says
			name: "the exit of a long and the entry of a short on the same close",
			candles: []ivCandle{r2Long,
				{close: 101, meta: kv{"r2_trend": 102, "r2_exit": 100.5, "r2_rsi": 95}, position: -10}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newR2Runner(t, tt.configure)
			r.feed(tt.candles...)
			if tt.quote != 0 {
				require.InDelta(t, tt.quote, r.quote(), 1e-9)
			}
		})
	}
}

// -trailing-atr, off by default: the stop loss at the broker trails the best
// price since the entry by that many ATR.
func TestRSI2_TrailingATR(t *testing.T) {
	stop := func(s *RSI2) float64 {
		legs := s.bracket.trades["BTCUSDT"].legs
		require.Len(t, legs, 1, "the stop loss alone")
		return rkLegLevel(legs[0])
	}
	rally := ivCandle{open: 100, close: 102.5, high: 103, low: 100, position: 10}

	r, s := newR2Runner(t, func(c *RSI2Config) { c.TakeProfitATR, c.TrailingATR = 0, 2 })
	r.feed(r2Long, rally)
	require.InDelta(t, 101, stop(s), 1e-9, "2 ATR behind the high of 103")

	r, s = newR2Runner(t, func(c *RSI2Config) { c.TakeProfitATR = 0 })
	r.feed(r2Long, rally)
	require.InDelta(t, 99, stop(s), 1e-9, "left where the entry placed it")
}

// The rule on the indicators the strategy computes, without overrides: a
// steady trend, then a sharp move against it sending the RSI 2 to its extreme,
// then the close back beyond the SMA 5.
func TestRSI2_OnItsIndicators(t *testing.T) {
	// the RSI 2 of the move: the average gain of 0.1 and the loss of 0 of the
	// trend, smoothed by 1/2 with the loss of 3: 0.05 and 1.5
	pullback := 100 - 100/(1+0.05/1.5)
	for _, tc := range []struct {
		name          string
		sign          float64 // of the trend
		side          float64 // the position held
		trendRSI, rsi float64 // the RSI 2 of the trend, and the one of the move against it
	}{
		{"a long on the pullback of an uptrend", 1, 10, 100, pullback},
		{"a short on the rally of a downtrend", -1, -10, 0, 100 - pullback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newRSI2(t, func(c *RSI2Config) {
				c.PositionSize, c.TakeProfitATR = map[string]float64{"BTCUSDT": 10}, 0
			})
			r := newIVRunner(t, s, 0)
			r.step = 24 * time.Hour

			// 360 candles moving 0.1 a day: the RSI 2 at 100 (0 in the
			// downtrend), the close beyond the SMA 200, no entry
			start := 150.0
			candles := make([]ivCandle, 0, 363)
			for i := range 360 {
				candles = append(candles, ivCandle{close: start + tc.sign*0.1*float64(i)})
			}
			last := candles[359].close // 185.9 or 114.1
			r.feed(candles...)
			require.Equal(t, tc.trendRSI, r.df.Metadata["r2_rsi"].Last(0))

			// 3 against the trend: the RSI 2 at 3.2 (96.8), the close still
			// beyond the SMA 200
			move := ivCandle{close: last - tc.sign*3, position: tc.side}
			r.feed(move)
			require.InDelta(t, tc.rsi, r.df.Metadata["r2_rsi"].Last(0), 1e-9)
			require.Positive(t, tc.sign*(move.close-r.df.Metadata["r2_trend"].Last(0)))

			// back towards the trend, still short of the SMA 5: held
			hold := last - tc.sign*1.9
			r.feed(ivCandle{close: hold, position: tc.side})
			require.Negative(t, tc.sign*(hold-r.df.Metadata["r2_exit"].Last(0)))

			// beyond the SMA 5: closed
			out := last + tc.sign*0.1
			r.feed(ivCandle{close: out, position: 0})
			require.InDelta(t, 10000+10*tc.sign*(out-move.close), r.quote(), 1e-9)
		})
	}
}

// The SMAs, the RSI (Wilder's) and the ATR the decisions read, and the chart.
func TestRSI2_Indicators(t *testing.T) {
	s := newRSI2(t, func(c *RSI2Config) { c.TrendLen, c.ExitLen = 4, 2 })
	closes := []float64{10, 12, 11, 13, 12.5, 14}
	df := &model.Dataframe{Pair: "BTCUSDT", Close: closes, Open: closes, High: closes, Low: closes,
		Metadata: map[string]model.Series[float64]{}}
	for i := range closes {
		df.Time = append(df.Time, time.Unix(int64(i)*86400, 0).UTC())
	}
	chart := s.Indicators(df)

	requireSeries(t, []float64{na, na, na, 11.5, 12.125, 12.625}, df.Metadata["r2_trend"], 1e-12)
	requireSeries(t, []float64{na, 11, 11.5, 12, 12.75, 13.25}, df.Metadata["r2_exit"], 1e-12)
	// the changes +2, -1, +2: the averages seeded with the SMA of the first 2,
	// gain 1 and loss 0.5, then smoothed by 1/2: gain 1.5 and loss 0.25
	rsi := df.Metadata["r2_rsi"]
	require.True(t, isNa(rsi[0]) && isNa(rsi[1]))
	require.InDelta(t, 100-100/(1+1/0.5), rsi[2], 1e-12)
	require.InDelta(t, 100-100/(1+1.5/0.25), rsi[3], 1e-12)
	requireSeries(t, pineRSI(closes, 2), rsi, 1e-12)
	requireSeries(t, pineATR(closes, closes, closes, 14), df.Metadata["r2_atr"], 1e-12)

	names := make([]string, 0, len(chart))
	for _, indicator := range chart {
		names = append(names, indicator.GroupName)
	}
	require.Equal(t, []string{"SMA 4", "SMA 2", "RSI 2"}, names)
	require.True(t, chart[0].Overlay && chart[1].Overlay && !chart[2].Overlay)
}

// No lookahead: the indicators of a candle are the same whatever the candles
// after it, so the decision of a candle reads only it and the ones before.
func TestRSI2_IndicatorsOfTheCandlesBefore(t *testing.T) {
	s := newRSI2(t, func(c *RSI2Config) { c.TrendLen = 20 })
	var closes []float64
	for i := range 120 {
		closes = append(closes, 100+10*math.Sin(float64(i)/5)+float64(i%3))
	}
	frame := func(n int) *model.Dataframe {
		df := &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}}
		for i := range n {
			df.Time = append(df.Time, time.Unix(int64(i)*86400, 0).UTC())
			df.Open = append(df.Open, closes[i])
			df.Close = append(df.Close, closes[i])
			df.High = append(df.High, closes[i]+1)
			df.Low = append(df.Low, closes[i]-1)
		}
		return df
	}
	whole := frame(len(closes))
	s.Indicators(whole)
	for n := 1; n <= len(closes); n++ {
		df := frame(n)
		s.Indicators(df)
		for _, key := range []string{"r2_trend", "r2_exit", "r2_rsi", "r2_atr"} {
			got, want := df.Metadata[key].Last(0), whole.Metadata[key][n-1]
			if isNa(want) {
				require.True(t, isNa(got), "%s at %d", key, n-1)
				continue
			}
			require.InDelta(t, want, got, 1e-9, "%s at %d", key, n-1)
		}
	}
}

// Restarted with the position open, the strategy follows it with its legs at
// the broker and still closes it on its exit, the close above the SMA 5: the
// rule needs no state of its own.
func TestRSI2_AfterARestart(t *testing.T) {
	r, s := newR2Runner(t, nil)
	bot := newTradingBot(t, r.wallet, r.step)
	r.broker, r.settle = bot.broker(), bot.settle
	r.feed(r2Long)
	legs := s.bracket.trades["BTCUSDT"].legs
	require.Len(t, legs, 2)

	bot.restart()
	s = newRSI2(t, func(c *RSI2Config) { c.PositionSize = map[string]float64{"BTCUSDT": 10} })
	r.s, r.broker = s, bot.broker()
	r.feed(r2(nil, 10))
	requireOpen(t, r.wallet, s.bracket.trades["BTCUSDT"].legs, 101.5, 99)

	r.feed(ivCandle{close: 100.6, meta: kv{"r2_exit": 100.5}, position: 0})
	for _, leg := range legs {
		got, err := r.wallet.Order("BTCUSDT", leg.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, got.Status, leg.Type)
	}
	require.InDelta(t, 10000+10*0.6, r.quote(), 1e-9)
}

// The strategy on the 1h candles of the test data (the 4314 candles hold the
// warmup of 210 and months of trades): each trade a market entry, then a
// single exit.
func TestRSI2_Backtest(t *testing.T) {
	for name, configure := range map[string]func(*RSI2Config){
		"the bracket defaults": nil,
		"no take profit":       func(c *RSI2Config) { c.TakeProfitATR = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			var s strategy.Strategy = newRSI2(t, func(c *RSI2Config) {
				c.Timeframe = "1h"
				if configure != nil {
					configure(c)
				}
			})
			checkBacktest(t, s)
		})
	}
}
