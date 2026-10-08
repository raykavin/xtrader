package llm

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/ui"
)

// fakeReporter books the results of the closes by the ID of their fill, in
// currency, and tells the open positions.
type fakeReporter struct {
	currency string
	profits  map[int64]float64
	open     map[string]exchange.MT5OpenPosition
}

func (f *fakeReporter) AccountSummary() (service.AccountSummary, bool) {
	return service.AccountSummary{Currency: f.currency}, f.currency != ""
}

func (f *fakeReporter) ClosedProfit(close, _ model.Order) (float64, bool) {
	profit, ok := f.profits[close.ID]
	return profit, ok
}

func (f *fakeReporter) OpenPosition(pair string) (exchange.MT5OpenPosition, bool) {
	position, ok := f.open[pair]
	return position, ok
}

// dayOrders are the stored orders of the pairs of a test, see SetOrderSource.
type dayOrders map[string][]model.Order

func (d dayOrders) add(pair string, orders ...model.Order) {
	for i := range orders {
		orders[i].Pair = pair
	}
	d[pair] = append(d[pair], orders...)
}

func filled(id int64, side model.SideType, price, quantity float64, at time.Time) model.Order {
	return model.Order{ID: id, Side: side, Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled,
		Price: price, Quantity: quantity, CreatedAt: at, UpdatedAt: at}
}

func legOf(id, group int64, side model.SideType, kind model.OrderType, status model.OrderStatusType, price,
	quantity float64, at time.Time) model.Order {
	return model.Order{ID: id, GroupID: &group, Side: side, Type: kind, Status: status, Price: price,
		Quantity: quantity, CreatedAt: at, UpdatedAt: at}
}

// closedPosition is a position of a test opened at open and closed at close by the
// fills of IDs id and id+1, its result booked by reporter.
func closedPosition(id int64, side model.SideType, open, close time.Time, reporter *fakeReporter,
	result float64) []model.Order {
	exit := model.SideTypeSell
	if side == model.SideTypeSell {
		exit = model.SideTypeBuy
	}
	reporter.profits[id+1] = result
	return []model.Order{filled(id, side, 100, 1, open), filled(id+1, exit, 101, 1, close)}
}

// noonZone returns a zone whose midnight is 12 hours away from at, for the
// days of a test not to turn while its candles go by.
func noonZone(at time.Time) *time.Location {
	utc := at.UTC()
	since := time.Duration(utc.Hour())*time.Hour + time.Duration(utc.Minute())*time.Minute
	offset := 12*time.Hour - since
	if offset > 12*time.Hour {
		offset -= 24 * time.Hour
	}
	return time.FixedZone("test-noon", int(offset.Seconds()))
}

// dailyRunner is a live runner of a Trader with the daily limits of
// trend_following_v3, 250 BRL of gain and 150 BRL of loss, its orders and its
// broker the ones of the test, the day of now at noon.
func dailyRunner(t *testing.T, holding float64) (*traderRunner, dayOrders, *fakeReporter, time.Time) {
	t.Helper()
	r, _ := liveRunner(t, func(c *Config) {
		c.DailyGain, c.DailyLoss, c.DailyCurrency = 250, 150, "BRL"
	}, holding)
	now := r.trader.now()
	r.trader.cfg.DayZone = noonZone(now)
	orders := dayOrders{}
	r.trader.SetOrderSource(func(pair string) ([]model.Order, error) { return orders[pair], nil })
	reporter := &fakeReporter{currency: "BRL", profits: map[int64]float64{}, open: map[string]exchange.MT5OpenPosition{}}
	r.trader.SetAccountReporter(reporter)
	start, _ := r.trader.dayOf(now)
	return r, orders, reporter, start
}

