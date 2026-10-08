package strategies

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
)

// tradingBot is a bot trading the paper wallet through the order controller,
// which stores its orders, as the agents do: restart starts another over the
// same account and orders, as after a restart of the agent. With mt5, the
// wallet tells its fills as MT5 does (see mt5Fills).
type tradingBot struct {
	t      *testing.T
	wallet *exchange.PaperWallet
	mt5    *mt5Fills // nil for the paper wallet as it is
	store  storage.Storage
	step   time.Duration // of the candles
	ctl    *order.Controller
}

func newTradingBot(t *testing.T, wallet *exchange.PaperWallet, step time.Duration) *tradingBot {
	t.Helper()
	store, err := storage.FromMemory()
	require.NoError(t, err)
	b := &tradingBot{t: t, wallet: wallet, store: store, step: step}
	b.start()
	return b
}

// newMT5Bot is a tradingBot of the paper wallet telling its fills as MT5
// does.
func newMT5Bot(t *testing.T, wallet *exchange.PaperWallet, step time.Duration) *tradingBot {
	t.Helper()
	store, err := storage.FromMemory()
	require.NoError(t, err)
	b := &tradingBot{t: t, wallet: wallet, store: store, step: step, mt5: &mt5Fills{PaperWallet: wallet,
		step: step, opened: map[int64]int64{}, protected: map[int64][]int64{}}}
	b.start()
	return b
}

func (b *tradingBot) start() {
	var exch service.Exchange = b.wallet
	if b.mt5 != nil {
		exch = b.mt5
	}
	b.ctl = order.NewController(context.Background(), exch, b.store, order.NewOrderFeed())
	b.ctl.Start()
	b.t.Cleanup(b.ctl.Stop)
}

// restart stops the bot, and starts another over the account and the orders
// it stored.
func (b *tradingBot) restart() {
	b.ctl.Stop()
	b.start()
}

// broker is the broker of the strategies: the order controller of the bot,
// the openings it tells as a live broker stamps them.
func (b *tradingBot) broker() service.Broker {
	if b.mt5 != nil {
		return b.ctl
	}
	return liveStamps{Controller: b.ctl, step: b.step}
}

// mt5Fills is the paper wallet as MT5 tells its fills: stamped as they
// execute, a moment after the close of the candle whose close placed them,
// rather than at the open of that candle, the positions they open and the
// ones their legs protect told by ticket (see order.Controller.HeldLegs).
type mt5Fills struct {
	*exchange.PaperWallet
	step      time.Duration
	opened    map[int64]int64
	protected map[int64][]int64
}

func (m *mt5Fills) CreateOrderMarketProtected(side model.SideType, pair string, size, stop,
	profit float64) ([]model.Order, error) {
	orders, err := m.PaperWallet.CreateOrderMarketProtected(side, pair, size, stop, profit)
	if err != nil || len(orders) == 0 {
		return orders, err
	}
	entry := &orders[0]
	entry.CreatedAt = entry.CreatedAt.Add(m.step + 2*time.Second)
	entry.UpdatedAt = entry.CreatedAt
	m.opened[entry.ExchangeID] = entry.ExchangeID
	for _, leg := range orders[1:] {
		m.protected[leg.ExchangeID] = []int64{entry.ExchangeID}
	}
	return orders, nil
}

func (m *mt5Fills) FillPositions(fill model.Order) (int64, []int64, bool) {
	if fill.GroupID != nil {
		positions, ok := m.protected[fill.ExchangeID]
		return 0, positions, ok
	}
	position, ok := m.opened[fill.ExchangeID]
	return position, nil, ok
}

// settle settles the fills of a candle, before the strategy runs, as the bot
// does.
func (b *tradingBot) settle() {
	b.ctl.UpdateOrders()
}

// liveStamps tells the openings of the positions as a live broker stamps
// them, at the close of the candle whose close filled them: the paper wallet
// stamps its fills with the time of its last candle, the open of that candle.
type liveStamps struct {
	*order.Controller
	step time.Duration
}

func (b liveStamps) HeldLegs(pair string, side model.SideType) ([]model.Order, time.Time, error) {
	legs, opened, err := b.Controller.HeldLegs(pair, side)
	if !opened.IsZero() {
		opened = opened.Add(b.step)
	}
	return legs, opened, err
}

