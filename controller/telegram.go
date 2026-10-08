package controller

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	log "github.com/sirupsen/logrus"
	tb "gopkg.in/tucnak/telebot.v2"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/ui"
)

// orderRegexp parses /buy and /sell as the Telegram of the bots does: an amount
// in quote, in % of the balance, or a size in lots, eg: 0.1 lot, with nothing
// after it, so a typo in the unit isn't taken for an amount in quote.
var orderRegexp = regexp.MustCompile(`^/(?P<side>buy|sell)(?:@\w+)?\s+(?P<pair>\w+)\s+` +
	`(?P<amount>\d+(?:\.\d+)?)(?:(?P<percent>%)|\s*(?P<lot>(?i:lot(?:s|es?)?)))?\s*$`)

var (
	// telegramAPIURL overrides the Telegram endpoint. Empty means the default
	// api.telegram.org; the tests point it at a local server.
	telegramAPIURL string

	// relayRetry is how long the relay of an agent waits to follow its events
	// again once they end.
	relayRetry = 5 * time.Second

	// pollRetry is how long the poller of the bot waits to poll again after an
	// error, eg: the API out of reach, see Telegram.report.
	pollRetry = 5 * time.Second
)

// Telegram serves the agents of a dashboard on a Telegram bot, in place of the
// Telegram of each bot: every user picks the agent of the commands with
// /agents, the commands reach it through its API, and the notifications of the
// agents relayed (see SyncRelays) are sent to every user, prefixed with their
// name. With the prompts of the database (see WithPrompts), the users list,
// show, edit and switch the prompts of the LLM agents too. The token of the
// bot is never logged, see redact.
type Telegram struct {
	ctx       context.Context
	dashboard *Dashboard
	users     atomic.Pointer[[]int64]
	refused   refusals // the updates of anyone else, logged
	token     string   // of the bot, kept out of the logs
	client    *tb.Bot
	menu      *tb.ReplyMarkup
	prompts   PromptStore
	onChange  func() // after a change of the prompts, eg: Manager.Trigger
	now       func() time.Time

	mu       sync.Mutex
	selected map[int64]string     // the agent of the commands, by user
	editing  map[int64]promptEdit // the edit of a prompt in progress, by user
	relays   map[string]relaying  // by agent
}

// relaying is the relay of the events of an agent, stopped by cancel.
type relaying struct {
	agent  *agent
	cancel context.CancelFunc
}

// TelegramOption configures a Telegram.
type TelegramOption func(*Telegram)

// WithPrompts serves the prompts of prompts on the bot, onChange called after
// each change.
func WithPrompts(prompts PromptStore, onChange func()) TelegramOption {
	return func(t *Telegram) { t.prompts, t.onChange = prompts, onChange }
}

