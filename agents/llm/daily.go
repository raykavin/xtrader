package llm

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/tools/log"
)

// maxDailyOrders bounds the orders of a day sent in the context: the last ones.
const maxDailyOrders = 50

// currencyRegexp matches the code of a currency, eg: BRL.
var currencyRegexp = regexp.MustCompile(`^[A-Z]{3}$`)

// DailyHistory is the operational day of the symbol so far, see
// Config.DailyGain: its positions and its orders, told apart, and its result.
// The results are the ones of the positions, each counted once: an order
// carries none.
type DailyHistory struct {
	Day       string          `json:"day"`                // eg: 2026-09-30
	TimeZone  string          `json:"time_zone"`          // of the day, eg: America/Belem
	Currency  string          `json:"currency,omitempty"` // of the account: of the results and of the limits
	Positions []DailyPosition `json:"positions"`
	Orders    []DailyOrder    `json:"orders"`
	Result    DailyResult     `json:"result"`
	Limits    *DailyLimits    `json:"limits,omitempty"`
}

// DailyPosition is a position of the symbol open during the day, oldest
// first: closed during it, its Result booked by the broker in the currency of
// the account, costs included (swap, commission and fee), null while unknown;
// or open, its UnrealizedResult as the broker shows it, null when unknown,
// which counts once it closes.
type DailyPosition struct {
	Side             string     `json:"side"`
	Status           string     `json:"status"` // closed or open
	Size             number     `json:"size"`
	Entry            number     `json:"entry_price"`
	Exit             number     `json:"exit_price"`
	ExitBy           string     `json:"exit_by,omitempty"`
	OpenedAt         time.Time  `json:"opened_at"`
	ClosedAt         *time.Time `json:"closed_at"`
	Result           number     `json:"result"`
	UnrealizedResult number     `json:"unrealized_result"`
}

