package strategies

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/tools/log"
)

func newMATrend(t *testing.T, configure func(*MATrendConfig)) *MATrend {
	t.Helper()
	cfg := DefaultMATrendConfig()
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewMATrend(cfg)
	require.NoError(t, err)
	return s
}

func TestNewMATrend_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*MATrendConfig)
		err       string
	}{
		{"fast EMA not shorter", func(c *MATrendConfig) { c.FastLen = 21 }, "fast EMA period"},
		{"zero period", func(c *MATrendConfig) { c.TrendLen = 0 }, "at least 1"},
		{"no distance filter", func(c *MATrendConfig) { c.MaxTrendDistancePct = 0 }, "max distance"},
		{"negative stop buffer", func(c *MATrendConfig) { c.StopBufferATR = -1 }, "stop buffer"},
		{"no trailing", func(c *MATrendConfig) { c.TrailingATR = 0 }, "trailing"},
		{"negative fixed stop", func(c *MATrendConfig) { c.StopValue = -1 }, "fixed stop loss"},
		{"negative fixed take profit", func(c *MATrendConfig) { c.TakeProfitValue = -1 }, "fixed stop loss"},
		{"min position value", func(c *MATrendConfig) { c.MinPositionValue = 0 }, "min position value"},
		{"position fraction", func(c *MATrendConfig) { c.PositionFraction = 1.5 }, "position fraction"},
		{"whole balance leveraged", func(c *MATrendConfig) { c.Leverage = 10 }, "below 1"},
		{"zero position size", func(c *MATrendConfig) { c.PositionSize = map[string]float64{"BTCUSDT": 0} },
			"position size of BTCUSDT"},
		{"NaN position size", func(c *MATrendConfig) { c.PositionSize = map[string]float64{"BTCUSDT": math.NaN()} },
			"position size of BTCUSDT"},
		{"direction", func(c *MATrendConfig) { c.Direction = "long" }, "invalid direction"},
		{"timeframe", func(c *MATrendConfig) { c.Timeframe = "" }, "timeframe"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultMATrendConfig()
			tt.configure(&cfg)
			_, err := NewMATrend(cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}
}

func TestMATrend_WarmupPeriod(t *testing.T) {
	// the trend SMA and the previous candle of the cross, plus the seed of the
	// slow EMA washed out
	require.Equal(t, 200+1+rkSeedFactor*21, newMATrend(t, nil).WarmupPeriod())
}

// maCandle is a candle of a MATrend test: its prices, the averages and ATR the
// strategy reads for it, and the asset held after it.
type maCandle struct {
	close, high, low  float64
	fast, slow, trend float64
	atr               float64 // 2 when zero
	position          float64
}

// maRunner feeds candles to a strategy trading the paper wallet and checks the
// position after each of them.
type maRunner struct {
	t      *testing.T
	s      *MATrend
	wallet *exchange.PaperWallet
	broker service.Broker // the wallet, unless a test wraps it
	df     *model.Dataframe
}

func newMARunner(t *testing.T, s *MATrend, holding float64) *maRunner {
	t.Helper()
	quote := 10000.0
	if holding > 0 {
		quote = 0
	}
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", quote),
		exchange.WithPaperAsset("BTC", holding))
	return &maRunner{t: t, s: s, wallet: wallet, broker: wallet,
		df: &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}}}
}

func (r *maRunner) feed(candles ...maCandle) {
	r.t.Helper()
	start := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, candle := range candles {
		if candle.high == 0 {
			candle.high, candle.low = candle.close, candle.close
		}
		if candle.atr == 0 {
			candle.atr = 2
		}
		at := start.Add(time.Duration(len(r.df.Close)) * time.Hour)
		r.wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: at, Close: candle.close, High: candle.high,
			Low: candle.low, Complete: true})

		df := r.df
		df.Time = append(df.Time, at)
		df.Close = append(df.Close, candle.close)
		df.High = append(df.High, candle.high)
		df.Low = append(df.Low, candle.low)
		df.Metadata["ma_fast"] = append(df.Metadata["ma_fast"], candle.fast)
		df.Metadata["ma_slow"] = append(df.Metadata["ma_slow"], candle.slow)
		df.Metadata["ma_trend"] = append(df.Metadata["ma_trend"], candle.trend)
		df.Metadata["ma_atr"] = append(df.Metadata["ma_atr"], candle.atr)
		r.s.OnCandle(df, r.broker)

		asset, _, err := r.wallet.Position("BTCUSDT")
		require.NoError(r.t, err)
		require.InDeltaf(r.t, candle.position, asset, 1e-9, "after candle %d", len(df.Close)-1)
	}
}

