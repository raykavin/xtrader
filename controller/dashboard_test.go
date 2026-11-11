package controller

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/ui"
)

var testBase = time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC)

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(body)
}

func postJSON(t *testing.T, url, body string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(data)
}

func candleAt(pair string, ts time.Time, close float64) model.Candle {
	return model.Candle{Pair: pair, Time: ts, Open: close, Close: close, High: close, Low: close, Volume: 1,
		Complete: true}
}

// fakeController is the order controller of an agent. The methods the tests
// don't need are the ones of the nil interface.
type fakeController struct {
	ui.OrderController

	mu      sync.Mutex
	status  order.Status
	account model.Account
	asset   float64
}

func (f *fakeController) Status() order.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeController) Start() { f.set(order.StatusRunning) }
func (f *fakeController) Stop()  { f.set(order.StatusStopped) }

func (f *fakeController) set(status order.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func (f *fakeController) Account() (model.Account, error) { return f.account, nil }

func (f *fakeController) Position(string) (float64, float64, error) { return f.asset, 1000, nil }

// newAgent serves the API of a chart, with its control panel when controller
// is set.
func newAgent(t *testing.T, controller ui.OrderController) (*ui.Chart, *httptest.Server) {
	t.Helper()
	chart, err := ui.New()
	require.NoError(t, err)
	if controller != nil {
		chart.SetOrderController(controller)
	}
	srv := httptest.NewServer(chart.Handler())
	t.Cleanup(srv.Close)
	return chart, srv
}

func newTestDashboard(t *testing.T, options ...DashboardOption) *Dashboard {
	t.Helper()
	d, err := NewDashboard([]Agent{{Name: "bot", Address: "localhost:5555"}}, options...)
	require.NoError(t, err)
	return d
}

// newDashboardServer serves the dashboard of the agents with a bundle, or the
// error page of bundleErr.
func newDashboardServer(t *testing.T, agents []Agent, bundleErr error) *httptest.Server {
	t.Helper()
	d, err := NewDashboard(agents)
	require.NoError(t, err)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log('ok')"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.css"), []byte("body{}"), 0o644))
	srv := httptest.NewServer(asAdmin(d.router(bundle{dir: dir, version: "v1.0.0"}, bundleErr)))
	t.Cleanup(srv.Close)
	return srv
}

// offlineAddress returns the address of a server no longer listening.
func offlineAddress(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	address := srv.Listener.Addr().String()
	srv.Close()
	return address
}

func decodeError(t *testing.T, body string) string {
	t.Helper()
	var response ui.ErrorResponse
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	return response.Error
}

func TestNewDashboard(t *testing.T) {
	d, err := NewDashboard(nil)
	require.NoError(t, err, "a database without agents yet")
	require.Empty(t, d.agents.list())

	for _, name := range []string{"", "my bot", "a/b", ".", "..", "-bot"} {
		_, err := NewDashboard([]Agent{{Name: name, Address: "localhost:5555"}})
		require.ErrorContains(t, err, "invalid agent name", name)
	}

	_, err = NewDashboard([]Agent{{Name: "bot", Address: "localhost:5555"}, {Name: "bot", Address: "localhost:5556"}})
	require.ErrorContains(t, err, "listed twice")

	for _, address := range []string{"", "ftp://localhost:21", "http://", "http://local host"} {
		_, err := NewDashboard([]Agent{{Name: "bot", Address: address}})
		require.ErrorContains(t, err, "agent bot", address)
	}
	// the API of an agent answers whoever reaches it: the dashboard reaches
	// the ones of its machine only, never sending its requests elsewhere
	for _, address := range []string{"https://bots.local/rk", "10.0.0.5:5555", "169.254.169.254:80",
		"0.0.0.0:5555", "[::]:5555", "localhost.evil.example:5555", "[::ffff:10.0.0.5]:5555"} {
		_, err := NewDashboard([]Agent{{Name: "bot", Address: address}})
		require.ErrorContains(t, err, "isn't on the loopback", address)
	}

	d, err = NewDashboard([]Agent{
		{Name: "crossover", Address: "localhost:5555"},
		{Name: "rk.trend_2", Address: "https://LOCALHOST:5600/rk"},
		{Name: "v4", Address: "127.0.0.2:5555"},
		{Name: "v6", Address: "[::1]:5555"},
	})
	require.NoError(t, err)
	require.Len(t, d.agents.list(), 4)
	crossover := agentNamed(t, d, "crossover")
	assert.Equal(t, "http://localhost:5555/api", crossover.api.String())
	assert.Equal(t, "/api", crossover.api.Path)
	assert.Equal(t, "https://LOCALHOST:5600/rk/api", agentNamed(t, d, "rk.trend_2").api.String())
	assert.Equal(t, "http://[::1]:5555/api", agentNamed(t, d, "v6").api.String())
}