// requireOpen checks that the wallet holds legs open, at levels, the take
// profit first, 0 for none.
func requireOpen(t *testing.T, wallet *exchange.PaperWallet, legs []model.Order, levels ...float64) {
	t.Helper()
	var got []float64
	for _, leg := range legs {
		order, err := wallet.Order("BTCUSDT", leg.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeNew, order.Status, "%s", order)
		got = append(got, rkLegLevel(order))
	}
	require.Equal(t, levels, got)
}

// donchianatr, its stop loss trailing at the broker, restarted after its
// entry: the stop loss found still trails.
func TestSignalStrategy_TrailingAfterARestart(t *testing.T) {
	breakout := []ivCandle{
		{close: 99, meta: kv{"sg_upper": 99.5, "sg_lower": 95}},
		{close: 100, meta: kv{"sg_upper": 100, "sg_lower": 95}, position: 100},
	}
	timeframe := func(c *SignalConfig) { c.Timeframe = "5m" }
	stop := func(s *SignalStrategy) float64 {
		legs := s.bracket.trades["BTCUSDT"].legs
		require.Len(t, legs, 1, "the stop loss alone")
		return rkLegLevel(legs[0])
	}
	start := func(t *testing.T) (*ivRunner, *tradingBot) {
		r, s := newSignalRunner(t, "donchianatr", timeframe)
		bot := newTradingBot(t, r.wallet, r.step)
		r.broker, r.settle = bot.broker(), bot.settle
		r.feed(breakout...)
		require.InDelta(t, 98, stop(s), 1e-9)
		return r, bot
	}
	restart := func(r *ivRunner, bot *tradingBot) *SignalStrategy {
		bot.restart()
		s := newSignalStrategy(t, "donchianatr", timeframe)
		r.s, r.broker = s, bot.broker()
		return s
	}

	t.Run("its stop loss found", func(t *testing.T) {
		r, bot := start(t)
		s := restart(r, bot)
		r.feed(ivCandle{open: 100, close: 102.5, high: 103, low: 100, position: 100})
		require.InDelta(t, 101, stop(s), 1e-9, "2 ATR behind the high of 103")
		r.feed(ivCandle{open: 102.5, close: 101.8, high: 102, low: 101.5, position: 100})
		r.feed(ivCandle{open: 101.8, close: 101.2, high: 101.9, low: 100.9, position: 0})
		require.InDelta(t, 10000+100*1, r.quote(), 1e-9, "stopped out at 101")
	})

	t.Run("from the best price since the entry", func(t *testing.T) {
		r, bot := start(t)
		r.feed(ivCandle{open: 100, close: 100.5, high: 103, low: 100, position: 100}) // 101 above the close
		s := restart(r, bot)
		r.feed(ivCandle{open: 100.5, close: 101.4, high: 101.5, low: 101, position: 100})
		require.InDelta(t, 101, stop(s), 1e-9, "103-2, not 101.5-2")
	})
}

// MaxBars counts the candles since the entry of a position found after a
// restart, and its close cancels the legs found.
func TestBracket_MaxBarsAfterARestart(t *testing.T) {
	configure := func(c *SignalConfig) { c.Timeframe, c.MaxBars = "5m", 3 }
	r, s := newSignalRunner(t, "emacross", configure)
	bot := newTradingBot(t, r.wallet, r.step)
	r.broker, r.settle = bot.broker(), bot.settle
	r.feed(sgLong...)
	r.feed(sgHold)
	legs := s.bracket.trades["BTCUSDT"].legs

	bot.restart()
	s = newSignalStrategy(t, "emacross", configure)
	r.s, r.broker = s, bot.broker()
	r.feed(sgHold)
	require.Equal(t, 2, s.bracket.trades["BTCUSDT"].bars)
	requireOpen(t, r.wallet, s.bracket.trades["BTCUSDT"].legs, 101.5, 99)

	hold := sgHold
	hold.position = 0
	r.feed(hold) // the 3rd candle since the entry
	for _, leg := range legs {
		got, err := r.wallet.Order("BTCUSDT", leg.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, got.Status, leg.Type)
	}
	require.InDelta(t, 10000+100*0.5, r.quote(), 1e-9)
}