func TestMATrend_Entries(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*MATrendConfig)
		candles   []maCandle
	}{
		{
			name: "buy when the close breaks above the trend MA, with the fast EMA already above",
			candles: []maCandle{
				{close: 97, fast: 101, slow: 100, trend: 98},
				{close: 100, fast: 101, slow: 100, trend: 98, position: 100}, // 2.04% above the trend MA
			},
		},
		{
			name:      "a fixed size, a lot",
			configure: func(c *MATrendConfig) { c.PositionSize = map[string]float64{"BTCUSDT": 0.5} },
			candles: []maCandle{
				{close: 97, fast: 101, slow: 100, trend: 98},
				{close: 100, fast: 101, slow: 100, trend: 98, position: 0.5},
			},
		},
		{
			name: "no new cross of the EMAs needed: they crossed long before, below the trend MA",
			candles: []maCandle{
				{close: 96, fast: 99, slow: 100, trend: 98},
				{close: 96.5, fast: 100.5, slow: 100, trend: 98}, // the cross, below the trend MA: no entry
				{close: 97, fast: 101, slow: 100, trend: 98},
				{close: 100, fast: 101.5, slow: 100, trend: 98, position: 100},
			},
		},
		{
			name: "no buy with the fast EMA below the slow one",
			candles: []maCandle{
				{close: 97, fast: 99, slow: 100, trend: 98},
				{close: 100, fast: 99.5, slow: 100, trend: 98},
			},
		},
		{
			name: "no buy on a cross of the EMAs above the trend MA, without a break",
			candles: []maCandle{
				{close: 99, fast: 99, slow: 100, trend: 98},
				{close: 100, fast: 101, slow: 100, trend: 98},
			},
		},
		{
			name: "no buy on a close back at the trend MA",
			candles: []maCandle{
				{close: 97, fast: 101, slow: 100, trend: 98},
				{close: 98, fast: 101, slow: 100, trend: 98},
			},
		},
		{
			name: "no buy when the break closes too far from the trend MA",
			candles: []maCandle{
				{close: 89, fast: 101, slow: 100, trend: 90},
				{close: 100, fast: 101, slow: 100, trend: 90}, // 11.1%
			},
		},
		{
			name:      "the distance is configurable",
			configure: func(c *MATrendConfig) { c.MaxTrendDistancePct = 12 },
			candles: []maCandle{
				{close: 89, fast: 101, slow: 100, trend: 90},
				{close: 100, fast: 101, slow: 100, trend: 90, position: 100},
			},
		},
		{
			name: "sell when the close breaks below the trend MA, with the slow EMA already above",
			candles: []maCandle{
				{close: 103, fast: 99, slow: 100, trend: 102},
				{close: 100, fast: 99, slow: 100, trend: 102, position: -100}, // 1.96% below
			},
		},
		{
			name: "no new cross of the EMAs needed to sell either",
			candles: []maCandle{
				{close: 104, fast: 101, slow: 100, trend: 102},
				{close: 103.5, fast: 99.5, slow: 100, trend: 102}, // the cross, above the trend MA: no entry
				{close: 103, fast: 99, slow: 100, trend: 102},
				{close: 100, fast: 98.5, slow: 100, trend: 102, position: -100},
			},
		},
		{
			name: "no sell with the fast EMA above the slow one",
			candles: []maCandle{
				{close: 103, fast: 101, slow: 100, trend: 102},
				{close: 100, fast: 101, slow: 100, trend: 102},
			},
		},
		{
			name: "no sell on a cross of the EMAs below the trend MA, without a break",
			candles: []maCandle{
				{close: 101, fast: 101, slow: 100, trend: 102},
				{close: 100, fast: 99, slow: 100, trend: 102},
			},
		},
		{
			name: "no sell when the break closes too far from the trend MA",
			candles: []maCandle{
				{close: 111, fast: 99, slow: 100, trend: 110},
				{close: 100, fast: 99, slow: 100, trend: 110}, // 9.1%
			},
		},
		{
			name:      "the direction keeps the other side out",
			configure: func(c *MATrendConfig) { c.Direction = RKDirectionBuyOnly },
			candles: []maCandle{
				{close: 103, fast: 99, slow: 100, trend: 102},
				{close: 100, fast: 99, slow: 100, trend: 102},
			},
		},
		{
			name:      "a share of the balance",
			configure: func(c *MATrendConfig) { c.PositionFraction = 0.25 },
			candles: []maCandle{
				{close: 97, fast: 101, slow: 100, trend: 98},
				{close: 100, fast: 101, slow: 100, trend: 98, position: 25},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newMARunner(t, newMATrend(t, tt.configure), 0).feed(tt.candles...)
		})
	}
}

