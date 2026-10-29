package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/tucnak/telebot.v2"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/ui"
)

const telegramUserID = 42

// telegramServer is a fake Telegram Bot API, recording the messages sent.
type telegramServer struct {
	*httptest.Server

	mu        sync.Mutex
	messages  []telegramMessage
	documents []telegramDocument
	commands  []tb.Command
	files     map[string]string // the content of the files users send, by file ID
	answers   map[string]string // in place of the usual answers, see answer
	requests  map[string]int    // by method, or ID of the file downloaded
}

// hangUp is an answer of the fake API: the connection closed without one, an
// error of the transport, which carries the URL of the request.
const hangUp = "hang up"

// telegramDocument is a document sent: its name, caption and content.
type telegramDocument struct {
	Name, Caption, Content string
}

// telegramMessage is a message sent, with its keyboard, if any, to a chat.
type telegramMessage struct {
	Text        string
	ReplyMarkup string
	ChatID      string
}

func newTelegramServer(t *testing.T) *telegramServer {
	t.Helper()
	server := &telegramServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(server.handle))

	previous := telegramAPIURL
	telegramAPIURL = server.URL
	t.Cleanup(func() {
		telegramAPIURL = previous
		server.Close()
	})
	return server
}

// answer makes body the answer of the requests of method, or of the downloads
// of the file of that ID, in place of the usual one, eg: an error or hangUp;
// "" for the usual one.
func (s *telegramServer) answer(method, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if body == "" {
		delete(s.answers, method)
		return
	}
	if s.answers == nil {
		s.answers = make(map[string]string)
	}
	s.answers[method] = body
}

// calls returns the number of requests of method so far.
func (s *telegramServer) calls(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[method]
}

func (s *telegramServer) handle(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:] // or the ID of the file downloaded
	s.mu.Lock()
	if s.requests == nil {
		s.requests = make(map[string]int)
	}
	s.requests[method]++
	answer, answered := s.answers[method]
	s.mu.Unlock()
	switch {
	case answered && answer == hangUp:
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
		return
	case answered:
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answer)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/file/") { // the download of a file of a user
		s.mu.Lock()
		content, ok := s.files[method]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, content)
		return
	}
	if method == "sendDocument" {
		s.receiveDocument(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	params := map[string]string{}
	if err := json.Unmarshal(body, &params); err != nil {
		_ = r.ParseForm()
		for key := range r.Form {
			params[key] = r.Form.Get(key)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	s.mu.Lock()
	defer s.mu.Unlock()
	switch method {
	case "getMe":
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"username":"xtrader"}}`))
	case "getFile":
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"file_id":%q,"file_path":%q}}`, params["file_id"],
			params["file_id"])
	case "setMyCommands":
		_ = json.Unmarshal([]byte(params["commands"]), &s.commands)
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	case "getMyCommands":
		payload, _ := json.Marshal(s.commands)
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":%s}`, payload)
	case "getUpdates":
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	case "sendMessage":
		s.messages = append(s.messages, telegramMessage{Text: params["text"], ReplyMarkup: params["reply_markup"],
			ChatID: params["chat_id"]})
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":42},"text":"ok"}}`))
	default:
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}
}

// receiveDocument records a document sent, a multipart form.
func (s *telegramServer) receiveDocument(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("document")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer func() { _ = file.Close() }()
	content, _ := io.ReadAll(file)
	s.mu.Lock()
	s.documents = append(s.documents, telegramDocument{Name: header.Filename, Caption: r.FormValue("caption"),
		Content: string(content)})
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":2,"chat":{"id":42},` +
		`"document":{"file_id":"sent-1","file_name":"prompt.txt"}}}`))
}

// sentDocuments returns the documents sent.
func (s *telegramServer) sentDocuments() []telegramDocument {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]telegramDocument(nil), s.documents...)
}

// userFile makes content the file of ID a user sends.
func (s *telegramServer) userFile(id, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files == nil {
		s.files = make(map[string]string)
	}
	s.files[id] = content
}

func (s *telegramServer) sent() []telegramMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]telegramMessage(nil), s.messages...)
}

func (s *telegramServer) last(t *testing.T) telegramMessage {
	t.Helper()
	messages := s.sent()
	require.NotEmpty(t, messages, "no message sent")
	return messages[len(messages)-1]
}

