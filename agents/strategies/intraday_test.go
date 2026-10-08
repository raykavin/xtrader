package strategies

import (
	"cmp"
	"context"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/raykavin/xtrader"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
)

// ivCandle is a candle of an intraday strategy test: its time, prices and
// volume, the indicators replacing the ones of Indicators, and the asset held
// after it.
type ivCandle struct {
	at                     string // HH:MM on the day of the runner, the previous candle + its step when empty
	open, close, high, low float64
	volume                 float64
	meta                   map[string]float64
	position               float64
}

// ivRunner feeds candles to a strategy trading the paper wallet, its
// indicators computed as the controller does, and checks the position after
// each of them.
type ivRunner struct {
	t      *testing.T
	s      strategy.Strategy
	wallet *exchange.PaperWallet
	broker service.Broker // the wallet, unless a test wraps it
	settle func()         // settles the fills of each candle before the strategy runs, if set, see tradingBot
	df     *model.Dataframe
	day    time.Time
	step   time.Duration

	fixed     map[string]float64         // indicators replaced on every candle
	overrides map[string]map[int]float64 // indicators replaced on a candle, by index
}

func newIVRunner(t *testing.T, s strategy.Strategy, holding float64) *ivRunner {
	t.Helper()
	quote := 10000.0
	if holding > 0 {
		quote = 0
	}
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", quote),
		exchange.WithPaperAsset("BTC", holding))
	return &ivRunner{t: t, s: s, wallet: wallet, broker: wallet,
		df:   &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}},
		day:  time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
		step: 5 * time.Minute, fixed: map[string]float64{}, overrides: map[string]map[int]float64{}}
}

func (r *ivRunner) feed(candles ...ivCandle) {
	r.t.Helper()
	for _, candle := range candles {
		at := r.day
		switch {
		case candle.at != "":
			clock, err := time.Parse("15:04", candle.at)
			require.NoError(r.t, err)
			at = r.day.Add(time.Duration(clock.Hour())*time.Hour + time.Duration(clock.Minute())*time.Minute)
		case len(r.df.Time) > 0:
			at = r.df.Time[len(r.df.Time)-1].Add(r.step)
		}
		if candle.open == 0 {
			candle.open = candle.close
		}
		if candle.high == 0 {
			candle.high = math.Max(candle.open, candle.close)
		}
		if candle.low == 0 {
			candle.low = math.Min(candle.open, candle.close)
		}
		if candle.volume == 0 {
			candle.volume = 1
		}

		r.wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: at, Open: candle.open, Close: candle.close,
			High: candle.high, Low: candle.low, Volume: candle.volume, Complete: true})
		if r.settle != nil {
			r.settle()
		}

		df := r.df
		df.Time = append(df.Time, at)
		df.Open = append(df.Open, candle.open)
		df.Close = append(df.Close, candle.close)
		df.High = append(df.High, candle.high)
		df.Low = append(df.Low, candle.low)
		df.Volume = append(df.Volume, candle.volume)
		index := len(df.Close) - 1
		for key, value := range candle.meta {
			if r.overrides[key] == nil {
				r.overrides[key] = map[int]float64{}
			}
			r.overrides[key][index] = value
		}

		r.s.Indicators(df)
		for key, value := range r.fixed {
			series := make(model.Series[float64], len(df.Close))
			for i := range series {
				series[i] = value
			}
			df.Metadata[key] = series
		}
		for key, values := range r.overrides {
			series := df.Metadata[key]
			if len(series) != len(df.Close) { // an indicator the configuration doesn't compute
				series = make(model.Series[float64], len(df.Close))
				for i := range series {
					series[i] = na
				}
				df.Metadata[key] = series
			}
			for i, value := range values {
				series[i] = value
			}
		}
		r.s.OnCandle(df, r.broker)

		asset, _, err := r.wallet.Position("BTCUSDT")
		require.NoError(r.t, err)
		require.InDeltaf(r.t, candle.position, asset, 1e-9, "after candle %d at %s", index, at.Format("15:04"))
	}
}

