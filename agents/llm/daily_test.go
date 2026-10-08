package llm

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
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

// linkedReporter is a fakeReporter telling the positions of the fills by their
// ID too, as the MT5 adapter tells them by their tickets (see fillPositions).
type linkedReporter struct {
	*fakeReporter
	links map[int64][2][]int64 // the position opened (none or one), and the ones closed
}

func (l *linkedReporter) FillPositions(fill model.Order) (int64, []int64, bool) {
	positions, ok := l.links[fill.ID]
	var opened int64
	if len(positions[0]) > 0 {
		opened = positions[0][0]
	}
	return opened, positions[1], ok
}

// B6: a fill of yesterday closing a position whose entry isn't among the
// orders of the bot (a sell) never inverts the day: the long of today closed
// at its take profit is the position of the day, its result booked, nothing
// pending nor open, the entries free; the fill left out is logged once.
func TestTrader_DailyOrphanFillDoesNotInvertTheDay(t *testing.T) {
	r, orders, reporter, start := dailyRunner(t, 0)
	linked := &linkedReporter{fakeReporter: reporter, links: map[int64][2][]int64{
		1:  {nil, {5}},  // closes the position 5, opened by no order of the bot
		10: {{10}, nil}, // the long of today
		12: {nil, {10}}, // its take profit
	}}
	r.trader.SetAccountReporter(linked)
	at := func(minutes int) time.Time { return start.Add(time.Duration(minutes) * time.Minute) }
	orders.add(testPair, filled(1, model.SideTypeSell, 95, 1, at(-600)),
		filled(10, model.SideTypeBuy, 100, 1, at(10)),
		legOf(11, 10, model.SideTypeSell, model.OrderTypeStopLoss, model.OrderStatusTypeCanceled, 98, 1, at(40)),
		legOf(12, 10, model.SideTypeSell, model.OrderTypeLimitMaker, model.OrderStatusTypeFilled, 104, 1, at(40)))
	reporter.profits[12] = 40

	day, blocked := r.trader.daily(testPair, r.trader.state.Load(), at(60))
	require.Empty(t, blocked)
	require.Len(t, day.Positions, 1)
	require.Equal(t, "LONG", day.Positions[0].Side)
	require.Equal(t, "closed", day.Positions[0].Status)
	require.Equal(t, number(100), day.Positions[0].Entry)
	require.Equal(t, number(104), day.Positions[0].Exit)
	require.Equal(t, number(40), day.Positions[0].Result)
	require.Zero(t, day.Result.PendingResults)
	require.Equal(t, number(40), day.Result.Realized)
	require.True(t, math.IsNaN(float64(day.Result.Unrealized)), "no position open")
	require.True(t, r.trader.orphans[1], "the fill left out, logged")
}

// B6: the result of a close of the day the broker doesn't book holds the
// entries of the pair, as documented, but past pendingResultLate it is told,
// once: logged as an error and notified, the skip saying how late it is; then
// told again once booked, the entries checked against the limits again.
func TestTrader_DailyPendingResultLate(t *testing.T) {
	r, orders, reporter, start := dailyRunner(t, 0)
	messages := &[]string{}
	r.trader.SetNotifier(func(message string) { *messages = append(*messages, message) })
	st := r.trader.state.Load()
	at := func(minutes int) time.Time { return start.Add(time.Duration(minutes) * time.Minute) }
	orders.add(testPair, closedPosition(1, model.SideTypeBuy, at(5), at(10), reporter, -20)...)
	delete(reporter.profits, 2) // unbooked

	_, blocked := r.trader.daily(testPair, st, at(15))
	require.Contains(t, blocked, "isn't booked by the broker yet")
	require.Empty(t, *messages, "not late yet")

	late := at(10).Add(pendingResultLate)
	_, blocked = r.trader.daily(testPair, st, late)
	require.Contains(t, blocked, "isn't booked by the broker yet, 10 min later: the entries wait for it")
	require.Len(t, *messages, 1)
	require.Contains(t, (*messages)[0], "the result of the close of")
	require.Contains(t, (*messages)[0], "(order 2) isn't booked by the broker yet, 10 min later")

	// the next analyses: still blocked, told once
	asked := len(r.provider.inputs)
	r.trader.now = func() time.Time { return late.Add(time.Hour) }
	r.step(100)
	require.Len(t, r.provider.inputs, asked, "the entries still wait: not asked")
	_, blocked = r.trader.daily(testPair, st, late.Add(2*time.Hour))
	require.Contains(t, blocked, "isn't booked by the broker yet, 130 min later")
	require.Len(t, *messages, 1, "told once")

	// booked at last: told, the entries free under the limits
	reporter.profits[2] = -20
	day, blocked := r.trader.daily(testPair, st, late.Add(2*time.Hour))
	require.Empty(t, blocked)
	require.Equal(t, number(-20), day.Result.Realized)
	require.Len(t, *messages, 2)
	require.Contains(t, (*messages)[1], "(order 2) is booked by the broker")
	_, _ = r.trader.daily(testPair, st, late.Add(2*time.Hour))
	require.Len(t, *messages, 2, "told once")
}