func TestMATrend_EntryAtTheCloseOfTheBreak(t *testing.T) {
	r := newMARunner(t, newMATrend(t, nil), 0)
	r.feed(maCandle{close: 97, fast: 101, slow: 100, trend: 98})
	r.feed(maCandle{close: 100, high: 101, low: 96, fast: 101, slow: 100, trend: 98, position: 100})

	// the 10000 bought 100 at 100, the close, not at the high or the low
	account, err := r.wallet.Account()
	require.NoError(t, err)
	_, quote := account.Balance("BTC", "USDT")
	require.InDelta(t, 0, quote.Free+quote.Lock, 1e-9)
}

func TestMATrend_Exits(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*MATrendConfig)
		holding   float64 // BTC held before the first candle, bought by someone else
		candles   []maCandle
	}{
		{
			name: "the initial stop of a long is below the trend MA",
			candles: []maCandle{
				{close: 97, fast: 101, slow: 100, trend: 98},
				{close: 100, fast: 101, slow: 100, trend: 98, position: 100},                    // stop 98-2 = 96
				{close: 97, high: 101, low: 96, fast: 101, slow: 100, trend: 98, position: 100}, // 101-6 = 95: kept at 96
				{close: 96, fast: 101, slow: 100, trend: 98, position: 0},
			},
		},
		{
			name:      "the buffer beyond the trend MA is configurable",
			configure: func(c *MATrendConfig) { c.StopBufferATR = 0 },
			candles: []maCandle{
				{close: 97, fast: 101, slow: 100, trend: 98},
				{close: 100, fast: 101, slow: 100, trend: 98, position: 100}, // stop 98
				{close: 98.5, fast: 101, slow: 100, trend: 98, position: 100},
				{close: 98, fast: 101, slow: 100, trend: 98, position: 0},
			},
		},
		{
			name: "the ATR moves the stop behind the highest high",
			candles: []maCandle{
				{close: 97, fast: 101, slow: 100, trend: 98},
				{close: 100, fast: 101, slow: 100, trend: 98, position: 100},                               // stop 96
				{close: 108, high: 110, low: 100, fast: 104, slow: 101, trend: 99, position: 100},          // 110-6 = 104
				{close: 106, high: 107, low: 105, atr: 10, fast: 105, slow: 102, trend: 99, position: 100}, // a wider ATR never loosens it
				{close: 104, high: 105, low: 103, fast: 105, slow: 102, trend: 99, position: 0},
			},
		},
		{
			name: "a wick through the stop is not enough",
			candles: []maCandle{
				{close: 97, fast: 101, slow: 100, trend: 98},
				{close: 100, fast: 101, slow: 100, trend: 98, position: 100}, // stop 96
				{close: 99, high: 100, low: 90, fast: 101, slow: 100, trend: 98, position: 100},
			},
		},
		{
			name: "the initial stop of a short is above the trend MA, then trails the lowest low",
			candles: []maCandle{
				{close: 103, fast: 99, slow: 100, trend: 102},
				{close: 100, fast: 99, slow: 100, trend: 102, position: -100},                   // stop 102+2 = 104
				{close: 92, high: 100, low: 90, fast: 96, slow: 99, trend: 101, position: -100}, // 90+6 = 96
				{close: 96, fast: 95, slow: 98, trend: 101, position: 0},
			},
		},
		{
			name: "the cross below closes the long at once, above the trend MA",
			candles: []maCandle{
				{close: 97, fast: 101, slow: 100, trend: 98},
				{close: 100, fast: 101, slow: 100, trend: 98, position: 100},
				{close: 101, fast: 99.5, slow: 100, trend: 98, position: 0}, // far above the stop: no short above the trend MA
			},
		},
		{
			name: "the cross below closes the long, and the break of the trend MA opens the short",
			candles: []maCandle{
				{close: 97, fast: 101, slow: 100, trend: 98},
				{close: 100, fast: 101, slow: 100, trend: 98, position: 100},
				{close: 97.5, fast: 99.5, slow: 100, trend: 98, position: -100}, // 0.51% below, with the 9750 left
			},
		},
		{
			name: "the cross above closes the short at once",
			candles: []maCandle{
				{close: 103, fast: 99, slow: 100, trend: 102},
				{close: 100, fast: 99, slow: 100, trend: 102, position: -100},
				{close: 99, fast: 100.5, slow: 100, trend: 102, position: 0}, // below the trend MA: no long
			},
		},
		{
			name:      "the cross below closes the long, even with the other side kept out",
			configure: func(c *MATrendConfig) { c.Direction = RKDirectionBuyOnly },
			candles: []maCandle{
				{close: 97, fast: 101, slow: 100, trend: 98},
				{close: 100, fast: 101, slow: 100, trend: 98, position: 100},
				{close: 97.5, fast: 99.5, slow: 100, trend: 98, position: 0},
			},
		},
		{
			name:    "a position found without a trade gets its stop from this candle",
			holding: 1,
			candles: []maCandle{
				{close: 50, fast: 51, slow: 50, trend: 48, position: 1}, // stop 48-2 = 46
				{close: 47, fast: 51, slow: 50, trend: 48, position: 1},
				{close: 46, fast: 51, slow: 50, trend: 48, position: 0},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newMARunner(t, newMATrend(t, tt.configure), tt.holding).feed(tt.candles...)
		})
	}
}

