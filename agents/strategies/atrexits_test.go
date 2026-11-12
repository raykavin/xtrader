package strategies

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
)

// The levels of a strategy placing its own replaced by the ones in ATR from
// the entry when asked: the stop loss StopATR away, the take profit
// TakeProfitATR away, each apart; as they are unasked, or without an ATR yet.
func TestBracket_ATRLevels(t *testing.T) {
	df := &model.Dataframe{Pair: "BTCUSDT"}
	for range 30 { // a true range of 2 on every candle: the ATR at 2
		df.High = append(df.High, 101)
		df.Low = append(df.Low, 99)
		df.Close = append(df.Close, 100)
	}
	levels := func(atrStop, atrProfit bool, entry model.SideType, df *model.Dataframe) (float64, float64) {
		cfg := DefaultBracketConfig()
		cfg.StopATR, cfg.TakeProfitATR, cfg.ATRStop, cfg.ATRTakeProfit = 1, 2, atrStop, atrProfit
		return newBracket("TEST", "15m", cfg).atrLevels(df, entry, 100, 95, 0)
	}

	stop, profit := levels(true, true, model.SideTypeBuy, df)
	require.InDelta(t, 98, stop, 1e-9)
	require.InDelta(t, 104, profit, 1e-9)
	stop, profit = levels(true, true, model.SideTypeSell, df)
	require.InDelta(t, 102, stop, 1e-9)
	require.InDelta(t, 96, profit, 1e-9)
	stop, profit = levels(true, false, model.SideTypeBuy, df)
	require.Equal(t, []float64{98, 0}, []float64{stop, profit}, "the stop only: the take profit of the strategy")
	stop, profit = levels(false, false, model.SideTypeBuy, df)
	require.Equal(t, []float64{95, 0}, []float64{stop, profit}, "unasked")

	short := &model.Dataframe{Pair: "BTCUSDT", High: df.High[:5], Low: df.Low[:5], Close: df.Close[:5]}
	stop, profit = levels(true, true, model.SideTypeBuy, short)
	require.Equal(t, []float64{95, 0}, []float64{stop, profit}, "no ATR yet: the levels of the strategy")
}

// A squeeze broken with the exits in ATR asked: the stop loss and the take
// profit from the close of the breakout in the ATR of its candles, in place of
// the candle of the breakout and of the height of the box; the stop only,
// the take profit of the box kept.
func TestSqueezeBreakout_ATRExits(t *testing.T) {
	r, s := newSQRunner(t, func(c *SqueezeBreakoutConfig) {
		c.StopATR, c.TakeProfitATR, c.ATRStop, c.ATRTakeProfit = 1, 2, true, true
	})
	r.feed(sqBox()...)
	entry := sqBreakout
	entry.position = 10
	r.feed(entry)
	series := pineATR(r.df.High, r.df.Low, r.df.Close, s.cfg.ATRLen)
	atr, closePrice := series[len(series)-1], r.df.Close.Last(0)
	legs := s.bracket.trades["BTCUSDT"].legs
	require.InDelta(t, closePrice-atr, *legs[1].Stop, 1e-9, "1 ATR under the close")
	require.InDelta(t, closePrice+2*atr, legs[0].Price, 1e-9, "2 ATR over the close")

	r, s = newSQRunner(t, func(c *SqueezeBreakoutConfig) { c.StopATR, c.ATRStop = 1, true })
	r.feed(sqBox()...)
	r.feed(entry)
	series = pineATR(r.df.High, r.df.Low, r.df.Close, s.cfg.ATRLen)
	legs = s.bracket.trades["BTCUSDT"].legs
	require.InDelta(t, r.df.Close.Last(0)-series[len(series)-1], *legs[1].Stop, 1e-9)
	require.InDelta(t, 103, legs[0].Price, 1e-9, "the take profit of the box")
}

// The stop loss and the take profit of matrend in ATR, at the broker: the stop
// 1 ATR from the close in place of the one beyond the trend MA, the take
// profit 2 ATR away closing the position there; the fixed values still
// replacing them.
func TestMATrend_ATRStops(t *testing.T) {
	// bought 100 at 100 with the ATR at 2: the stop at 98 rather than 96, the
	// take profit at 104
	entry := []maCandle{
		{close: 97, fast: 101, slow: 100, trend: 98},
		{close: 100, fast: 101, slow: 100, trend: 98, position: 100},
	}

	t.Run("both at the broker", func(t *testing.T) {
		s := newMATrend(t, func(c *MATrendConfig) { c.StopATR, c.TakeProfitATR = 1, 2 })
		r := newMARunner(t, s, 0)
		r.feed(entry...)
		require.InDelta(t, 98, s.trades["BTCUSDT"].stop, 1e-9)
		leg := s.legs["BTCUSDT"]
		require.Equal(t, model.OrderTypeStopLoss, leg.Type)
		require.InDelta(t, 98, *leg.Stop, 1e-9)
		require.InDelta(t, 104, s.targets["BTCUSDT"], 1e-9)

		r.feed(maCandle{close: 103, high: 104.5, low: 100, fast: 104, slow: 101, trend: 99})
		_, quote, err := r.wallet.Position("BTCUSDT")
		require.NoError(t, err)
		require.InDelta(t, 10400, quote, 1e-9, "400 earned at 104")
	})

	t.Run("the fixed values replace them", func(t *testing.T) {
		s := newMATrend(t, func(c *MATrendConfig) { c.StopATR, c.StopValue = 1, 50 })
		r := newMARunner(t, s, 0)
		r.feed(entry...)
		require.InDelta(t, 99.5, *s.legs["BTCUSDT"].Stop, 1e-9)
	})

	cfg := DefaultMATrendConfig()
	cfg.TakeProfitATR = -1
	_, err := NewMATrend(cfg)
	require.ErrorContains(t, err, "ATR multiples")
}

// The take profit of trendconfluence in ATR, placed at the broker with the
// entry and its stop: 2 ATR from the close; none unasked.
func TestRKTrendConfluence_TakeProfitATR(t *testing.T) {
	candle := func() *model.Dataframe {
		df := signalCandle(model.SideTypeBuy) // at 100, the ATR at 1
		df.High, df.Low = model.Series[float64]{100}, model.Series[float64]{100}
		return df
	}
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000))
	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: time.Now(), Low: 100, High: 100, Close: 100, Complete: true})
	s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) { c.BrokerStopATR, c.TakeProfitATR = 1, 2 })
	s.OnCandle(candle(), wallet)
	protection := s.protection["BTCUSDT"]
	require.NotNil(t, protection)
	require.InDelta(t, 99, protection.stop, 1e-9)
	require.InDelta(t, 102, protection.profit, 1e-9)

	cfg := DefaultRKTrendConfluenceConfig()
	cfg.TakeProfitATR = -1
	_, err := NewRKTrendConfluence(cfg)
	require.ErrorContains(t, err, "take profit ATR")
}
