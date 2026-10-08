package llm

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/tools/log"
)

// maxDailyOrders bounds the orders of a day sent in the context: the last ones.
const maxDailyOrders = 50

// pendingResultLate is how long the result of a close of the day may stay
// unbooked by the broker, the new entries of its pair waiting for it under
// daily limits (see Trader.daily), before it is told: logged as an error and
// notified (see SetNotifier), once per close. The entries keep waiting for it,
// until the next operational day at most: without it, the result of the day
// is unknown.
const pendingResultLate = 10 * time.Minute

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

// The keys of the daily limits in the header of a prompt, see
// StrategyDailyLimits.
const (
	dailyGainKey = "daily gain limit"
	dailyLossKey = "daily loss limit"
)

// StrategyDailyLimits returns the daily limits the header of strategy
// declares, eg: "# Daily gain limit: 250 BRL" and "# Daily loss limit: 150
// BRL" (see promptLines), for Config.DailyGain, Config.DailyLoss and
// Config.DailyCurrency: the gain and the loss of the realized result of each
// symbol in an operational day, 0 for none, in currency, the one of the
// account. A line it wouldn't read fails, rather than leaving a limit off, see
// checkDailyLines.
func StrategyDailyLimits(strategy string) (gain, loss float64, currency string, err error) {
	if err := checkDailyLines(strategy); err != nil {
		return 0, 0, "", err
	}
	read := func(key, name string) (float64, string, error) {
		value, ok := headerValue(strategy, key)
		if !ok {
			return 0, "", nil
		}
		value = strings.TrimSpace(value)
		fields := strings.Fields(value)
		if len(fields) != 2 {
			return 0, "", fmt.Errorf("# %s %q: an amount and the currency of the account, eg: 250 BRL", name, value)
		}
		text := fields[0]
		if groupedRegexp.MatchString(text) {
			return 0, "", fmt.Errorf("# %s %q: the amount %s is ambiguous, its dot or comma may group the thousands "+
				"or be the decimal one: write it without grouping, eg: 1000 or 1000.00, and its decimals with "+
				"one or two digits, eg: 1.5 or 1,50", name, value, text)
		}
		if !strings.Contains(text, ".") {
			text = strings.Replace(text, ",", ".", 1) // eg: 250,00
		}
		amount, err := strconv.ParseFloat(text, 64)
		if err != nil || !(amount > 0) || math.IsInf(amount, 0) {
			return 0, "", fmt.Errorf("# %s %q: the amount must be a positive number, eg: 250 BRL", name, value)
		}
		code := strings.ToUpper(fields[1])
		if !currencyRegexp.MatchString(code) {
			return 0, "", fmt.Errorf("# %s %q: the currency is a code of 3 letters, eg: BRL", name, value)
		}
		return amount, code, nil
	}
	gain, gainCurrency, err := read(dailyGainKey, "Daily gain limit:")
	if err != nil {
		return 0, 0, "", err
	}
	loss, lossCurrency, err := read(dailyLossKey, "Daily loss limit:")
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

// groupedRegexp matches an amount whose dot or comma comes before 3 digits,
// eg: 1.000 or 1,500: grouping the thousands, or the decimal one.
var groupedRegexp = regexp.MustCompile(`[.,]\d{3}([.,]|$)`)

// checkDailyLines fails on a line of the header of strategy (see promptLines)
// that reads like a daily limit, a number with a key naming a day and a limit
// (eg: "# Daily loss limits: 150 BRL", or "# Limite diário: 150 BRL"), unless
// it is one StrategyDailyLimits reads: of its keys, once. A limit the agent
// wouldn't apply, nor send to the model, is refused with the prompt rather
// than left off. The text after the header goes to the model, its lines
// starting with # too, never refused: see textDailyLines.
func checkDailyLines(strategy string) error {
	lines, body := promptLines(strategy)
	seen := make(map[string]bool)
	for _, line := range lines[:body] {
		text := strings.TrimSpace(line)
		if !strings.HasPrefix(text, "#") {
			continue
		}
		key, _, _ := headerField(text)
		daily := key == dailyGainKey || key == dailyLossKey
		if !daily && !(dailyLike(key) && strings.ContainsAny(text, "0123456789")) {
			continue
		}
		switch {
		case !daily:
			return fmt.Errorf("the line %q isn't a daily limit the agent reads: \"# Daily gain limit: 250 BRL\" or "+
				"\"# Daily loss limit: 150 BRL\"; in the header, it isn't sent to the model either: a note for the "+
				"model goes in the text, after the header", text)
		case seen[key]:
			return fmt.Errorf("the line %q: the %s is given twice", text, key)
		}
		seen[key] = true
	}
	return nil
}

// textDailyLines returns the lines of the text of strategy, after its header,
// reading like a daily limit in money (see dailyMoneyLine), eg: "# Daily loss
// limit: 150 BRL" after the first line of the strategy: text for the model,
// not a limit the agent applies, logged by the Trader.
func textDailyLines(strategy string) []string {
	lines, body := promptLines(strategy)
	var found []string
	for _, line := range lines[body:] {
		if text := strings.TrimSpace(line); strings.HasPrefix(text, "#") && dailyMoneyLine(text) {
			found = append(found, text)
		}
	}
	return found
}

// dailyLike reports whether the key of a header line, in lower case, names a
// day and a limit, eg: "daily loss limits" or "limite diário".
func dailyLike(key string) bool {
	return strings.Contains(key, "limit") &&
		(strings.Contains(key, "daily") || strings.Contains(key, "diári") || strings.Contains(key, "diari"))
}

// exitsAwaited is an AccountReporter telling the positions of the bot on a
// pair closed at the broker, eg: by their stop loss, whose exits, and results,
// aren't in its history yet, the legs closing them still open meanwhile: when
// it noticed the oldest close, which is not later, eg: the MT5 adapter, see
// daily.
type exitsAwaited interface {
	ExitsAwaited(pair string) (since time.Time, ok bool)
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
// of the day sent in the context (see DailyHistory). The prompt the Trader was
// made with is checked against its account with CheckPromptAccount.
func (t *Trader) SetAccountReporter(reporter service.AccountReporter) {
	t.currencyMtx.Lock()
	defer t.currencyMtx.Unlock()
	t.reporter, t.currency = reporter, "" // the currency of its account read again
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
// midnight of Config.DayZone, LocalZone when unset.
func (t *Trader) dayOf(at time.Time) (start, end time.Time) {
	zone := t.cfg.DayZone
	if zone == nil {
		zone = LocalZone()
	}
	local := at.In(zone)
	start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone)
	return start, start.AddDate(0, 0, 1)
}

// LocalZone returns the time zone of the agent by its name, eg: America/Belem
// rather than Local, for Config.DayZone and the time_zone of DailyHistory: the
// one of TZ, a name of the IANA database (eg: America/Belem) or the path of
// its file; without TZ, the one of the system, /etc/localtime, by the name of
// the file its link points to, else time.Local. An empty or unknown TZ is UTC,
// as it is for time.Local, the unknown one logged.
func LocalZone() *time.Location {
	return localZone()
}

// localZone is LocalZone, resolved once.
var localZone = sync.OnceValue(func() *time.Location { return zoneOf(os.LookupEnv, "/etc/localtime") })

// zoneOf is LocalZone, the environment read with lookup and the zone of the
// system at localtime.
func zoneOf(lookup func(string) (string, bool), localtime string) *time.Location {
	named := func(path string) (*time.Location, bool) {
		_, name, found := strings.Cut(filepath.ToSlash(path), "zoneinfo/")
		if !found {
			return nil, false
		}
		zone, err := time.LoadLocation(name)
		return zone, err == nil
	}
	tz, set := lookup("TZ")
	if !set {
		if target, err := filepath.EvalSymlinks(localtime); err == nil {
			if zone, ok := named(target); ok {
				return zone
			}
		}
		return time.Local
	}
	tz = strings.TrimPrefix(tz, ":")
	switch {
	case tz == "" || tz == "UTC":
		return time.UTC
	case filepath.IsAbs(tz):
		if zone, ok := named(tz); ok {
			return zone
		}
		if data, err := os.ReadFile(tz); err == nil {
			if zone, err := time.LoadLocationFromTZData(tz, data); err == nil {
				return zone
			}
		}
	default:
		if zone, err := time.LoadLocation(tz); err == nil {
			return zone
		}
	}
	log.Warnf("[LLM] TZ %q isn't a time zone known: the operational days of the daily limits start at the "+
		"midnight of UTC", tz)
	return time.UTC
}

// dayClock returns the time of the analysis of df, of its operational day and
// of its economic calendar: now, live, else the close of its last candle, eg:
// in a backtest.
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

	closed, open, roles := t.pairTrades(pair, orders)
	realized := 0.0
	var pending, booked []closedTrade // of the day, their results unbooked and booked, oldest first
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
				booked = append(booked, trade)
			} else {
				history.Result.PendingResults++
				pending = append(pending, trade)
			}
		}
		history.Positions = append(history.Positions, position)
	}
	stale := false // the orders of the bot hold a position the broker doesn't, eg: its legs not updated yet
	if open != nil {
		position := DailyPosition{Side: describeSide(open.side), Status: "open", Size: number(open.size),
			Entry: number(open.entry), Exit: nan, OpenedAt: open.opened.UTC(), Result: nan, UnrealizedResult: nan}
		if book, ok := t.reporter.(openPositions); ok {
			held, found := book.OpenPosition(pair)
			if found {
				position.UnrealizedResult = number(held.Profit)
				history.Result.Unrealized = number(held.Profit)
			}
			stale = !found
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
	t.tellBooked(pair, booked, pending, start.Location())
	history.Limits = &DailyLimits{Gain: nan, Loss: nan}
	if limits.gain > 0 {
		history.Limits.Gain = number(limits.gain)
	}
	if limits.loss > 0 {
		history.Limits.Loss = number(limits.loss)
	}
	why := ""
	result := cents(realized)
	var since time.Time // of a close at the broker whose result is awaited
	awaited := false
	if book, ok := t.reporter.(exitsAwaited); ok {
		since, awaited = book.ExitsAwaited(pair)
	}
	t.tellAwaitedBooked(pair, awaited || stale, pending, start.Location())
	switch currency := history.Currency; {
	case currency == "":
		why = "the currency of the account is unknown: the daily limits can't be checked"
	case currency != limits.currency:
		why = fmt.Sprintf("the account is in %s, the daily limits in %s", currency, limits.currency)
		t.tellCurrency(st)
	case len(pending) > 0:
		why = t.pendingWhy(pair, pending, at, start.Location())
	case awaited && today(since):
		why = t.awaitedWhy(pair, since, at, start.Location())
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

// pendingWhy returns why the new entries on pair wait, the results of the
// closes pending unbooked by the broker, oldest first: the one of the oldest.
// Each one late (see pendingResultLate) is told once, logged as an error and
// notified.
func (t *Trader) pendingWhy(pair string, pending []closedTrade, at time.Time, zone *time.Location) string {
	oldest := pending[0].closed
	if at.Sub(oldest) < pendingResultLate {
		return fmt.Sprintf("the result of the close of %s isn't booked by the broker yet",
			oldest.In(zone).Format("15:04:05"))
	}
	for _, trade := range pending {
		late := at.Sub(trade.closed)
		if late < pendingResultLate || t.overdue[trade.close.ID] {
			continue
		}
		if t.overdue == nil {
			t.overdue = make(map[int64]bool)
		}
		t.overdue[trade.close.ID] = true
		message := fmt.Sprintf("⚠️ %s: the result of the close of %s (order %d) isn't booked by the broker yet, "+
			"%.0f min later: the new entries on %s wait for it, the daily limits unchecked without it, until the "+
			"next day at most. See the log of the MT5 adapter.", pair, trade.closed.In(zone).Format("15:04:05"),
			trade.close.ID, late.Minutes(), pair)
		log.Errorf("[LLM] %s", message)
		if notify := t.notifier(); notify != nil {
			notify(message)
		}
	}
	return fmt.Sprintf("the result of the close of %s isn't booked by the broker yet, %.0f min later: the entries "+
		"wait for it, until the next day at most", oldest.In(zone).Format("15:04:05"), at.Sub(oldest).Minutes())
}

// awaitedWhy returns why the new entries on pair wait: a position closed at
// the broker, noticed at since, eg: by its stop loss, its exit and its result
// not in the history yet (see exitsAwaited). Late (see pendingResultLate), it
// is told once, logged as an error and notified, as pendingWhy does, and its
// result told once booked, see tellAwaitedBooked.
func (t *Trader) awaitedWhy(pair string, since, at time.Time, zone *time.Location) string {
	why := fmt.Sprintf("a position on %s closed at the broker (noticed at %s), its result isn't booked yet", pair,
		since.In(zone).Format("15:04:05"))
	late := at.Sub(since)
	if late < pendingResultLate {
		return why
	}
	if told, ok := t.awaitedLate[pair]; !ok || !told.Equal(since) {
		if t.awaitedLate == nil {
			t.awaitedLate = make(map[string]time.Time)
		}
		t.awaitedLate[pair] = since
		message := fmt.Sprintf("⚠️ %s: a position closed at the broker (noticed at %s), eg: by its stop loss, has "+
			"no exit in its history yet, %.0f min later: its result is unknown, the new entries on %s wait for it, "+
			"the daily limits unchecked without it, until the next day at most. See the log of the MT5 adapter.",
			pair, since.In(zone).Format("15:04:05"), late.Minutes(), pair)
		log.Errorf("[LLM] %s", message)
		if notify := t.notifier(); notify != nil {
			notify(message)
		}
	}
	return fmt.Sprintf("%s, %.0f min later: the entries wait for it, until the next day at most", why,
		late.Minutes())
}

// tellBooked tells the results of pair booked at last, of the closes told late
// (see pendingWhy) none of whose results is pending any more: the entries
// checked against the daily limits again.
func (t *Trader) tellBooked(pair string, booked, pending []closedTrade, zone *time.Location) {
	for _, trade := range booked {
		id := trade.close.ID
		if !t.overdue[id] || slices.ContainsFunc(pending, func(p closedTrade) bool { return p.close.ID == id }) {
			continue
		}
		delete(t.overdue, id)
		t.tell(fmt.Sprintf("✅ %s: the result of the close of %s (order %d) is booked by the broker: the new entries "+
			"on %s are checked against the daily limits again.", pair, trade.closed.In(zone).Format("15:04:05"), id,
			pair))
	}
}

// tellAwaitedBooked tells once the close at the broker of pair told late (see
// awaitedWhy) settled: its exit in the history, nothing awaited on pair any
// more, and its result among the ones of the day, the orders of the bot
// holding no position the broker doesn't (unsettled false): the entries
// checked against the daily limits again. The results of the day still
// pending then are told once booked instead, as the closes told late, see
// tellBooked.
func (t *Trader) tellAwaitedBooked(pair string, unsettled bool, pending []closedTrade, zone *time.Location) {
	since, told := t.awaitedLate[pair]
	if !told || unsettled {
		return
	}
	delete(t.awaitedLate, pair)
	if len(pending) > 0 {
		if t.overdue == nil {
			t.overdue = make(map[int64]bool)
		}
		for _, trade := range pending {
			t.overdue[trade.close.ID] = true
		}
		return
	}
	t.tell(fmt.Sprintf("✅ %s: the position closed at the broker (noticed at %s) is in its history, its result "+
		"booked: the new entries on %s are checked against the daily limits again.", pair,
		since.In(zone).Format("15:04:05"), pair))
}

// tellCurrency tells once the prompt of st, applied while the currency of the
// account was unknown, refused for daily limits in another currency: logged as
// an error and reported to the PromptSource (see PromptReporter), as the
// switch to it would have been. Its new entries are refused meanwhile, see
// daily.
func (t *Trader) tellCurrency(st *promptState) {
	if t.currencyTold == st {
		return
	}
	t.currencyTold = st
	err := t.checkDailyCurrency(st.daily)
	if err == nil {
		return
	}
	log.Errorf("[LLM] the prompt %s refused: %v: no new entry with it", describePrompt(st.ref), err)
	if reporter, ok := t.prompts.(PromptReporter); ok {
		reporter.ReportPrompt(st.ref, err)
	}
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