func TestMATrend_BrokerStop(t *testing.T) {
	entry := []maCandle{
		{close: 97, fast: 101, slow: 100, trend: 98},
		{close: 100, fast: 101, slow: 100, trend: 98, position: 100},
	}

	t.Run("placed with the entry, below the trend MA", func(t *testing.T) {
		s := newMATrend(t, func(c *MATrendConfig) { c.BrokerStop = true })
		newMARunner(t, s, 0).feed(entry...)
		leg := s.legs["BTCUSDT"]
		require.Equal(t, model.OrderTypeStopLoss, leg.Type)
		require.Equal(t, model.SideTypeSell, leg.Side)
		require.InDelta(t, 96, *leg.Stop, 1e-9)
	})

	t.Run("of the size of a lot", func(t *testing.T) {
		s := newMATrend(t, func(c *MATrendConfig) {
			c.BrokerStop, c.PositionSize = true, map[string]float64{"BTCUSDT": 0.5}
		})
		newMARunner(t, s, 0).feed(entry[0], maCandle{close: 100, fast: 101, slow: 100, trend: 98, position: 0.5})
		require.InDelta(t, 0.5, s.legs["BTCUSDT"].Quantity, 1e-9)
	})

	t.Run("moved as it trails, and closed by the broker on a wick", func(t *testing.T) {
		s := newMATrend(t, func(c *MATrendConfig) { c.BrokerStop = true })
		r := newMARunner(t, s, 0)
		r.feed(entry...)
		r.feed(maCandle{close: 108, high: 110, low: 100, fast: 104, slow: 101, trend: 99, position: 100})
		require.InDelta(t, 104, *s.legs["BTCUSDT"].Stop, 1e-9, "110-6")
		r.feed(maCandle{close: 106, high: 107, low: 103.5, fast: 105, slow: 102, trend: 99, position: 0})
	})

	t.Run("canceled before the reversal", func(t *testing.T) {
		s := newMATrend(t, func(c *MATrendConfig) { c.BrokerStop = true })
		r := newMARunner(t, s, 0)
		r.feed(entry...)
		long := *s.legs["BTCUSDT"]

		r.feed(maCandle{close: 97.5, fast: 99.5, slow: 100, trend: 98, position: -100})
		canceled, err := r.wallet.Order("BTCUSDT", long.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, canceled.Status)
		short := s.legs["BTCUSDT"]
		require.Equal(t, model.SideTypeBuy, short.Side)
		require.InDelta(t, 100, *short.Stop, 1e-9, "the short's own stop: 98+2")
	})

	t.Run("filled as it is canceled: the reversal only opens the short", func(t *testing.T) {
		s := newMATrend(t, func(c *MATrendConfig) { c.BrokerStop = true })
		r := newMARunner(t, s, 0)
		r.feed(entry...)
		r.broker = fillingBroker{r.wallet}
		r.feed(maCandle{close: 97.5, fast: 99.5, slow: 100, trend: 98, position: -100})
		require.Contains(t, s.legs, "BTCUSDT", "the short is the entry, with its stop")
		require.InDelta(t, 100, *s.legs["BTCUSDT"].Stop, 1e-9)
	})

	t.Run("no position without a broker placing it", func(t *testing.T) {
		s := newMATrend(t, func(c *MATrendConfig) { c.BrokerStop = true })
		broker := &sizingBroker{quote: 10000}
		df := &model.Dataframe{
			Pair:  "BTCUSDT",
			Close: model.Series[float64]{97, 100},
			High:  model.Series[float64]{97, 100},
			Low:   model.Series[float64]{97, 100},
			Metadata: map[string]model.Series[float64]{
				"ma_fast": {101, 101}, "ma_slow": {100, 100}, "ma_trend": {98, 98}, "ma_atr": {2, 2},
			},
		}
		s.OnCandle(df, broker)
		require.Zero(t, broker.orders)
	})
}