// B29, the live MT5 path: the order controller tells the legs by their
// tickets and the opening of the position by the time MT5 stamped its fill,
// from the orders it stored, without the help of liveStamps: MaxBars counts
// the candles since the entry.
func TestBracket_MaxBarsAfterARestartOnMT5(t *testing.T) {
	configure := func(c *SignalConfig) { c.Timeframe, c.MaxBars = "5m", 3 }
	r, s := newSignalRunner(t, "emacross", configure)
	bot := newMT5Bot(t, r.wallet, r.step)
	r.broker, r.settle = bot.broker(), bot.settle
	r.feed(sgLong...)
	r.feed(sgHold)
	legs := s.bracket.trades["BTCUSDT"].legs
	require.Len(t, legs, 2)
	held, opened, err := bot.ctl.HeldLegs("BTCUSDT", model.SideTypeBuy)
	require.NoError(t, err)
	require.Len(t, held, 2)
	entry := r.df.Time[len(r.df.Time)-2] // the candle whose close placed it
	require.Equal(t, entry.Add(r.step+2*time.Second), opened, "the time of the fill")

	bot.restart()
	s = newSignalStrategy(t, "emacross", configure)
	r.s, r.broker = s, bot.broker()
	r.feed(sgHold)
	require.Equal(t, 2, s.bracket.trades["BTCUSDT"].bars)
	requireOpen(t, r.wallet, s.bracket.trades["BTCUSDT"].legs, 101.5, 99)

	hold := sgHold
	hold.position = 0
	r.feed(hold) // the 3rd candle since the entry
	for _, leg := range legs {
		got, err := r.wallet.Order("BTCUSDT", leg.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, got.Status, leg.Type)
	}
	require.InDelta(t, 10000+100*0.5, r.quote(), 1e-9)
}

func TestScalp_AfterARestart(t *testing.T) {
	s := newScalp(t, func(c *ScalpConfig) { c.MaxBars = 3 })
	r := newSCRunner(t, s, 0)
	bot := newTradingBot(t, r.wallet, time.Minute)
	r.broker, r.settle = bot.broker(), bot.settle
	r.feed(scLong...)
	hold := scCandle{close: 100.2, ema: 99, rsi: 50, position: 100}
	r.feed(hold)

	bot.restart()
	r.s = newScalp(t, func(c *ScalpConfig) { c.MaxBars = 3 })
	r.broker = bot.broker()
	r.feed(hold)
	requireOpen(t, r.wallet, r.s.trades["BTCUSDT"].legs, 101.5, 99)
	hold.position = 0
	r.feed(hold) // the 3rd candle since the entry
	require.Empty(t, r.s.trades)
}

// matrend, its stop trailing at the broker, restarted after its entry.
func TestMATrend_BrokerStopAfterARestart(t *testing.T) {
	configure := func(c *MATrendConfig) { c.BrokerStop = true }
	r := newMARunner(t, newMATrend(t, configure), 0)
	bot := newTradingBot(t, r.wallet, time.Hour)
	r.broker, r.settle = bot.broker(), bot.settle
	r.feed(maCandle{close: 97, fast: 101, slow: 100, trend: 98},
		maCandle{close: 100, fast: 101, slow: 100, trend: 98, position: 100})

	bot.restart()
	r.s, r.broker = newMATrend(t, configure), bot.broker()
	r.feed(maCandle{close: 108, high: 110, low: 100, fast: 104, slow: 101, trend: 99, position: 100})
	require.Contains(t, r.s.legs, "BTCUSDT", "the stop found")
	require.InDelta(t, 104, rkLegLevel(*r.s.legs["BTCUSDT"]), 1e-9, "moved to 110-6")
	r.feed(maCandle{close: 106, high: 107, low: 103.5, fast: 105, slow: 102, trend: 99, position: 0})
}

// rkCandle is a candle of the tests of RKTrendConfluence fed to a dataframe
// that keeps them all, its ATR 2.
type rkCandle struct {
	close, high, low float64
	signal           model.SideType
	position         float64
}

