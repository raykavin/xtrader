package ui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
)

// fakeLLM is a strategy of a language model keeping its decisions, which may be
// asked again.
type fakeLLM struct {
	fakeStrategy

	mu         sync.Mutex
	decisions  []Decision
	orders     map[int64]string
	listener   func(Decision)
	reanalyzed []string // pair and channel of each reanalysis
	refuse     error
}

func (f *fakeLLM) LLM() string { return "openai gpt-test" }

func (f *fakeLLM) Decisions(pair string, limit int) ([]Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var list []Decision
	for i := len(f.decisions) - 1; i >= 0 && len(list) < limit; i-- {
		if pair == "" || f.decisions[i].Pair == pair {
			list = append(list, f.decisions[i])
		}
	}
	return list, nil
}

func (f *fakeLLM) Decision(id string) (Decision, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.decisions {
		if d.ID == id {
			return d, true, nil
		}
	}
	return Decision{}, false, nil
}

func (f *fakeLLM) OrderDecision(orderID int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.orders[orderID]
}

func (f *fakeLLM) Attachment(id string, index int) (string, []byte, error) {
	switch {
	case id == "d1" && index == 0:
		return "image/png", []byte("PNG"), nil
	case id == "d1" && index == 1:
		return "image/svg+xml", []byte("<svg onload=alert(1)>"), nil
	}
	return "", nil, ErrUnknownDecision
}

func (f *fakeLLM) ListenDecisions(listener func(Decision)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listener = listener
}

func (f *fakeLLM) Reanalyze(pair string, channel Channel, note string) (ReanalysisResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != nil {
		return ReanalysisResponse{}, f.refuse
	}
	f.reanalyzed = append(f.reanalyzed, strings.TrimSpace(pair+" "+string(channel)+" "+note))
	return ReanalysisResponse{Status: "queued", Message: "asked on the next tick"}, nil
}

func newLLMChart(t *testing.T) (*Chart, *fakeLLM) {
	t.Helper()
	llm := &fakeLLM{orders: map[int64]string{7: "d1"}, decisions: []Decision{
		{ID: "d0", Pair: "BTCUSDm", Action: "WAIT", Reason: "no level", Outcome: "applied"},
		{ID: "d1", Pair: "BTCUSDm", Action: "ENTER_LONG", Reason: "a support held", Outcome: "applied",
			OrderIDs: []int64{7}, Attachments: []DecisionAttachment{{Index: 0, MediaType: "image/png", Size: 3}}},
		{ID: "d2", Pair: "XAUUSDm", Action: "WAIT", Reason: "the range", Outcome: "applied"},
	}}
	c := newTestChart(t, WithStrategyIndicators(llm))
	at := time.Date(2026, 9, 27, 13, 0, 0, 0, time.UTC)
	c.OnCandle(candleAt("BTCUSDm", at, 84000))
	c.OnCandle(candleAt("XAUUSDm", at, 4300))
	return c, llm
}

// send sends a request from channel, the trade panel without one.
func send(t *testing.T, method, url, body string, channel Channel) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	require.NoError(t, err)
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json") // as the controller sends them
	}
	if channel != "" {
		req.Header.Set(ChannelHeader, string(channel))
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(data)
}