// quote returns the quote balance of the wallet.
func (r *ivRunner) quote() float64 {
	r.t.Helper()
	_, quote, err := r.wallet.Position("BTCUSDT")
	require.NoError(r.t, err)
	return quote
}

func TestBracketConfig_Invalid(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*BracketConfig)
		err       string
	}{
		{"zero ATR period", func(c *BracketConfig) { c.ATRLen = 0 }, "at least 1"},
		{"no stop loss", func(c *BracketConfig) { c.StopATR = 0 }, "stop loss and take profit"},
		{"no take profit", func(c *BracketConfig) { c.TakeProfitATR = -1 }, "stop loss and take profit"},
		{"negative fixed stop", func(c *BracketConfig) { c.StopValue = -1 }, "fixed stop loss"},
		{"negative fixed take profit", func(c *BracketConfig) { c.TakeProfitValue = -1 }, "fixed stop loss"},
		{"negative max bars", func(c *BracketConfig) { c.MaxBars = -1 }, "max bars"},
		{"negative min ATR", func(c *BracketConfig) { c.MinATRPct = -1 }, "min ATR"},
		{"min position value", func(c *BracketConfig) { c.MinPositionValue = 0 }, "min position value"},
		{"position fraction", func(c *BracketConfig) { c.PositionFraction = 1.5 }, "position fraction"},
		{"leverage below 1", func(c *BracketConfig) { c.Leverage = 0.5 }, "leverage"},
		{"whole balance leveraged", func(c *BracketConfig) { c.Leverage = 10 }, "below 1"},
		{"zero position size", func(c *BracketConfig) { c.PositionSize = map[string]float64{"BTCUSDT": 0} },
			"position size of BTCUSDT"},
		{"direction", func(c *BracketConfig) { c.Direction = "long" }, "invalid direction"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultBracketConfig()
			tt.configure(&cfg)
			require.ErrorContains(t, cfg.validate(), tt.err)
		})
	}
	require.NoError(t, DefaultBracketConfig().validate())
}

// the bracket of the intraday strategies, through the simplest one to drive:
// ZScoreReversion, its entries on the Z-Score of the metadata

func newBracketRunner(t *testing.T, configure func(*ZScoreReversionConfig)) (*ivRunner, *ZScoreReversion) {
	t.Helper()
	s := newZScore(t, func(c *ZScoreReversionConfig) {
		c.ADXLen = 0
		if configure != nil {
			configure(c)
		}
	})
	r := newIVRunner(t, s, 0)
	r.fixed["zs_atr"] = 1
	return r, s
}

// the entries of the bracket tests: 100 bought or sold at 100, the stop loss 1.5
// away and the take profit 3 away, with the ATR at 1
var (
	zsLong = []ivCandle{
		{close: 100, meta: map[string]float64{"zs_z": -1}},
		{close: 100, meta: map[string]float64{"zs_z": -2.5}, position: 100},
	}
	zsShort = []ivCandle{
		{close: 100, meta: map[string]float64{"zs_z": 1}},
		{close: 100, meta: map[string]float64{"zs_z": 2.5}, position: -100},
	}
)

// zsHold is a candle of a trade still away from the mean: Z at -2.
func zsHold(closePrice, position float64) ivCandle {
	return ivCandle{close: closePrice, meta: map[string]float64{"zs_z": -2}, position: position}
}