// agentNamed returns the agent of name of the dashboard.
func agentNamed(t *testing.T, d *Dashboard, name string) *agent {
	t.Helper()
	a, ok := d.agents.get(name)
	require.True(t, ok, name)
	return a
}

func TestSetAgents_KeepsTheProxiesOfTheSameAddress(t *testing.T) {
	d, err := NewDashboard([]Agent{{Name: "llm", Address: "localhost:5580"}, {Name: "gold", Address: "localhost:5555"}})
	require.NoError(t, err)
	llm, gold := agentNamed(t, d, "llm"), agentNamed(t, d, "gold")

	require.NoError(t, d.SetAgents([]Agent{{Name: "gold", Address: "localhost:5556"}, {Name: "llm",
		Address: "localhost:5580"}}))
	require.Same(t, llm.proxy, agentNamed(t, d, "llm").proxy, "the same address, the same proxy")
	require.NotSame(t, gold.proxy, agentNamed(t, d, "gold").proxy, "a new address, a new proxy")
	require.Equal(t, "gold", d.agents.list()[0].Name, "in the new order")

	err = d.SetAgents([]Agent{{Name: "a b", Address: "localhost:1"}, {Name: "llm", Address: "localhost:5580"},
		{Name: "bad", Address: "localhost:bad"}})
	require.ErrorContains(t, err, "invalid agent name")
	require.ErrorContains(t, err, "agent bad")
	require.Len(t, d.agents.list(), 1, "the invalid ones left out, the others served")
	require.NoError(t, d.SetAgents(nil))
	_, ok := d.agents.get("llm")
	require.False(t, ok, "disabled")
}