// rkFeed feeds the candles to s, trading broker, on df, after the wallet and
// settle, if any, see tradingBot.
func rkFeed(t *testing.T, s *RKTrendConfluence, wallet *exchange.PaperWallet, broker service.Broker,
	settle func(), df *model.Dataframe, candles ...rkCandle) {
	t.Helper()
	start := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, candle := range candles {
		at := start.Add(time.Duration(len(df.Close)) * time.Hour)
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: at, Close: candle.close, High: candle.high,
			Low: candle.low, Complete: true})
		if settle != nil {
			settle()
		}
		buy, sell := 0.0, 0.0
		switch candle.signal {
		case model.SideTypeBuy:
			buy = 1
		case model.SideTypeSell:
			sell = 1
		}
		df.Time = append(df.Time, at)
		df.Close = append(df.Close, candle.close)
		df.High = append(df.High, candle.high)
		df.Low = append(df.Low, candle.low)
		df.Metadata["rk_buy"] = append(df.Metadata["rk_buy"], buy)
		df.Metadata["rk_sell"] = append(df.Metadata["rk_sell"], sell)
		df.Metadata["rk_atr"] = append(df.Metadata["rk_atr"], 2)
		s.OnCandle(df, broker)

		asset, _, err := wallet.Position("BTCUSDT")
		require.NoError(t, err)
		require.InDeltaf(t, candle.position, asset, 1e-9, "after candle %d", len(df.Close)-1)
	}
}

// trendconfluence, its stop trailing at the broker, restarted after a move
// whose stop was above the close: the trailing goes on from its best price.
func TestRKTrendConfluence_BrokerTrailingAfterARestart(t *testing.T) {
	configure := func(c *RKTrendConfluenceConfig) { c.BrokerTrailingATR = 1 } // 100-2 = 98
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000))
	bot := newTradingBot(t, wallet, time.Hour)
	df := &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}}
	rkFeed(t, newRKTrendConfluence(t, configure), wallet, bot.broker(), bot.settle, df,
		rkCandle{close: 100, high: 101, low: 99, signal: model.SideTypeBuy, position: 100},
		rkCandle{close: 101, high: 110, low: 100, position: 100}, // 110-2 = 108 is above the close: not sent
	)

	bot.restart()
	s := newRKTrendConfluence(t, configure)
	rkFeed(t, s, wallet, bot.broker(), bot.settle, df, rkCandle{close: 109, high: 109.5, low: 108.5, position: 100})
	require.Contains(t, s.protection, "BTCUSDT")
	require.InDelta(t, 108, s.protection["BTCUSDT"].stop, 1e-9)
	requireOpen(t, wallet, []model.Order{s.protection["BTCUSDT"].leg}, 108)
}

// closesInPlace closes a protected position with its legs in place, as MT5
// does (see service.ProtectedCloser); here it fails as failingExits does.
type closesInPlace struct {
	*failingExits
}

func (closesInPlace) ClosesProtected() bool {
	return true
}

// A close that fails leaves the position to its stop loss: its legs are placed
// again, at their levels, where they were canceled for it, or kept in place.
func TestBracket_FailedExitKeepsTheProtection(t *testing.T) {
	configure := func(c *SignalConfig) { c.MaxBars = 1 }
	wick := ivCandle{close: 99.2, low: 98.5, meta: kv{"sg_fast": 101, "sg_slow": 100}}

	t.Run("placed again", func(t *testing.T) {
		r, s := newSignalRunner(t, "emacross", configure)
		r.feed(sgLong...)
		r.broker = &failingExits{PaperWallet: r.wallet, fails: 2}
		r.feed(sgHold) // TIME, the close rejected
		trade := s.bracket.trades["BTCUSDT"]
		require.True(t, trade.exiting)
		requireOpen(t, r.wallet, trade.legs, 101.5, 99)

		r.feed(wick) // the stop loss closes it, though the close is rejected again
		require.InDelta(t, 10000-100*1, r.quote(), 1e-9)
		require.Empty(t, s.bracket.trades)
	})

	t.Run("kept in place", func(t *testing.T) {
		r, s := newSignalRunner(t, "emacross", configure)
		r.feed(sgLong...)
		legs := s.bracket.trades["BTCUSDT"].legs
		r.broker = closesInPlace{&failingExits{PaperWallet: r.wallet, fails: 2}}
		r.feed(sgHold)
		require.Equal(t, legs, s.bracket.trades["BTCUSDT"].legs)
		requireOpen(t, r.wallet, legs, 101.5, 99)
		r.feed(wick)
		require.InDelta(t, 10000-100*1, r.quote(), 1e-9)
	})
}

