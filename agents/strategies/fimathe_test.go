package strategies

import (
	"context"
	"encoding/csv"
	"math"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/ui"
)

func newFimathe(t *testing.T, configure func(*FimatheConfig)) *Fimathe {
	t.Helper()
	cfg := DefaultFimatheConfig()
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewFimathe(cfg)
	require.NoError(t, err)
	return s
}

func TestNewFimathe_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*FimatheConfig)
		err       string
	}{
		{"timeframe", func(c *FimatheConfig) { c.Timeframe = "x" }, "invalid timeframe"},
		{"analysis timeframe", func(c *FimatheConfig) { c.AnalysisTimeframe = "" }, "invalid analysis timeframe"},
		{"analysis not longer", func(c *FimatheConfig) { c.AnalysisTimeframe = "15m" }, "longer than the timeframe"},
		{"analysis not a multiple", func(c *FimatheConfig) { c.AnalysisTimeframe = "50m" }, "a multiple"},
		{"analysis not dividing a day", func(c *FimatheConfig) { c.AnalysisTimeframe = "7h" }, "divide a day"},
		{"mapping", func(c *FimatheConfig) { c.Mapping = "month" }, "invalid mapping"},
		{"timezone", func(c *FimatheConfig) { c.Timezone = "Moon/Base" }, "invalid timezone"},
		{"pivot", func(c *FimatheConfig) { c.PivotLen = 0 }, "pivots"},
		{"subcycle pivot", func(c *FimatheConfig) { c.SubcyclePivot = 0 }, "pivots"},
		{"analysis bars", func(c *FimatheConfig) { c.AnalysisBars = 10 }, "two tops and two bottoms"},
		{"mark", func(c *FimatheConfig) { c.Mark = "shadow" }, "invalid mark"},
		{"widths crossed", func(c *FimatheConfig) { c.MinChannelATR = 9 }, "channel widths"},
		{"negative stop buffer", func(c *FimatheConfig) { c.StopBufferATR = -1 }, "stop buffer"},
		{"negative target", func(c *FimatheConfig) { c.TargetLevels = -1 }, "target levels"},
		{"trail", func(c *FimatheConfig) { c.Trail = "follow" }, "invalid trail"},
		{"trail step", func(c *FimatheConfig) { c.TrailStep = 0 }, "trail step"},
		{"surfing with a take profit", func(c *FimatheConfig) { c.TargetLevels = 2 }, "Ou será take ou será stop"},
		{"negative risk", func(c *FimatheConfig) { c.MaxRiskPct = -1 }, "max risk"},
		{"a fixed stop", func(c *FimatheConfig) { c.StopValue = 1 }, "technical"},
		{"a fixed take profit", func(c *FimatheConfig) { c.TakeProfitValue = 1 }, "technical"},
		{"the bracket", func(c *FimatheConfig) { c.MaxBars = -1 }, "max bars"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultFimatheConfig()
			tt.configure(&cfg)
			_, err := NewFimathe(cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}

	// a take profit with the stop loss locked at the entry, or never moved
	newFimathe(t, func(c *FimatheConfig) { c.TargetLevels, c.Trail = 2, FimatheLock })
	newFimathe(t, func(c *FimatheConfig) { c.TargetLevels, c.Trail = 2, FimatheOff })
	// the ATR multiples of the bracket aren't used
	newFimathe(t, func(c *FimatheConfig) { c.StopATR, c.TakeProfitATR = 0, 0 })
}

func TestFimathe_WarmupPeriod(t *testing.T) {
	// the analysis bars and the seed of their ATR before a period, in candles
	// of 15m, then two weeks
	require.Equal(t, (120+seedWarmup(14)+2)*4+2*7*96+1, newFimathe(t, nil).WarmupPeriod())
	require.Equal(t, (120+seedWarmup(14)+2)*4+2*96+1,
		newFimathe(t, func(c *FimatheConfig) { c.Mapping = FimatheDay }).WarmupPeriod())
}

func TestFimathe_Periods(t *testing.T) {
	s := newFimathe(t, nil)
	monday := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	sunday := time.Date(2024, 1, 14, 0, 0, 0, 0, time.UTC)
	require.Equal(t, sunday, s.periodStart(monday), "the week starts on Sunday")
	require.Equal(t, sunday, s.periodStart(sunday))
	require.Equal(t, sunday.AddDate(0, 0, 7), s.periodStart(sunday.AddDate(0, 0, 7).Add(time.Minute)))
	require.Equal(t, sunday.AddDate(0, 0, 7), s.periodEnd(sunday))

	day := newFimathe(t, func(c *FimatheConfig) { c.Mapping = FimatheDay })
	require.Equal(t, time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC), day.periodStart(monday))

	brt := newFimathe(t, func(c *FimatheConfig) { c.Timezone = "America/Sao_Paulo" })
	start := brt.periodStart(time.Date(2024, 1, 14, 2, 0, 0, 0, time.UTC)) // Saturday 23:00 in Brasília
	require.Equal(t, time.Date(2024, 1, 7, 3, 0, 0, 0, time.UTC), start.UTC())
}