// The strategy of a language model is told apart: its snapshot names it and
// its orders carry their decision; the others go without.
func TestLLM_Identification(t *testing.T) {
	c, _ := newLLMChart(t)
	c.OnOrder(model.Order{ID: 7, Pair: "BTCUSDm", Side: model.SideTypeBuy, Status: model.OrderStatusTypeFilled})
	c.OnOrder(model.Order{ID: 8, Pair: "BTCUSDm", Side: model.SideTypeSell, Status: model.OrderStatusTypeNew})
	srv := newTestServer(t, c)

	_, body := get(t, srv.URL+"/api/BTCUSDm/snapshot")
	var snapshot Snapshot
	require.NoError(t, json.Unmarshal([]byte(body), &snapshot))
	require.Equal(t, &LLMInfo{Model: "openai gpt-test", Decisions: true}, snapshot.LLM)
	require.Equal(t, "d1", snapshot.Orders[0].DecisionID, "the order placed by a decision")
	require.Empty(t, snapshot.Orders[1].DecisionID)

	plain := newTestChart(t, WithStrategyIndicators(fakeStrategy{}))
	plain.OnCandle(candleAt("BTCUSDm", time.Date(2026, 9, 27, 13, 0, 0, 0, time.UTC), 84000))
	plain.OnOrder(model.Order{ID: 7, Pair: "BTCUSDm", Side: model.SideTypeBuy})
	_, body = get(t, newTestServer(t, plain).URL+"/api/BTCUSDm/snapshot")
	require.NotContains(t, body, `"llm"`, "a strategy of another kind")
	require.NotContains(t, body, `"decision_id"`)
	resp, _ := get(t, newTestServer(t, plain).URL+"/api/decisions")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestLLM_Decisions(t *testing.T) {
	c, _ := newLLMChart(t)
	srv := newTestServer(t, c)

	resp, body := get(t, srv.URL+"/api/decisions?pair=btcusdm&limit=1")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list DecisionsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	require.Len(t, list.Decisions, 1)
	require.Equal(t, "d1", list.Decisions[0].ID, "the newest of the pair, its case aside")

	_, body = get(t, srv.URL+"/api/decisions")
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	require.Len(t, list.Decisions, 3, "every pair")

	resp, body = get(t, srv.URL+"/api/decisions/d1")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var decision Decision
	require.NoError(t, json.Unmarshal([]byte(body), &decision))
	require.Equal(t, "a support held", decision.Reason)

	for path, status := range map[string]int{
		"/api/decisions/none":        http.StatusNotFound,
		"/api/decisions?pair=EURUSD": http.StatusNotFound,
		"/api/decisions?limit=0":     http.StatusBadRequest,
		"/api/decisions?limit=x":     http.StatusBadRequest,
	} {
		resp, _ := get(t, srv.URL+path)
		require.Equal(t, status, resp.StatusCode, path)
	}
}

// The images are served as they are, the other files as downloads.
func TestLLM_Attachments(t *testing.T) {
	c, _ := newLLMChart(t)
	srv := newTestServer(t, c)

	resp, body := get(t, srv.URL+"/api/decisions/d1/attachments/0")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "image/png", resp.Header.Get("Content-Type"))
	require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	require.Equal(t, "PNG", body)

	resp, _ = get(t, srv.URL+"/api/decisions/d1/attachments/1")
	require.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"), "an SVG isn't rendered")
	require.Equal(t, "attachment", resp.Header.Get("Content-Disposition"))

	for _, path := range []string{"/api/decisions/d1/attachments/9", "/api/decisions/d0/attachments/0",
		"/api/decisions/d1/attachments/x"} {
		resp, _ := get(t, srv.URL+path)
		require.Equal(t, http.StatusNotFound, resp.StatusCode, path)
	}
}

