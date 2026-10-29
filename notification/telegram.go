package notification

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	tb "gopkg.in/tucnak/telebot.v2"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/service"
)

// amountUnit ends the amount of an order: in the currency of the cash (the
// account's at MT5, the quote in a paper wallet), in % of the balance, or its
// size in lots, eg: 0.1 lot, in the units the broker trades the market in.
// Nothing may follow it, so a typo in the unit isn't taken for an amount.
const amountUnit = `(?:(?P<percent>%)|\s*(?P<lot>(?i:lot(?:s|es?)?)))?\s*$`

var (
	buyRegexp  = regexp.MustCompile(`/buy\s+(?P<pair>\w+)\s+(?P<amount>\d+(?:\.\d+)?)` + amountUnit)
	sellRegexp = regexp.MustCompile(`/sell\s+(?P<pair>\w+)\s+(?P<amount>\d+(?:\.\d+)?)` + amountUnit)

	// telegramAPIURL overrides the Telegram endpoint. Empty means the default
	// api.telegram.org; the tests point it at a local server.
	telegramAPIURL string
)

type telegram struct {
	settings        model.Settings
	orderController *order.Controller
	defaultMenu     *tb.ReplyMarkup
	client          *tb.Bot
}

type Option func(telegram *telegram)

// authorizedUser filters out updates coming from anyone outside the user list
// of the settings, so that only the bot owners can drive it.
func authorizedUser(settings model.Settings) func(*tb.Update) bool {
	return func(u *tb.Update) bool {
		if u.Message == nil || u.Message.Sender == nil {
			log.Error("no message, ", u)
			return false
		}

		for _, user := range settings.Telegram.Users {
			if int(u.Message.Sender.ID) == user {
				return true
			}
		}

		log.Error("invalid user, ", u.Message)
		return false
	}
}

// NewTelegram returns the Telegram bot of the engine: its orders, errors and
// commands, eg: /buy, /profit, for the users of settings.Telegram.
//
// Deprecated: the engine creates it only with settings.Telegram.Enabled,
// which no agent sets: the agents relay their orders, messages and errors to
// the Telegram of the controller (controller/telegram.go), which drives them.
// Kept until its removal is decided.
func NewTelegram(controller *order.Controller, settings model.Settings, options ...Option) (service.Telegram, error) {
	menu := &tb.ReplyMarkup{ResizeReplyKeyboard: true}
	poller := &tb.LongPoller{Timeout: 10 * time.Second}

	userMiddleware := tb.NewMiddlewarePoller(poller, authorizedUser(settings))

	client, err := tb.NewBot(tb.Settings{
		URL:       telegramAPIURL,
		ParseMode: tb.ModeMarkdown,
		Token:     settings.Telegram.Token,
		Poller:    userMiddleware,
	})
	if err != nil {
		return nil, err
	}

	var (
		statusBtn  = menu.Text("/status")
		profitBtn  = menu.Text("/profit")
		balanceBtn = menu.Text("/balance")
		startBtn   = menu.Text("/start")
		stopBtn    = menu.Text("/stop")
		buyBtn     = menu.Text("/buy")
		sellBtn    = menu.Text("/sell")
	)

	err = client.SetCommands([]tb.Command{
		{Text: "/help", Description: "Display help instructions"},
		{Text: "/stop", Description: "Stop buy and sell coins"},
		{Text: "/start", Description: "Start buy and sell coins"},
		{Text: "/status", Description: "Check bot status"},
		{Text: "/balance", Description: "Wallet balance"},
		{Text: "/profit", Description: "Summary of last trade results"},
		{Text: "/buy", Description: "open a buy order, eg: /buy BTCUSDT 100, 50% or 0.1 lot"},
		{Text: "/sell", Description: "open a sell order, eg: /sell BTCUSDT 100, 50% or 0.1 lot"},
	})
	if err != nil {
		return nil, err
	}

	menu.Reply(
		menu.Row(statusBtn, balanceBtn, profitBtn),
		menu.Row(startBtn, stopBtn, buyBtn, sellBtn),
	)

	bot := &telegram{
		orderController: controller,
		client:          client,
		settings:        settings,
		defaultMenu:     menu,
	}

	for _, option := range options {
		option(bot)
	}

	client.Handle("/help", bot.HelpHandle)
	client.Handle("/start", bot.StartHandle)
	client.Handle("/stop", bot.StopHandle)
	client.Handle("/status", bot.StatusHandle)
	client.Handle("/balance", bot.BalanceHandle)
	client.Handle("/profit", bot.ProfitHandle)
	client.Handle("/buy", bot.BuyHandle)
	client.Handle("/sell", bot.SellHandle)

	return bot, nil
}

