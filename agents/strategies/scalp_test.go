package strategies

import (
	"context"
	"errors"
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

func newScalp(t *testing.T, configure func(*ScalpConfig)) *Scalp {
	t.Helper()
	cfg := DefaultScalpConfig()
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewScalp(cfg)
	require.NoError(t, err)
	return s
}

func TestNewScalp_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ScalpConfig)
		err       string
	}{
		{"zero period", func(c *ScalpConfig) { c.RSILen = 0 }, "at least 1"},
		{"RSI levels crossed", func(c *ScalpConfig) { c.Oversold = 80 }, "RSI levels"},
		{"RSI level out of range", func(c *ScalpConfig) { c.Overbought = 100 }, "RSI levels"},
		{"no stop loss", func(c *ScalpConfig) { c.StopATR = 0 }, "stop loss and take profit"},
		{"negative fixed stop", func(c *ScalpConfig) { c.StopValue = -1 }, "fixed stop loss"},
		{"no take profit", func(c *ScalpConfig) { c.TakeProfitATR = -1 }, "stop loss and take profit"},
		{"negative max bars", func(c *ScalpConfig) { c.MaxBars = -1 }, "max bars"},
		{"negative min ATR", func(c *ScalpConfig) { c.MinATRPct = -1 }, "min ATR"},
		{"min position value", func(c *ScalpConfig) { c.MinPositionValue = 0 }, "min position value"},
		{"whole balance leveraged", func(c *ScalpConfig) { c.Leverage = 10 }, "below 1"},
		{"zero position size", func(c *ScalpConfig) { c.PositionSize = map[string]float64{"BTCUSDT": 0} },
			"position size of BTCUSDT"},
		{"NaN position size", func(c *ScalpConfig) { c.PositionSize = map[string]float64{"BTCUSDT": math.NaN()} },
			"position size of BTCUSDT"},
		{"direction", func(c *ScalpConfig) { c.Direction = "long" }, "invalid direction"},
		{"timeframe", func(c *ScalpConfig) { c.Timeframe = "" }, "timeframe"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultScalpConfig()
			tt.configure(&cfg)
			_, err := NewScalp(cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}
}

func TestScalp_WarmupPeriod(t *testing.T) {
	// the previous candle of the cross, plus the seed of the EMA washed out
	require.Equal(t, 50+1+rkSeedFactor*50, newScalp(t, nil).WarmupPeriod())
}

// scCandle is a candle of a Scalp test: its prices, the EMA, RSI and ATR the
// strategy reads for it, and the asset held after it.
type scCandle struct {
	close, high, low float64
	ema, rsi         float64
	atr              float64 // 1 when zero
	position         float64
}

// scRunner feeds candles to a strategy trading the paper wallet and checks the
// position after each of them.
type scRunner struct {
	t      *testing.T
	s      *Scalp
	wallet *exchange.PaperWallet
	broker service.Broker // the wallet, unless a test wraps it
	df     *model.Dataframe
}

func newSCRunner(t *testing.T, s *Scalp, holding float64) *scRunner {
	t.Helper()
	quote := 10000.0
	if holding > 0 {
		quote = 0
	}
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", quote),
		exchange.WithPaperAsset("BTC", holding))
	return &scRunner{t: t, s: s, wallet: wallet, broker: wallet,
		df: &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}}}
}

func (r *scRunner) feed(candles ...scCandle) {
	r.t.Helper()
	start := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, candle := range candles {
		if candle.high == 0 {
			candle.high, candle.low = candle.close, candle.close
		}
		if candle.atr == 0 {
			candle.atr = 1
		}
		at := start.Add(time.Duration(len(r.df.Close)) * time.Minute)
		r.wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: at, Close: candle.close, High: candle.high,
			Low: candle.low, Complete: true})

		df := r.df
		df.Time = append(df.Time, at)
		df.Close = append(df.Close, candle.close)
		df.High = append(df.High, candle.high)
		df.Low = append(df.Low, candle.low)
		df.Metadata["sc_ema"] = append(df.Metadata["sc_ema"], candle.ema)
		df.Metadata["sc_rsi"] = append(df.Metadata["sc_rsi"], candle.rsi)
		df.Metadata["sc_atr"] = append(df.Metadata["sc_atr"], candle.atr)
		r.s.OnCandle(df, r.broker)

		asset, _, err := r.wallet.Position("BTCUSDT")
		require.NoError(r.t, err)
		require.InDeltaf(r.t, candle.position, asset, 1e-9, "after candle %d", len(df.Close)-1)
	}
}