// B21: the daily limits are read in any case, the spaces around the key
// tolerated, in the whole header, blank lines included; a line that reads like
// a daily limit but isn't one read, eg: of another key, without a colon,
// twice, or in the text after the header, refuses the prompt rather than
// leaving the limit off.
func TestStrategyDailyLimits_HeaderLines(t *testing.T) {
	for header, want := range map[string][2]float64{
		"# daily loss limit: 150 BRL":                                            {0, 150},
		"# Daily loss limit : 150 BRL":                                           {0, 150},
		"#Daily  Gain   LIMIT:250 BRL":                                           {250, 0},
		"## Daily loss limit: 150 BRL":                                           {0, 150},
		"# t: x\n\n# Daily loss limit: 150 BRL":                                  {0, 150},
		"# t: x\n# Daily gain limit: 250 BRL\n\n  \n# Daily loss limit: 150 BRL": {250, 150},
		"# t: x\n\n# Flags: y\n# Daily loss limit: 150 BRL":                      {0, 150},
		"# The daily limits of each pair, in the currency of the account":        {0, 0},
	} {
		gain, loss, _, err := StrategyDailyLimits(header + "\n\nText.")
		require.NoError(t, err, header)
		require.Equal(t, want, [2]float64{gain, loss}, header)
	}
	sent := systemPrompt("# t: x\n\n# Daily loss limit: 150 BRL\n\nText.")
	require.NotContains(t, sent, "Daily loss limit", "the header, blank lines included, isn't sent")
	require.True(t, strings.HasSuffix(sent, "Strategy:\nText."))

	for strategy, want := range map[string]string{
		"# Daily loss limits: 150 BRL\n\nText.":                             "isn't a daily limit the agent reads",
		"# Limite diário de perda: 150 BRL\n\nText.":                        "isn't a daily limit the agent reads",
		"# Daily loss limit 150 BRL\n\nText.":                               "isn't a daily limit the agent reads",
		"# Max daily loss limit = 150 BRL\n\nText.":                         "isn't a daily limit the agent reads",
		"# Daily loss limit: 150 BRL\n# daily loss limit: 100 BRL\n\nText.": "the daily loss limit is given twice",
		"# t: x\n\n# Daily loss limits: 150 BRL\n\nText.":                   "isn't a daily limit the agent reads",
		"# t: x\n## Limite diário: 3 operações\n\nText.":                    "in the header, it isn't sent to the model",
	} {
		_, _, _, err := StrategyDailyLimits(strategy)
		require.ErrorContains(t, err, want, strategy)
	}

	// refused as a prompt of the Trader: the previous one kept
	r, _, _, _ := dailyRunner(t, 0)
	_, err := r.trader.promptState(Prompt{Content: "# t: x\n# daily loss limits: 150 BRL\n\nText."})
	require.ErrorContains(t, err, `the line "# daily loss limits: 150 BRL" isn't a daily limit the agent reads`)
}