func TestDashboard_IndexAndAssets(t *testing.T) {
	srv := newDashboardServer(t, []Agent{{Name: "bot", Address: offlineAddress(t)}}, nil)

	resp, body := get(t, srv.URL+"/")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, `/ui/v1.0.0/app.js`)
	assert.Contains(t, body, `/ui/v1.0.0/app.css`)
	assert.Contains(t, body, `"v1.0.0"`)

	resp, body = get(t, srv.URL+"/ui/v1.0.0/app.js")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "console.log('ok')", body)
	assert.Contains(t, resp.Header.Get("Cache-Control"), "immutable")

	resp, _ = get(t, srv.URL+"/ui/v1.0.0/missing.js")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestDashboard_LocalBundleIsNotCached(t *testing.T) {
	d := newTestDashboard(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.js"), []byte("1"), 0o644))
	srv := httptest.NewServer(asAdmin(d.router(bundle{dir: dir, version: versionLocal}, nil)))
	defer srv.Close()

	resp, _ := get(t, srv.URL+"/ui/local/app.js")
	assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
}

func TestDashboard_ErrorPageWhenBundleMissing(t *testing.T) {
	srv := newDashboardServer(t, []Agent{{Name: "bot", Address: offlineAddress(t)}},
		errors.New("boom: open /home/op/.cache/xtrader/ui/v1.0.0/app.js: no network"))

	for _, path := range []string{"/", "/login", "/settings", "/performance"} {
		resp, body := get(t, srv.URL+path)
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, path)
		assert.NotContains(t, body, "boom", "the reason in the log only, the login being public")
		assert.NotContains(t, body, "/home/op", path)
		assert.Contains(t, body, "log do controller", path)
		assert.Contains(t, body, "ui.dir: web/dist", path)
		assert.Equal(t, errorPolicy, resp.Header.Get("Content-Security-Policy"), path)
	}

	// API still works
	resp, _ := get(t, srv.URL+"/api/agents")
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// assets are not served
	resp, _ = get(t, srv.URL+"/ui/v1.0.0/app.js")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestDashboard_HandlerWithoutBundle(t *testing.T) {
	d := newTestDashboard(t, WithCacheDir(t.TempDir()), withReleaseURL("http://127.0.0.1:0/releases"),
		WithUIVersion("v0.0.1"))

	srv := httptest.NewServer(d.Handler(context.Background()))
	defer srv.Close()

	resp, body := get(t, srv.URL+"/")
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Contains(t, body, "Painel indisponível")
}

func TestDashboard_Agents(t *testing.T) {
	_, bot := newAgent(t, nil)
	offline := offlineAddress(t)
	srv := newDashboardServer(t, []Agent{
		{Name: "ninjaagent", Address: offline},
		{Name: "crossover", Address: bot.URL},
	}, nil)

	resp, body := get(t, srv.URL+"/api/agents")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var response ui.AgentsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	assert.Equal(t, []ui.AgentStatus{
		{Name: "ninjaagent", Address: offline, Online: false},
		{Name: "crossover", Address: bot.URL, Online: true},
	}, response.Agents, "in the order of the configuration")

	d, err := NewDashboard([]Agent{{Name: "ninjaagent", Address: offline}, {Name: "crossover", Address: bot.URL}})
	require.NoError(t, err)
	assert.True(t, d.Online(context.Background(), "crossover"))
	assert.False(t, d.Online(context.Background(), "ninjaagent"))
	assert.False(t, d.Online(context.Background(), "unknown"))
}

func TestDashboard_ProxiesTheAgentAPI(t *testing.T) {
	eth, ethBot := newAgent(t, &fakeController{status: order.StatusRunning})
	eth.OnCandle(candleAt("ETHUSDT", testBase, 3000))
	eth.OnOrder(model.Order{ID: 7, Pair: "ETHUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Price: 3000, Quantity: 2, CreatedAt: testBase, UpdatedAt: testBase})
	btc, btcBot := newAgent(t, nil)
	btc.OnCandle(candleAt("BTCUSDT", testBase, 40000))

	srv := newDashboardServer(t, []Agent{
		{Name: "eth", Address: ethBot.Listener.Addr().String()},
		{Name: "btc", Address: btcBot.URL},
		{Name: "down", Address: offlineAddress(t)},
	}, nil)

	// each agent answers with its own state
	resp, body := get(t, srv.URL+"/api/agents/eth/pairs")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"pairs":["ETHUSDT"]}`, body)
	_, body = get(t, srv.URL+"/api/agents/btc/pairs")
	assert.JSONEq(t, `{"pairs":["BTCUSDT"]}`, body)

	resp, body = get(t, srv.URL+"/api/agents/eth/ethusdt/snapshot")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var snap ui.Snapshot
	require.NoError(t, json.Unmarshal([]byte(body), &snap))
	assert.Equal(t, "ETHUSDT", snap.Pair)
	require.Len(t, snap.Orders, 1)
	assert.Equal(t, int64(7), snap.Orders[0].ID)

	resp, _ = get(t, srv.URL+"/api/agents/btc/ETHUSDT/snapshot")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "the pair of another agent")

	resp, body = get(t, srv.URL+"/api/agents/eth/ETHUSDT/orders.csv")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Disposition"), "history_ETHUSDT.csv")
	assert.Contains(t, body, "FILLED,BUY,7")

	// the controls reach the bot of the agent
	resp, body = postJSON(t, srv.URL+"/api/agents/eth/controls/stop", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"enabled":true,"status":"stopped","bot_control":true}`, body)

	// the proxy is the trade panel, whatever the browser tells: of an agent
	// with the Telegram controls alone, the orders refused, the bot stopped
	telegramOnly, telegramBot := newAgent(t, nil)
	controller := &fakeController{status: order.StatusRunning}
	telegramOnly.SetOrderController(controller, ui.ChannelTelegram)
	proxy := newDashboardServer(t, []Agent{{Name: "telegram", Address: telegramBot.URL}}, nil)
	req, err := http.NewRequest(http.MethodGet, proxy.URL+"/api/agents/telegram/controls/balance", nil)
	require.NoError(t, err)
	req.Header.Set(ui.ChannelHeader, string(ui.ChannelTelegram))
	spoofed, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = spoofed.Body.Close()
	assert.Equal(t, http.StatusNotFound, spoofed.StatusCode)
	resp, body = postJSON(t, proxy.URL+"/api/agents/telegram/controls/stop", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the dashboard stops the bot whatever its trade panel")
	assert.JSONEq(t, `{"enabled":false,"status":"stopped","bot_control":true}`, body)
	assert.Equal(t, order.StatusStopped, controller.Status())

	resp, body = postJSON(t, srv.URL+"/api/agents/btc/controls/stop", "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "Controles do agente desligados neste canal: ligue-os em "+
		"Configurações › Agentes.", decodeError(t, body))

	resp, body = get(t, srv.URL+"/api/agents/nobody/pairs")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "Agente desconhecido.", decodeError(t, body))

	resp, body = get(t, srv.URL+"/api/agents/down/pairs")
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.Contains(t, decodeError(t, body), "O agente down não responde")
}

// publishCandles publishes a new candle of the chart every few milliseconds,
// until the test ends, for a subscriber to get one once it follows the events.
func publishCandles(t *testing.T, chart *ui.Chart, pair string) {
	t.Helper()
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			case <-time.After(10 * time.Millisecond):
				chart.OnCandle(candleAt(pair, testBase.Add(time.Duration(i)*time.Hour), 3000))
			}
		}
	}()
}