// the entries of the tests: 100 bought or sold at 100, the stop loss 1 away
// and the take profit 1.5 away, with the ATR at 1
var (
	scLong = []scCandle{
		{close: 100, ema: 99, rsi: 25},
		{close: 100, ema: 99, rsi: 35, position: 100},
	}
	scShort = []scCandle{
		{close: 100, ema: 101, rsi: 75},
		{close: 100, ema: 101, rsi: 65, position: -100},
	}
)

func TestScalp_Entries(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ScalpConfig)
		candles   []scCandle
	}{
		{name: "buy on the RSI crossing up oversold, above the EMA", candles: scLong},
		{
			name: "no buy below the EMA",
			candles: []scCandle{
				{close: 100, ema: 101, rsi: 25},
				{close: 100, ema: 101, rsi: 35},
			},
		},
		{
			name: "no buy while the RSI stays oversold",
			candles: []scCandle{
				{close: 100, ema: 99, rsi: 20},
				{close: 100, ema: 99, rsi: 28},
			},
		},
		{
			name: "no buy on the RSI crossing up away from oversold",
			candles: []scCandle{
				{close: 100, ema: 99, rsi: 45},
				{close: 100, ema: 99, rsi: 55},
			},
		},
		{name: "sell on the RSI crossing down overbought, below the EMA", candles: scShort},
		{
			name: "no sell above the EMA",
			candles: []scCandle{
				{close: 100, ema: 99, rsi: 75},
				{close: 100, ema: 99, rsi: 65},
			},
		},
		{
			name: "no sell while the RSI stays overbought",
			candles: []scCandle{
				{close: 100, ema: 101, rsi: 80},
				{close: 100, ema: 101, rsi: 72},
			},
		},
		{
			name:      "no entry while the ATR is too small for the costs",
			configure: func(c *ScalpConfig) { c.MinATRPct = 2 }, // an ATR of 1 is 1% of 100
			candles: []scCandle{
				{close: 100, ema: 99, rsi: 25},
				{close: 100, ema: 99, rsi: 35},
			},
		},
		{
			name:      "an entry once the ATR is large enough",
			configure: func(c *ScalpConfig) { c.MinATRPct = 2 },
			candles: []scCandle{
				{close: 100, ema: 99, rsi: 25, atr: 3},
				{close: 100, ema: 99, rsi: 35, atr: 3, position: 100},
			},
		},
		{
			name:      "the direction keeps the other side out",
			configure: func(c *ScalpConfig) { c.Direction = RKDirectionBuyOnly },
			candles: []scCandle{
				{close: 100, ema: 101, rsi: 75},
				{close: 100, ema: 101, rsi: 65},
			},
		},
		{
			name:      "a share of the balance",
			configure: func(c *ScalpConfig) { c.PositionFraction = 0.25 },
			candles: []scCandle{
				{close: 100, ema: 99, rsi: 25},
				{close: 100, ema: 99, rsi: 35, position: 25},
			},
		},
		{
			name:      "a fixed size, a lot",
			configure: func(c *ScalpConfig) { c.PositionSize = map[string]float64{"BTCUSDT": 0.5} },
			candles: []scCandle{
				{close: 100, ema: 99, rsi: 25},
				{close: 100, ema: 99, rsi: 35, position: 0.5},
			},
		},
		{
			name:      "no lot worth more than the balance",
			configure: func(c *ScalpConfig) { c.PositionSize = map[string]float64{"BTCUSDT": 150} },
			candles: []scCandle{
				{close: 100, ema: 99, rsi: 25},
				{close: 100, ema: 99, rsi: 35},
			},
		},
		{
			name:      "one position at a time",
			configure: func(c *ScalpConfig) { c.PositionFraction = 0.25 },
			candles: []scCandle{
				{close: 100, ema: 99, rsi: 25},
				{close: 100, ema: 99, rsi: 35, position: 25},
				{close: 100, ema: 99, rsi: 25, position: 25},
				{close: 100, ema: 99, rsi: 35, position: 25}, // another signal, while holding, with 7500 left
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newSCRunner(t, newScalp(t, tt.configure), 0).feed(tt.candles...)
		})
	}
}