func (t telegram) Start() {
	go t.client.Start()
	for _, id := range t.settings.Telegram.Users {
		_, err := t.client.Send(&tb.User{ID: int64(id)}, "Bot initialized.", t.defaultMenu)
		if err != nil {
			log.Error(err)
		}
	}
}

func (t telegram) Notify(text string) {
	for _, user := range t.settings.Telegram.Users {
		_, err := t.client.Send(&tb.User{ID: int64(user)}, text)
		if err != nil {
			log.Error(err)
		}
	}
}

func (t telegram) BalanceHandle(m *tb.Message) {
	message := "*BALANCE*\n"
	quotesValue := make(map[string]float64)
	total := 0.0

	account, err := t.orderController.Account()
	if err != nil {
		log.Error(err)
		t.OnError(err)
		return
	}

	// on an account the broker reports, in its currency, the assets valued by
	// the point value of their pairs; elsewhere in their quotes
	summary, reported := t.orderController.AccountSummary()
	for _, pair := range t.settings.Pairs {
		assetPair, quotePair := exchange.SplitAssetQuote(pair)
		point := 1.0
		if reported && summary.Currency != "" {
			quotePair = summary.Currency
			if point, err = t.orderController.PointValue(pair); err != nil {
				log.Error(err)
				t.OnError(err)
				return
			}
		}
		assetBalance, quoteBalance := account.Balance(assetPair, quotePair)

		assetSize := assetBalance.Free + assetBalance.Lock
		quoteSize := quoteBalance.Free + quoteBalance.Lock

		quote, err := t.orderController.LastQuote(pair)
		if err != nil {
			log.Error(err)
			t.OnError(err)
			return
		}

		assetValue := assetSize * quote * point
		quotesValue[quotePair] = quoteSize
		total += assetValue
		message += fmt.Sprintf("%s: `%.4f` ≅ `%.2f` %s \n", assetPair, assetSize, assetValue, quotePair)
	}

	for quote, value := range quotesValue {
		total += value
		message += fmt.Sprintf("%s: `%.4f`\n", quote, value)
	}

	message += fmt.Sprintf("-----\nTotal: `%.4f`\n", total)
	if reported {
		// the account as the broker reports it, in its currency, eg: BRL
		account := summary
		message += fmt.Sprintf("-----\nAccount (%s)\nBalance: `%.2f`\nEquity: `%.2f`\nOpen P&L: `%.2f`\n"+
			"Free margin: `%.2f`\n", account.Currency, account.Balance, account.Equity, account.Profit,
			account.FreeMargin)
	}

	_, err = t.client.Send(m.Sender, message)
	if err != nil {
		log.Error(err)
	}
}

func (t telegram) HelpHandle(m *tb.Message) {
	commands, err := t.client.GetCommands()
	if err != nil {
		log.Error(err)
		t.OnError(err)
		return
	}

	lines := make([]string, 0, len(commands))
	for _, command := range commands {
		lines = append(lines, fmt.Sprintf("/%s - %s", command.Text, command.Description))
	}

	_, err = t.client.Send(m.Sender, strings.Join(lines, "\n"))
	if err != nil {
		log.Error(err)
	}
}