func TestStrategyDailyLimits(t *testing.T) {
	gain, loss, currency, err := StrategyDailyLimits("# t\n# Daily gain limit: 250 BRL\n# Daily loss limit: 150,00 brl\n\nText")
	require.NoError(t, err)
	require.Equal(t, 250.0, gain)
	require.Equal(t, 150.0, loss)
	require.Equal(t, "BRL", currency)

	gain, loss, currency, err = StrategyDailyLimits("# Daily loss limit: 80.5 USD\n\nText")
	require.NoError(t, err)
	require.Zero(t, gain, "one limit alone")
	require.Equal(t, 80.5, loss)
	require.Equal(t, "USD", currency)

	gain, loss, _, err = StrategyDailyLimits("# t\n\nDaily gain limit: 250 BRL in the text, not in the header")
	require.NoError(t, err)
	require.Zero(t, gain+loss)

	for header, want := range map[string]string{
		"# Daily gain limit: 250":                                "an amount and the currency",
		"# Daily gain limit: R$ 250,00":                          "the amount must be a positive number",
		"# Daily loss limit: 0 BRL":                              "the amount must be a positive number",
		"# Daily loss limit: -150 BRL":                           "the amount must be a positive number",
		"# Daily gain limit: 250 REAL":                           "a code of 3 letters",
		"# Daily gain limit: 250 BRL\n# Daily loss limit: 1 USD": "both in the currency of the account",
	} {
		_, _, _, err := StrategyDailyLimits(header + "\n\nText")
		require.ErrorContains(t, err, want, header)
	}
}

// A prompt with daily limits it can't apply is refused, the previous kept:
// unreadable, or in a currency other than the one of the account.
func TestTrader_DailyLimitsOfThePrompt(t *testing.T) {
	r, _, reporter, _ := dailyRunner(t, 0)
	state, err := r.trader.promptState(Prompt{Content: "# Daily gain limit: 250 BRL\n# Daily loss limit: 150 BRL\n\nText"})
	require.NoError(t, err)
	require.Equal(t, dailyLimits{gain: 250, loss: 150, currency: "BRL"}, state.daily)

	_, err = r.trader.promptState(Prompt{Content: "# Daily gain limit: abc BRL\n\nText"})
	require.ErrorContains(t, err, "positive number")

	reporter.currency = "USD"
	r.trader.currency = "" // read again
	_, err = r.trader.promptState(Prompt{Content: "# Daily gain limit: 250 BRL\n\nText"})
	require.ErrorContains(t, err, "the daily limits are in BRL, the account in USD")
}