func TestScalp_Bracket(t *testing.T) {
	// the stop loss and the take profit are placed with the entry, at the broker
	for _, tc := range []struct {
		name         string
		candles      []scCandle
		stop, profit float64
		side         model.SideType
	}{
		{"long", scLong, 99, 101.5, model.SideTypeSell},
		{"short", scShort, 101, 98.5, model.SideTypeBuy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScalp(t, nil)
			newSCRunner(t, s, 0).feed(tc.candles...)
			legs := s.trades["BTCUSDT"].legs
			require.Len(t, legs, 2)
			takeProfit, stopLoss := legs[0], legs[1]
			require.Equal(t, model.OrderTypeLimitMaker, takeProfit.Type)
			require.InDelta(t, tc.profit, takeProfit.Price, 1e-9)
			require.Equal(t, model.OrderTypeStopLoss, stopLoss.Type)
			require.InDelta(t, tc.stop, *stopLoss.Stop, 1e-9)
			for _, leg := range legs {
				require.Equal(t, tc.side, leg.Side)
				require.Equal(t, model.OrderStatusTypeNew, leg.Status)
				require.InDelta(t, 100, leg.Quantity, 1e-9)
			}
		})
	}

	t.Run("of the size of a lot", func(t *testing.T) {
		s := newScalp(t, func(c *ScalpConfig) { c.PositionSize = map[string]float64{"BTCUSDT": 0.5} })
		newSCRunner(t, s, 0).feed(scLong[0], scCandle{close: 100, ema: 99, rsi: 35, position: 0.5})
		for _, leg := range s.trades["BTCUSDT"].legs {
			require.InDelta(t, 0.5, leg.Quantity, 1e-9)
		}
	})

	t.Run("at fixed values, whatever the ATR", func(t *testing.T) {
		s := newScalp(t, func(c *ScalpConfig) { c.StopValue, c.TakeProfitValue = 50, 150 })
		newSCRunner(t, s, 0).feed(
			scCandle{close: 100, ema: 99, rsi: 25, atr: 2},
			scCandle{close: 100, ema: 99, rsi: 35, atr: 2, position: 100},
		)
		legs := s.trades["BTCUSDT"].legs
		require.InDelta(t, 101.5, legs[0].Price, 1e-9, "150 on 100")
		require.InDelta(t, 99.5, *legs[1].Stop, 1e-9, "50 on 100")
	})

	t.Run("configurable in ATR", func(t *testing.T) {
		s := newScalp(t, func(c *ScalpConfig) { c.StopATR, c.TakeProfitATR = 2, 3 })
		newSCRunner(t, s, 0).feed(
			scCandle{close: 100, ema: 99, rsi: 25, atr: 2},
			scCandle{close: 100, ema: 99, rsi: 35, atr: 2, position: 100},
		)
		legs := s.trades["BTCUSDT"].legs
		require.InDelta(t, 106, legs[0].Price, 1e-9, "3 ATR of 2")
		require.InDelta(t, 96, *legs[1].Stop, 1e-9, "2 ATR of 2")
	})
}