// B22: the prompt the Trader starts with, made before the account is known,
// is checked against it once SetAccountReporter is set: limits in another
// currency refuse it. Unknown then, it is told refused once the currency is
// known (logged and reported to the source of the prompts), its entries
// refused meanwhile.
func TestTrader_CheckPromptAccount(t *testing.T) {
	usd := promptOf(7, "usd", 2, "# usd: x\n# Daily loss limit: 150 USD\n\nText.")
	r, prompts := promptRunner(t, func(c *Config) { c.Live = true }, usd, PromptOverrides{})
	require.NoError(t, r.trader.CheckPromptAccount(), "no account yet")

	reporter := &fakeReporter{currency: "BRL", profits: map[int64]float64{}, open: map[string]exchange.MT5OpenPosition{}}
	r.trader.SetAccountReporter(reporter)
	err := r.trader.CheckPromptAccount()
	require.ErrorContains(t, err, "the prompt usd v2: the daily limits are in USD, the account in BRL")

	brl := promptOf(8, "brl", 1, "# brl: x\n# Daily loss limit: 150 BRL\n\nText.")
	ok, _ := promptRunner(t, func(c *Config) { c.Live = true }, brl, PromptOverrides{})
	ok.trader.SetAccountReporter(reporter)
	require.NoError(t, ok.trader.CheckPromptAccount())

	// the currency of the account unknown at the start: told once known
	unknown := &fakeReporter{profits: map[int64]float64{}, open: map[string]exchange.MT5OpenPosition{}}
	r.trader.SetAccountReporter(unknown)
	require.NoError(t, r.trader.CheckPromptAccount(), "the currency unknown")
	r.trader.SetOrderSource(func(string) ([]model.Order, error) { return nil, nil })
	st := r.trader.state.Load()
	_, blocked := r.trader.daily(testPair, st, r.trader.now())
	require.Equal(t, "the currency of the account is unknown: the daily limits can't be checked", blocked)
	require.Empty(t, prompts.reports)

	unknown.currency = "BRL"
	_, blocked = r.trader.daily(testPair, st, r.trader.now())
	require.Equal(t, "the account is in BRL, the daily limits in USD", blocked)
	require.Len(t, prompts.reports, 1)
	require.Equal(t, usd.Ref, prompts.reports[0].ref)
	require.ErrorContains(t, prompts.reports[0].err, "the daily limits are in USD, the account in BRL")
	_, _ = r.trader.daily(testPair, st, r.trader.now())
	require.Len(t, prompts.reports, 1, "told once")
}

// B36: the operational day is the one of the zone of the agent by its name,
// eg: America/Belem rather than Local: of TZ, a name or the path of its file,
// else of the link of the zone of the system; an empty or unknown TZ is UTC,
// as for time.Local.
func TestZoneOf(t *testing.T) {
	env := func(tz string, set bool) func(string) (string, bool) {
		return func(name string) (string, bool) {
			if name != "TZ" {
				return "", false
			}
			return tz, set
		}
	}
	dir := t.TempDir()
	zoneinfo := filepath.Join(dir, "zoneinfo", "America")
	require.NoError(t, os.MkdirAll(zoneinfo, 0o755))
	belemFile := filepath.Join(zoneinfo, "Belem")
	require.NoError(t, os.WriteFile(belemFile, []byte("TZif"), 0o644))
	link := filepath.Join(dir, "localtime")
	require.NoError(t, os.Symlink(belemFile, link))
	plain := filepath.Join(dir, "plain")
	require.NoError(t, os.WriteFile(plain, []byte("TZif"), 0o644))

	for name, test := range map[string]struct {
		lookup    func(string) (string, bool)
		localtime string
		want      string
	}{
		"a name":                        {env("America/Belem", true), link, "America/Belem"},
		"a name with a colon":           {env(":America/Belem", true), link, "America/Belem"},
		"the path of its file":          {env("/usr/share/zoneinfo/America/Belem", true), link, "America/Belem"},
		"empty":                         {env("", true), link, "UTC"},
		"unknown":                       {env("Nowhere/Land", true), link, "UTC"},
		"unset, the link of the system": {env("", false), link, "America/Belem"},
		"unset, not a link":             {env("", false), plain, time.Local.String()},
		"unset, none":                   {env("", false), filepath.Join(dir, "missing"), time.Local.String()},
	} {
		require.Equal(t, test.want, zoneOf(test.lookup, test.localtime).String(), name)
	}
	belem := zoneOf(env("America/Belem", true), link)
	at := time.Date(2026, 9, 30, 2, 30, 0, 0, time.UTC)
	require.Equal(t, "2026-09-29T23:30:00-03:00", at.In(belem).Format(time.RFC3339), "the zone itself, not a name only")
}