func TestMATrend_Signals(t *testing.T) {
	// the averages of Indicators: the fast EMA crosses the slow one on the last
	// candle of a rise, above the trend SMA and close to it
	s := newMATrend(t, func(c *MATrendConfig) { c.TrendLen = 50 })
	df := &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}}
	entries := 0
	for i := 0; i < 400; i++ {
		price := 100 + 0.02*float64(i) + 2*math.Sin(float64(i)/8) // a rise with swings
		df.Time = append(df.Time, time.Unix(int64(i)*3600, 0))
		df.Close = append(df.Close, price)
		df.High = append(df.High, price+0.5)
		df.Low = append(df.Low, price-0.5)

		s.Indicators(df)
		entry, cross := s.signal(df)
		fast, slow, trend := df.Metadata["ma_fast"], df.Metadata["ma_slow"], df.Metadata["ma_trend"]
		switch {
		case crossover(fast, slow, i):
			require.Equal(t, model.SideTypeBuy, cross, "candle %d", i)
		case crossunder(fast, slow, i):
			require.Equal(t, model.SideTypeSell, cross, "candle %d", i)
		default:
			require.Empty(t, cross, "candle %d", i)
		}
		// an entry is exactly a break with the EMAs on its side
		var want model.SideType
		switch {
		case crossover(df.Close, trend, i) && fast[i] > slow[i]:
			want = model.SideTypeBuy
		case crossunder(df.Close, trend, i) && slow[i] > fast[i]:
			want = model.SideTypeSell
		}
		if want != "" && rkSign(want)*(price-trend[i])/trend[i]*100 > s.cfg.MaxTrendDistancePct {
			want = ""
		}
		require.Equal(t, want, entry, "candle %d", i)
		if entry != "" {
			entries++
		}
	}
	require.Positive(t, entries)
}