// closesWithLegs closes a protected position with its legs in place, as MT5
// does (see service.ProtectedCloser): it tells the state of the legs as the
// close is sent, and the cancels after it. The paper wallet sells only once
// they are out of the way: they are taken out with the close, as legs gone
// with the position.
type closesWithLegs struct {
	*exchange.PaperWallet
	legs     []model.Order
	atClose  []model.OrderStatusType
	closed   bool
	canceled []int64 // after the close, by ExchangeID
}

func (*closesWithLegs) ClosesProtected() bool {
	return true
}

func (b *closesWithLegs) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	if b.closed { // an entry after the close
		return b.PaperWallet.CreateOrderMarket(side, pair, size)
	}
	for _, leg := range b.legs {
		got, err := b.PaperWallet.Order(pair, leg.ExchangeID)
		if err != nil {
			return model.Order{}, err
		}
		b.atClose = append(b.atClose, got.Status)
	}
	for _, leg := range b.legs {
		_ = b.PaperWallet.Cancel(leg) // the other one of the group with it
	}
	b.closed = true
	return b.PaperWallet.CreateOrderMarket(side, pair, size)
}

func (b *closesWithLegs) Cancel(o model.Order) error {
	if b.closed {
		b.canceled = append(b.canceled, o.ExchangeID)
	}
	return b.PaperWallet.Cancel(o)
}

// B4s: where the market order closes the position with its legs in place
// (MT5), the close is sent with them, never left unprotected meanwhile, and
// the ones left once flat are canceled.
func TestRKCloseProtected_ClosesWithTheLegsInPlace(t *testing.T) {
	exchangeIDs := func(legs []model.Order) []int64 {
		var ids []int64
		for _, leg := range legs {
			ids = append(ids, leg.ExchangeID)
		}
		return ids
	}

	t.Run("bracket", func(t *testing.T) {
		r, s := newSignalRunner(t, "emacross", func(c *SignalConfig) { c.MaxBars = 1 })
		r.feed(sgLong...)
		legs := s.bracket.trades["BTCUSDT"].legs
		require.Len(t, legs, 2)
		broker := &closesWithLegs{PaperWallet: r.wallet, legs: legs}
		r.broker = broker
		hold := sgHold
		hold.position = 0
		r.feed(hold) // TIME
		require.Equal(t, []model.OrderStatusType{model.OrderStatusTypeNew, model.OrderStatusTypeNew}, broker.atClose,
			"the legs in place as the close is sent")
		require.ElementsMatch(t, exchangeIDs(legs), broker.canceled, "the ones left canceled once flat")
		require.Empty(t, s.bracket.trades)
		require.InDelta(t, 10000+100*0.5, r.quote(), 1e-9, "closed at 100.5")
	})

	t.Run("matrend", func(t *testing.T) {
		s := newMATrend(t, func(c *MATrendConfig) { c.BrokerStop = true })
		r := newMARunner(t, s, 0)
		r.feed(maCandle{close: 97, fast: 101, slow: 100, trend: 98},
			maCandle{close: 100, fast: 101, slow: 100, trend: 98, position: 100})
		require.Contains(t, s.legs, "BTCUSDT")
		legs := []model.Order{*s.legs["BTCUSDT"]}
		broker := &closesWithLegs{PaperWallet: r.wallet, legs: legs}
		r.broker = broker
		r.feed(maCandle{close: 97.5, fast: 99.5, slow: 100, trend: 98, position: -100}) // the reversal
		require.Equal(t, []model.OrderStatusType{model.OrderStatusTypeNew}, broker.atClose)
		require.Equal(t, exchangeIDs(legs), broker.canceled)
		require.Empty(t, s.exits)
		require.Contains(t, s.legs, "BTCUSDT", "the stop of the short")
		require.NotEqual(t, legs[0].ExchangeID, s.legs["BTCUSDT"].ExchangeID)
	})
}