// NewTelegram creates the Telegram bot of the token for the dashboard, which
// only the users, by their Telegram ID, can drive (see SetUsers). Call Start
// to serve it. Its errors never carry the token.
func NewTelegram(dashboard *Dashboard, token string, users []int64, options ...TelegramOption) (*Telegram, error) {
	t := &Telegram{ctx: context.Background(), dashboard: dashboard, token: token, selected: make(map[int64]string),
		editing: make(map[int64]promptEdit), relays: make(map[string]relaying), now: time.Now}
	t.SetUsers(users)
	for _, option := range options {
		option(t)
	}

	poller := &tb.LongPoller{Timeout: 10 * time.Second}
	client, err := tb.NewBot(tb.Settings{
		URL:      telegramAPIURL,
		Token:    token,
		Poller:   tb.NewMiddlewarePoller(poller, t.authorized),
		Reporter: t.report,
	})
	if err != nil {
		return nil, t.redact(err)
	}

	commands := []tb.Command{
		{Text: "/help", Description: "Display help instructions"},
		{Text: "/agents", Description: "List the agents, to pick the one of the commands"},
		{Text: "/agent", Description: "Pick the agent of the commands, eg: /agent matrend"},
		{Text: "/stop", Description: "Stop buy and sell coins"},
		{Text: "/start", Description: "Start buy and sell coins"},
		{Text: "/status", Description: "Check bot status"},
		{Text: "/balance", Description: "Wallet balance"},
		{Text: "/profit", Description: "Summary of last trade results"},
		{Text: "/buy", Description: "open a buy order, eg: /buy BTCUSDT 100, 50% or 0.1 lot"},
		{Text: "/sell", Description: "open a sell order, eg: /sell BTCUSDT 100, 50% or 0.1 lot"},
		{Text: "/reanalyze", Description: "Ask the language model of the agent again now, eg: /reanalyze BTCUSDm, " +
			"with a note for it after the pair if any: /reanalyze BTCUSDm in profit, close it? (the agents " +
			"trading with an LLM)"},
	}
	if t.prompts != nil {
		commands = append(commands,
			tb.Command{Text: "/prompts", Description: "List the prompts of the LLM agents"},
			tb.Command{Text: "/prompt", Description: "Show a prompt, eg: /prompt trend_following"},
			tb.Command{Text: "/editprompt", Description: "Edit a prompt, eg: /editprompt trend_following, then send " +
				"its new content, or a .txt file, and /confirm it"},
			tb.Command{Text: "/confirm", Description: "Save the new content of the prompt edited, once shown"},
			tb.Command{Text: "/useprompt", Description: "Switch the prompt of an LLM agent, from its next analysis, " +
				"eg: /useprompt llm trend_following (default: the one of the agent)"},
			tb.Command{Text: "/cancel", Description: "Give up the edit of a prompt"})
	}
	if err := client.SetCommands(commands); err != nil {
		return nil, t.redact(err)
	}

	menu := &tb.ReplyMarkup{ResizeReplyKeyboard: true}
	menu.Reply(
		menu.Row(menu.Text("/agents"), menu.Text("/reanalyze")),
		menu.Row(menu.Text("/status"), menu.Text("/balance"), menu.Text("/profit")),
		menu.Row(menu.Text("/start"), menu.Text("/stop"), menu.Text("/buy"), menu.Text("/sell")),
	)

	t.client, t.menu = client, menu
	client.Handle("/help", t.handleHelp)
	client.Handle("/agents", t.handleAgents)
	client.Handle("/agent", t.handleAgent)
	client.Handle("/start", t.handleStart)
	client.Handle("/stop", t.handleStop)
	client.Handle("/status", t.handleStatus)
	client.Handle("/balance", t.handleBalance)
	client.Handle("/profit", t.handleProfit)
	client.Handle("/buy", t.handleOrder)
	client.Handle("/sell", t.handleOrder)
	client.Handle("/reanalyze", t.handleReanalyze)
	client.Handle("/reanalyse", t.handleReanalyze)
	if t.prompts != nil {
		client.Handle("/prompts", t.handlePrompts)
		client.Handle("/prompt", t.handlePrompt)
		client.Handle("/editprompt", t.handleEditPrompt)
		client.Handle("/useprompt", t.handleUsePrompt)
		client.Handle("/confirm", t.handleConfirm)
		client.Handle("/cancel", t.handleCancel)
		client.Handle(tb.OnText, t.handleText)
		client.Handle(tb.OnDocument, t.handleDocument)
	}
	return t, nil
}

// SetUsers makes users, by their Telegram ID, the ones the bot answers.
func (t *Telegram) SetUsers(users []int64) {
	list := append([]int64(nil), users...)
	t.users.Store(&list)
}

// authorized filters out the updates of anyone but the users, so that only
// the owners of the bots drive them. A refused update is logged by the ID of
// its sender only, never its content, see refusals.
func (t *Telegram) authorized(u *tb.Update) bool {
	if u.Message == nil || u.Message.Sender == nil {
		t.refused.log(0, time.Now())
		return false
	}
	if slices.Contains(*t.users.Load(), u.Message.Sender.ID) {
		return true
	}
	t.refused.log(u.Message.Sender.ID, time.Now())
	return false
}