// fmBar is a bar of the analysis timeframe: its open, high, low and close.
type fmBar [4]float64

// fmCandles returns the candles of 15m of bars of 1h: each one flat at its
// open, then reaching its high, then its low, then closing.
func fmCandles(bars ...fmBar) []ivCandle {
	var candles []ivCandle
	for _, b := range bars {
		o, h, l, c := b[0], b[1], b[2], b[3]
		candles = append(candles,
			ivCandle{open: o, close: o, high: o, low: o},
			ivCandle{open: o, close: o, high: h, low: o},
			ivCandle{open: o, close: o, high: o, low: l},
			ivCandle{open: o, close: c, high: math.Max(o, c), low: math.Min(o, c)},
		)
	}
	return candles
}

// fmUptrend is a day of bars of 1h rising by 2 every 4 bars: the tops (of 1
// bar on each side) at 104, 106 ... 114, the bottoms at 100.5 ... 110.5, the
// last one unconfirmed at the end of the day, the ATR of 1 bar at 1. The
// reference channel of the next day goes from 108.5 to 114, its 50% at
// 111.25, its neutral zone from 103 to 108.5. The day closes at last, 111 by
// default.
func fmUptrend(last float64) []fmBar {
	var bars []fmBar
	for j := 0; j < 6; j++ {
		b := 100 + 2*float64(j)
		bars = append(bars,
			fmBar{b + 1, b + 3, b + 1, b + 3},
			fmBar{b + 3, b + 4, b + 2.5, b + 3.5},
			fmBar{b + 3.5, b + 3.6, b + 1, b + 1.2},
			fmBar{b + 1.2, b + 1.5, b + 0.5, b + 1},
		)
	}
	bars[23] = fmBar{111.2, math.Max(111.5, last), 110.5, last}
	return bars
}

// fmMirror returns bars mirrored around 110, a trend up as a trend down.
func fmMirror(bars []fmBar) []fmBar {
	mirrored := make([]fmBar, len(bars))
	for k, b := range bars {
		mirrored[k] = fmBar{220 - b[0], 220 - b[2], 220 - b[1], 220 - b[3]}
	}
	return mirrored
}

// newFmRunner feeds a Fimathe mapped each day, of bars of 1h and candles of
// 15m, the tops and the bottoms of 1 bar (or candle) on each side found in a
// day, the ATR of 1 bar, without buffer nor bounds of width, trading positions
// of 10.
func newFmRunner(t *testing.T, configure func(*FimatheConfig)) (*ivRunner, *Fimathe) {
	t.Helper()
	s := newFimathe(t, func(c *FimatheConfig) {
		c.Mapping = FimatheDay
		c.PivotLen, c.AnalysisBars, c.ATRLen, c.SubcyclePivot = 1, 24, 1, 1
		c.MinChannelATR, c.MaxChannelATR = 0, 100
		c.StopBufferATR = 0
		c.PositionSize = map[string]float64{"BTCUSDT": 10}
		if configure != nil {
			configure(c)
		}
	})
	r := newIVRunner(t, s, 0)
	r.step = 15 * time.Minute
	return r, s
}

// fmStop returns the stop loss of the trade of the pair, and its take profit,
// 0 for none.
func fmStop(t *testing.T, s *Fimathe) (stop, profit float64) {
	t.Helper()
	trade := s.bracket.trades["BTCUSDT"]
	require.NotNil(t, trade)
	for _, leg := range trade.legs {
		if leg.Type == model.OrderTypeStopLoss {
			stop = rkLegLevel(leg)
		} else {
			profit = leg.Price
		}
	}
	return stop, profit
}