func TestScalp_FailedExitKeepsTheProtection(t *testing.T) {
	s := newScalp(t, func(c *ScalpConfig) { c.MaxBars = 1 })
	r := newSCRunner(t, s, 0)
	r.feed(scLong...)
	r.broker = &failingExits{PaperWallet: r.wallet, fails: 2}
	r.feed(scCandle{close: 100.2, ema: 99, rsi: 50, position: 100}) // TIME, the close rejected
	requireOpen(t, r.wallet, s.trades["BTCUSDT"].legs, 101.5, 99)

	r.feed(scCandle{close: 99.5, high: 100, low: 98.5, ema: 99, rsi: 40, position: 0})
	_, quote, err := r.wallet.Position("BTCUSDT")
	require.NoError(t, err)
	require.InDelta(t, 10000-100*1, quote, 1e-9, "stopped out at 99")
}

func TestMATrend_FailedExitKeepsTheProtection(t *testing.T) {
	s := newMATrend(t, func(c *MATrendConfig) { c.BrokerStop = true })
	r := newMARunner(t, s, 0)
	r.feed(maCandle{close: 97, fast: 101, slow: 100, trend: 98},
		maCandle{close: 100, fast: 101, slow: 100, trend: 98, position: 100})
	r.broker = &failingExits{PaperWallet: r.wallet, fails: 2}
	r.feed(maCandle{close: 97.5, fast: 99.5, slow: 100, trend: 98, position: 100}) // the reversal rejected
	require.Contains(t, s.legs, "BTCUSDT")
	requireOpen(t, r.wallet, []model.Order{*s.legs["BTCUSDT"]}, 96)

	r.feed(maCandle{close: 97, high: 97.5, low: 95.5, fast: 99.5, slow: 100, trend: 98, position: 0})
	_, quote, err := r.wallet.Position("BTCUSDT")
	require.NoError(t, err)
	require.InDelta(t, 10000-100*4, quote, 1e-9, "stopped out at 96")
	require.Empty(t, s.exits)
}

func TestRKTrendConfluence_FailedExitKeepsTheProtection(t *testing.T) {
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000))
	broker := &failingExits{PaperWallet: wallet}
	s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) { c.BrokerStopATR = 1 }) // 100-2 = 98
	df := &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}}
	rkFeed(t, s, wallet, broker, nil, df, rkCandle{close: 100, high: 100, low: 100, signal: model.SideTypeBuy,
		position: 100})

	broker.fails = 2
	rkFeed(t, s, wallet, broker, nil, df, rkCandle{close: 99.5, high: 100, low: 99.5, signal: model.SideTypeSell,
		position: 100}) // the reversal rejected
	require.Contains(t, s.protection, "BTCUSDT")
	requireOpen(t, wallet, []model.Order{s.protection["BTCUSDT"].leg}, 98)

	rkFeed(t, s, wallet, broker, nil, df, rkCandle{close: 98.5, high: 99.5, low: 97.5})
	_, quote, err := wallet.Position("BTCUSDT")
	require.NoError(t, err)
	require.InDelta(t, 10000-100*2, quote, 1e-9, "stopped out at 98")
	require.Empty(t, s.exits)
}

// A close that fails is retried on the next candle, the position protected
// meanwhile, though the signal that decided it is gone.
func TestRKTrendConfluence_FailedExitIsRetried(t *testing.T) {
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000))
	broker := &failingExits{PaperWallet: wallet}
	s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) {
		c.BrokerStopATR, c.Direction = 1, RKDirectionBuyOnly
	})
	df := &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}}
	rkFeed(t, s, wallet, broker, nil, df, rkCandle{close: 100, high: 100, low: 100, signal: model.SideTypeBuy,
		position: 100})
	broker.fails = 1
	rkFeed(t, s, wallet, broker, nil, df,
		rkCandle{close: 101, high: 101, low: 100, signal: model.SideTypeSell, position: 100},
		rkCandle{close: 101, high: 101, low: 100.5, position: 0},
	)
	require.Empty(t, s.protection)
	require.Empty(t, s.exits)
	_, quote, err := wallet.Position("BTCUSDT")
	require.NoError(t, err)
	require.InDelta(t, 10000+100*1, quote, 1e-9, "closed at 101")
}