// The reanalysis follows its own switch, from the channels the bot is started
// and stopped from, the web dashboard whatever its trade panel, for a strategy
// of a language model only.
func TestLLM_Reanalyze(t *testing.T) {
	c, llm := newLLMChart(t)
	controller := &fakeController{status: order.StatusRunning}
	c.SetOrderController(controller, ChannelTelegram)
	c.SetReanalysis(true)
	srv := newTestServer(t, c)

	_, body := send(t, http.MethodGet, srv.URL+"/api/controls", "", ChannelTelegram)
	require.JSONEq(t, `{"enabled":true,"status":"running","bot_control":true,"reanalysis":true}`, body)
	_, body = send(t, http.MethodGet, srv.URL+"/api/controls", "", "")
	require.JSONEq(t, `{"enabled":false,"status":"running","bot_control":true,"reanalysis":true}`, body,
		"the trade panel disabled: no orders, but the reanalysis, and the bot started and stopped")

	resp, body := send(t, http.MethodPost, srv.URL+"/api/controls/reanalyze", `{"pair":"BTCUSDm"}`, ChannelWeb)
	require.Equal(t, http.StatusOK, resp.StatusCode, "from the web without its trade panel: %s", body)
	require.Equal(t, []string{"BTCUSDm web"}, llm.reanalyzed)
	llm.reanalyzed = nil

	resp, body = send(t, http.MethodPost, srv.URL+"/api/controls/reanalyze", `{"pair":"btcusdm"}`, ChannelTelegram)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.JSONEq(t, `{"status":"queued","message":"asked on the next tick"}`, body)
	require.Equal(t, []string{"BTCUSDm telegram"}, llm.reanalyzed)

	// a note for the model, trimmed, MaxReanalysisNote characters at most
	resp, _ = send(t, http.MethodPost, srv.URL+"/api/controls/reanalyze",
		`{"pair":"BTCUSDm","note":"  in profit: close it?  "}`, ChannelTelegram)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "BTCUSDm telegram in profit: close it?", llm.reanalyzed[len(llm.reanalyzed)-1])
	long := strings.Repeat("ã", MaxReanalysisNote)
	resp, _ = send(t, http.MethodPost, srv.URL+"/api/controls/reanalyze", `{"pair":"BTCUSDm","note":"`+long+`"}`,
		ChannelTelegram)
	require.Equal(t, http.StatusOK, resp.StatusCode, "characters, not bytes")
	resp, body = send(t, http.MethodPost, srv.URL+"/api/controls/reanalyze", `{"pair":"BTCUSDm","note":"`+long+
		`!"}`, ChannelTelegram)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, body, "A mensagem tem 501 caracteres; o máximo é 500.")
	require.Len(t, llm.reanalyzed, 3)

	resp, _ = send(t, http.MethodPost, srv.URL+"/api/controls/reanalyze", `{"pair":"EURUSD"}`, ChannelTelegram)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = send(t, http.MethodPost, srv.URL+"/api/controls/reanalyze", `{`, ChannelTelegram)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	llm.refuse = errors.New("the provider is rate limited for 20s")
	resp, body = send(t, http.MethodPost, srv.URL+"/api/controls/reanalyze", `{"pair":"BTCUSDm"}`, ChannelTelegram)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, body, "rate limited")

	// a strategy of another kind: no reanalysis, whatever the controls and
	// the switch
	plain := newTestChart(t, WithStrategyIndicators(fakeStrategy{}))
	plain.OnCandle(candleAt("BTCUSDm", time.Date(2026, 9, 27, 13, 0, 0, 0, time.UTC), 84000))
	plain.SetOrderController(controller)
	plain.SetReanalysis(true)
	plainSrv := newTestServer(t, plain)
	for _, channel := range []Channel{ChannelWeb, ChannelTelegram} {
		_, body = send(t, http.MethodGet, plainSrv.URL+"/api/controls", "", channel)
		require.JSONEq(t, `{"enabled":true,"status":"running","bot_control":true}`, body, channel)
		resp, body = send(t, http.MethodPost, plainSrv.URL+"/api/controls/reanalyze", `{"pair":"BTCUSDm"}`, channel)
		require.Equal(t, http.StatusNotFound, resp.StatusCode, channel)
		require.Contains(t, body, "não usa LLM", channel)
	}
}