func TestFimathe_Mapping(t *testing.T) {
	t.Run("an uptrend: the channel of the last top and bottom, its neutral zone below", func(t *testing.T) {
		r, s := newFmRunner(t, nil)
		r.feed(fmCandles(fmUptrend(111)...)...)
		r.feed(ivCandle{open: 111, close: 111, high: 111.1, low: 110.9})
		m := s.maps["BTCUSDT"]
		require.True(t, m.ok, m.reason)
		require.Equal(t, model.SideTypeBuy, m.side)
		require.Equal(t, FimatheWick, m.mark)
		require.InDelta(t, 114, m.top, 1e-9)
		require.InDelta(t, 108.5, m.bottom, 1e-9)
		require.InDelta(t, 111.25, m.mid(), 1e-9)
		require.InDelta(t, 103, m.far(), 1e-9)
		require.InDelta(t, 1, m.atr, 1e-9)
		day0 := r.day
		require.Equal(t, day0.Add(21*time.Hour), m.topAt)
		require.Equal(t, day0.Add(19*time.Hour), m.bottomAt)
	})

	t.Run("a downtrend: the neutral zone above", func(t *testing.T) {
		r, s := newFmRunner(t, nil)
		r.feed(fmCandles(fmMirror(fmUptrend(111))...)...)
		r.feed(ivCandle{open: 109, close: 109, high: 109.1, low: 108.9})
		m := s.maps["BTCUSDT"]
		require.True(t, m.ok, m.reason)
		require.Equal(t, model.SideTypeSell, m.side)
		require.InDelta(t, 111.5, m.top, 1e-9)
		require.InDelta(t, 106, m.bottom, 1e-9)
		require.InDelta(t, 117, m.far(), 1e-9)
	})

	for _, tc := range []struct {
		name      string
		configure func(*FimatheConfig)
		bars      []fmBar
		reason    string
	}{
		{"no tops nor bottoms on flat bars", nil, func() []fmBar {
			bars := make([]fmBar, 24)
			for k := range bars {
				bars[k] = fmBar{100, 101, 99, 100}
			}
			return bars
		}(), "no trend"},
		{"a channel narrower than the min", func(c *FimatheConfig) { c.MinChannelATR = 6 }, fmUptrend(111),
			"channel too narrow: 5.50 ATR"},
		{"the price in the reversal zone", nil, fmUptrend(102), "in the reversal zone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := newFmRunner(t, tc.configure)
			r.feed(fmCandles(tc.bars...)...)
			r.feed(ivCandle{open: 100, close: 100})
			m := s.maps["BTCUSDT"]
			require.False(t, m.ok)
			require.Contains(t, m.reason, tc.reason)
		})
	}

	t.Run("a channel of the wicks too wide marked on the bodies", func(t *testing.T) {
		r, s := newFmRunner(t, func(c *FimatheConfig) { c.MaxChannelATR = 5 })
		r.feed(fmCandles(fmUptrend(111)...)...)
		r.feed(ivCandle{open: 111, close: 111})
		m := s.maps["BTCUSDT"]
		require.True(t, m.ok, m.reason)
		require.Equal(t, FimatheBody, m.mark)
		require.InDelta(t, 113.5, m.top, 1e-9, "the body of the top bar")
		require.InDelta(t, 109, m.bottom, 1e-9, "the body of the bottom bar")
	})
}