// refusalLogEvery is how often the updates refused are logged at most.
const refusalLogEvery = time.Minute

// refusals logs the updates refused by the bot: anyone can write to it, so a
// line at most each refusalLogEvery, with the ID of the last sender (0 for an
// update without one) and the count of the ones since the previous line.
type refusals struct {
	mu     sync.Mutex
	last   time.Time // of the previous line
	missed int       // the updates refused since
}

func (r *refusals) log(sender int64, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.missed++
	if !r.last.IsZero() && now.Sub(r.last) < refusalLogEvery {
		return
	}
	last := "without a sender"
	if sender != 0 {
		last = fmt.Sprintf("of the user %d", sender)
	}
	log.Warnf("telegram: refused %d update(s) of users not allowed, the last %s", r.missed, last)
	r.last, r.missed = now, 0
}

// Start serves the bot until ctx is done.
func (t *Telegram) Start(ctx context.Context) {
	t.ctx = ctx
	go t.client.Start()
	go func() {
		<-ctx.Done()
		t.client.Stop() // once, while Start runs: it blocks otherwise
	}()

	var names []string
	for _, a := range t.dashboard.agents.list() {
		names = append(names, a.Name)
	}
	go t.notify(fmt.Sprintf("Controller initialized, agents: %s.\nPick the one of the commands with /agents.",
		strings.Join(names, ", ")), t.menu)
}

// SyncRelays relays the events of the agents of names, the ones of the
// dashboard, and no other: a relay of an agent whose address changed starts
// over, and a relay stopped says nothing of its agent.
func (t *Telegram) SyncRelays(names []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	wanted := make(map[string]*agent, len(names))
	for _, name := range names {
		if a, ok := t.dashboard.agents.get(name); ok {
			wanted[name] = a
		}
	}
	for name, r := range t.relays {
		if a, ok := wanted[name]; !ok || a.Address != r.agent.Address {
			r.cancel()
			delete(t.relays, name)
		}
	}
	for name, a := range wanted {
		if _, ok := t.relays[name]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(t.ctx)
		t.relays[name] = relaying{agent: a, cancel: cancel}
		go t.relay(ctx, a)
	}
}

// notify sends the message to every user.
func (t *Telegram) notify(message string, options ...any) {
	for _, user := range *t.users.Load() {
		t.send(&tb.User{ID: user}, message, options...)
	}
}

func (t *Telegram) send(to tb.Recipient, message string, options ...any) {
	if _, err := t.client.Send(to, message, options...); err != nil {
		log.Error(t.redact(err))
	}
}

// reply answers the message on behalf of the agent.
func (t *Telegram) reply(m *tb.Message, a *agent, message string, options ...any) {
	t.send(m.Sender, fmt.Sprintf("[%s] %s", a.Name, message), options...)
}

// agentOf returns the agent of the commands of the user, the only one when
// there is a single agent, or tells how to pick one.
func (t *Telegram) agentOf(m *tb.Message) (*agent, bool) {
	t.mu.Lock()
	name, picked := t.selected[m.Sender.ID]
	t.mu.Unlock()

	if a, ok := t.dashboard.agents.get(name); picked && ok {
		return a, true
	}
	if agents := t.dashboard.agents.list(); len(agents) == 1 {
		return agents[0], true
	}
	t.send(m.Sender, "No agent picked: send /agents to pick one.", t.menu)
	return nil, false
}

// call calls the API of the agent, replying the error, if any.
func (t *Telegram) call(m *tb.Message, a *agent, method, path string, body, out any) bool {
	if err := t.dashboard.call(t.ctx, a, method, path, body, out); err != nil {
		t.reply(m, a, err.Error())
		return false
	}
	return true
}