func TestScalp_Exits(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ScalpConfig)
		holding   float64
		entry     []scCandle
		candles   []scCandle
		quote     float64 // after the trade, 10000 before
	}{
		{
			name:    "the take profit closes the long",
			entry:   scLong,
			candles: []scCandle{{close: 101, high: 101.6, low: 100.5, ema: 99, rsi: 60, position: 0}},
			quote:   10000 + 100*1.5,
		},
		{
			name:    "the stop loss closes the long",
			entry:   scLong,
			candles: []scCandle{{close: 99.5, high: 100, low: 98.9, ema: 99, rsi: 40, position: 0}},
			quote:   10000 - 100*1,
		},
		{
			name:    "a candle touching both closes the long at the stop loss, the worst case",
			entry:   scLong,
			candles: []scCandle{{close: 100, high: 102, low: 98.5, ema: 99, rsi: 50, position: 0}},
			quote:   10000 - 100*1,
		},
		{
			name:    "the take profit closes the short",
			entry:   scShort,
			candles: []scCandle{{close: 99, high: 99.5, low: 98.4, ema: 101, rsi: 40, position: 0}},
			quote:   10000 + 100*1.5,
		},
		{
			name:    "the stop loss closes the short",
			entry:   scShort,
			candles: []scCandle{{close: 100.5, high: 101.1, low: 100, ema: 101.5, rsi: 60, position: 0}},
			quote:   10000 - 100*1,
		},
		{
			name:      "a trade still open after max bars is closed at the market",
			configure: func(c *ScalpConfig) { c.MaxBars = 3 },
			entry:     scLong,
			candles: []scCandle{
				{close: 100.5, ema: 99, rsi: 50, position: 100},
				{close: 100.2, ema: 99, rsi: 50, position: 100},
				{close: 100.4, ema: 99, rsi: 50, position: 0}, // the third candle after the entry
			},
			quote: 10000 + 100*0.4,
		},
		{
			name:    "the close back across the EMA ends the long at the market",
			entry:   scLong,
			candles: []scCandle{{close: 99.2, high: 100, low: 99.1, ema: 99.5, rsi: 40, position: 0}},
			quote:   10000 - 100*0.8,
		},
		{
			name:    "the close back across the EMA ends the short at the market",
			entry:   scShort,
			candles: []scCandle{{close: 100.6, high: 100.9, low: 100, ema: 100.5, rsi: 55, position: 0}},
			quote:   10000 - 100*0.6,
		},
		{
			name:    "no exit inside the bracket, on the side of the EMA",
			entry:   scLong,
			candles: []scCandle{{close: 100.3, high: 101.4, low: 99.1, ema: 99, rsi: 55, position: 100}},
			quote:   0,
		},
		{
			name:      "no time limit with max bars at 0",
			configure: func(c *ScalpConfig) { c.MaxBars = 0 },
			entry:     scLong,
			candles: []scCandle{
				{close: 100.1, ema: 99, rsi: 50, position: 100},
				{close: 100.1, ema: 99, rsi: 50, position: 100},
				{close: 100.1, ema: 99, rsi: 50, position: 100},
			},
			quote: 0,
		},
		{
			name:      "a position found without a trade is followed from this candle",
			configure: func(c *ScalpConfig) { c.MaxBars = 2 },
			holding:   1,
			candles: []scCandle{
				{close: 50, ema: 49, rsi: 50, position: 1},
				{close: 50, ema: 49, rsi: 50, position: 0}, // the second candle it is followed
			},
			quote: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newSCRunner(t, newScalp(t, tt.configure), tt.holding)
			r.feed(tt.entry...)
			r.feed(tt.candles...)

			if tt.quote > 0 {
				_, quote, err := r.wallet.Position("BTCUSDT")
				require.NoError(t, err)
				require.InDelta(t, tt.quote, quote, 1e-9)
			}
		})
	}
}