func TestFimathe_Entries(t *testing.T) {
	tests := []struct {
		name    string
		last    float64 // the close of the day before
		mirror  bool
		candles []ivCandle
		line    float64
	}{
		{
			name:    "under the 50%: a buy on the close back above it",
			last:    111,
			candles: []ivCandle{{open: 111, close: 111.5, high: 111.6, low: 110.9, position: 10}},
			line:    111.25,
		},
		{
			name: "from the neutral zone: the buy on the close above the 50%, not before",
			last: 111,
			candles: []ivCandle{
				{open: 111, close: 107, high: 111, low: 106.8},
				{open: 107, close: 110, high: 110.2, low: 106.9},
				{open: 110, close: 111.3, high: 111.4, low: 109.9, position: 10},
			},
			line: 111.25,
		},
		{
			name: "held above the 50%: the buy on the close above the top of the channel",
			last: 112,
			candles: []ivCandle{
				{open: 112, close: 113, high: 113.5, low: 111.9},
				{open: 113, close: 114.2, high: 114.3, low: 112.9, position: 10},
			},
			line: 114,
		},
		{
			name: "back under the 50%: the buy on the close above it again",
			last: 112,
			candles: []ivCandle{
				{open: 112, close: 111, high: 112.1, low: 110.8},
				{open: 111, close: 111.5, high: 111.6, low: 110.9, position: 10},
			},
			line: 111.25,
		},
		{
			name: "beyond the channel: the entry once the price came back into it",
			last: 115,
			candles: []ivCandle{
				{open: 115, close: 116, high: 116.2, low: 114.9},
				{open: 116, close: 113, high: 116, low: 112.9},
				{open: 113, close: 114.5, high: 114.6, low: 112.9, position: 10},
			},
			line: 114,
		},
		{
			name:    "a downtrend: a sell on the close back below the 50%",
			last:    111,
			mirror:  true,
			candles: []ivCandle{{open: 109, close: 108.5, high: 109.1, low: 108.4, position: -10}},
			line:    108.75,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, s := newFmRunner(t, nil)
			bars := fmUptrend(tt.last)
			if tt.mirror {
				bars = fmMirror(bars)
			}
			r.feed(fmCandles(bars...)...)
			r.feed(tt.candles...)
			trade := s.trades["BTCUSDT"]
			require.NotNil(t, trade)
			require.InDelta(t, tt.line, trade.line, 1e-9)
			stop, profit := fmStop(t, s)
			if tt.mirror {
				require.InDelta(t, 117, stop, 1e-9, "beyond the neutral zone above")
			} else {
				require.InDelta(t, 103, stop, 1e-9, "beyond the neutral zone below")
			}
			require.Zero(t, profit, "surfing: no take profit")
		})
	}

	t.Run("no entry before the close beyond the level", func(t *testing.T) {
		r, _ := newFmRunner(t, nil)
		r.feed(fmCandles(fmUptrend(111)...)...)
		// the wick above the 50%, the close under it
		r.feed(ivCandle{open: 111, close: 111.2, high: 111.9, low: 110.9})
	})

	t.Run("the close in the reversal zone ends the mapping", func(t *testing.T) {
		r, s := newFmRunner(t, nil)
		r.feed(fmCandles(fmUptrend(111)...)...)
		r.feed(ivCandle{open: 111, close: 102.9, high: 111, low: 102.5},
			ivCandle{open: 102.9, close: 111.5, high: 111.6, low: 102.8})
		analysis, ok := s.ChartAnalysis("BTCUSDT")
		require.True(t, ok)
		require.Equal(t, "INVALIDATED", analysis.Action)
		require.Nil(t, analysis.Intent)
	})

	t.Run("no trend, no entry", func(t *testing.T) {
		r, _ := newFmRunner(t, nil)
		bars := make([]fmBar, 24)
		for k := range bars {
			bars[k] = fmBar{111, 111.5, 110.5, 111}
		}
		r.feed(fmCandles(bars...)...)
		r.feed(ivCandle{open: 111, close: 111.5}, ivCandle{open: 111.5, close: 114.5})
	})

	t.Run("the direction keeps the other side out", func(t *testing.T) {
		r, _ := newFmRunner(t, func(c *FimatheConfig) { c.Direction = RKDirectionSellOnly })
		r.feed(fmCandles(fmUptrend(111)...)...)
		r.feed(ivCandle{open: 111, close: 111.5})
	})

	t.Run("no entry losing more than the max risk at its stop loss", func(t *testing.T) {
		// 50 bought at 111.5, the stop loss at 103: 425 lost, above 3% of 10000
		r, _ := newFmRunner(t, func(c *FimatheConfig) { c.PositionSize = map[string]float64{"BTCUSDT": 50} })
		r.feed(fmCandles(fmUptrend(111)...)...)
		r.feed(ivCandle{open: 111, close: 111.5})
	})
}