// The day of the context: the positions and the orders of the pair told
// apart, the results on the positions only, each once, the ones of the other
// days and of the other pairs left out.
func TestTrader_DailyHistory(t *testing.T) {
	r, orders, reporter, start := dailyRunner(t, 0)
	at := func(minutes int) time.Time { return start.Add(time.Duration(minutes) * time.Minute) }

	// yesterday, closed before the midnight: left out
	orders.add(testPair, closedPosition(1, model.SideTypeBuy, at(-120), at(-1), reporter, -500)...)
	// a long closed by its take profit, its stop loss canceled
	orders.add(testPair, filled(10, model.SideTypeBuy, 100, 1, at(10)),
		legOf(11, 10, model.SideTypeSell, model.OrderTypeStopLoss, model.OrderStatusTypeCanceled, 98, 1, at(40)),
		legOf(12, 10, model.SideTypeSell, model.OrderTypeLimitMaker, model.OrderStatusTypeFilled, 104, 1, at(40)))
	reporter.profits[12] = 120
	// a short closed by its stop loss, its take profit canceled
	orders.add(testPair, filled(20, model.SideTypeSell, 104, 1, at(60)),
		legOf(21, 20, model.SideTypeBuy, model.OrderTypeStopLoss, model.OrderStatusTypeFilled, 105, 1, at(80)),
		legOf(22, 20, model.SideTypeBuy, model.OrderTypeLimitMaker, model.OrderStatusTypeCanceled, 100, 1, at(80)))
	reporter.profits[21] = -40
	// orders without a fill
	orders.add(testPair,
		model.Order{ID: 30, Side: model.SideTypeBuy, Type: model.OrderTypeMarket, Status: model.OrderStatusTypeRejected,
			CreatedAt: at(90), UpdatedAt: at(90)},
		model.Order{ID: 31, Side: model.SideTypeBuy, Type: model.OrderTypeLimit, Status: model.OrderStatusTypeExpired,
			Price: 99, Quantity: 1, CreatedAt: at(95), UpdatedAt: at(100)})
	// the position open now, its stop loss pending
	orders.add(testPair, filled(40, model.SideTypeBuy, 102, 1, at(110)),
		legOf(41, 40, model.SideTypeSell, model.OrderTypeStopLoss, model.OrderStatusTypeNew, 100, 1, at(110)))
	reporter.open[testPair] = exchange.MT5OpenPosition{Side: model.SideTypeBuy, Quantity: 1, Entry: 102, Profit: 15.5}
	// another pair: its own day
	orders.add("XAUUSD", closedPosition(50, model.SideTypeBuy, at(5), at(6), reporter, -900)...)

	day, blocked := r.trader.daily(testPair, r.trader.state.Load(), at(120))
	require.Empty(t, blocked)
	require.Equal(t, start.Format(time.DateOnly), day.Day)
	require.Equal(t, "BRL", day.Currency)
	require.Len(t, day.Positions, 3, "the two closed during the day and the open one")
	require.Equal(t, "LONG", day.Positions[0].Side)
	require.Equal(t, "closed", day.Positions[0].Status)
	require.Equal(t, number(120), day.Positions[0].Result)
	require.Equal(t, "take profit", day.Positions[0].ExitBy)
	require.Equal(t, "SHORT", day.Positions[1].Side)
	require.Equal(t, number(-40), day.Positions[1].Result)
	require.Equal(t, "stop loss", day.Positions[1].ExitBy)
	require.Equal(t, "open", day.Positions[2].Status)
	require.Nil(t, day.Positions[2].ClosedAt)
	require.Equal(t, number(15.5), day.Positions[2].UnrealizedResult)

	require.Equal(t, number(80), day.Result.Realized, "120 − 40, the open one apart and the orders never counted")
	require.Equal(t, number(15.5), day.Result.Unrealized)
	require.Zero(t, day.Result.PendingResults)

	roles := map[int64]string{}
	statuses := map[int64]string{}
	for _, order := range day.Orders {
		roles[order.ID], statuses[order.ID] = order.Role, order.Status
	}
	require.Equal(t, map[int64]string{10: "entry", 11: "stop_loss", 12: "take_profit", 20: "entry", 21: "stop_loss",
		22: "take_profit", 30: "other", 31: "other", 40: "entry", 41: "stop_loss"}, roles, "yesterday left out")
	require.Equal(t, "CANCELED", statuses[11])
	require.Equal(t, "REJECTED", statuses[30])
	require.Equal(t, "EXPIRED", statuses[31])
	require.Equal(t, "NEW", statuses[41])
	require.Equal(t, &DailyLimits{Gain: 250, Loss: 150}, day.Limits)

	// in the context sent: the unknowns as null
	data, err := json.Marshal(Input{Daily: day})
	require.NoError(t, err)
	require.Contains(t, string(data), `"daily_history":{"day":"`+day.Day+`"`)
	require.Contains(t, string(data), `"status":"open","size":1,"entry_price":102,"exit_price":null`)

	// a close the broker hasn't booked yet: the entries wait for it
	delete(reporter.profits, 21)
	day, blocked = r.trader.daily(testPair, r.trader.state.Load(), at(120))
	require.Equal(t, 1, day.Result.PendingResults)
	require.Equal(t, number(120), day.Result.Realized)
	require.Contains(t, blocked, "isn't booked by the broker yet")
	require.True(t, day.Limits.EntriesBlocked)
}