func TestScalp_ExitCancelsTheBracket(t *testing.T) {
	s := newScalp(t, func(c *ScalpConfig) { c.MaxBars = 1 })
	r := newSCRunner(t, s, 0)
	r.feed(scLong...)
	legs := s.trades["BTCUSDT"].legs

	r.feed(scCandle{close: 100.2, ema: 99, rsi: 50, position: 0})
	for _, leg := range legs {
		got, err := r.wallet.Order("BTCUSDT", leg.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, got.Status, leg.Type)
	}

	// and a new entry, with the 10020 left, gets a bracket of its own
	r.feed(scCandle{close: 100, ema: 99, rsi: 25}, scCandle{close: 100, ema: 99, rsi: 35, position: 100.2})
	require.Len(t, s.trades["BTCUSDT"].legs, 2)
}

func TestScalp_NoEntryWithoutTheBracket(t *testing.T) {
	s := newScalp(t, nil)
	broker := &sizingBroker{quote: 10000}
	df := &model.Dataframe{
		Pair:  "BTCUSDT",
		Close: model.Series[float64]{100, 100},
		High:  model.Series[float64]{100, 100},
		Low:   model.Series[float64]{100, 100},
		Metadata: map[string]model.Series[float64]{
			"sc_ema": {99, 99}, "sc_rsi": {25, 35}, "sc_atr": {1, 1},
		},
	}
	s.OnCandle(df, broker)
	require.Zero(t, broker.orders, "a broker that can't place the stop loss and take profit gets no order")
}

func TestScalp_ExitAfterALegFilled(t *testing.T) {
	s := newScalp(t, func(c *ScalpConfig) { c.MaxBars = 1 })
	r := newSCRunner(t, s, 0)
	r.feed(scLong...)

	// the stop loss is filled as it is canceled: the time stop finds the
	// position closed, rather than open the other side
	r.broker = fillingBroker{r.wallet}
	r.feed(scCandle{close: 100.2, ema: 99, rsi: 50, position: 0})
	require.Empty(t, s.trades)
}

// failingExits rejects the next orders at the market, eg: the market closed.
type failingExits struct {
	*exchange.PaperWallet
	fails int
}

func (b *failingExits) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	if b.fails > 0 {
		b.fails--
		return model.Order{}, errors.New("market closed")
	}
	return b.PaperWallet.CreateOrderMarket(side, pair, size)
}

func TestScalp_FailedExitIsRetried(t *testing.T) {
	s := newScalp(t, nil)
	r := newSCRunner(t, s, 0)
	r.feed(scLong...)
	legs := s.trades["BTCUSDT"].legs

	// the invalidation cancels the legs, and the close is rejected
	r.broker = &failingExits{PaperWallet: r.wallet, fails: 2}
	r.feed(scCandle{close: 99.5, ema: 99.7, rsi: 40, position: 100})
	for _, leg := range legs {
		got, err := r.wallet.Order("BTCUSDT", leg.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, got.Status, leg.Type)
	}

	// left without them, it is closed as soon as the market takes the order,
	// though the close is back above the EMA
	r.feed(scCandle{close: 100, ema: 99.7, rsi: 45, position: 100})
	r.feed(scCandle{close: 100, ema: 99.7, rsi: 45, position: 0})
	require.Empty(t, s.trades)
}

func TestScalp_Signals(t *testing.T) {
	// the indicators of Indicators: an entry is exactly the RSI turning back
	// from an extreme on the side of the EMA
	s := newScalp(t, nil)
	df := &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}}
	buys, sells := 0, 0
	for i := 0; i < 600; i++ {
		price := 100 + 4*math.Sin(float64(i)/60) + 1.5*math.Sin(float64(i)/3) // trends with pullbacks
		df.Time = append(df.Time, time.Unix(int64(i)*60, 0))
		df.Close = append(df.Close, price)
		df.High = append(df.High, price+0.2)
		df.Low = append(df.Low, price-0.2)

		s.Indicators(df)
		ema, rsi := df.Metadata["sc_ema"], df.Metadata["sc_rsi"]
		var want model.SideType
		switch {
		case i < 1 || isNa(ema[i]) || isNa(rsi[i-1]):
		case price > ema[i] && rsi[i-1] <= 30 && rsi[i] > 30:
			want = model.SideTypeBuy
		case price < ema[i] && rsi[i-1] >= 70 && rsi[i] < 70:
			want = model.SideTypeSell
		}
		got := s.signal(df)
		require.Equal(t, want, got, "candle %d", i)
		switch got {
		case model.SideTypeBuy:
			buys++
		case model.SideTypeSell:
			sells++
		}
	}
	require.Positive(t, buys)
	require.Positive(t, sells)
}