// DailyOrder is an order of the symbol created or updated during the day,
// oldest first: its role (entry or exit, a fill opening or closing a position;
// stop_loss or take_profit, a leg of one; other), its type, and its status, eg:
// FILLED, NEW, CANCELED, EXPIRED or REJECTED.
type DailyOrder struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"`
	Side      string    `json:"side"`
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	Price     number    `json:"price"`
	Quantity  number    `json:"quantity"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DailyResult is the result of the day in the currency of the account:
// Realized, of the positions closed during it, costs included; Unrealized, of
// the position open, null without one, counted once it closes; and
// PendingResults, the closes whose result the broker hasn't booked yet.
type DailyResult struct {
	Realized       number `json:"realized"`
	Unrealized     number `json:"unrealized"`
	PendingResults int    `json:"pending_results,omitempty"`
}

// DailyLimits are the daily limits of the symbol on the realized result of the
// day, not the take profit nor the stop loss of a position: Gain and Loss,
// null for none, and whether the new entries are refused until the next
// operational day, and why.
type DailyLimits struct {
	Gain           number `json:"gain"`
	Loss           number `json:"loss"`
	EntriesBlocked bool   `json:"entries_blocked"`
	Reason         string `json:"reason,omitempty"`
}

// dailyLimits are the daily limits of a prompt, see Config.DailyGain.
type dailyLimits struct {
	gain, loss float64
	currency   string
}

// set reports whether there is a limit.
func (l dailyLimits) set() bool {
	return l.gain > 0 || l.loss > 0
}

// StrategyDailyLimits returns the daily limits the header of strategy
// declares, eg: "# Daily gain limit: 250 BRL" and "# Daily loss limit: 150
// BRL", for Config.DailyGain, Config.DailyLoss and Config.DailyCurrency: the
// gain and the loss of the realized result of each symbol in an operational
// day, 0 for none, in currency, the one of the account.
func StrategyDailyLimits(strategy string) (gain, loss float64, currency string, err error) {
	read := func(key string) (float64, string, error) {
		value, ok := headerValue(strategy, key)
		if !ok {
			return 0, "", nil
		}
		value = strings.TrimSpace(value)
		fields := strings.Fields(value)
		if len(fields) != 2 {
			return 0, "", fmt.Errorf("# %s %q: an amount and the currency of the account, eg: 250 BRL", key, value)
		}
		text := fields[0]
		if !strings.Contains(text, ".") {
			text = strings.Replace(text, ",", ".", 1) // eg: 250,00
		}
		amount, err := strconv.ParseFloat(text, 64)
		if err != nil || !(amount > 0) || math.IsInf(amount, 0) {
			return 0, "", fmt.Errorf("# %s %q: the amount must be a positive number, eg: 250 BRL", key, value)
		}
		code := strings.ToUpper(fields[1])
		if !currencyRegexp.MatchString(code) {
			return 0, "", fmt.Errorf("# %s %q: the currency is a code of 3 letters, eg: BRL", key, value)
		}
		return amount, code, nil
	}
	gain, gainCurrency, err := read("Daily gain limit:")
	if err != nil {
		return 0, 0, "", err
	}
	loss, lossCurrency, err := read("Daily loss limit:")
	if err != nil {
		return 0, 0, "", err
	}
	if gainCurrency != "" && lossCurrency != "" && gainCurrency != lossCurrency {
		return 0, 0, "", fmt.Errorf("the daily limits in %s and in %s: both in the currency of the account",
			gainCurrency, lossCurrency)
	}
	currency = gainCurrency
	if currency == "" {
		currency = lossCurrency
	}
	return gain, loss, currency, nil
}

// openPositions is an AccountReporter telling the position open of a pair, eg:
// the MT5 adapter: its profit in the currency of the account.
type openPositions interface {
	OpenPosition(pair string) (exchange.MT5OpenPosition, bool)
}

// SetAccountReporter sets where the Trader reads the results of the trades of
// the bot as the broker books them, in the currency of the account, eg: the
// MT5 adapter, before the bot runs: the daily limits of the prompts (see
// Config.DailyGain) are applied with it only, and the results of the positions
// of the day sent in the context (see DailyHistory).
func (t *Trader) SetAccountReporter(reporter service.AccountReporter) {
	t.reporter = reporter
}

// accountCurrency returns the currency of the account, read once, empty while
// unknown.
func (t *Trader) accountCurrency() string {
	t.currencyMtx.Lock()
	defer t.currencyMtx.Unlock()
	if t.currency == "" && t.reporter != nil {
		if account, ok := t.reporter.AccountSummary(); ok {
			t.currency = strings.ToUpper(strings.TrimSpace(account.Currency))
		}
	}
	return t.currency
}

// checkDailyCurrency fails on daily limits in a currency other than the one
// of the account, when known.
func (t *Trader) checkDailyCurrency(limits dailyLimits) error {
	if !limits.set() || t.reporter == nil {
		return nil
	}
	if currency := t.accountCurrency(); currency != "" && currency != limits.currency {
		return fmt.Errorf("the daily limits are in %s, the account in %s", limits.currency, currency)
	}
	return nil
}

// dayOf returns the start and the end of the operational day of at: from the
// midnight of Config.DayZone, time.Local when unset.
func (t *Trader) dayOf(at time.Time) (start, end time.Time) {
	zone := t.cfg.DayZone
	if zone == nil {
		zone = time.Local
	}
	local := at.In(zone)
	start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone)
	return start, start.AddDate(0, 0, 1)
}

// dayClock returns the time of the analysis of df for its operational day: now,
// live, else the close of its last candle, eg: in a backtest.
func (t *Trader) dayClock(df *model.Dataframe) time.Time {
	if t.cfg.Live || len(df.Time) == 0 {
		return t.now()
	}
	return df.Time[len(df.Time)-1].Add(t.period)
}

// daily returns the operational day of pair at at, its positions, its orders
// and its result (nil without the orders of the bot, see SetOrderSource), and
// why new entries on pair are refused, empty when they aren't: a daily limit
// of st reached, a close of the day the broker hasn't booked yet, or the
// currency of the account unknown or not the one of the limits. The limits
// are applied with the results the broker books only, see SetAccountReporter.
func (t *Trader) daily(pair string, st *promptState, at time.Time) (*DailyHistory, string) {
	limits := st.daily
	enforced := limits.set() && t.reporter != nil
	if limits.set() && t.reporter == nil && t.dailyWarned != st.ref {
		t.dailyWarned = st.ref
		log.Warnf("[LLM] the daily limits of the prompt %s need the results the broker books (a live agent on MT5): "+
			"not applied", describePrompt(st.ref))
	}
	if t.orders == nil {
		if enforced {
			return nil, "the orders of the bot are unknown: the daily limits can't be checked"
		}
		return nil, ""
	}
	orders, err := t.orders(pair)
	if err != nil {
		log.Warnf("[LLM] %s | the stored orders, for the day: %v", pair, err)
		if enforced {
			return nil, "the orders of the bot can't be read: the daily limits can't be checked"
		}
		return nil, ""
	}

	start, end := t.dayOf(at)
	nan := number(math.NaN())
	history := &DailyHistory{Day: start.Format(time.DateOnly), TimeZone: start.Location().String(),
		Positions: []DailyPosition{}, Orders: []DailyOrder{}, Result: DailyResult{Unrealized: nan}}
	if t.reporter != nil {
		history.Currency = t.accountCurrency()
	}
	today := func(at time.Time) bool { return !at.Before(start) && at.Before(end) }

	closed, open, roles := tradesOf(orders)
	realized := 0.0
	var pendingAt time.Time
	for _, trade := range closed {
		if !today(trade.closed) {
			continue
		}
		closedAt := trade.closed.UTC()
		position := DailyPosition{Side: describeSide(trade.side), Status: "closed", Size: number(trade.size),
			Entry: number(trade.entry), Exit: number(trade.exit), ExitBy: trade.exitBy,
			OpenedAt: trade.opened.UTC(), ClosedAt: &closedAt, Result: nan, UnrealizedResult: nan}
		if t.reporter != nil {
			if profit, ok := t.reporter.ClosedProfit(trade.close, trade.open); ok {
				position.Result = number(profit)
				realized += profit
			} else {
				history.Result.PendingResults++
				pendingAt = trade.closed
			}
		}
		history.Positions = append(history.Positions, position)
	}
	if open != nil {
		position := DailyPosition{Side: describeSide(open.side), Status: "open", Size: number(open.size),
			Entry: number(open.entry), Exit: nan, OpenedAt: open.opened.UTC(), Result: nan, UnrealizedResult: nan}
		if book, ok := t.reporter.(openPositions); ok {
			if held, found := book.OpenPosition(pair); found {
				position.UnrealizedResult = number(held.Profit)
				history.Result.Unrealized = number(held.Profit)
			}
		}
		history.Positions = append(history.Positions, position)
	}
	if t.reporter != nil {
		history.Result.Realized = number(cents(realized))
	} else {
		history.Result.Realized = nan // unknown in the currency of the account
	}

	var dayOrders []model.Order
	for _, order := range orders {
		if today(order.CreatedAt) || today(order.UpdatedAt) {
			dayOrders = append(dayOrders, order)
		}
	}
	sort.SliceStable(dayOrders, func(i, j int) bool {
		if a, b := dayOrders[i].UpdatedAt, dayOrders[j].UpdatedAt; !a.Equal(b) {
			return a.Before(b)
		}
		return dayOrders[i].ID < dayOrders[j].ID
	})
	if len(dayOrders) > maxDailyOrders {
		dayOrders = dayOrders[len(dayOrders)-maxDailyOrders:]
	}
	for _, order := range dayOrders {
		history.Orders = append(history.Orders, DailyOrder{ID: order.ID, Role: orderRole(order, roles),
			Side: string(order.Side), Type: string(order.Type), Status: string(order.Status),
			Price: number(order.Price), Quantity: number(order.Quantity), CreatedAt: order.CreatedAt.UTC(),
			UpdatedAt: order.UpdatedAt.UTC()})
	}

	if !enforced {
		return history, ""
	}
	history.Limits = &DailyLimits{Gain: nan, Loss: nan}
	if limits.gain > 0 {
		history.Limits.Gain = number(limits.gain)
	}
	if limits.loss > 0 {
		history.Limits.Loss = number(limits.loss)
	}
	why := ""
	result := cents(realized)
	switch currency := history.Currency; {
	case currency == "":
		why = "the currency of the account is unknown: the daily limits can't be checked"
	case currency != limits.currency:
		why = fmt.Sprintf("the account is in %s, the daily limits in %s", currency, limits.currency)
	case history.Result.PendingResults > 0:
		why = fmt.Sprintf("the result of the close of %s isn't booked by the broker yet",
			pendingAt.In(start.Location()).Format("15:04:05"))
	case limits.gain > 0 && result >= limits.gain:
		why = fmt.Sprintf("the daily gain limit of %.2f %s reached: %+.2f %s on %s today", limits.gain,
			limits.currency, result, limits.currency, pair)
	case limits.loss > 0 && result <= -limits.loss:
		why = fmt.Sprintf("the daily loss limit of %.2f %s reached: %+.2f %s on %s today", limits.loss,
			limits.currency, result, limits.currency, pair)
	}
	history.Limits.EntriesBlocked, history.Limits.Reason = why != "", why
	return history, why
}

// orderRole names the role of order in the day, see DailyOrder: the legs by
// their type, the fills by roles (see tradesOf), the other orders apart.
func orderRole(order model.Order, roles map[int64]string) string {
	switch {
	case order.GroupID != nil && (order.Type == model.OrderTypeStopLoss || order.Type == model.OrderTypeStopLossLimit):
		return "stop_loss"
	case order.GroupID != nil:
		return "take_profit"
	case roles[order.ID] != "":
		return roles[order.ID]
	}
	return "other"
}

// cents rounds value to the cents: the results compared with the limits.
func cents(value float64) float64 {
	return math.Round(value*100) / 100
}