func TestBracket_Legs(t *testing.T) {
	for _, tc := range []struct {
		name         string
		candles      []ivCandle
		stop, profit float64
		side         model.SideType
	}{
		{"long", zsLong, 98.5, 103, model.SideTypeSell},
		{"short", zsShort, 101.5, 97, model.SideTypeBuy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := newBracketRunner(t, nil)
			r.feed(tc.candles...)
			legs := s.bracket.trades["BTCUSDT"].legs
			require.Len(t, legs, 2)
			takeProfit, stopLoss := legs[0], legs[1]
			require.Equal(t, model.OrderTypeLimitMaker, takeProfit.Type)
			require.InDelta(t, tc.profit, takeProfit.Price, 1e-9)
			require.Equal(t, model.OrderTypeStopLoss, stopLoss.Type)
			require.InDelta(t, tc.stop, *stopLoss.Stop, 1e-9)
			for _, leg := range legs {
				require.Equal(t, tc.side, leg.Side)
				require.InDelta(t, 100, leg.Quantity, 1e-9)
			}
		})
	}
}

func TestBracket_FixedLegs(t *testing.T) {
	// 100 bought or sold at 100: a loss of 50 is 0.5 away, a profit of 150 is
	// 1.5 away, whatever the ATR; the ATR levels are 1.5 and 3 ATR away
	both := func(c *BracketConfig) { c.StopValue, c.TakeProfitValue = 50, 150 }
	for _, tc := range []struct {
		name         string
		candles      []ivCandle
		configure    func(*BracketConfig)
		atr          float64
		stop, profit float64
	}{
		{"long", zsLong, both, 1, 99.5, 101.5},
		{"short", zsShort, both, 1, 100.5, 98.5},
		{"whatever the ATR", zsLong, both, 4, 99.5, 101.5},
		{"the stop loss only", zsLong, func(c *BracketConfig) { c.StopValue = 50 }, 1, 99.5, 103},
		{"the take profit only", zsLong, func(c *BracketConfig) { c.TakeProfitValue = 150 }, 1, 98.5, 101.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := newBracketRunner(t, func(c *ZScoreReversionConfig) { tc.configure(&c.BracketConfig) })
			r.fixed["zs_atr"] = tc.atr
			r.feed(tc.candles...)
			legs := s.bracket.trades["BTCUSDT"].legs
			require.Len(t, legs, 2)
			require.InDelta(t, tc.profit, legs[0].Price, 1e-9)
			require.InDelta(t, tc.stop, *legs[1].Stop, 1e-9)
		})
	}

	t.Run("of a lot", func(t *testing.T) {
		// 1 of loss and 2 of profit on 0.5: 2 and 4 away
		r, s := newBracketRunner(t, func(c *ZScoreReversionConfig) {
			c.StopValue, c.TakeProfitValue = 1, 2
			c.PositionSize = map[string]float64{"BTCUSDT": 0.5}
		})
		r.feed(zsLong[0], ivCandle{close: 100, meta: map[string]float64{"zs_z": -2.5}, position: 0.5})
		legs := s.bracket.trades["BTCUSDT"].legs
		require.InDelta(t, 104, legs[0].Price, 1e-9)
		require.InDelta(t, 98, *legs[1].Stop, 1e-9)
	})
}

func TestBracket_Sizing(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ZScoreReversionConfig)
		position  float64
	}{
		{"the whole balance", nil, 100},
		{"a share of the balance", func(c *ZScoreReversionConfig) { c.PositionFraction = 0.25 }, 25},
		{"a fixed size, a lot", func(c *ZScoreReversionConfig) { c.PositionSize = map[string]float64{"BTCUSDT": 0.5} },
			0.5},
		{"no lot worth more than the balance",
			func(c *ZScoreReversionConfig) { c.PositionSize = map[string]float64{"BTCUSDT": 150} }, 0},
		{"the direction keeps the other side out", func(c *ZScoreReversionConfig) { c.Direction = RKDirectionSellOnly },
			0},
		{"no entry while the ATR is too small for the costs", func(c *ZScoreReversionConfig) { c.MinATRPct = 2 }, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newBracketRunner(t, tt.configure)
			r.feed(zsLong[0], ivCandle{close: 100, meta: map[string]float64{"zs_z": -2.5}, position: tt.position})
		})
	}
}