// controls returns the state of the control panel of the agent, which the
// commands operating the bot need.
func (t *Telegram) controls(m *tb.Message, a *agent) (ui.ControlsResponse, bool) {
	var controls ui.ControlsResponse
	if !t.call(m, a, http.MethodGet, "controls", nil, &controls) {
		return controls, false
	}
	if !controls.Enabled {
		t.reply(m, a, "The Telegram controls of the agent are off: turn them on in the settings of the dashboard.")
		return controls, false
	}
	return controls, true
}

func (t *Telegram) handleHelp(m *tb.Message) {
	commands, err := t.client.GetCommands()
	if err != nil {
		log.Error(t.redact(err))
		return
	}
	lines := make([]string, 0, len(commands))
	for _, command := range commands {
		lines = append(lines, fmt.Sprintf("/%s - %s", strings.TrimPrefix(command.Text, "/"), command.Description))
	}
	t.send(m.Sender, strings.Join(lines, "\n"))
}

// handleAgents lists the agents, with a button to pick each one.
func (t *Telegram) handleAgents(m *tb.Message) {
	t.mu.Lock()
	selected := t.selected[m.Sender.ID]
	t.mu.Unlock()

	lines := []string{"Agents:"}
	keyboard := &tb.ReplyMarkup{ResizeReplyKeyboard: true}
	var rows []tb.Row
	for i, status := range t.dashboard.statuses(t.ctx) {
		state := "offline"
		if status.Online {
			state = "online"
		}
		if status.Name == selected {
			state += ", picked"
		}
		lines = append(lines, fmt.Sprintf("• %s: %s", status.Name, state))

		button := keyboard.Text("/agent " + status.Name)
		if i%2 == 0 {
			rows = append(rows, keyboard.Row(button))
		} else {
			rows[len(rows)-1] = append(rows[len(rows)-1], button)
		}
	}
	keyboard.Reply(rows...)
	t.send(m.Sender, strings.Join(lines, "\n"), keyboard)
}

// handleAgent picks the agent of the commands of the user.
func (t *Telegram) handleAgent(m *tb.Message) {
	name := strings.TrimSpace(m.Payload)
	if name == "" {
		t.handleAgents(m)
		return
	}
	a, ok := t.dashboard.agents.get(name)
	if !ok {
		t.send(m.Sender, fmt.Sprintf("Unknown agent %q: send /agents to list them.", name), t.menu)
		return
	}

	t.mu.Lock()
	t.selected[m.Sender.ID] = a.Name
	t.mu.Unlock()
	t.reply(m, a, "Agent picked: the commands go to it.", t.menu)
}

func (t *Telegram) handleStatus(m *tb.Message) {
	a, ok := t.agentOf(m)
	if !ok {
		return
	}
	if controls, ok := t.controls(m, a); ok {
		t.reply(m, a, fmt.Sprintf("Status: %s", controls.Status))
	}
}

func (t *Telegram) handleStart(m *tb.Message) {
	a, ok := t.agentOf(m)
	if !ok {
		return
	}
	controls, ok := t.controls(m, a)
	if !ok {
		return
	}
	if controls.Status == string(order.StatusRunning) {
		t.reply(m, a, "Bot is already running.", t.menu)
		return
	}
	if t.call(m, a, http.MethodPost, "controls/start", nil, nil) {
		t.reply(m, a, "Bot started.", t.menu)
	}
}

func (t *Telegram) handleStop(m *tb.Message) {
	a, ok := t.agentOf(m)
	if !ok {
		return
	}
	controls, ok := t.controls(m, a)
	if !ok {
		return
	}
	if controls.Status == string(order.StatusStopped) {
		t.reply(m, a, "Bot is already stopped.", t.menu)
		return
	}
	if t.call(m, a, http.MethodPost, "controls/stop", nil, nil) {
		t.reply(m, a, "Bot stopped. New orders are rejected until /start — including protective stops, "+
			"so any open position is left unprotected.", t.menu)
	}
}