func TestFimathe_StopBySubcycle(t *testing.T) {
	entry := ivCandle{open: 111, close: 111.5, high: 111.6, low: 110.9, position: 10}

	t.Run("surfing: 0x0 at the first 50%, one step more at each step", func(t *testing.T) {
		r, s := newFmRunner(t, nil)
		r.feed(fmCandles(fmUptrend(111)...)...)
		r.feed(entry)
		trade := s.trades["BTCUSDT"]
		// the subcycle: the last top (111.5) and bottom (110.5) of the candles of
		// 15m before the entry, projected from its line, by 0.5
		require.InDelta(t, 1, trade.sub, 1e-9)
		require.InDelta(t, 110.5, trade.subLow, 1e-9)
		require.InDelta(t, 111.5, trade.subHigh, 1e-9)

		r.feed(ivCandle{open: 111.5, close: 111.7, high: 111.8, low: 111.4, position: 10})
		stop, _ := fmStop(t, s)
		require.InDelta(t, 103, stop, 1e-9, "short of the first step, 111.75")
		r.feed(ivCandle{open: 111.7, close: 111.8, high: 111.9, low: 111.6, position: 10})
		stop, _ = fmStop(t, s)
		require.InDelta(t, 111.25, stop, 1e-9, "0x0")
		r.feed(ivCandle{open: 111.8, close: 112.3, high: 112.4, low: 111.7, position: 10})
		stop, _ = fmStop(t, s)
		require.InDelta(t, 111.75, stop, 1e-9, "a step more")
		require.Len(t, trade.moves, 2)

		// stopped out, the mapping entered once
		r.feed(ivCandle{open: 112.3, close: 111.6, high: 112.3, low: 111.5})
		require.InDelta(t, 10000+10*(111.75-111.5), r.quote(), 1e-9)
		r.feed(ivCandle{open: 111.6, close: 111, high: 111.6, low: 110.9},
			ivCandle{open: 111, close: 111.5, high: 111.6, low: 110.9})
		require.Empty(t, s.trades)
	})

	t.Run("a step of the whole subcycle", func(t *testing.T) {
		r, s := newFmRunner(t, func(c *FimatheConfig) { c.TrailStep = 1 })
		r.feed(fmCandles(fmUptrend(111)...)...)
		r.feed(entry, ivCandle{open: 111.5, close: 112.3, high: 112.4, low: 111.4, position: 10})
		stop, _ := fmStop(t, s)
		require.InDelta(t, 111.25, stop, 1e-9, "0x0 past 112.25")
	})

	t.Run("locked at 0x0 with a take profit two levels away", func(t *testing.T) {
		r, s := newFmRunner(t, func(c *FimatheConfig) { c.Trail, c.TargetLevels = FimatheLock, 2 })
		r.feed(fmCandles(fmUptrend(111)...)...)
		r.feed(entry)
		stop, profit := fmStop(t, s)
		require.InDelta(t, 103, stop, 1e-9)
		require.InDelta(t, 111.25+2*5.5, profit, 1e-9, "two heights of the channel from the entry line")
		r.feed(ivCandle{open: 111.5, close: 113, high: 113.1, low: 111.4, position: 10})
		stop, _ = fmStop(t, s)
		require.InDelta(t, 111.25, stop, 1e-9, "locked at 0x0, whatever the steps passed")
	})

	t.Run("never moved", func(t *testing.T) {
		r, s := newFmRunner(t, func(c *FimatheConfig) { c.Trail = FimatheOff })
		r.feed(fmCandles(fmUptrend(111)...)...)
		r.feed(entry, ivCandle{open: 111.5, close: 113, high: 113.1, low: 111.4, position: 10})
		stop, _ := fmStop(t, s)
		require.InDelta(t, 103, stop, 1e-9)
	})
}