// B36: the day of the context names the zone of the operational day,
// Config.DayZone or else LocalZone, never "Local".
func TestTrader_DailyTimeZoneNamed(t *testing.T) {
	r, _, _, start := dailyRunner(t, 0)
	belem, err := time.LoadLocation("America/Belem")
	require.NoError(t, err)
	r.trader.cfg.DayZone = belem
	day, _ := r.trader.daily(testPair, r.trader.state.Load(), start)
	require.Equal(t, "America/Belem", day.TimeZone)

	defer func(zone func() *time.Location) { localZone = zone }(localZone)
	localZone = func() *time.Location { return belem }
	r.trader.cfg.DayZone = nil
	day, _ = r.trader.daily(testPair, r.trader.state.Load(), start)
	require.Equal(t, "America/Belem", day.TimeZone, "LocalZone by default")
}

// positionReporter is a linkedReporter booking the results of the closes by
// their fill and the entry of the position closed, as the MT5 adapter: none
// for a position the fill didn't close.
type positionReporter struct {
	*linkedReporter
	results map[[2]int64]float64 // by the IDs of the close and of the entry
}

func (p *positionReporter) ClosedProfit(close, open model.Order) (float64, bool) {
	profit, ok := p.results[[2]int64{close.ID, open.ID}]
	return profit, ok
}

// B6: an exit the broker tells closing a known position and an unknown one
// (see TestTradesOf_LinkedKnownAndUnknownPosition) books the result of the
// known one only: the other position of the bot is open, nothing pending,
// the entries free.
func TestTrader_DailyLinkedKnownAndUnknownPosition(t *testing.T) {
	r, orders, reporter, start := dailyRunner(t, 0)
	at := func(minutes int) time.Time { return start.Add(time.Duration(minutes) * time.Minute) }
	r.trader.SetAccountReporter(&positionReporter{
		linkedReporter: &linkedReporter{fakeReporter: reporter, links: map[int64][2][]int64{
			1: {{11}, nil}, 2: {{33}, nil}, 3: {nil, {11, 99}},
		}},
		results: map[[2]int64]float64{{3, 1}: 20},
	})
	orders.add(testPair, filled(1, model.SideTypeBuy, 100, 1, at(10)), filled(2, model.SideTypeBuy, 101, 1, at(11)),
		filled(3, model.SideTypeSell, 102, 2, at(12)))

	day, blocked := r.trader.daily(testPair, r.trader.state.Load(), at(30))
	require.Empty(t, blocked)
	require.Zero(t, day.Result.PendingResults)
	require.Equal(t, number(20), day.Result.Realized)
	require.Len(t, day.Positions, 2)
	require.Equal(t, [2]string{"closed", "open"}, [2]string{day.Positions[0].Status, day.Positions[1].Status})
	require.Equal(t, number(101), day.Positions[1].Entry, "the position 33")
	require.True(t, r.trader.orphans[3], "the rest of the exit left out, logged")
}

// B21: a title after a blank line of the header with an amount of something
// else than a currency, eg: "2 ATR", or a currency that isn't its amount, eg:
// "max 1 EUR pair", is text for the model, never refusing the prompt: only an
// amount in a currency of ISO 4217 first in the value of the line, or ending
// it, reads like a limit in money and keeps the header, refused.
func TestStrategyDailyLimits_AmountNotInACurrency(t *testing.T) {
	for _, title := range []string{
		"## Limite diário de perda: 2 ATR",
		"## Daily limit: 2 trades per NY session",
		"## Daily limit: 2 trades, max 1 EUR pair",
		"# Daily loss limit for 2 USD pairs",
		"## Limite diário: 3 RSI",
	} {
		require.False(t, dailyMoneyLine(title), title)
		strategy := "# Indicators: atr\n\n" + title + "\nNunca perca mais que 2 ATR no dia."
		_, _, _, err := StrategyDailyLimits(strategy)
		require.NoError(t, err, title)
		require.NoError(t, CheckPrompt(strategy), title)
		require.Contains(t, systemPrompt(strategy), "Strategy:\n"+title+"\n", "sent to the model")
	}
	for _, line := range []string{
		"# Daily loss limits: 150 BRL",
		"# Limite diário de perda: 150 BRL por dia",
		"# Daily loss limit 150 BRL",
		"# Max daily loss limit = 150 BRL",
		"# Limite diário: 1.000,50 USD.",
		"# Daily loss limits: 150 USC",
	} {
		require.True(t, dailyMoneyLine(line), line)
		_, _, _, err := StrategyDailyLimits("# Indicators: atr\n\n" + line + "\n\nText.")
		require.ErrorContains(t, err, "isn't a daily limit the agent reads", line)
	}
}