func (t *Telegram) handleBalance(m *tb.Message) {
	a, ok := t.agentOf(m)
	if !ok {
		return
	}
	var balance ui.BalanceResponse
	if _, ok := t.controls(m, a); !ok || !t.call(m, a, http.MethodGet, "controls/balance", nil, &balance) {
		return
	}

	lines := []string{"BALANCE"}
	for _, asset := range balance.Balances {
		if asset.Asset == asset.Quote {
			lines = append(lines, fmt.Sprintf("%s: %.4f", asset.Asset, asset.Quantity))
		} else {
			lines = append(lines, fmt.Sprintf("%s: %.4f ≅ %.2f %s", asset.Asset, asset.Quantity, asset.Value,
				asset.Quote))
		}
	}
	lines = append(lines, "-----", fmt.Sprintf("Total: %.4f", balance.Total))
	if account := balance.Account; account != nil {
		// the account as the broker reports it, in its currency, eg: BRL
		lines = append(lines, "-----", fmt.Sprintf("Account (%s)", account.Currency),
			fmt.Sprintf("Balance: %.2f", account.Balance), fmt.Sprintf("Equity: %.2f", account.Equity),
			fmt.Sprintf("Open P&L: %.2f", account.Profit), fmt.Sprintf("Free margin: %.2f", account.FreeMargin))
	}
	t.reply(m, a, strings.Join(lines, "\n"))
}

// handleProfit summarizes the trades closed on each pair, as the dashboard
// counts them: since the start of the current period of the agent, after a
// reset of its results.
func (t *Telegram) handleProfit(m *tb.Message) {
	a, ok := t.agentOf(m)
	if !ok {
		return
	}
	var pairs ui.PairsResponse
	if !t.call(m, a, http.MethodGet, "pairs", nil, &pairs) {
		return
	}

	since, _ := t.dashboard.currentPeriod(t.ctx, a.Name)
	summaries := 0
	for _, pair := range pairs.Pairs {
		var snapshot ui.Snapshot
		if !t.call(m, a, http.MethodGet, pair+"/snapshot", nil, &snapshot) {
			return
		}
		if summary, ok := profitSummary(snapshot, since); ok {
			t.reply(m, a, summary)
			summaries++
		}
	}
	if summaries == 0 {
		message := "No trades registered."
		if !since.IsZero() {
			message = "No trades closed since " + since.UTC().Format("2006-01-02 15:04") + " UTC."
		}
		t.reply(m, a, message)
	}
}

// closedAt returns when the order filled, in Unix seconds: its updated_at, the
// time of the fill the exchange reported, kept by the agent and the store once
// filled (a trade never moves to another period), its creation for the ones
// filled at once.
func closedAt(o ui.Order) int64 {
	if o.UpdatedAt != 0 {
		return o.UpdatedAt
	}
	return o.CreatedAt
}