func TestFimathe_AfterARestart(t *testing.T) {
	start := func(t *testing.T) (*ivRunner, *tradingBot) {
		r, _ := newFmRunner(t, nil)
		bot := newTradingBot(t, r.wallet, r.step)
		r.broker, r.settle = bot.broker(), bot.settle
		r.feed(fmCandles(fmUptrend(111)...)...)
		return r, bot
	}
	restart := func(r *ivRunner, bot *tradingBot) *Fimathe {
		bot.restart()
		_, s := newFmRunner(t, nil)
		r.s, r.broker = s, bot.broker()
		return s
	}

	t.Run("the position found surfs the subcycle of its entry", func(t *testing.T) {
		r, bot := start(t)
		r.feed(ivCandle{open: 111, close: 111.5, high: 111.6, low: 110.9, position: 10})
		s := restart(r, bot)
		r.feed(ivCandle{open: 111.5, close: 111.8, high: 111.9, low: 111.4, position: 10})
		trade := s.trades["BTCUSDT"]
		require.NotNil(t, trade)
		require.InDelta(t, 111.25, trade.line, 1e-9, "the entry line of its mapping, read again")
		require.InDelta(t, 1, trade.sub, 1e-9)
		stop, _ := fmStop(t, s)
		require.InDelta(t, 111.25, stop, 1e-9, "0x0")
		r.feed(ivCandle{open: 111.8, close: 112.3, high: 112.4, low: 111.7, position: 10})
		stop, _ = fmStop(t, s)
		require.InDelta(t, 111.75, stop, 1e-9)
	})

	t.Run("no second entry of the mapping", func(t *testing.T) {
		r, bot := start(t)
		r.feed(ivCandle{open: 111, close: 111, high: 111.1, low: 110.9})
		// a position opened and closed in the period, eg: before a restart
		_, err := bot.ctl.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		require.NoError(t, err)
		_, err = bot.ctl.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 1)
		require.NoError(t, err)
		restart(r, bot)
		r.feed(ivCandle{open: 111, close: 111.5, high: 111.6, low: 110.9})
	})

	t.Run("a restart before the trigger still enters", func(t *testing.T) {
		r, bot := start(t)
		r.feed(ivCandle{open: 111, close: 111, high: 111.1, low: 110.9})
		restart(r, bot)
		r.feed(ivCandle{open: 111, close: 111.5, high: 111.6, low: 110.9, position: 10})
	})
}

// The levels drawn on the chart are the ones the strategy trades: the channel,
// the neutral zone, the 50%, the expansions and the reversal zone of its
// mapping, the entry planned while armed, then the entry made, the subcycle
// and the moves of the stop loss.
func TestFimathe_Drawings(t *testing.T) {
	r, s := newFmRunner(t, nil)
	r.feed(fmCandles(fmUptrend(111)...)...)

	analysis, ok := s.ChartAnalysis("BTCUSDT")
	require.True(t, ok)
	require.Empty(t, analysis.Drawings, "no mapping on the first day")

	r.feed(ivCandle{open: 111, close: 111, high: 111.1, low: 110.9})
	analysis, _ = s.ChartAnalysis("BTCUSDT")
	require.Equal(t, "Fimathe", analysis.Source)
	require.Equal(t, "15m", analysis.Timeframe)
	require.Equal(t, "WAIT", analysis.Action)
	require.Equal(t, "fimathe-BTCUSDT-2024-01-16", analysis.DecisionID)
	drawn := map[string]ui.Drawing{}
	for _, d := range analysis.Drawings {
		drawn[d.Label] = d
	}
	channel := drawn["Canal de referência (16/01)"]
	require.Equal(t, ui.DrawingZone, channel.Kind)
	require.Equal(t, "1h", channel.Timeframe)
	require.Equal(t, r.day.Add(19*time.Hour).Unix(), channel.From, "from the bar of the last bottom")
	require.InDelta(t, 108.5, *channel.Low, 1e-9)
	require.InDelta(t, 114, *channel.High, 1e-9)
	neutral := drawn["Zona neutra"]
	require.InDelta(t, 103, *neutral.Low, 1e-9)
	require.InDelta(t, 108.5, *neutral.High, 1e-9)
	for label, price := range map[string]float64{"50% do canal": 111.25, "Expansão 100%": 119.5,
		"Expansão 200%": 125, "Zona de reversão": 103} {
		require.InDelta(t, price, *drawn[label].Price, 1e-9, label)
	}
	require.NotNil(t, analysis.Intent)
	require.Equal(t, "BUY", analysis.Intent.Side)
	require.Equal(t, ui.IntentMarket, analysis.Intent.OrderType)
	require.Equal(t, "waiting", analysis.Intent.Outcome)
	require.InDelta(t, 111.25, *analysis.Intent.Entry, 1e-9, "armed at the 50%")
	require.InDelta(t, 103, *analysis.Intent.StopLoss, 1e-9)
	require.Nil(t, analysis.Intent.TakeProfit)

	// the entry, then the stop loss moved twice
	r.feed(ivCandle{open: 111, close: 111.5, high: 111.6, low: 110.9, position: 10})
	entryAt := r.df.Time[len(r.df.Time)-1]
	r.feed(ivCandle{open: 111.5, close: 111.8, high: 111.9, low: 111.4, position: 10},
		ivCandle{open: 111.8, close: 112.3, high: 112.4, low: 111.7, position: 10})
	analysis, _ = s.ChartAnalysis("BTCUSDT")
	require.Equal(t, "HOLD", analysis.Action)
	require.Nil(t, analysis.Intent, "the orders are drawn as orders")
	drawn = map[string]ui.Drawing{}
	for _, d := range analysis.Drawings {
		drawn[d.Label] = d
	}
	entry := drawn["Entrada: rompimento 50%"]
	require.Equal(t, ui.DrawingMarker, entry.Kind)
	require.Equal(t, entryAt.Unix(), entry.From)
	require.InDelta(t, 111.25, *entry.Price, 1e-9)
	sub := drawn["Subciclo"]
	require.Equal(t, "15m", sub.Timeframe)
	require.InDelta(t, 110.5, *sub.Low, 1e-9)
	require.InDelta(t, 111.5, *sub.High, 1e-9)
	require.InDelta(t, 111.75, *drawn["Subciclo 50%"].Price, 1e-9)
	require.InDelta(t, 112.25, *drawn["Subciclo 100%"].Price, 1e-9)
	zero, plus := drawn["Stop 0x0"], drawn["Stop +1"]
	require.InDelta(t, 111.25, *zero.Price, 1e-9)
	require.Equal(t, entryAt.Add(15*time.Minute).Unix(), zero.From)
	require.InDelta(t, 111.75, *plus.Price, 1e-9)
	require.Equal(t, entryAt.Add(30*time.Minute).Unix(), plus.From)
}