// B21: only the header is read, and refused: a title of the text after it,
// eg: after a blank line, or after the first line of the strategy, is text
// for the model, never refusing the prompt (the one an agent starts with
// would stop it), a limit there told in the log only.
func TestStrategyDailyLimits_TitlesOfTheText(t *testing.T) {
	for strategy, title := range map[string]string{
		"# Indicators: atr\n\n## Limite diário: 3 operações\nText.":       "## Limite diário: 3 operações",
		"# Indicators: atr\n\n# Daily limit: 2 trades\n\nText.":           "# Daily limit: 2 trades",
		"# Indicators: atr\n\nText.\n# Daily limit: 2 trades":             "# Daily limit: 2 trades",
		"# Indicators: atr\n\n## Seção\nText.":                            "## Seção",
		"# Indicators: atr\n\nText.\n\n## Limite diário: 3 operações\nOk": "## Limite diário: 3 operações",
		"# t: x\n\nText.\n# Daily loss limit: 150 BRL":                    "# Daily loss limit: 150 BRL",
		"# t: x\n\nText.\n\n# Daily loss limits: 150 BRL":                 "# Daily loss limits: 150 BRL",
	} {
		gain, loss, _, err := StrategyDailyLimits(strategy)
		require.NoError(t, err, strategy)
		require.Zero(t, gain+loss, strategy)
		require.NoError(t, CheckPrompt(strategy), strategy)
		sent := strings.TrimPrefix(systemPrompt(strategy), basePrompt+"\n\nStrategy:\n")
		require.Contains(t, strings.Split(sent, "\n"), title, "a line sent to the model: %s", strategy)
		groups, ok := StrategyIndicators(strategy)
		if strings.HasPrefix(strategy, "# Indicators:") {
			require.True(t, ok, strategy)
			require.Equal(t, []string{"atr"}, groups, strategy)
		}
	}
	require.Equal(t, "## Seção\nText.", stripComments("# Indicators: atr\n\n## Seção\nText."))

	require.Empty(t, textDailyLines("# Indicators: atr\n\n## Limite diário: 3 operações\nText."), "no amount")
	require.Equal(t, []string{"# Daily loss limit: 150 BRL"},
		textDailyLines("# t: x\n\nText.\n# Daily loss limit: 150 BRL"), "the limit in the text, logged")

	// applied as a prompt of the Trader
	r, _, _, _ := dailyRunner(t, 0)
	st, err := r.trader.promptState(Prompt{Content: "# Indicators: atr\n\n## Limite diário: 3 operações\nText."})
	require.NoError(t, err)
	require.Contains(t, st.system, "## Limite diário: 3 operações\nText.")
}

// B21: an amount of a daily limit whose dot or comma may group the thousands
// or be the decimal one, eg: 1.000 (a thousand on the panel, see parseAmount
// of web/src/lib/periods.ts, but 1 for ParseFloat), is refused, the formats
// that can't be misread taken.
func TestStrategyDailyLimits_AmbiguousAmount(t *testing.T) {
	for _, amount := range []string{"1.000", "1.500", "1,500", "1,000", "0.500", "1.000.000", "1.000,00",
		"1,000.50", "12.345"} {
		strategy := "# Daily gain limit: " + amount + " BRL\n\nText."
		_, _, _, err := StrategyDailyLimits(strategy)
		require.ErrorContains(t, err, "the amount "+amount+" is ambiguous", amount)
		require.ErrorContains(t, err, "eg: 1000 or 1000.00", amount)
	}
	for amount, want := range map[string]float64{"1000": 1000, "1000.00": 1000, "1000,00": 1000, "1.5": 1.5,
		"1,50": 1.5, "250,5": 250.5, "0.5": 0.5, "1500.0000": 1500} {
		gain, _, _, err := StrategyDailyLimits("# Daily gain limit: " + amount + " BRL\n\nText.")
		require.NoError(t, err, amount)
		require.Equal(t, want, gain, amount)
	}

	// refused as a prompt of the Trader, rather than a limit of 1 BRL
	r, _, _, _ := dailyRunner(t, 0)
	_, err := r.trader.promptState(Prompt{Content: "# Daily loss limit: 1.500 BRL\n\nText."})
	require.ErrorContains(t, err, "the amount 1.500 is ambiguous")
}