// profitSummary sums up the trades closed on the pair of the snapshot, the
// orders filled with a profit, from since (the start of the current period of
// the agent, zero for all of them), as the dashboard does: as the account
// booked them, in its currency, when it did for all (eg: MT5), else the ones
// of the core, in the quote. It reports false without any.
func profitSummary(snapshot ui.Snapshot, since time.Time) (string, bool) {
	var closed []ui.Order
	booked := true
	for _, o := range snapshot.Orders {
		if o.Status != string(model.OrderStatusTypeFilled) || (o.Profit == 0 && o.ProfitValue == 0) {
			continue
		}
		if !since.IsZero() && closedAt(o) < since.Unix() {
			continue // closed before the current period
		}
		closed = append(closed, o)
		booked = booked && o.AccountProfit != nil
	}
	trades := len(closed)
	if trades == 0 {
		return "", false
	}

	currency := snapshot.Quote
	if booked {
		currency = cmp.Or(closed[0].AccountCurrency, snapshot.AccountCurrency, snapshot.Quote)
	}
	var wins, losses int
	var profit, grossProfit, grossLoss float64
	for _, o := range closed {
		value := o.ProfitValue
		if booked {
			value = *o.AccountProfit
		}
		profit += value
		if value > 0 || (value == 0 && o.Profit > 0) {
			wins++
			grossProfit += value
		} else {
			losses++
			grossLoss -= value
		}
	}

	factor := "∞"
	if grossLoss > 0 {
		factor = strconv.FormatFloat(grossProfit/grossLoss, 'f', 2, 64)
	} else if grossProfit == 0 {
		factor = "0.00"
	}
	summary := fmt.Sprintf("PAIR: %s\nTrades: %d (%d wins, %d losses, %.1f%%)\nProfit: %.2f %s\nProfit factor: %s",
		snapshot.Pair, trades, wins, losses, float64(wins)/float64(trades)*100, profit, currency, factor)
	if !since.IsZero() {
		summary += "\nSince: " + since.UTC().Format("2006-01-02 15:04") + " UTC"
	}
	return summary, true
}

// handleOrder creates the market order of /buy or /sell with the control panel
// of the agent. The order is notified by the relay of the agent.
func (t *Telegram) handleOrder(m *tb.Message) {
	a, ok := t.agentOf(m)
	if !ok {
		return
	}

	match := orderRegexp.FindStringSubmatch(strings.TrimSpace(m.Text))
	if match == nil {
		command := "/buy"
		if strings.HasPrefix(m.Text, "/sell") {
			command = "/sell"
		}
		t.reply(m, a, fmt.Sprintf("Invalid command.\nExamples of usage:\n%[1]s BTCUSDT 100\n\n%[1]s BTCUSDT 50%%\n\n"+
			"%[1]s BTCUSDT 0.1 lot", command))
		return
	}
	command := make(map[string]string)
	for i, name := range orderRegexp.SubexpNames() {
		if i != 0 && name != "" {
			command[name] = match[i]
		}
	}

	amount, err := strconv.ParseFloat(command["amount"], 64)
	if err != nil || amount <= 0 || math.IsInf(amount, 0) {
		t.reply(m, a, "Invalid amount")
		return
	}
	request := ui.ControlOrderRequest{
		Pair:    strings.ToUpper(command["pair"]),
		Side:    command["side"],
		Amount:  amount,
		Percent: command["percent"] != "",
		Lot:     command["lot"] != "",
	}

	var created ui.Order
	err = t.dashboard.call(t.ctx, a, http.MethodPost, "controls/order", request, &created)
	var apiErr *apiError
	switch {
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict:
		t.reply(m, a, "Bot is stopped, no order was created. Send /start to resume.", t.menu)
	case err != nil:
		t.reply(m, a, err.Error())
	default:
		log.Infof("[TELEGRAM] %s: %s ORDER CREATED: %s", a.Name, strings.ToUpper(request.Side), orderLine(created))
	}
}

// handleReanalyze asks the language model of the agent again about a pair,
// the only one of the agent or the one named, now rather than at the next
// close: its decision is notified by the relay of the agent once taken.
func (t *Telegram) handleReanalyze(m *tb.Message) {
	a, ok := t.agentOf(m)
	if !ok {
		return
	}
	controls, ok := t.controls(m, a)
	if !ok {
		return
	}
	if !controls.Reanalysis {
		t.reply(m, a, "The agent doesn't trade with a language model: there is nothing to reanalyze.")
		return
	}

	var pairs ui.PairsResponse
	if !t.call(m, a, http.MethodGet, "pairs", nil, &pairs) {
		return
	}
	if len(pairs.Pairs) == 0 {
		t.reply(m, a, "No candle yet: nothing to reanalyze.")
		return
	}
	pair, note := reanalysisTarget(m.Payload, pairs.Pairs)
	if pair == "" {
		t.reply(m, a, fmt.Sprintf("Name the pair first, eg: /reanalyze %s, with a note after it if any (pairs: %s)",
			pairs.Pairs[0], strings.Join(pairs.Pairs, ", ")))
		return
	}

	var response ui.ReanalysisResponse
	if t.call(m, a, http.MethodPost, "controls/reanalyze", ui.ReanalysisRequest{Pair: pair, Note: note},
		&response) {
		t.reply(m, a, "🔄 "+response.Message+" The decision is notified once taken.")
	}
}