// trendconfluence, its trailing stop on the close, restarted after a move: the
// stop is replayed from the candle of the entry, not set up again from the
// candle after the restart.
func TestRKTrendConfluence_StopsAfterARestart(t *testing.T) {
	configure := func(c *RKTrendConfluenceConfig) { c.TrailingATR = 1 }
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000))
	bot := newTradingBot(t, wallet, time.Hour)
	df := &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}}
	rkFeed(t, newRKTrendConfluence(t, configure), wallet, bot.broker(), bot.settle, df,
		rkCandle{close: 100, high: 101, low: 99, signal: model.SideTypeBuy, position: 100},
		rkCandle{close: 109, high: 110, low: 100, position: 100}, // the stop at 110-2
	)

	bot.restart()
	s := newRKTrendConfluence(t, configure)
	rkFeed(t, s, wallet, bot.broker(), bot.settle, df, rkCandle{close: 107, high: 109, low: 106.5, position: 0})
	_, quote, err := wallet.Position("BTCUSDT")
	require.NoError(t, err)
	require.InDelta(t, 10000+100*7, quote, 1e-9, "stopped on the close of 107, under 108")
}

// B35: the positions a session opened before a restart count against its
// MaxTrades, told by the order controller from the orders it stored; the next
// session trades again.
func TestORB_MaxTradesAfterARestart(t *testing.T) {
	failed := ivCandle{close: 97.9, high: 99, low: 97.9, position: 0} // closes the breakout at 97.9
	quote := 10000 - 100*2.1
	for _, tt := range []struct {
		name      string
		maxTrades int
		position  float64 // after the second breakout of the session
	}{
		{name: "the trade of the session before the restart counted", maxTrades: 1, position: 0},
		{name: "the ones left still taken", maxTrades: 2, position: quote / 100},
	} {
		t.Run(tt.name, func(t *testing.T) {
			configure := func(c *ORBConfig) { c.StopATR, c.MaxTrades = 3, tt.maxTrades }
			r, _ := newORBRunner(t, configure)
			bot := newTradingBot(t, r.wallet, r.step)
			r.broker, r.settle = bot.broker(), bot.settle
			r.feed(orbLong...)
			r.feed(failed)

			bot.restart()
			r.s, r.broker = newORB(t, configure), bot.broker()
			r.feed(ivCandle{close: 100, position: tt.position})
		})
	}

	t.Run("the next session", func(t *testing.T) {
		configure := func(c *ORBConfig) { c.StopATR = 3 }
		r, _ := newORBRunner(t, configure)
		bot := newTradingBot(t, r.wallet, r.step)
		r.broker, r.settle = bot.broker(), bot.settle
		r.feed(orbLong...)
		r.feed(failed)

		bot.restart()
		r.s, r.broker = newORB(t, configure), bot.broker()
		r.day = r.day.Add(24 * time.Hour)
		r.feed(append(orbLongRange[:5:5], ivCandle{open: 98.5, close: 100, position: quote / 100})...)
	})
}

// B35 on AsiaLondon: one trade per London session, a restart in between.
func TestAsiaLondon_MaxTradesAfterARestart(t *testing.T) {
	configure := func(c *AsiaLondonConfig) { c.StopATR, c.LondonEnd = 3, "12:00" }
	r, _ := newALRunner(t, configure)
	bot := newTradingBot(t, r.wallet, r.step)
	r.broker, r.settle = bot.broker(), bot.settle
	r.feed(alBreakout(98.5, nil, 0, 100)...)
	r.feed(ivCandle{open: 100, close: 97.9, position: 0})

	bot.restart()
	r.s, r.broker = newAsiaLondon(t, configure), bot.broker()
	r.feed(ivCandle{open: 97.9, close: 100, position: 0})
}