// waitFor waits for a message sent containing text.
func (s *telegramServer) waitFor(t *testing.T, text string) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, message := range s.sent() {
			if strings.Contains(message.Text, text) {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "no message with %q in %v", text, s.sent())
}

func newTestTelegram(t *testing.T, agents ...Agent) (*Telegram, *telegramServer) {
	t.Helper()
	return newTestTelegramWith(t, agents, nil)
}

// newTestTelegramWith creates the bot of the agents with options.
func newTestTelegramWith(t *testing.T, agents []Agent, options []TelegramOption) (*Telegram, *telegramServer) {
	t.Helper()
	server := newTelegramServer(t)
	dashboard, err := NewDashboard(agents)
	require.NoError(t, err)
	telegram, err := NewTelegram(dashboard, "token", []int64{telegramUserID}, options...)
	require.NoError(t, err)
	return telegram, server
}

// command is a command of the user, its payload the text after it.
func command(text string) *tb.Message {
	_, payload, _ := strings.Cut(text, " ")
	return &tb.Message{Text: text, Payload: payload, Sender: &tb.User{ID: telegramUserID}}
}

func TestTelegram_PickAgent(t *testing.T) {
	_, eth := newAgent(t, &fakeController{status: order.StatusRunning})
	telegram, server := newTestTelegram(t,
		Agent{Name: "eth", Address: eth.URL},
		Agent{Name: "down", Address: offlineAddress(t)},
	)

	telegram.handleStatus(command("/status"))
	assert.Equal(t, "No agent picked: send /agents to pick one.", server.last(t).Text)

	telegram.handleAgents(command("/agents"))
	message := server.last(t)
	assert.Equal(t, "Agents:\n• eth: online\n• down: offline", message.Text)
	assert.Contains(t, message.ReplyMarkup, `"/agent eth"`)
	assert.Contains(t, message.ReplyMarkup, `"/agent down"`)

	telegram.handleAgent(command("/agent nobody"))
	assert.Equal(t, `Unknown agent "nobody": send /agents to list them.`, server.last(t).Text)

	telegram.handleAgent(command("/agent eth"))
	assert.Equal(t, "[eth] Agent picked: the commands go to it.", server.last(t).Text)
	telegram.handleStatus(command("/status"))
	assert.Equal(t, "[eth] Status: running", server.last(t).Text)

	telegram.handleAgents(command("/agents"))
	assert.Equal(t, "Agents:\n• eth: online, picked\n• down: offline", server.last(t).Text)

	telegram.handleAgent(command("/agent down"))
	telegram.handleStatus(command("/status"))
	assert.Regexp(t, `^\[down\] agent down is offline at 127\.0\.0\.1:\d+$`, server.last(t).Text)

	telegram.handleAgent(command("/agent"))
	assert.True(t, strings.HasPrefix(server.last(t).Text, "Agents:"), "the agents to pick from")
}

func TestTelegram_SingleAgent(t *testing.T) {
	_, eth := newAgent(t, &fakeController{status: order.StatusStopped})
	telegram, server := newTestTelegram(t, Agent{Name: "eth", Address: eth.URL})

	telegram.handleStatus(command("/status"))
	assert.Equal(t, "[eth] Status: stopped", server.last(t).Text, "the only agent, without picking it")
}

func TestTelegram_ControlsOff(t *testing.T) {
	_, eth := newAgent(t, nil)
	telegram, server := newTestTelegram(t, Agent{Name: "eth", Address: eth.URL})

	for _, handle := range []func(*tb.Message){telegram.handleStatus, telegram.handleStart, telegram.handleStop,
		telegram.handleBalance} {
		handle(command("/status"))
		assert.Equal(t, "[eth] The Telegram controls of the agent are off: turn them on in the settings of the dashboard.",
			server.last(t).Text)
	}

	// the trade panel of the dashboard only
	chart, web := newAgent(t, nil)
	chart.SetOrderController(&fakeController{status: order.StatusRunning}, ui.ChannelWeb)
	telegram, server = newTestTelegram(t, Agent{Name: "web", Address: web.URL})
	telegram.handleStop(command("/stop"))
	assert.Equal(t, "[web] The Telegram controls of the agent are off: turn them on in the settings of the dashboard.",
		server.last(t).Text)
}