// The scene of a candle is the same whatever the candles after it: the
// mapping reads the bars closed before its period, the entries and the end
// the closes up to the candle.
func TestFimathe_NoLookAhead(t *testing.T) {
	r, s := newFmRunner(t, nil)
	r.feed(fmCandles(fmUptrend(111)...)...)
	r.feed(ivCandle{open: 111, close: 107, high: 111, low: 106.8},
		ivCandle{open: 107, close: 110, high: 110.2, low: 106.9},
		ivCandle{open: 110, close: 111.3, high: 111.4, low: 109.9, position: 10},
		ivCandle{open: 111.3, close: 112.5, high: 112.6, low: 111.2, position: 10})
	full := *r.df
	period := s.periodStart(full.Time[len(full.Time)-1])
	for end := 97; end <= len(full.Time); end++ {
		prefix := full
		prefix.Time, prefix.Open, prefix.High = full.Time[:end], full.Open[:end], full.High[:end]
		prefix.Low, prefix.Close = full.Low[:end], full.Close[:end]
		cut, _ := s.periodScene(&prefix, period, false)
		whole := s.scene(&full, s.mapAt(&full, 96, period), 96, end-1)
		require.Equal(t, whole, cut, "up to the candle %d", end-1)
	}
}

// TestFimathe_Backtest runs Fimathe on the 1h candles of the test data, the
// analysis on 4h, the channel of each week, positions of 20% of the balance
// (the whole balance would risk more than the max 3% at the stop loss beyond
// the neutral zone): each trade a market entry, then a single exit, its stop
// loss.
func TestFimathe_Backtest(t *testing.T) {
	if testing.Short() {
		t.Skip("long: left out by -short")
	}
	checkBacktest(t, newFimathe(t, func(c *FimatheConfig) {
		c.Timeframe, c.AnalysisTimeframe, c.PositionFraction = "1h", "4h", 0.2
	}))
}