// The order controller tells the positions opened on a pair since a time, the
// ones replayed from the storage after a restart included, a reversal as an
// opening; PositionsReplayed the replayed ones only.
func TestController_PositionsOpenedAfterARestart(t *testing.T) {
	r, _ := newORBRunner(t, func(c *ORBConfig) { c.StopATR = 3 })
	bot := newTradingBot(t, r.wallet, r.step)
	r.broker, r.settle = bot.broker(), bot.settle
	r.feed(orbLong...)
	opened := r.df.Time[len(r.df.Time)-1]
	r.feed(ivCandle{close: 97.9, high: 99, low: 97.9, position: 0})
	require.Equal(t, 1, bot.ctl.PositionsOpened("BTCUSDT", opened))
	require.Zero(t, bot.ctl.PositionsOpened("BTCUSDT", opened.Add(time.Second)))
	require.Zero(t, bot.ctl.PositionsOpened("ETHUSDT", time.Time{}))
	require.Zero(t, bot.ctl.PositionsReplayed("BTCUSDT", opened), "opened by this run")

	bot.restart()
	require.Equal(t, 1, bot.ctl.PositionsOpened("BTCUSDT", opened), "replayed from the storage")
	require.Equal(t, 1, bot.ctl.PositionsReplayed("BTCUSDT", opened))
}

// manualTrade opens and closes a position at the market through the order
// controller of bot, as the trade panel and Telegram do.
func manualTrade(t *testing.T, bot *tradingBot) {
	t.Helper()
	_, err := bot.ctl.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
	require.NoError(t, err)
	_, err = bot.ctl.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 1)
	require.NoError(t, err)
	bot.settle()
}

// B35, without a restart: a manual position of the session (the trade panel
// or Telegram, the same order controller), opened and closed before the
// breakout, doesn't count against MaxTrades, as before the count after a
// restart: the strategy counts its own entries.
func TestORB_ManualTradeWithoutARestart(t *testing.T) {
	r, _ := newORBRunner(t, func(c *ORBConfig) { c.StopATR = 3 })
	bot := newTradingBot(t, r.wallet, r.step)
	r.broker, r.settle = bot.broker(), bot.settle
	r.feed(orbLongRange...)                                // the range, 10:00 to 10:15
	r.feed(ivCandle{open: 98.2, close: 98.4, position: 0}) // 10:15, the trade session: no breakout yet
	manualTrade(t, bot)
	r.feed(ivCandle{open: 98.5, close: 100, position: 100}) // 10:20, the breakout: its entry
}

// B35 on AsiaLondon, without a restart: as TestORB_ManualTradeWithoutARestart.
func TestAsiaLondon_ManualTradeWithoutARestart(t *testing.T) {
	r, _ := newALRunner(t, func(c *AsiaLondonConfig) { c.StopATR = 3 })
	bot := newTradingBot(t, r.wallet, r.step)
	r.broker, r.settle = bot.broker(), bot.settle
	r.feed(alLongAsia...)
	r.feed(ivCandle{at: "07:00", open: 98.5, close: 98.6, position: 0}) // London: no breakout yet
	manualTrade(t, bot)
	r.feed(ivCandle{open: 98.6, close: 100, position: 100})
}

// B35, the limit of the count after a restart: the orders stored don't tell
// who placed them, so a manual position of the session opened before a
// restart counts against MaxTrades after it; one opened after it doesn't.
func TestORB_ManualTradeAndARestart(t *testing.T) {
	for _, tt := range []struct {
		name     string
		before   bool    // the manual trade before the restart, else after it
		position float64 // after the breakout
	}{
		{name: "before the restart: counted", before: true, position: 0},
		{name: "after the restart: not counted", position: 100},
	} {
		t.Run(tt.name, func(t *testing.T) {
			configure := func(c *ORBConfig) { c.StopATR = 3 }
			r, _ := newORBRunner(t, configure)
			bot := newTradingBot(t, r.wallet, r.step)
			r.broker, r.settle = bot.broker(), bot.settle
			r.feed(orbLongRange...)
			r.feed(ivCandle{open: 98.2, close: 98.4, position: 0})
			if tt.before {
				manualTrade(t, bot)
			}

			bot.restart()
			r.s, r.broker = newORB(t, configure), bot.broker()
			if !tt.before {
				manualTrade(t, bot)
			}
			r.feed(ivCandle{open: 98.5, close: 100, position: tt.position})
		})
	}
}