func (t telegram) ProfitHandle(m *tb.Message) {
	summaries := t.orderController.Summaries() // read under its lock: the orders fill meanwhile
	if len(summaries) == 0 {
		_, err := t.client.Send(m.Sender, "No trades registered.")
		if err != nil {
			log.Error(err)
		}
		return
	}

	pairs := make([]string, 0, len(summaries))
	for pair := range summaries {
		pairs = append(pairs, pair)
	}
	sort.Strings(pairs)
	for _, pair := range pairs {
		_, err := t.client.Send(m.Sender, fmt.Sprintf("*PAIR*: `%s`\n`%s`", pair, summaries[pair]))
		if err != nil {
			log.Error(err)
		}
	}
}

// notifyOrderError reports a failed manual order back to the user. The
// controller notifies exchange/storage failures on its own, but orders
// rejected because the bot is stopped, or its entries blocked, never reach
// that path — without this the command would silently do nothing.
func (t telegram) notifyOrderError(m *tb.Message, err error) {
	if errors.Is(err, order.ErrEntriesBlocked) {
		_, sendErr := t.client.Send(m.Sender, "The entries of the bot are blocked, no position was opened: "+
			err.Error(), t.defaultMenu)
		if sendErr != nil {
			log.Error(sendErr)
		}
		return
	}
	if !errors.Is(err, order.ErrBotStopped) {
		log.Error(err)
		return
	}

	_, sendErr := t.client.Send(m.Sender, "Bot is stopped, no order was created. Send /start to resume.", t.defaultMenu)
	if sendErr != nil {
		log.Error(sendErr)
	}
}

func (t telegram) BuyHandle(m *tb.Message) {
	match := buyRegexp.FindStringSubmatch(m.Text)
	if len(match) == 0 {
		_, err := t.client.Send(m.Sender, "Invalid command.\nExamples of usage:\n`/buy BTCUSDT 100`\n\n"+
			"`/buy BTCUSDT 50%`\n\n`/buy BTCUSDT 0.1 lot`")
		if err != nil {
			log.Error(err)
		}
		return
	}

	command := make(map[string]string)
	for i, name := range buyRegexp.SubexpNames() {
		if i != 0 && name != "" {
			command[name] = match[i]
		}
	}

	pair := strings.ToUpper(command["pair"])
	amount, err := strconv.ParseFloat(command["amount"], 64)
	if err != nil {
		log.Error(err)
		t.OnError(err)
		return
	} else if amount <= 0 {
		_, err := t.client.Send(m.Sender, "Invalid amount")
		if err != nil {
			log.Error(err)
		}
		return
	}

	if command["lot"] != "" {
		order, err := t.orderController.CreateOrderMarket(model.SideTypeBuy, pair, amount)
		if err != nil {
			t.notifyOrderError(m, err)
			return
		}
		log.Info("[TELEGRAM]: BUY ORDER CREATED: ", order)
		return
	}

	if command["percent"] != "" {
		_, quote, err := t.orderController.Position(pair)
		if err != nil {
			log.Error(err)
			t.OnError(err)
			return
		}

		amount = amount * quote / 100.0
	}

	order, err := t.orderController.CreateOrderMarketQuote(model.SideTypeBuy, pair, amount)
	if err != nil {
		t.notifyOrderError(m, err)
		return
	}
	log.Info("[TELEGRAM]: BUY ORDER CREATED: ", order)
}