// The gain limit reached, exactly: no entry on the pair, the model not even
// asked, in the reanalyses too, with why.
func TestTrader_DailyGainLimit(t *testing.T) {
	r, orders, reporter, start := dailyRunner(t, 0)
	orders.add(testPair, closedPosition(1, model.SideTypeBuy, start.Add(time.Minute), start.Add(2*time.Minute), reporter,
		249.99)...)
	asked := len(r.provider.inputs)
	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	require.Len(t, r.provider.inputs, asked+1, "under the limit: asked")
	in := r.provider.last()
	require.Contains(t, in.Rules.Actions, ActionEnterLong)
	require.Empty(t, in.Rules.EntriesBlockedByDailyLimit)
	require.False(t, in.Daily.Limits.EntriesBlocked)
	require.Equal(t, number(249.99), in.Daily.Result.Realized)

	orders.add(testPair, closedPosition(3, model.SideTypeBuy, start.Add(3*time.Minute), start.Add(4*time.Minute), reporter,
		0.01)...)
	r.step(100)
	require.Len(t, r.provider.inputs, asked+1, "250.00 reached: not asked")

	messages := &[]string{}
	r.trader.SetNotifier(func(message string) { *messages = append(*messages, message) })
	_, err := r.trader.Reanalyze(testPair, ui.ChannelWeb, "enter now")
	require.NoError(t, err)
	r.inProgress(101)
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Len(t, r.provider.inputs, asked+1, "nor on a reanalysis")
	require.Len(t, *messages, 1)
	require.Contains(t, (*messages)[0], "the daily gain limit of 250.00 BRL reached: +250.00 BRL on BTCUSDT today")
}

// The loss limit reached: no entry on the pair until the next day, the other
// pairs free, each with its own day.
func TestTrader_DailyLossLimit(t *testing.T) {
	r, orders, reporter, start := dailyRunner(t, 0)
	st := r.trader.state.Load()
	orders.add(testPair, closedPosition(1, model.SideTypeSell, start.Add(time.Minute), start.Add(2*time.Minute), reporter,
		-149.99)...)
	_, blocked := r.trader.daily(testPair, st, start.Add(time.Hour))
	require.Empty(t, blocked, "a cent short of the limit")

	orders.add(testPair, closedPosition(3, model.SideTypeBuy, start.Add(3*time.Minute), start.Add(4*time.Minute), reporter,
		-0.01)...)
	_, blocked = r.trader.daily(testPair, st, start.Add(time.Hour))
	require.Equal(t, "the daily loss limit of 150.00 BRL reached: -150.00 BRL on BTCUSDT today", blocked)
	asked := len(r.provider.inputs)
	r.step(100)
	require.Len(t, r.provider.inputs, asked, "not asked")

	// the other pair, of its own day
	_, blocked = r.trader.daily("XAUUSD", st, start.Add(time.Hour))
	require.Empty(t, blocked)
	orders.add("XAUUSD", closedPosition(5, model.SideTypeBuy, start.Add(5*time.Minute), start.Add(6*time.Minute), reporter,
		300)...)
	_, blocked = r.trader.daily("XAUUSD", st, start.Add(time.Hour))
	require.Contains(t, blocked, "the daily gain limit of 250.00 BRL reached: +300.00 BRL on XAUUSD")

	// the next day: free again
	_, blocked = r.trader.daily(testPair, st, start.AddDate(0, 0, 1).Add(time.Second))
	require.Empty(t, blocked)
}

// The results of the day before never block the day: the one of the close
// counts, a position opened the day before and closed today on today.
func TestTrader_DailyLimitsOfTheDayOfTheClose(t *testing.T) {
	r, orders, reporter, start := dailyRunner(t, 0)
	st := r.trader.state.Load()
	orders.add(testPair, closedPosition(1, model.SideTypeBuy, start.Add(-3*time.Hour), start.Add(-time.Second), reporter,
		-500)...)
	day, blocked := r.trader.daily(testPair, st, start.Add(time.Hour))
	require.Empty(t, blocked, "yesterday's loss")
	require.Empty(t, day.Positions)
	require.Equal(t, number(0), day.Result.Realized)

	orders.add(testPair, closedPosition(3, model.SideTypeBuy, start.Add(-time.Hour), start.Add(time.Second), reporter, -150)...)
	day, blocked = r.trader.daily(testPair, st, start.Add(time.Hour))
	require.Contains(t, blocked, "daily loss limit")
	require.Len(t, day.Positions, 1, "opened yesterday, closed today")
}