func TestTelegram_StartStop(t *testing.T) {
	controller := &fakeController{status: order.StatusRunning}
	_, eth := newAgent(t, controller)
	telegram, server := newTestTelegram(t, Agent{Name: "eth", Address: eth.URL})

	telegram.handleStart(command("/start"))
	assert.Equal(t, "[eth] Bot is already running.", server.last(t).Text)

	telegram.handleStop(command("/stop"))
	assert.True(t, strings.HasPrefix(server.last(t).Text, "[eth] Bot stopped. New orders are rejected"))
	assert.Equal(t, order.StatusStopped, controller.Status())

	telegram.handleStop(command("/stop"))
	assert.Equal(t, "[eth] Bot is already stopped.", server.last(t).Text)

	telegram.handleStart(command("/start"))
	assert.Equal(t, "[eth] Bot started.", server.last(t).Text)
	assert.Equal(t, order.StatusRunning, controller.Status())
}

func TestTelegram_Orders(t *testing.T) {
	controller := &fakeController{status: order.StatusRunning, asset: 2}
	chart, eth := newAgent(t, controller)
	chart.OnCandle(candleAt("ETHUSDT", testBase, 3000))
	telegram, server := newTestTelegram(t, Agent{Name: "eth", Address: eth.URL})

	for _, text := range []string{"/buy ETHUSDT 0.1 lot", "/sell ethusdt 50%", "/buy ETHUSDT 100",
		"/sell ETHUSDT 0.5 lots"} {
		telegram.handleOrder(command(text))
	}
	assert.Equal(t, []string{"BUY ETHUSDT size 0.1", "SELL ETHUSDT size 1", "BUY ETHUSDT quote 100",
		"SELL ETHUSDT size 0.5"}, controller.created(), "as the Telegram of the bots")
	assert.Empty(t, server.sent(), "the orders are notified by the relay")

	telegram.handleOrder(command("/buy ETHUSDT 1 lotz"))
	assert.Contains(t, server.last(t).Text, "[eth] Invalid command.\nExamples of usage:\n/buy BTCUSDT 100")
	telegram.handleOrder(command("/sell ETHUSDT"))
	assert.Contains(t, server.last(t).Text, "/sell BTCUSDT 0.1 lot")
	telegram.handleOrder(command("/buy ETHUSDT 0"))
	assert.Equal(t, "[eth] Invalid amount", server.last(t).Text)

	telegram.handleOrder(command("/buy GOLD 1 lot"))
	assert.Equal(t, "[eth] unknown pair", server.last(t).Text)

	controller.orderErr = order.ErrBotStopped
	telegram.handleOrder(command("/buy ETHUSDT 1 lot"))
	assert.Equal(t, "[eth] Bot is stopped, no order was created. Send /start to resume.", server.last(t).Text)

	controller.orderErr = fmt.Errorf("%w: perda diária de todos os agentes atingida", order.ErrEntriesBlocked)
	telegram.handleOrder(command("/buy ETHUSDT 1 lot"))
	assert.Contains(t, server.last(t).Text, "[eth] Entradas bloqueadas, nenhuma ordem criada:")
	assert.Contains(t, server.last(t).Text, "perda diária de todos os agentes atingida")

	controller.orderErr = errors.New("insufficient funds")
	telegram.handleOrder(command("/buy ETHUSDT 1 lot"))
	assert.Equal(t, "[eth] insufficient funds", server.last(t).Text)
}

func TestTelegram_Balance(t *testing.T) {
	controller := &fakeController{status: order.StatusRunning, account: model.Account{Balances: []model.Balance{
		{Asset: "ETH", Free: 1},
		{Asset: "USDT", Free: 400, Lock: 100},
	}}}
	chart, eth := newAgent(t, controller)
	chart.OnCandle(candleAt("ETHUSDT", testBase, 3000))
	telegram, server := newTestTelegram(t, Agent{Name: "eth", Address: eth.URL})

	telegram.handleBalance(command("/balance"))
	assert.Equal(t, "[eth] BALANCE\nETH: 1.0000 ≅ 3000.00 USDT\nUSDT: 500.0000\n-----\nTotal: 3500.0000",
		server.last(t).Text)
}