func (t telegram) SellHandle(m *tb.Message) {
	match := sellRegexp.FindStringSubmatch(m.Text)
	if len(match) == 0 {
		_, err := t.client.Send(m.Sender, "Invalid command.\nExamples of usage:\n`/sell BTCUSDT 100`\n\n"+
			"`/sell BTCUSDT 50%`\n\n`/sell BTCUSDT 0.1 lot`")
		if err != nil {
			log.Error(err)
		}
		return
	}

	command := make(map[string]string)
	for i, name := range sellRegexp.SubexpNames() {
		if i != 0 && name != "" {
			command[name] = match[i]
		}
	}

	pair := strings.ToUpper(command["pair"])
	amount, err := strconv.ParseFloat(command["amount"], 64)
	if err != nil {
		log.Error(err)
		t.OnError(err)
		return
	} else if amount <= 0 {
		_, err := t.client.Send(m.Sender, "Invalid amount")
		if err != nil {
			log.Error(err)
		}
		return
	}

	if command["lot"] != "" {
		order, err := t.orderController.CreateOrderMarket(model.SideTypeSell, pair, amount)
		if err != nil {
			t.notifyOrderError(m, err)
			return
		}
		log.Info("[TELEGRAM]: SELL ORDER CREATED: ", order)
		return
	}

	if command["percent"] != "" {
		asset, _, err := t.orderController.Position(pair)
		if err != nil {
			return
		}

		amount = amount * asset / 100.0
		order, err := t.orderController.CreateOrderMarket(model.SideTypeSell, pair, amount)
		if err != nil {
			t.notifyOrderError(m, err)
			return
		}
		log.Info("[TELEGRAM]: SELL ORDER CREATED: ", order)
		return
	}

	order, err := t.orderController.CreateOrderMarketQuote(model.SideTypeSell, pair, amount)
	if err != nil {
		t.notifyOrderError(m, err)
		return
	}
	log.Info("[TELEGRAM]: SELL ORDER CREATED: ", order)
}

func (t telegram) StatusHandle(m *tb.Message) {
	status := t.orderController.Status()
	_, err := t.client.Send(m.Sender, fmt.Sprintf("Status: `%s`", status))
	if err != nil {
		log.Error(err)
	}
}

func (t telegram) StartHandle(m *tb.Message) {
	if t.orderController.Status() == order.StatusRunning {
		_, err := t.client.Send(m.Sender, "Bot is already running.", t.defaultMenu)
		if err != nil {
			log.Error(err)
		}
		return
	}

	t.orderController.Start()
	_, err := t.client.Send(m.Sender, "Bot started.", t.defaultMenu)
	if err != nil {
		log.Error(err)
	}
}

func (t telegram) StopHandle(m *tb.Message) {
	if t.orderController.Status() == order.StatusStopped {
		_, err := t.client.Send(m.Sender, "Bot is already stopped.", t.defaultMenu)
		if err != nil {
			log.Error(err)
		}
		return
	}

	t.orderController.Stop()
	_, err := t.client.Send(m.Sender,
		"Bot stopped. New orders are rejected until /start — including protective stops, "+
			"so any open position is left unprotected.", t.defaultMenu)
	if err != nil {
		log.Error(err)
	}
}

func (t telegram) OnOrder(order model.Order) {
	title := ""
	switch order.Status {
	case model.OrderStatusTypeFilled:
		title = fmt.Sprintf("✅ ORDER FILLED - %s", order.Pair)
	case model.OrderStatusTypeNew:
		title = fmt.Sprintf("🆕 NEW ORDER - %s", order.Pair)
	case model.OrderStatusTypeCanceled, model.OrderStatusTypeRejected:
		title = fmt.Sprintf("❌ ORDER CANCELED / REJECTED - %s", order.Pair)
	}
	message := fmt.Sprintf("%s\n-----\n%s", title, order)
	t.Notify(message)
}

func (t telegram) OnError(err error) {
	title := "🛑 ERROR"

	var orderError *exchange.OrderError
	if errors.As(err, &orderError) {
		message := fmt.Sprintf(`%s
		-----
		Pair: %s
		Quantity: %.4f
		-----
		%s`, title, orderError.Pair, orderError.Quantity, orderError.Err)
		t.Notify(message)
		return
	}

	t.Notify(fmt.Sprintf("%s\n-----\n%s", title, err))
}