func TestBracket_Exits(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ZScoreReversionConfig)
		candles   []ivCandle
		quote     float64 // after the trade, 10000 before
	}{
		{
			name:    "the take profit closes the long",
			candles: []ivCandle{{close: 102, high: 103.5, low: 100, meta: map[string]float64{"zs_z": -2}}},
			quote:   10000 + 100*3,
		},
		{
			name:    "the stop loss closes the long",
			candles: []ivCandle{{close: 99, high: 100, low: 98, meta: map[string]float64{"zs_z": -3}}},
			quote:   10000 - 100*1.5,
		},
		{
			name:      "a trade still open after max bars is closed at the market",
			configure: func(c *ZScoreReversionConfig) { c.MaxBars = 2 },
			candles:   []ivCandle{zsHold(100.5, 100), zsHold(100.4, 0)},
			quote:     10000 + 100*0.4,
		},
		{
			name:      "no time limit with max bars at 0",
			configure: func(c *ZScoreReversionConfig) { c.MaxBars = 0 },
			candles:   []ivCandle{zsHold(100.1, 100), zsHold(100.1, 100), zsHold(100.1, 100), zsHold(100.1, 100)},
		},
		{
			name:    "the exit of the strategy closes it at the market",
			candles: []ivCandle{{close: 101, meta: map[string]float64{"zs_z": 0.1}}},
			quote:   10000 + 100*1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newBracketRunner(t, tt.configure)
			r.feed(zsLong...)
			r.feed(tt.candles...)
			if tt.quote > 0 {
				require.InDelta(t, tt.quote, r.quote(), 1e-9)
			}
		})
	}
}

func TestBracket_ExitCancelsTheLegs(t *testing.T) {
	r, s := newBracketRunner(t, func(c *ZScoreReversionConfig) { c.MaxBars = 1 })
	r.feed(zsLong...)
	legs := s.bracket.trades["BTCUSDT"].legs

	r.feed(zsHold(100.2, 0))
	for _, leg := range legs {
		got, err := r.wallet.Order("BTCUSDT", leg.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, got.Status, leg.Type)
	}
	require.Empty(t, s.bracket.trades)
}

func TestBracket_FollowsAPositionFound(t *testing.T) {
	// a position opened before a restart: followed without legs, and closed
	// by the time limit
	s := newZScore(t, func(c *ZScoreReversionConfig) { c.ADXLen, c.MaxBars = 0, 2 })
	r := newIVRunner(t, s, 1)
	r.fixed["zs_atr"] = 1
	r.feed(ivCandle{close: 50, meta: map[string]float64{"zs_z": -1}, position: 1},
		ivCandle{close: 50, meta: map[string]float64{"zs_z": -1}, position: 0})
	require.InDelta(t, 50, r.quote(), 1e-9)
}

func TestBracket_ExitAfterALegFilled(t *testing.T) {
	r, s := newBracketRunner(t, func(c *ZScoreReversionConfig) { c.MaxBars = 1 })
	r.feed(zsLong...)

	// the stop loss is filled as it is canceled: the time stop finds the
	// position closed, rather than open the other side
	r.broker = fillingBroker{r.wallet}
	r.feed(zsHold(100.2, 0))
	require.Empty(t, s.bracket.trades)
}

func TestBracket_FailedExitIsRetried(t *testing.T) {
	r, s := newBracketRunner(t, nil)
	r.feed(zsLong...)

	// the exit at the mean cancels the legs, and the close is rejected
	r.broker = &failingExits{PaperWallet: r.wallet, fails: 2}
	r.feed(ivCandle{close: 100.5, meta: map[string]float64{"zs_z": 0.2}, position: 100})

	// left without them, it is closed as soon as the market takes the order,
	// though Z went back away from the mean
	r.feed(zsHold(100, 100), zsHold(100, 0))
	require.Empty(t, s.bracket.trades)
}