func TestMATrend_Backtest(t *testing.T) {
	ctx := context.Background()
	s := newMATrend(t, nil)

	csvFeed, err := exchange.NewCSVFeed(s.Timeframe(), exchange.PairFeed{
		Pair:      "BTCUSDT",
		File:      "../../testdata/btc-1h.csv",
		Timeframe: "1h",
	})
	require.NoError(t, err)

	store, err := storage.FromMemory()
	require.NoError(t, err)
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 10000),
		exchange.WithPaperFee(0.001, 0.001), exchange.WithDataFeed(csvFeed))

	bot, err := xtrader.NewBot(ctx, xtrader.Settings{Pairs: []string{"BTCUSDT"}}, wallet, s,
		xtrader.WithBacktest(wallet), xtrader.WithStorage(store), xtrader.WithLogLevel(log.ErrorLevel))
	require.NoError(t, err)
	require.NoError(t, bot.Run(ctx))

	orders, err := store.Orders()
	require.NoError(t, err)
	require.NotEmpty(t, orders)

	// every entry is a cross on the side of the trend MA, close enough to it,
	// with the averages of the whole history: a candle repeating the time of
	// the previous one replaces it, as in the dataframe of the strategy
	candles, err := csvFeed.CandlesByPeriod(ctx, "BTCUSDT", "1h", time.Time{}, time.Now())
	require.NoError(t, err)
	index := make(map[int64]int, len(candles))
	var closes []float64
	for _, candle := range candles {
		if i, ok := index[candle.Time.Unix()]; ok && i == len(closes)-1 {
			closes[i] = candle.Close
			continue
		}
		index[candle.Time.Unix()] = len(closes)
		closes = append(closes, candle.Close)
	}
	fast, slow, trend := pineEMA(closes, 9), pineEMA(closes, 21), pineSMA(closes, 200)

	position, entries, withoutCross := 0.0, 0, 0
	for _, order := range orders {
		require.Equal(t, model.OrderStatusTypeFilled, order.Status, order.String())
		if position == 0 {
			i, ok := index[order.CreatedAt.Unix()]
			require.True(t, ok, order.String())
			if order.Side == model.SideTypeBuy {
				require.True(t, crossover(closes, trend, i), "buy on the break above the trend MA: %s", order)
				require.Greater(t, fast[i], slow[i], "the fast EMA above the slow one: %s", order)
			} else {
				require.True(t, crossunder(closes, trend, i), "sell on the break below the trend MA: %s", order)
				require.Greater(t, slow[i], fast[i], "the slow EMA above the fast one: %s", order)
			}
			require.LessOrEqual(t, math.Abs(closes[i]-trend[i])/trend[i]*100, 3.0, "close to the trend MA: %s", order)
			require.InDelta(t, closes[i], order.Price, 1e-9, "at the close of the break: %s", order)
			if !crossover(fast, slow, i) && !crossunder(fast, slow, i) {
				withoutCross++
			}
			entries++
		}
		quantity := order.Quantity
		if order.Side == model.SideTypeSell {
			quantity = -quantity
		}
		if position += quantity; math.Abs(position) < 1e-6 {
			position = 0
		}
	}
	require.Positive(t, entries)
	require.Positive(t, withoutCross, "entries without a cross of the EMAs on their candle")
}

func TestMATrend_FixedStops(t *testing.T) {
	// 100 bought at 100: a loss of 50 is 0.5 away and a profit of 100 is 1
	// away, in place of the stop 1 ATR of 2 beyond the trend MA, 96
	entry := []maCandle{
		{close: 97, fast: 101, slow: 100, trend: 98},
		{close: 100, fast: 101, slow: 100, trend: 98, position: 100},
	}

	t.Run("the stop loss at the broker, not trailed", func(t *testing.T) {
		s := newMATrend(t, func(c *MATrendConfig) { c.StopValue = 50 })
		r := newMARunner(t, s, 0)
		r.feed(entry...)
		leg := s.legs["BTCUSDT"]
		require.Equal(t, model.OrderTypeStopLoss, leg.Type)
		require.InDelta(t, 99.5, *leg.Stop, 1e-9)

		// the ATR would trail it to 110-6
		r.feed(maCandle{close: 108, high: 110, low: 100, fast: 104, slow: 101, trend: 99, position: 100})
		require.InDelta(t, 99.5, *s.legs["BTCUSDT"].Stop, 1e-9)
		// the broker closes the long on a wick through it
		r.feed(maCandle{close: 100, high: 101, low: 99.4, fast: 104, slow: 101, trend: 99})
	})

	t.Run("the take profit at the broker", func(t *testing.T) {
		s := newMATrend(t, func(c *MATrendConfig) { c.StopValue, c.TakeProfitValue = 50, 100 })
		r := newMARunner(t, s, 0)
		r.feed(entry...)
		r.feed(maCandle{close: 100.5, high: 101.2, low: 100, fast: 101, slow: 100, trend: 98})
		_, quote, err := r.wallet.Position("BTCUSDT")
		require.NoError(t, err)
		require.InDelta(t, 10100, quote, 1e-9, "100 earned at 101")
	})

	t.Run("the take profit only: the stop in ATR trails without moving it", func(t *testing.T) {
		s := newMATrend(t, func(c *MATrendConfig) { c.TakeProfitValue = 1000 }) // at 110
		r := newMARunner(t, s, 0)
		r.feed(entry...)
		r.feed(maCandle{close: 108, high: 109, low: 100, fast: 104, slow: 101, trend: 99, position: 100})
		leg := s.legs["BTCUSDT"]
		require.Equal(t, model.OrderTypeLimitMaker, leg.Type)
		require.InDelta(t, 110, leg.Price, 1e-9)
		require.InDelta(t, 103, s.trades["BTCUSDT"].stop, 1e-9, "trailed on the close: 109-6")
	})
}