func TestTelegram_Profit(t *testing.T) {
	chart, eth := newAgent(t, nil)
	chart.OnCandle(candleAt("ETHUSDT", testBase, 3000))
	chart.OnCandle(candleAt("BTCUSDT", testBase, 40000))
	telegram, server := newTestTelegram(t, Agent{Name: "eth", Address: eth.URL})

	telegram.handleProfit(command("/profit"))
	assert.Equal(t, "[eth] No trades registered.", server.last(t).Text)

	for id, profit := range []float64{30, -10, 20} {
		chart.OnOrder(model.Order{ID: int64(id + 1), Pair: "ETHUSDT", Side: model.SideTypeSell,
			Status: model.OrderStatusTypeFilled, Profit: profit / 1000, ProfitValue: profit, UpdatedAt: testBase})
	}
	chart.OnOrder(model.Order{ID: 9, Pair: "ETHUSDT", Status: model.OrderStatusTypeFilled, UpdatedAt: testBase})
	telegram.handleProfit(command("/profit"))
	assert.Equal(t, "[eth] PAIR: ETHUSDT\nTrades: 3 (2 wins, 1 losses, 66.7%)\nProfit: 40.00 USDT\n"+
		"Profit factor: 5.00", server.last(t).Text, "the pairs without trades are left out")
}

func TestProfitSummary(t *testing.T) {
	_, ok := profitSummary(ui.Snapshot{Pair: "ETHUSDT"}, time.Time{})
	assert.False(t, ok)

	summary, ok := profitSummary(ui.Snapshot{Pair: "ETHUSDT", Quote: "USDT", Orders: []ui.Order{
		{Status: "FILLED", Profit: 0.01, ProfitValue: 5},
		{Status: "CANCELED", Profit: -0.5, ProfitValue: -100},
	}}, time.Time{})
	assert.True(t, ok)
	assert.Contains(t, summary, "Trades: 1 (1 wins, 0 losses, 100.0%)\nProfit: 5.00 USDT\nProfit factor: ∞")

	// as the account booked them, in its currency, when it did for all
	booked := func(profit float64) *float64 { return &profit }
	orders := []ui.Order{
		{Status: "FILLED", Profit: -0.002, ProfitValue: -14.49, AccountProfit: booked(-75.1), AccountCurrency: "BRL"},
		{Status: "FILLED", Profit: 0.0015, ProfitValue: 12.74, AccountProfit: booked(66.02), AccountCurrency: "BRL"},
	}
	summary, ok = profitSummary(ui.Snapshot{Pair: "BTCUSDm", Quote: "USD", AccountCurrency: "BRL", Orders: orders},
		time.Time{})
	assert.True(t, ok)
	assert.Contains(t, summary, "Trades: 2 (1 wins, 1 losses, 50.0%)\nProfit: -9.08 BRL\nProfit factor: 0.88")

	// one not booked yet: counted apart, awaited, never the one of the core
	orders[1].AccountProfit, orders[1].AccountCurrency = nil, ""
	summary, ok = profitSummary(ui.Snapshot{Pair: "BTCUSDm", Quote: "USD", AccountCurrency: "BRL", Orders: orders},
		time.Time{})
	assert.True(t, ok)
	assert.Contains(t, summary, "Trades: 1 (0 wins, 1 losses, 0.0%)\nProfit: -75.10 BRL\nProfit factor: 0.00\n"+
		"Awaiting the broker: 1 trade(s), their result not booked yet")
	assert.NotContains(t, summary, " USD")

	// a simulation: the ones of the core, told
	summary, ok = profitSummary(ui.Snapshot{Pair: "XAUUSDm", Quote: "USD", Simulated: true, Orders: []ui.Order{
		{Status: "FILLED", Profit: 0.01, ProfitValue: 5}}}, time.Time{})
	assert.True(t, ok)
	assert.Contains(t, summary, "Profit: 5.00 USD (simulated)")
}