// The limit reached while a position is held: it is managed as usual, the
// model asked with its management actions, told why the entries are refused.
func TestTrader_DailyLimitKeepsManagingThePosition(t *testing.T) {
	r, orders, reporter, start := dailyRunner(t, 1)
	orders.add(testPair, closedPosition(1, model.SideTypeBuy, start.Add(time.Minute), start.Add(2*time.Minute), reporter,
		-200)...)
	asked := len(r.provider.inputs)
	r.step(100, decide(ActionExit, 0.8, nil, nil, nil))
	require.Len(t, r.provider.inputs, asked+1, "asked about the position")
	in := r.provider.last()
	require.Equal(t, []Action{ActionHold, ActionExit}, in.Rules.Actions)
	require.Contains(t, in.Rules.EntriesBlockedByDailyLimit, "daily loss limit")
	require.True(t, in.Daily.Limits.EntriesBlocked)
	require.Equal(t, OutcomeApplied, r.lastDecision().Outcome)
	asset, _, err := r.wallet.Position(testPair)
	require.NoError(t, err)
	require.Zero(t, asset, "closed by the EXIT")
}

// A limit reached while the model is asked: the entry it answers is refused,
// whatever the decision.
func TestTrader_DailyLimitRefusesTheEntryOfTheModel(t *testing.T) {
	r, orders, reporter, start := dailyRunner(t, 0)
	r.provider.asked = func() {
		// a position closed at its stop loss meanwhile
		orders.add(testPair, closedPosition(1, model.SideTypeBuy, start.Add(time.Minute), start.Add(2*time.Minute), reporter,
			-160)...)
	}
	r.step(100, enterLong(100))
	require.Equal(t, OutcomeRefused, r.lastDecision().Outcome)
	asset, _, err := r.wallet.Position(testPair)
	require.NoError(t, err)
	require.Zero(t, asset, "no entry")
}

// Without the results the broker books, eg: a backtest, the daily limits
// can't be applied: the day is sent, its results unknown.
func TestTrader_DailyLimitsWithoutTheBroker(t *testing.T) {
	r, orders, reporter, start := dailyRunner(t, 0)
	r.trader.SetAccountReporter(nil)
	orders.add(testPair, closedPosition(1, model.SideTypeBuy, start.Add(time.Minute), start.Add(2*time.Minute), reporter,
		-500)...)
	day, blocked := r.trader.daily(testPair, r.trader.state.Load(), start.Add(time.Hour))
	require.Empty(t, blocked)
	require.Nil(t, day.Limits)
	require.Len(t, day.Positions, 1)
	require.True(t, strings.Contains(mustJSON(t, day), `"result":null`), "the results unknown")
}

// The account in another currency than the limits: the entries refused, as
// the limits can't be checked.
func TestTrader_DailyLimitsInAnotherCurrency(t *testing.T) {
	r, _, reporter, start := dailyRunner(t, 0)
	reporter.currency = "USD"
	r.trader.currency = ""
	_, blocked := r.trader.daily(testPair, r.trader.state.Load(), start.Add(time.Hour))
	require.Equal(t, "the account is in USD, the daily limits in BRL", blocked)
}

// The operational day: from the midnight of the zone of the day, eg: of
// America/Belem, three hours behind UTC.
func TestTrader_DayOf(t *testing.T) {
	belem, err := time.LoadLocation("America/Belem")
	require.NoError(t, err)
	trader := &Trader{cfg: Config{DayZone: belem}}
	start, end := trader.dayOf(time.Date(2026, 9, 30, 2, 30, 0, 0, time.UTC))
	require.Equal(t, time.Date(2026, 9, 29, 0, 0, 0, 0, belem), start, "23:30 of the day before in Belém")
	require.Equal(t, time.Date(2026, 9, 30, 0, 0, 0, 0, belem), end)
	start, _ = trader.dayOf(time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC))
	require.Equal(t, time.Date(2026, 9, 30, 0, 0, 0, 0, belem), start, "the midnight of Belém")
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return string(data)
}