// reanalysisTarget reads the payload of /reanalyze: the pair of the agent it
// starts with, and the note for the model after it, if any. Without a pair of
// its own, the payload is the note of the only pair of the agent; with several
// pairs, the pair is empty: to be named.
func reanalysisTarget(payload string, pairs []string) (pair, note string) {
	payload = strings.TrimSpace(payload)
	first, rest := payload, ""
	if i := strings.IndexFunc(payload, unicode.IsSpace); i >= 0 {
		first, rest = payload[:i], payload[i:]
	}
	for _, p := range pairs {
		if strings.EqualFold(first, p) {
			return p, strings.TrimSpace(rest)
		}
	}
	if len(pairs) == 1 {
		return pairs[0], payload
	}
	return "", payload
}

// relay sends the notifications of the agent to the users until ctx is done,
// following its events again whenever they end: its orders, its messages and
// errors, and when it goes offline and back, once it was online.
func (t *Telegram) relay(ctx context.Context, a *agent) {
	var seen, online bool
	for {
		err := t.dashboard.events(ctx, a, func() {
			if seen && !online {
				t.notify(fmt.Sprintf("[%s] ✅ Agent back online.", a.Name))
			}
			seen, online = true, true
		}, func(name string, data []byte) {
			t.onEvent(a, name, data)
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Debug(err)
		}
		if online {
			t.notify(fmt.Sprintf("[%s] ⚠️ Agent offline at %s.", a.Name, a.Address))
			online = false
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(relayRetry):
		}
	}
}

// onEvent notifies an event of the agent, as the Telegram of the bots did.
func (t *Telegram) onEvent(a *agent, name string, data []byte) {
	switch name {
	case "order":
		var event ui.OrderEvent
		if err := json.Unmarshal(data, &event); err != nil {
			log.Error(err)
			return
		}
		var title string
		switch model.OrderStatusType(event.Order.Status) {
		case model.OrderStatusTypeFilled:
			title = "✅ ORDER FILLED - " + event.Pair
		case model.OrderStatusTypeNew:
			title = "🆕 NEW ORDER - " + event.Pair
		case model.OrderStatusTypeCanceled, model.OrderStatusTypeRejected:
			title = "❌ ORDER CANCELED / REJECTED - " + event.Pair
		default:
			return
		}
		t.notify(fmt.Sprintf("[%s] %s\n-----\n%s", a.Name, title, orderLine(event.Order)))
	case "notification":
		var event ui.NotificationEvent
		if err := json.Unmarshal(data, &event); err != nil {
			log.Error(err)
			return
		}
		if event.Error {
			t.notify(fmt.Sprintf("[%s] 🛑 ERROR\n-----\n%s", a.Name, event.Message))
		} else {
			t.notify(fmt.Sprintf("[%s] %s", a.Name, event.Message))
		}
	}
}

// orderLine describes an order of the API as model.Order does.
func orderLine(o ui.Order) string {
	return model.Order{
		ID:       o.ID,
		Pair:     o.Pair,
		Side:     model.SideType(o.Side),
		Type:     model.OrderType(o.Type),
		Status:   model.OrderStatusType(o.Status),
		Price:    o.Price,
		Quantity: o.Quantity,
	}.String()
}