// awaitingReporter is a fakeReporter telling the positions closed at the
// broker whose exits it hasn't booked yet, as the MT5 adapter with its
// history lagging (see exitsAwaited).
type awaitingReporter struct {
	*fakeReporter
	since   time.Time
	awaited bool
}

func (a *awaitingReporter) ExitsAwaited(string) (time.Time, bool) { return a.since, a.awaited }

// B6: a position closed by its stop loss while the history of the broker
// lags: its legs open still, the entries wait for its result, told once late;
// then the stop loss filled with its result booked, the loss counts, the daily
// loss limit reached.
func TestTrader_DailyStopLossHitHistoryLate(t *testing.T) {
	r, orders, reporter, start := dailyRunner(t, 0)
	messages := &[]string{}
	r.trader.SetNotifier(func(message string) { *messages = append(*messages, message) })
	awaiting := &awaitingReporter{fakeReporter: reporter}
	r.trader.SetAccountReporter(awaiting)
	st := r.trader.state.Load()
	at := func(minutes int) time.Time { return start.Add(time.Duration(minutes) * time.Minute) }
	orders.add(testPair, filled(1, model.SideTypeBuy, 100, 1, at(10)),
		legOf(2, 1, model.SideTypeSell, model.OrderTypeStopLoss, model.OrderStatusTypeNew, 98, 1, at(10)),
		legOf(3, 1, model.SideTypeSell, model.OrderTypeLimitMaker, model.OrderStatusTypeNew, 104, 1, at(10)))

	day, blocked := r.trader.daily(testPair, st, at(15))
	require.Empty(t, blocked, "open, nothing awaited")
	require.Equal(t, "open", day.Positions[0].Status)

	// closed by its stop loss at the broker, its exit not in the history
	awaiting.since, awaiting.awaited = at(20), true
	day, blocked = r.trader.daily(testPair, st, at(22))
	require.Contains(t, blocked, "closed at the broker (noticed at 00:20:00), its result isn't booked yet")
	require.True(t, day.Limits.EntriesBlocked)
	require.Empty(t, *messages, "not late yet")
	late := at(20).Add(pendingResultLate)
	_, blocked = r.trader.daily(testPair, st, late)
	require.Contains(t, blocked, "its result isn't booked yet, 10 min later: the entries wait for it")
	require.Len(t, *messages, 1)
	require.Contains(t, (*messages)[0], "has no exit in its history yet, 10 min later")
	_, _ = r.trader.daily(testPair, st, late.Add(time.Minute))
	require.Len(t, *messages, 1, "told once")
	asked := len(r.provider.inputs)
	r.trader.now = func() time.Time { return late.Add(time.Minute) }
	r.step(100)
	require.Len(t, r.provider.inputs, asked, "the entries wait: not asked")

	// a close of yesterday awaited holds nothing today
	awaiting.since = start.Add(-time.Minute)
	_, blocked = r.trader.daily(testPair, st, at(22))
	require.Empty(t, blocked)

	// the history lists the exit: the stop loss filled, its loss booked
	awaiting.awaited = false
	orders[testPair][1].Status, orders[testPair][1].UpdatedAt = model.OrderStatusTypeFilled, at(20)
	orders[testPair][2].Status, orders[testPair][2].UpdatedAt = model.OrderStatusTypeCanceled, at(20)
	reporter.profits[2] = -160
	day, blocked = r.trader.daily(testPair, st, late.Add(2*time.Minute))
	require.Contains(t, blocked, "the daily loss limit of 150.00 BRL reached: -160.00 BRL")
	require.Zero(t, day.Result.PendingResults)
	require.Equal(t, number(-160), day.Result.Realized)
	require.Len(t, day.Positions, 1)
	require.Equal(t, "closed", day.Positions[0].Status)
	require.Equal(t, "stop loss", day.Positions[0].ExitBy)
	require.Len(t, *messages, 2, "its result told booked")
	require.Equal(t, "✅ "+testPair+": the position closed at the broker (noticed at 00:20:00) is in its history, "+
		"its result booked: the new entries on "+testPair+" are checked against the daily limits again.",
		(*messages)[1])
	_, _ = r.trader.daily(testPair, st, late.Add(3*time.Minute))
	require.Len(t, *messages, 2, "told once")
}

