package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const fakeMCPToken = "test-token"

// fakeMCP is an MCP server of a terminal with the tools of the calendar: the
// shapes of the server of MetaTrader 5, its session, its token, and a record
// of the tools called.
type fakeMCP struct {
	t       *testing.T
	server  *httptest.Server
	sse     bool // answers in server-sent events
	mtx     sync.Mutex
	session string
	opened  int
	calls   []string              // tools called, with their day for the values
	values  map[string][]mcpValue // releases, by day of the server
}

func newFakeMCP(t *testing.T) *fakeMCP {
	f := &fakeMCP{t: t, values: map[string][]mcpValue{
		"2026-09-25": {
			{EventID: "840020013", Time: "2026-09-25T12:30:00", Actual: ptr(0.3), Forecast: ptr(0.9),
				Previous: ptr(0.4), Revised: ptr(0.7)},
			{EventID: "840020014", Time: "2026-09-25T14:00:00", Forecast: ptr(1.2)},
			{EventID: "840099999", Time: "2026-09-25T15:00:00"},
			{EventID: "156040006", Time: "2026-09-25T12:00:00", Actual: ptr(5)}, // another currency
		},
		"2026-09-26": {
			{EventID: "840020014", Time: "2026-09-26T00:30:00", Forecast: ptr(1.1)},
		},
	}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeMCP) url() string { return f.server.URL + "/mcp" }

func (f *fakeMCP) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+fakeMCPToken {
		w.Header().Set("WWW-Authenticate", `Bearer realm="MetaTrader5-MCP"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var message struct {
		ID     *int64          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&message))

	f.mtx.Lock()
	defer f.mtx.Unlock()
	switch message.Method {
	case "initialize":
		f.opened++
		f.session = fmt.Sprintf("session-%d", f.opened)
		w.Header().Set("Mcp-Session-Id", f.session)
		f.answer(w, *message.ID, map[string]any{"protocolVersion": mcpProtocolVersion,
			"serverInfo": map[string]any{"name": "MetaTrader 5 MCP", "version": "1.0"}})
		return
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if r.Header.Get("Mcp-Session-Id") != f.session {
		w.WriteHeader(http.StatusNotFound) // a session the server forgot
		return
	}
	require.Equal(f.t, "tools/call", message.Method)

	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	require.NoError(f.t, json.Unmarshal(message.Params, &call))
	switch call.Name {
	case "economic_calendar_list_events_by_currency":
		f.calls = append(f.calls, call.Name+":"+fmt.Sprint(call.Arguments["currency_code"]))
		if call.Arguments["currency_code"] != "USD" {
			f.text(w, *message.ID, true, "Tool 'economic_calendar_list_events_by_currency' unknown currency code")
			return
		}
		f.text(w, *message.ID, false, `{ "ok": true, "count": 3, "events": [
			{"id": "840020013", "importance": "High importance", "unit": "Percentage", "name": "CPI (Mensal)", "event_code": "cpi-mm"},
			{"id": "840020014", "importance": "Medium importance", "unit": "Percentage", "name": "Vendas", "event_code": "retail-sales"},
			{"id": "840099999", "importance": "Low importance", "unit": "None", "name": "Leilão", "event_code": "auction"}]}`)
	case "economic_calendar_list_values":
		from := fmt.Sprint(call.Arguments["datetime_from"])
		day := from[:10]
		f.calls = append(f.calls, call.Name+":"+day)
		to, err := time.Parse("2006-01-02T15:04:05", fmt.Sprint(call.Arguments["datetime_to"]))
		require.NoError(f.t, err)
		start, err := time.Parse("2006-01-02T15:04:05", from)
		require.NoError(f.t, err)
		require.Equal(f.t, 24*time.Hour, to.Sub(start), "a day at a time")
		data, _ := json.Marshal(map[string]any{"ok": true, "count": len(f.values[day]), "truncated": false,
			"values": f.values[day]})
		f.text(w, *message.ID, false, string(data))
	default:
		f.t.Fatalf("tool %s called", call.Name)
	}
}

// answer writes the result of a request, in JSON or in an event stream.
func (f *fakeMCP) answer(w http.ResponseWriter, id int64, result any) {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if f.sse {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

// text writes the text result of a tool.
func (f *fakeMCP) text(w http.ResponseWriter, id int64, isError bool, text string) {
	f.answer(w, id, map[string]any{"isError": isError, "content": []map[string]any{{"type": "text", "text": text}}})
}

func (f *fakeMCP) called() []string {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeMCP) forget() {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	f.session = "restarted"
}

func newTestCalendar(t *testing.T, f *fakeMCP, now time.Time, options ...MCPOption) *MCPCalendar {
	t.Helper()
	calendar, err := NewMCPCalendar(f.url(), fakeMCPToken, options...)
	require.NoError(t, err)
	calendar.now = func() time.Time { return now }
	return calendar
}

func TestMCPClient_OnlyTheToolsOfTheCalendar(t *testing.T) {
	f := newFakeMCP(t)
	client := newMCPClient(f.url(), fakeMCPToken, nil)
	for _, tool := range []string{"trade_send_market_order", "trade_close_single_position", "write_file",
		"delete_folder", "send_web_request", "chart_add_expert"} {
		err := client.callTool(context.Background(), tool, nil, nil)
		require.ErrorIs(t, err, ErrToolNotAllowed, tool)
	}
	require.Zero(t, f.opened, "refused before any request, the session included")
	require.Empty(t, f.called())
}

func TestMCPCalendar_Events(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sse %v", sse), func(t *testing.T) {
			f := newFakeMCP(t)
			f.sse = sse
			now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC) // both days over
			calendar := newTestCalendar(t, f, now)

			from := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
			to := time.Date(2026, 9, 26, 1, 0, 0, 0, time.UTC)
			events, err := calendar.Events(context.Background(), []string{"USD"}, from, to)
			require.NoError(t, err)
			require.Len(t, events, 4, "the releases of USD, oldest first, another currency left out")

			cpi := events[0]
			require.Equal(t, time.Date(2026, 9, 25, 12, 30, 0, 0, time.UTC), cpi.Time)
			require.Equal(t, "USD", cpi.Currency)
			require.Equal(t, "CPI (Mensal)", cpi.Name)
			require.Equal(t, "cpi-mm", cpi.Code)
			require.Equal(t, ImportanceHigh, cpi.Importance)
			require.Equal(t, "%", cpi.Unit)
			require.Equal(t, 0.3, cpi.Actual)
			require.Equal(t, 0.9, cpi.Forecast)
			require.Equal(t, 0.7, cpi.Previous, "the revised previous value")

			sales := events[1]
			require.Equal(t, ImportanceMedium, sales.Importance)
			require.True(t, math.IsNaN(sales.Actual), "not published")
			require.True(t, math.IsNaN(sales.Previous))
			require.Equal(t, ImportanceLow, events[2].Importance)
			require.Equal(t, "", events[2].Unit)
			require.Equal(t, time.Date(2026, 9, 26, 0, 30, 0, 0, time.UTC), events[3].Time)
			require.Equal(t, 1, f.opened, "one session")
		})
	}
}

func TestMCPCalendar_ServerOffset(t *testing.T) {
	f := newFakeMCP(t)
	calendar := newTestCalendar(t, f, time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC),
		WithMCPServerOffset(2*time.Hour))
	events, err := calendar.Events(context.Background(), []string{"USD"},
		time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC), time.Date(2026, 9, 25, 11, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, time.Date(2026, 9, 25, 10, 30, 0, 0, time.UTC), events[0].Time, "12:30 on the server, UTC+2")
}

func TestMCPCalendar_Cache(t *testing.T) {
	f := newFakeMCP(t)
	now := time.Date(2026, 9, 26, 6, 0, 0, 0, time.UTC) // the 25th over, the 26th running
	calendar := newTestCalendar(t, f, now)
	from := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 26, 6, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		_, err := calendar.Events(context.Background(), []string{"USD"}, from, to)
		require.NoError(t, err)
	}
	require.Equal(t, []string{
		"economic_calendar_list_events_by_currency:USD",
		"economic_calendar_list_values:2026-09-25",
		"economic_calendar_list_values:2026-09-26",
	}, f.called(), "each day once, the descriptions once")

	// the running day read again after its TTL, the day over never
	calendar.now = func() time.Time { return now.Add(mcpDayTTL + time.Second) }
	_, err := calendar.Events(context.Background(), []string{"USD"}, from, to)
	require.NoError(t, err)
	require.Equal(t, "economic_calendar_list_values:2026-09-26", f.called()[3])
	require.Len(t, f.called(), 4)

	// the descriptions read again a day later
	calendar.now = func() time.Time { return now.Add(mcpEventsTTL + time.Second) }
	_, err = calendar.Events(context.Background(), []string{"USD"}, from, to)
	require.NoError(t, err)
	require.Contains(t, f.called()[4:], "economic_calendar_list_events_by_currency:USD")
}

func TestMCPCalendar_UnknownCurrency(t *testing.T) {
	f := newFakeMCP(t)
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	calendar := newTestCalendar(t, f, now)
	from, to := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 25, 23, 0, 0, 0, time.UTC)

	events, err := calendar.Events(context.Background(), []string{"XYZ", "USD"}, from, to)
	require.NoError(t, err, "a currency the calendar refuses is skipped")
	require.Len(t, events, 3)

	_, err = calendar.Events(context.Background(), []string{"XYZ"}, from, to)
	require.NoError(t, err)
	var asked int
	for _, call := range f.called() {
		if call == "economic_calendar_list_events_by_currency:XYZ" {
			asked++
		}
	}
	require.Equal(t, 1, asked, "not asked again within the hour")
}

func TestMCPCalendar_SessionForgotten(t *testing.T) {
	f := newFakeMCP(t)
	calendar := newTestCalendar(t, f, time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC))
	from, to := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 25, 23, 0, 0, 0, time.UTC)
	_, err := calendar.Events(context.Background(), []string{"USD"}, from, to)
	require.NoError(t, err)

	f.forget() // eg: the terminal restarted
	events, err := calendar.Events(context.Background(), []string{"USD"}, from, to.Add(24*time.Hour))
	require.NoError(t, err)
	require.Len(t, events, 4)
	require.Equal(t, 2, f.opened, "a new session opened once")
}

func TestMCPCalendar_WrongToken(t *testing.T) {
	f := newFakeMCP(t)
	calendar, err := NewMCPCalendar(f.url(), "wrong")
	require.NoError(t, err)
	_, err = calendar.Events(context.Background(), []string{"USD"}, time.Now().Add(-time.Hour), time.Now())
	var status *mcpHTTPError
	require.True(t, errors.As(err, &status), "%v", err)
	require.Equal(t, http.StatusUnauthorized, status.Status)

	_, err = NewMCPCalendar(" ", fakeMCPToken)
	require.Error(t, err)
}

func TestMCPPayload(t *testing.T) {
	stream := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n" +
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\n" + "data: \"id\":2,\"result\":{\"ok\":true}}\n\n"
	payload, err := mcpPayload("text/event-stream", []byte(stream), 2)
	require.NoError(t, err)
	require.JSONEq(t, `{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`, string(payload), "multi-line data")

	_, err = mcpPayload("text/event-stream", []byte(stream), 3)
	require.Error(t, err)

	payload, err = mcpPayload("application/json", []byte(`{"id":3}`), 3)
	require.NoError(t, err)
	require.Equal(t, `{"id":3}`, string(payload))
}