// TestFimathe_OnTheTestData follows Fimathe candle by candle on the 1h
// candles of the test data, as the bot does (the window of its warmup handed
// to it, the paper wallet filling its orders), the analysis on 4h: the scene
// of each candle is the same read on all the candles, the ones after it
// included; the chart draws the channel of the mapping traded, and each entry
// on its candle at its line.
func TestFimathe_OnTheTestData(t *testing.T) {
	if testing.Short() {
		t.Skip("long: left out by -short")
	}
	file, err := os.Open("../../testdata/btc-1h.csv")
	require.NoError(t, err)
	defer func() { _ = file.Close() }()
	rows, err := csv.NewReader(file).ReadAll()
	require.NoError(t, err)

	s := newFimathe(t, func(c *FimatheConfig) {
		c.Timeframe, c.AnalysisTimeframe, c.PositionFraction = "1h", "4h", 0.2
	})
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000))
	full := &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}}
	type seen struct {
		index            int
		mapping          fimatheMap
		arm              int
		trigger, invalid time.Time
		line             float64
	}
	var scenes []seen
	entries := 0
	at := func(df *model.Dataframe, i int) time.Time {
		if i < 0 {
			return time.Time{}
		}
		return df.Time[i]
	}
	for _, row := range rows {
		values := make([]float64, 5)
		for k := range values {
			values[k], err = strconv.ParseFloat(row[k], 64)
			require.NoError(t, err)
		}
		candle := model.Candle{Pair: "BTCUSDT", Time: time.Unix(int64(values[0]), 0).UTC(), Open: values[1],
			Close: values[2], Low: values[3], High: values[4], Volume: 1, Complete: true}
		wallet.OnCandle(candle)
		full.Time, full.Open, full.Close = append(full.Time, candle.Time), append(full.Open, candle.Open),
			append(full.Close, candle.Close)
		full.Low, full.High, full.Volume = append(full.Low, candle.Low), append(full.High, candle.High),
			append(full.Volume, 1)
		if len(full.Close) < s.WarmupPeriod() {
			continue
		}

		// the window of the warmup, as the controller hands it
		sample := full.Sample(s.WarmupPeriod())
		sample.Metadata = map[string]model.Series[float64]{}
		s.Indicators(&sample)
		before := s.trades["BTCUSDT"]
		s.OnCandle(&sample, wallet)

		last := len(sample.Time) - 1
		sc, _ := s.periodScene(&sample, s.periodStart(sample.Time[last]), true)
		scenes = append(scenes, seen{index: len(full.Time) - 1, mapping: sc.fimatheMap, arm: sc.arm,
			trigger: at(&sample, sc.trigger), invalid: at(&sample, sc.invalid), line: sc.line})

		analysis, ok := s.ChartAnalysis("BTCUSDT")
		require.True(t, ok)
		drawn := map[string]ui.Drawing{}
		for _, d := range analysis.Drawings {
			drawn[d.ID] = d
		}
		id := sc.period.Format(time.DateOnly)
		if trade := s.trades["BTCUSDT"]; trade != nil && trade != before && trade.entryAt.Equal(sample.Time[last]) {
			entries++
			entry, ok := drawn[id+"-entry"]
			require.True(t, ok, "the entry of %s drawn", sample.Time[last])
			require.Equal(t, trade.entryAt.Unix(), entry.From)
			require.InDelta(t, trade.line, *entry.Price, 1e-9)
			require.Equal(t, string(trade.side), analysis.Action)
		}
		if sc.ok && s.trades["BTCUSDT"] == nil {
			channel, ok := drawn[id+"-channel"]
			require.True(t, ok, "the channel of %s drawn", id)
			require.InDelta(t, sc.bottom, *channel.Low, 1e-9)
			require.InDelta(t, sc.top, *channel.High, 1e-9)
			if analysis.Intent != nil {
				require.InDelta(t, sc.level(), *analysis.Intent.Entry, 1e-9, "the entry planned at the level armed")
			}
		}
	}
	require.Positive(t, entries, "the strategy trades")
	mapped, triggered, ended := 0, 0, 0
	for _, c := range scenes {
		if c.mapping.ok {
			mapped++
		}
		if !c.trigger.IsZero() {
			triggered++
		}
		if !c.invalid.IsZero() {
			ended++
		}
	}
	t.Logf("%d candles followed: %d mapped, %d after an entry, %d after an end in the reversal zone; %d entries",
		len(scenes), mapped, triggered, ended, entries)
	require.Positive(t, ended, "ends in the reversal zone met")

	// the same scenes read on all the candles
	for _, c := range scenes {
		period := s.periodStart(full.Time[c.index])
		start := 0
		for full.Time[start].Before(period) {
			start++
		}
		sc := s.scene(full, s.mapAt(full, start, period), start, c.index)
		require.Equal(t, c.mapping, sc.fimatheMap, "the mapping of %s", period)
		require.Equal(t, c.arm, sc.arm, "armed on %s", full.Time[c.index])
		require.Equal(t, c.trigger, at(full, sc.trigger), "the entry, on %s", full.Time[c.index])
		require.Equal(t, c.invalid, at(full, sc.invalid), "the end, on %s", full.Time[c.index])
		require.InDelta(t, c.line, sc.line, 1e-9)
	}
}