func TestBracket_NoEntryWithoutProtection(t *testing.T) {
	s := newZScore(t, func(c *ZScoreReversionConfig) { c.ADXLen = 0 })
	broker := &sizingBroker{quote: 10000}
	df := &model.Dataframe{
		Pair:  "BTCUSDT",
		Close: model.Series[float64]{100, 100},
		High:  model.Series[float64]{100, 100},
		Low:   model.Series[float64]{100, 100},
		Metadata: map[string]model.Series[float64]{
			"zs_z": {-1, -2.5}, "zs_atr": {1, 1},
		},
	}
	s.OnCandle(df, broker)
	require.Zero(t, broker.orders, "a broker that can't place the stop loss and take profit gets no order")
}

// TestIntraday_Backtest runs each intraday strategy on the 1h candles of the
// test data: each trade is a market entry on a signal, placed with its stop
// loss and take profit, then a single exit.
func TestIntraday_Backtest(t *testing.T) {
	tests := []struct {
		name string
		new  func() (strategy.Strategy, error)
	}{
		{"orb", func() (strategy.Strategy, error) {
			cfg := DefaultORBConfig()
			cfg.Timeframe, cfg.Timezone, cfg.SessionStart, cfg.OpenRange, cfg.SessionEnd = "1h", "UTC", "13:00",
				2*time.Hour, "21:00"
			return NewORB(cfg)
		}},
		{"vwap", func() (strategy.Strategy, error) {
			cfg := DefaultVWAPReclaimConfig()
			cfg.Timeframe = "1h"
			return NewVWAPReclaim(cfg)
		}},
		{"sweep", func() (strategy.Strategy, error) {
			cfg := DefaultLiquiditySweepConfig()
			cfg.Timeframe, cfg.DayLevels = "1h", true
			return NewLiquiditySweep(cfg)
		}},
		{"atr breakout", func() (strategy.Strategy, error) {
			cfg := DefaultATRBreakoutConfig()
			cfg.Timeframe = "1h"
			return NewATRBreakout(cfg)
		}},
		{"zscore", func() (strategy.Strategy, error) {
			cfg := DefaultZScoreReversionConfig()
			cfg.Timeframe = "1h"
			return NewZScoreReversion(cfg)
		}},
		{"ema pullback", func() (strategy.Strategy, error) {
			cfg := DefaultEMAPullbackConfig()
			cfg.Timeframe = "1h"
			return NewEMAPullback(cfg)
		}},
		{"asia london", func() (strategy.Strategy, error) {
			cfg := DefaultAsiaLondonConfig()
			cfg.Timeframe, cfg.Timezone = "1h", "UTC"
			return NewAsiaLondon(cfg)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := tt.new()
			require.NoError(t, err)
			checkBacktest(t, s)
		})
	}
}

// checkBacktest runs s on the 1h candles of the test data, and checks that it
// trades: each trade a market entry, then a single exit.
func checkBacktest(t *testing.T, s strategy.Strategy) {
	t.Helper()
	ctx := context.Background()
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
	// listed by their update, a leg filled on the candle of the next entry
	// may come after it: by their creation, each trade after the one before
	slices.SortFunc(orders, func(a, b *model.Order) int { return cmp.Compare(a.ID, b.ID) })

	entries, exits, position := 0, 0, 0.0
	for _, order := range orders {
		switch {
		case order.Type == model.OrderTypeMarket && position == 0:
			require.Equal(t, model.OrderStatusTypeFilled, order.Status, order.String())
			entries++
		case order.Type == model.OrderTypeLimitMaker || order.Type == model.OrderTypeStopLoss:
			if order.Status != model.OrderStatusTypeFilled {
				continue // the leg left, canceled with the exit
			}
			exits++
		default:
			exits++
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
	require.Positive(t, entries, "the strategy trades")
	require.Equal(t, entries, exits+open, "one exit per trade, the last one may be open")
}