// After a reset of the results of the agent, only the trades closed since the
// start of its current period count: their close, the last update of their
// order, at or after it.
func TestProfitSummary_SinceThePeriod(t *testing.T) {
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	orders := []ui.Order{
		{Status: "FILLED", Profit: -0.01, ProfitValue: -50, CreatedAt: start.Add(-2 * time.Hour).Unix(),
			UpdatedAt: start.Add(-time.Second).Unix()},
		{Status: "FILLED", Profit: 0.02, ProfitValue: 30, CreatedAt: start.Add(-time.Hour).Unix(),
			UpdatedAt: start.Unix()}, // opened before, closed at the start: the new period
		{Status: "FILLED", Profit: 0.01, ProfitValue: 10, CreatedAt: start.Add(time.Hour).Unix()},
		{Status: "FILLED", CreatedAt: start.Add(-3 * time.Hour).Unix()}, // an entry, without a profit
	}
	summary, ok := profitSummary(ui.Snapshot{Pair: "XAUUSDm", Quote: "USD", Orders: orders}, start)
	assert.True(t, ok)
	assert.Equal(t, "PAIR: XAUUSDm\nTrades: 2 (2 wins, 0 losses, 100.0%)\nProfit: 40.00 USD\nProfit factor: ∞\n"+
		"Since: 2026-10-02 12:00 UTC", summary)

	_, ok = profitSummary(ui.Snapshot{Pair: "XAUUSDm", Quote: "USD", Orders: orders[:1]}, start)
	assert.False(t, ok, "none since the start")
}

func TestTelegram_Help(t *testing.T) {
	telegram, server := newTestTelegram(t, Agent{Name: "eth", Address: offlineAddress(t)})
	telegram.handleHelp(command("/help"))
	help := server.last(t).Text
	assert.Contains(t, help, "/agents - List the agents")
	assert.Contains(t, help, "/buy - open a buy order")
}

// restartable serves a chart on an address it can stop and serve again.
type restartable struct {
	t       *testing.T
	address string
	handler http.Handler
	server  *http.Server
}

func (r *restartable) start() {
	listener, err := net.Listen("tcp", r.address)
	require.NoError(r.t, err)
	r.address = listener.Addr().String()
	r.server = &http.Server{Handler: r.handler}
	go func() { _ = r.server.Serve(listener) }()
}

func (r *restartable) stop() {
	require.NoError(r.t, r.server.Close())
}