// The reanalysis is switched apart from the trade panel, each combination of
// the two, from the web dashboard and from Telegram, its controls on there or
// off: a reanalysis forbidden is refused (403) from any channel, the model
// never asked, and the orders follow the channels only.
func TestLLM_ReanalysisApartFromTheTradePanel(t *testing.T) {
	for _, tc := range []struct {
		name              string
		panel, telegram   bool // the channels of the control panel
		reanalysis        bool
		web, fromTelegram int  // the status of a reanalysis from each channel
		askedWeb, askedTg bool // whether the model was asked from each channel
	}{
		{"panel on, reanalysis on", true, true, true, http.StatusOK, http.StatusOK, true, true},
		{"panel off, reanalysis on", false, true, true, http.StatusOK, http.StatusOK, true, true},
		{"panel on, reanalysis off", true, true, false, http.StatusForbidden, http.StatusForbidden, false, false},
		{"panel off, reanalysis off", false, true, false, http.StatusForbidden, http.StatusForbidden, false, false},
		{"Telegram off, reanalysis on", false, false, true, http.StatusOK, http.StatusNotFound, true, false},
		{"Telegram off, reanalysis off", true, false, false, http.StatusForbidden, http.StatusNotFound, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, llm := newLLMChart(t)
			// as the agent of the controller: the trade panel and the Telegram
			// controls of its settings, and the switch of its reanalysis
			c.SetOrderController(&fakeController{status: order.StatusRunning}, ChannelWeb)
			var channels []Channel
			if tc.panel {
				channels = append(channels, ChannelWeb)
			}
			if tc.telegram {
				channels = append(channels, ChannelTelegram)
			}
			c.SetChannels(channels...)
			c.SetReanalysis(tc.reanalysis)
			srv := newTestServer(t, c)

			for _, channel := range []Channel{ChannelWeb, ChannelTelegram} {
				_, body := send(t, http.MethodGet, srv.URL+"/api/controls", "", channel)
				controls := decodeControls(t, body)
				controlled := channel == ChannelWeb || tc.telegram // started and stopped from it
				require.Equal(t, controlled, controls.BotControl, channel)
				require.Equal(t, controlled && tc.reanalysis, controls.Reanalysis, channel)
				require.Equal(t, controlled && !tc.reanalysis, controls.ReanalysisDisabled, channel)
				require.Equal(t, channel == ChannelWeb && tc.panel || channel == ChannelTelegram && tc.telegram,
					controls.Enabled, "%s: the orders follow the channels only", channel)
			}

			resp, body := send(t, http.MethodPost, srv.URL+"/api/controls/reanalyze", `{"pair":"BTCUSDm"}`,
				ChannelWeb)
			require.Equal(t, tc.web, resp.StatusCode, body)
			resp, telBody := send(t, http.MethodPost, srv.URL+"/api/controls/reanalyze", `{"pair":"BTCUSDm"}`,
				ChannelTelegram)
			require.Equal(t, tc.fromTelegram, resp.StatusCode, telBody)
			for status, answer := range map[int]string{tc.web: body, tc.fromTelegram: telBody} {
				if status == http.StatusForbidden {
					require.Contains(t, answer, "Reanálise não permitida")
				}
			}
			var want []string
			if tc.askedWeb {
				want = append(want, "BTCUSDm web")
			}
			if tc.askedTg {
				want = append(want, "BTCUSDm telegram")
			}
			require.Equal(t, want, llm.reanalyzed, "the model asked only when allowed")
		})
	}
}

// The reanalysis is switched while the bot runs, the dashboards told at once,
// and nothing told without a change.
func TestLLM_ReanalysisSwitchedWhileRunning(t *testing.T) {
	c, llm := newLLMChart(t)
	c.SetOrderController(&fakeController{status: order.StatusRunning}, ChannelWeb)
	c.SetChannels() // the trade panel off
	srv := newTestServer(t, c)
	events := c.events.subscribe()
	defer c.events.unsubscribe(events)

	resp, _ := send(t, http.MethodPost, srv.URL+"/api/controls/reanalyze", `{"pair":"BTCUSDm"}`, ChannelWeb)
	require.Equal(t, http.StatusForbidden, resp.StatusCode, "forbidden until allowed")

	c.SetReanalysis(true)
	event := nextEvent(t, events)
	require.Equal(t, "controls", event.name)
	require.JSONEq(t, `{"enabled":false,"status":"running","bot_control":true,"reanalysis":true}`,
		string(event.data))
	resp, _ = send(t, http.MethodPost, srv.URL+"/api/controls/reanalyze", `{"pair":"BTCUSDm"}`, ChannelWeb)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	c.SetReanalysis(true) // no change, nothing told
	c.SetReanalysis(false)
	event = nextEvent(t, events)
	require.JSONEq(t, `{"enabled":false,"status":"running","bot_control":true,"reanalysis_disabled":true}`,
		string(event.data))
	resp, _ = send(t, http.MethodPost, srv.URL+"/api/controls/reanalyze", `{"pair":"BTCUSDm"}`, ChannelWeb)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Equal(t, []string{"BTCUSDm web"}, llm.reanalyzed)
}

// The decisions kept by the strategy reach the dashboards on the event stream.
func TestLLM_DecisionEvents(t *testing.T) {
	c, llm := newLLMChart(t)
	events := c.events.subscribe()
	defer c.events.unsubscribe(events)

	require.NotNil(t, llm.listener, "the chart listens to the decisions")
	llm.listener(Decision{ID: "d3", Pair: "BTCUSDm", Action: "HOLD", Reason: "the trend holds"})
	event := <-events
	require.Equal(t, "decision", event.name)
	var decision DecisionEvent
	require.NoError(t, json.Unmarshal(event.data, &decision))
	require.Equal(t, "BTCUSDm", decision.Pair)
	require.Equal(t, "the trend holds", decision.Decision.Reason)
}