func TestDashboard_ProxiesTheAgentEvents(t *testing.T) {
	c, bot := newAgent(t, nil)
	srv := newDashboardServer(t, []Agent{{Name: "bot", Address: bot.URL}}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/agents/bot/events", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, ": connected\n", line, "flushed as it comes")

	publishCandles(t, c, "ETHUSDT")
	for line != "event: candle\n" {
		line, err = reader.ReadString('\n')
		require.NoError(t, err)
	}
	line, err = reader.ReadString('\n')
	require.NoError(t, err)
	var event ui.CandleEvent
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
	assert.Equal(t, "ETHUSDT", event.Pair)
}

func TestDashboard_Call(t *testing.T) {
	c, bot := newAgent(t, nil)
	c.OnCandle(candleAt("ETHUSDT", testBase, 3000))
	d, err := NewDashboard([]Agent{{Name: "bot", Address: bot.URL}, {Name: "down", Address: offlineAddress(t)}})
	require.NoError(t, err)
	ctx := context.Background()

	var pairs ui.PairsResponse
	require.NoError(t, d.call(ctx, agentNamed(t, d, "bot"), http.MethodGet, "pairs", nil, &pairs))
	assert.Equal(t, []string{"ETHUSDT"}, pairs.Pairs)

	// the errors of the API, in JSON or in text
	var apiErr *apiError
	err = d.call(ctx, agentNamed(t, d, "bot"), http.MethodPost, "controls/start", nil, nil)
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, &apiError{Status: http.StatusNotFound,
		Message: "Controles do agente desligados neste canal: ligue-os em Configurações › Agentes."}, apiErr)
	err = d.call(ctx, agentNamed(t, d, "bot"), http.MethodGet, "BTCUSDT/snapshot", nil, nil)
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, &apiError{Status: http.StatusNotFound, Message: "Ativo desconhecido."}, apiErr)

	err = d.call(ctx, agentNamed(t, d, "down"), http.MethodGet, "pairs", nil, nil)
	require.ErrorContains(t, err, "O agente down não responde em")
	err = d.events(ctx, agentNamed(t, d, "down"), func() { t.Error("connected") }, nil)
	require.ErrorContains(t, err, "O agente down não responde em")
}