func TestTelegram_Relay(t *testing.T) {
	previous := relayRetry
	relayRetry = 20 * time.Millisecond
	t.Cleanup(func() { relayRetry = previous })

	chart, err := ui.New()
	require.NoError(t, err)
	agent := &restartable{t: t, address: "127.0.0.1:0", handler: chart.Handler()}
	agent.start()
	t.Cleanup(agent.stop)

	telegram, server := newTestTelegram(t, Agent{Name: "eth", Address: agent.address})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	telegram.ctx = ctx
	telegram.SyncRelays([]string{"eth"})

	// once the relay follows the events
	filled := model.Order{ID: 7, Pair: "ETHUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Price: 3000, Quantity: 0.5}
	require.Eventually(t, func() bool {
		chart.OnOrder(filled)
		return len(server.sent()) > 0
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "[eth] ✅ ORDER FILLED - ETHUSDT\n-----\n"+filled.String(), server.sent()[0].Text)

	chart.OnOrder(model.Order{ID: 8, Pair: "ETHUSDT", Status: model.OrderStatusTypePendingCancel})
	chart.Notify("[PROFIT] 12.000000 USDT (1.2 %)")
	server.waitFor(t, "[eth] [PROFIT] 12.000000 USDT (1.2 %)")
	chart.OnError(&exchange.OrderError{Err: errors.New("insufficient funds"), Pair: "ETHUSDT", Quantity: 1})
	server.waitFor(t, "[eth] 🛑 ERROR\n-----\nPair: ETHUSDT\nQuantity: 1.0000\n-----\ninsufficient funds")
	for _, message := range server.sent() {
		assert.NotContains(t, message.Text, "PENDING_CANCEL", "only the statuses the bots notified")
	}

	agent.stop()
	server.waitFor(t, "[eth] ⚠️ Agent offline at "+agent.address+".")
	agent.start()
	server.waitFor(t, "[eth] ✅ Agent back online.")
}

func TestTelegram_Start(t *testing.T) {
	telegram, server := newTestTelegram(t, Agent{Name: "eth", Address: offlineAddress(t)},
		Agent{Name: "rk", Address: offlineAddress(t)})
	ctx, cancel := context.WithCancel(context.Background())
	telegram.Start(ctx)
	server.waitFor(t, "Controller initialized, agents: eth, rk.\nPick the one of the commands with /agents.")
	cancel()

	for _, message := range server.sent() {
		assert.NotContains(t, message.Text, "offline", "the agents never online aren't notified")
	}
}

// fakeLLM is a strategy of a language model, which may be asked again.
type fakeLLM struct {
	mu         sync.Mutex
	reanalyzed []string
	refuse     error
}

func (f *fakeLLM) Timeframe() string                                     { return "15m" }
func (f *fakeLLM) WarmupPeriod() int                                     { return 1 }
func (f *fakeLLM) OnCandle(*model.Dataframe, service.Broker)             {}
func (f *fakeLLM) Indicators(*model.Dataframe) []strategy.ChartIndicator { return nil }
func (f *fakeLLM) LLM() string                                           { return "openai gpt-test" }

func (f *fakeLLM) Reanalyze(pair string, channel ui.Channel, note string) (ui.ReanalysisResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != nil {
		return ui.ReanalysisResponse{}, f.refuse
	}
	f.reanalyzed = append(f.reanalyzed, strings.TrimSpace(pair+" "+string(channel)+" | "+note))
	return ui.ReanalysisResponse{Status: "queued",
		Message: "The model is asked about " + pair + " on the next tick of the candle in progress."}, nil
}

func TestTelegram_Reanalyze(t *testing.T) {
	llm := &fakeLLM{}
	chart, err := ui.New(ui.WithStrategyIndicators(llm))
	require.NoError(t, err)
	chart.OnCandle(candleAt("BTCUSDm", testBase, 84000))
	chart.SetOrderController(&fakeController{status: order.StatusRunning}, ui.ChannelTelegram)
	chart.SetReanalysis(true)
	srv := httptest.NewServer(chart.Handler())
	t.Cleanup(srv.Close)
	telegram, server := newTestTelegram(t, Agent{Name: "llm", Address: srv.URL})

	telegram.handleReanalyze(command("/reanalyze"))
	assert.Equal(t, "[llm] 🔄 The model is asked about BTCUSDm on the next tick of the candle in progress. "+
		"The decision is notified once taken.", server.last(t).Text, "the only pair")
	assert.Equal(t, []string{"BTCUSDm telegram |"}, llm.reanalyzed)

	chart.OnCandle(candleAt("XAUUSDm", testBase, 4300))
	telegram.handleReanalyze(command("/reanalyze"))
	assert.Equal(t, "[llm] Name the pair first, eg: /reanalyze BTCUSDm, with a note after it if any (pairs: "+
		"BTCUSDm, XAUUSDm)", server.last(t).Text)
	telegram.handleReanalyze(command("/reanalyze xauusdm"))
	assert.Equal(t, []string{"BTCUSDm telegram |", "XAUUSDm telegram |"}, llm.reanalyzed)
	telegram.handleReanalyze(command("/reanalyze EURUSD"))
	assert.Contains(t, server.last(t).Text, "Name the pair first", "not a pair of the agent")

	// a note for the model after the pair
	telegram.handleReanalyze(command("/reanalyze XAUUSDm Estamos no lucro, acredito que você pode encerrar a " +
		"posição? O que acha?"))
	assert.Equal(t, "XAUUSDm telegram | Estamos no lucro, acredito que você pode encerrar a posição? O que acha?",
		llm.reanalyzed[len(llm.reanalyzed)-1])
	telegram.handleReanalyze(command("/reanalyze XAUUSDm " + strings.Repeat("x", ui.MaxReanalysisNote+1)))
	assert.Contains(t, server.last(t).Text, "the note has 501 characters, 500 at most")

	llm.mu.Lock()
	llm.refuse = errors.New("openai gpt-test is rate limited for 20s: ask again later")
	llm.mu.Unlock()
	telegram.handleReanalyze(command("/reanalyze BTCUSDm"))
	assert.Equal(t, "[llm] openai gpt-test is rate limited for 20s: ask again later", server.last(t).Text)
	llm.mu.Lock()
	llm.refuse = nil
	asked := len(llm.reanalyzed)
	llm.mu.Unlock()

	// its reanalysis switched off in its settings, its Telegram controls on
	chart.SetReanalysis(false)
	telegram.handleReanalyze(command("/reanalyze BTCUSDm"))
	assert.Equal(t, "[llm] The reanalysis of the agent is off: turn on Permitir reanálise in its settings on the "+
		"dashboard.", server.last(t).Text)
	assert.Len(t, llm.reanalyzed, asked, "the model not asked")
	chart.SetReanalysis(true)
	telegram.handleReanalyze(command("/reanalyze BTCUSDm"))
	assert.Len(t, llm.reanalyzed, asked+1, "asked again, switched on")

	// a strategy of another kind, and an agent without the Telegram controls
	plain, eth := newAgent(t, &fakeController{status: order.StatusRunning})
	plain.OnCandle(candleAt("ETHUSDT", testBase, 3000))
	telegram, server = newTestTelegram(t, Agent{Name: "eth", Address: eth.URL})
	telegram.handleReanalyze(command("/reanalyze"))
	assert.Equal(t, "[eth] The agent doesn't trade with a language model: there is nothing to reanalyze.",
		server.last(t).Text)

	webOnly, err := ui.New(ui.WithStrategyIndicators(llm))
	require.NoError(t, err)
	webOnly.SetOrderController(&fakeController{status: order.StatusRunning}, ui.ChannelWeb)
	webOnly.SetReanalysis(true) // allowed, from the channels the bot is controlled from only
	web := httptest.NewServer(webOnly.Handler())
	t.Cleanup(web.Close)
	telegram, server = newTestTelegram(t, Agent{Name: "web", Address: web.URL})
	telegram.handleReanalyze(command("/reanalyze"))
	assert.Equal(t, "[web] The Telegram controls of the agent are off: turn them on in the settings of the dashboard.",
		server.last(t).Text)
}

func TestReanalysisTarget(t *testing.T) {
	one, two := []string{"BTCUSDm"}, []string{"BTCUSDm", "XAUUSDm"}
	for _, tc := range []struct {
		payload    string
		pairs      []string
		pair, note string
	}{
		{"", one, "BTCUSDm", ""},
		{"", two, "", ""},
		{"xauusdm", two, "XAUUSDm", ""},
		{" XAUUSDm  in profit,\nclose it? ", two, "XAUUSDm", "in profit,\nclose it?"},
		{"XAUUSDm\nclose it?", two, "XAUUSDm", "close it?"},
		{"in profit, close it?", one, "BTCUSDm", "in profit, close it?"},
		{"in profit, close it?", two, "", "in profit, close it?"},
	} {
		pair, note := reanalysisTarget(tc.payload, tc.pairs)
		assert.Equal(t, tc.pair, pair, tc.payload)
		assert.Equal(t, tc.note, note, tc.payload)
	}
}

// Anyone can write to the bot: an update of someone else is refused and
// logged by the ID of its sender only, never its content, a line a minute at
// most.
func TestTelegram_RefusedUpdatesLogTheSenderOnly(t *testing.T) {
	telegram, _ := newTestTelegram(t)
	logs := captureLogs(t)

	stranger := &tb.Message{Text: "/buy BTCUSDT 1 my-secret-text", Payload: "BTCUSDT 1 my-secret-text",
		Sender: &tb.User{ID: 666, Username: "eve-the-stranger", FirstName: "Eve"},
		Chat:   &tb.Chat{ID: 666, Username: "eve-the-stranger"}}
	require.False(t, telegram.authorized(&tb.Update{ID: 1, Message: stranger}))
	require.False(t, telegram.authorized(&tb.Update{ID: 2, Callback: &tb.Callback{Data: "my-secret-callback",
		Sender: &tb.User{ID: 667, Username: "eve-the-stranger"}}}), "an update without a message")
	require.True(t, telegram.authorized(&tb.Update{ID: 3, Message: command("/status")}), "the user")
	for i := range 100 {
		require.False(t, telegram.authorized(&tb.Update{ID: 4 + i, Message: stranger}))
	}

	text := logs.String()
	assert.Contains(t, text, "the last of the user 666")
	assert.Equal(t, 1, strings.Count(text, "telegram: refused"), "a line a minute at most: %s", text)
	for _, content := range []string{"my-secret", "eve-the-stranger", "Eve", "BTCUSDT"} {
		assert.NotContains(t, text, content)
	}

	// a minute after, the count of the ones in between
	var r refusals
	now := time.Now()
	r.log(666, now)
	for range 5 {
		r.log(666, now.Add(time.Second))
	}
	r.log(0, now.Add(refusalLogEvery))
	text = logs.String()
	assert.Contains(t, text, "telegram: refused 6 update(s) of users not allowed, the last without a sender")
	assert.Equal(t, 3, strings.Count(text, "telegram: refused"), text)
}