// B6: the close at the broker told late (see awaitedWhy), its result told
// once booked, as the README promises ("Quando ele chega, outra notificação
// avisa que as entradas voltaram a ser conferidas pelos limites"): not while
// the orders of the bot hold the position still (its legs not updated yet);
// the result of its stop loss pending then, told once booked as a close told
// late, without another warning.
func TestTrader_DailyAwaitedLateAllClear(t *testing.T) {
	for _, pendingFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending first %v", pendingFirst), func(t *testing.T) {
			r, orders, reporter, start := dailyRunner(t, 0) // a loss of 150 BRL a day: 20 doesn't reach it
			messages := &[]string{}
			r.trader.SetNotifier(func(message string) { *messages = append(*messages, message) })
			awaiting := &awaitingReporter{fakeReporter: reporter}
			r.trader.SetAccountReporter(awaiting)
			st := r.trader.state.Load()
			at := func(minutes int) time.Time { return start.Add(time.Duration(minutes) * time.Minute) }
			orders.add(testPair, filled(1, model.SideTypeBuy, 100, 1, at(10)),
				legOf(2, 1, model.SideTypeSell, model.OrderTypeStopLoss, model.OrderStatusTypeNew, 98, 1, at(10)),
				legOf(3, 1, model.SideTypeSell, model.OrderTypeLimitMaker, model.OrderStatusTypeNew, 104, 1, at(10)))

			awaiting.since, awaiting.awaited = at(20), true
			_, blocked := r.trader.daily(testPair, st, at(31))
			require.Contains(t, blocked, "11 min later")
			require.Len(t, *messages, 1, "told late")
			require.True(t, strings.HasPrefix((*messages)[0], "⚠️"))

			// the exit in the history, the legs not updated yet: not told
			awaiting.awaited = false
			_, _ = r.trader.daily(testPair, st, at(32))
			require.Len(t, *messages, 1, "the orders hold the position still")

			orders[testPair][1].Status, orders[testPair][1].UpdatedAt = model.OrderStatusTypeFilled, at(20)
			orders[testPair][2].Status, orders[testPair][2].UpdatedAt = model.OrderStatusTypeCanceled, at(20)
			if pendingFirst {
				day, blocked := r.trader.daily(testPair, st, at(33))
				require.Equal(t, 1, day.Result.PendingResults)
				require.Contains(t, blocked, "the result of the close of 00:20:00 isn't booked by the broker yet, "+
					"13 min later")
				require.Len(t, *messages, 1, "neither the all clear nor another warning")
			}
			reporter.profits[2] = -20
			day, blocked := r.trader.daily(testPair, st, at(34))
			require.Empty(t, blocked, "the entries free again")
			require.Equal(t, number(-20), day.Result.Realized)
			require.Len(t, *messages, 2)
			require.True(t, strings.HasPrefix((*messages)[1], "✅ "+testPair+": "), (*messages)[1])
			if pendingFirst {
				require.Contains(t, (*messages)[1], "the result of the close of 00:20:00 (order 2) is booked")
			} else {
				require.Contains(t, (*messages)[1], "the position closed at the broker (noticed at 00:20:00) is "+
					"in its history")
			}
			_, _ = r.trader.daily(testPair, st, at(35))
			require.Len(t, *messages, 2, "told once")
			require.Empty(t, r.trader.awaitedLate)
			require.Empty(t, r.trader.overdue)
		})
	}
}