func TestScalp_Backtest(t *testing.T) {
	ctx := context.Background()
	s := newScalp(t, func(c *ScalpConfig) { c.Timeframe = "1h" })

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

	// the indicators of the whole history: a candle repeating the time of the
	// previous one replaces it, as in the dataframe of the strategy
	candles, err := csvFeed.CandlesByPeriod(ctx, "BTCUSDT", "1h", time.Time{}, time.Now())
	require.NoError(t, err)
	index := make(map[int64]int, len(candles))
	var closes, highs, lows []float64
	for _, candle := range candles {
		if i, ok := index[candle.Time.Unix()]; ok && i == len(closes)-1 {
			closes[i], highs[i], lows[i] = candle.Close, candle.High, candle.Low
			continue
		}
		index[candle.Time.Unix()] = len(closes)
		closes, highs, lows = append(closes, candle.Close), append(highs, candle.High), append(lows, candle.Low)
	}
	ema, rsi, atr := pineEMA(closes, 50), pineRSI(closes, 7), pineATR(highs, lows, closes, 14)

	// each trade: a market entry on a signal with its bracket, then one exit,
	// a leg filled or a market order, the other leg canceled
	entries, exitsByLeg, exitsByMarket := 0, 0, 0
	position := 0.0
	for _, order := range orders {
		switch {
		case order.Type == model.OrderTypeMarket && position == 0:
			i, ok := index[order.CreatedAt.Unix()]
			require.True(t, ok, order.String())
			if order.Side == model.SideTypeBuy {
				require.Greater(t, closes[i], ema[i], "above the EMA: %s", order)
				require.True(t, rsi[i-1] <= 30 && rsi[i] > 30, "RSI crossing up oversold: %s", order)
			} else {
				require.Less(t, closes[i], ema[i], "below the EMA: %s", order)
				require.True(t, rsi[i-1] >= 70 && rsi[i] < 70, "RSI crossing down overbought: %s", order)
			}
			require.InDelta(t, closes[i], order.Price, 1e-9, "at the close: %s", order)
			entries++
		case order.Type == model.OrderTypeLimitMaker || order.Type == model.OrderTypeStopLoss:
			// the legs, at 1.5 and 1 ATR of the close of their entry
			i := index[order.CreatedAt.Unix()]
			distance := 1.5
			if order.Type == model.OrderTypeStopLoss {
				distance = 1
			}
			level := order.Price
			if order.Stop != nil {
				level = *order.Stop
			}
			require.InDelta(t, distance*atr[i], math.Abs(level-closes[i]), 1e-6*closes[i], order.String())
			if order.Status == model.OrderStatusTypeFilled {
				exitsByLeg++
			} else {
				continue
			}
		default:
			exitsByMarket++
		}
		if order.Status == model.OrderStatusTypeFilled {
			quantity := order.Quantity
			if order.Side == model.SideTypeSell {
				quantity = -quantity
			}
			if position += quantity; math.Abs(position) < 1e-6 {
				position = 0
			}
		}
	}
	open := 0
	if position != 0 {
		open = 1
	}
	require.Positive(t, entries)
	require.Positive(t, exitsByLeg, "trades ended at their stop loss or take profit")
	require.Positive(t, exitsByMarket, "trades ended at the market, by time or across the EMA")
	require.Equal(t, entries, exitsByLeg+exitsByMarket+open, "one exit per trade, the last one may be open")
}
