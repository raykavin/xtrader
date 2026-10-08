package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakeMCPToken = "test-token"

// fakeMCP is an MCP server of a terminal with the tools of the calendar: the
// shapes of the server of MetaTrader 5, its session, its token, and a record
// of the tools called.
type fakeMCP struct {
	t        *testing.T
	server   *httptest.Server
	sse      bool // answers in server-sent events
	keepOpen bool // the event streams held open after the answer, as the spec allows
	mtx      sync.Mutex
	session  string
	opened   int
	calls    []string              // tools called, with their day for the values
	values   map[string][]mcpValue // releases, by day of the server
	fail     map[string]int        // the calls failing (eg: the terminal busy), by call, the times left
	failOK   bool                  // the calls of fail answered ok:false, rather than isError
	// how a currency it doesn't know (any but USD and EUR, and the ones of
	// unknown) is refused: isError with its text, "unknown currency code" when
	// empty, or a JSON-RPC error invalid params when "rpc"
	refusal string
	unknown map[string]bool
	// the initialized notifications answered 500, the times left, the session
	// forgotten then
	failInitialized int
	staleInit       int           // the initialize requests sent with a session
	initDelay       time.Duration // before an initialize is answered, set before the calls

	ranges    []string // the ranges of the values read, from-to
	page      int      // the most values answered at a time, the rest truncated, when > 0
	inclusive bool     // datetime_to taken as inclusive, the release at it answered
	moreUSD   []string // more events of USD, JSON objects
}

func newFakeMCP(t *testing.T) *fakeMCP {
	f := &fakeMCP{t: t, values: map[string][]mcpValue{
		"2026-09-25": {
			{EventID: "840020013", Time: "2026-09-25T12:30:00", Actual: ptr(0.3), Forecast: ptr(0.9),
				Previous: ptr(0.4), Revised: ptr(0.7)},
			{EventID: "840020014", Time: "2026-09-25T14:00:00", Forecast: ptr(1.2)},
			{EventID: "840099999", Time: "2026-09-25T15:00:00"},
			{EventID: "156040006", Time: "2026-09-25T12:00:00", Actual: ptr(5)},      // another currency
			{EventID: "999010007", Time: "2026-09-25T11:45:00", Forecast: ptr(2.15)}, // of EUR
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

	if f.keepOpen {
		defer f.holdOpen(w, r) // once the lock is released
	}
	if message.Method == "initialize" {
		time.Sleep(f.initDelay)
	}
	f.mtx.Lock()
	defer f.mtx.Unlock()
	switch message.Method {
	case "initialize":
		if session := r.Header.Get("Mcp-Session-Id"); session != "" {
			f.staleInit++
			if session != f.session {
				w.WriteHeader(http.StatusNotFound) // a session it forgot
				return
			}
		}
		f.opened++
		f.session = fmt.Sprintf("session-%d", f.opened)
		w.Header().Set("Mcp-Session-Id", f.session)
		f.answer(w, *message.ID, map[string]any{"protocolVersion": mcpProtocolVersion,
			"serverInfo": map[string]any{"name": "MetaTrader 5 MCP", "version": "1.0"}})
		return
	case "notifications/initialized":
		if f.failInitialized > 0 {
			f.failInitialized--
			f.session = "" // eg: the terminal restarted meanwhile
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
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
		if f.failing(w, *message.ID) {
			return
		}
		switch currency := fmt.Sprint(call.Arguments["currency_code"]); {
		case f.unknown[currency]:
			f.refuse(w, *message.ID)
			return
		case currency == "EUR":
			f.text(w, *message.ID, false, `{ "ok": true, "count": 1, "events": [
			{"id": "999010007", "importance": "High importance", "unit": "Percentage", "name": "Taxa do BCE", "event_code": "ecb-rate"}]}`)
			return
		case currency != "USD":
			f.refuse(w, *message.ID)
			return
		}
		more := ""
		for _, event := range f.moreUSD {
			more += ",\n" + event
		}
		f.text(w, *message.ID, false, `{ "ok": true, "count": 3, "events": [
			{"id": "840020013", "importance": "High importance", "unit": "Percentage", "name": "CPI (Mensal)", "event_code": "cpi-mm", "time_mode": "Source publishes an exact time of an event"},
			{"id": "840020014", "importance": "Medium importance", "unit": "Percentage", "name": "Vendas", "event_code": "retail-sales"},
			{"id": "840099999", "importance": "Low importance", "unit": "None", "name": "Leilão", "event_code": "auction"}`+
			more+`]}`)
	case "economic_calendar_list_values":
		from := fmt.Sprint(call.Arguments["datetime_from"])
		day := from[:10]
		f.calls = append(f.calls, call.Name+":"+day)
		if f.failing(w, *message.ID) {
			return
		}
		to, err := time.Parse("2006-01-02T15:04:05", fmt.Sprint(call.Arguments["datetime_to"]))
		require.NoError(f.t, err)
		start, err := time.Parse("2006-01-02T15:04:05", from)
		require.NoError(f.t, err)
		require.True(f.t, to.After(start) && to.Sub(start) <= 24*time.Hour, "a day at most at a time")
		f.ranges = append(f.ranges, from[11:16]+"-"+to.Format("15:04"))
		values := []mcpValue{}
		for _, day := range []string{day, to.Format(time.DateOnly)} {
			for _, value := range f.values[day] {
				at, err := time.Parse("2006-01-02T15:04:05", value.Time)
				require.NoError(f.t, err)
				if !at.Before(start) && (at.Before(to) || f.inclusive && at.Equal(to)) {
					values = append(values, value)
				}
			}
			if to.Format(time.DateOnly) == day {
				break
			}
		}
		truncated := f.page > 0 && len(values) > f.page
		if truncated {
			values = values[:f.page]
		}
		data, _ := json.Marshal(map[string]any{"ok": true, "count": len(values), "truncated": truncated,
			"values": values})
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

// refuse answers the call for the events of a currency it doesn't know, see
// refusal.
func (f *fakeMCP) refuse(w http.ResponseWriter, id int64) {
	switch {
	case strings.HasPrefix(f.refusal, "{"):
		f.text(w, id, false, f.refusal) // eg: ok false
	case f.refusal == "":
		f.text(w, id, true, "Tool 'economic_calendar_list_events_by_currency' unknown currency code")
	case f.refusal == "rpc":
		f.answerError(w, id, mcpInvalidParams, "Invalid params: currency_code")
	default:
		f.text(w, id, true, f.refusal)
	}
}

// answerError writes the JSON-RPC error of a request.
func (f *fakeMCP) answerError(w http.ResponseWriter, id int64, code int, message string) {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message}})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

// failing answers the last call recorded with a failure of the tool, while
// fail holds it, and reports whether it did.
func (f *fakeMCP) failing(w http.ResponseWriter, id int64) bool {
	call := f.calls[len(f.calls)-1]
	if f.fail[call] == 0 {
		return false
	}
	f.fail[call]--
	if f.failOK {
		f.text(w, id, false, `{"ok": false, "error": "calendar not ready"}`)
	} else {
		f.text(w, id, true, "Tool 'economic_calendar' timed out waiting for the terminal")
	}
	return true
}

// holdOpen holds an event stream open after its answer, until the client
// leaves it.
func (f *fakeMCP) holdOpen(w http.ResponseWriter, r *http.Request) {
	if w.Header().Get("Content-Type") != "text/event-stream" {
		return
	}
	w.(http.Flusher).Flush()
	select {
	case <-r.Context().Done():
	case <-time.After(10 * time.Second):
	}
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

// asked returns how many times call was made.
func (f *fakeMCP) asked(call string) int {
	var n int
	for _, made := range f.called() {
		if made == call {
			n++
		}
	}
	return n
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
	require.NoError(t, err, "a currency the calendar doesn't know is skipped")
	require.Len(t, events, 3)

	// none of the currencies known: no calendar, rather than a calm one
	_, err = calendar.Events(context.Background(), []string{"XYZ"}, from, to)
	require.ErrorContains(t, err, "XYZ")
	require.Equal(t, 1, f.asked("economic_calendar_list_events_by_currency:XYZ"), "not asked again within the hour")
}

// A tool failing for another reason than the currency, eg: the terminal busy,
// is tried again at once, then the calendar is unavailable for this read
// only, never taken for a calendar without any release: it is read again on
// the next one.
func TestMCPCalendar_ToolFailing(t *testing.T) {
	from, to := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 25, 23, 0, 0, 0, time.UTC)
	for _, failOK := range []bool{false, true} {
		for _, call := range []string{"economic_calendar_list_events_by_currency:USD",
			"economic_calendar_list_values:2026-09-25"} {
			t.Run(fmt.Sprintf("%s ok:false %v", call, failOK), func(t *testing.T) {
				f := newFakeMCP(t)
				f.failOK = failOK
				calendar := newTestCalendar(t, f, time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC))
				calendar.retry = time.Millisecond

				f.fail = map[string]int{call: 1}
				events, err := calendar.Events(context.Background(), []string{"USD"}, from, to)
				require.NoError(t, err, "tried again at once")
				require.Len(t, events, 3)
				require.Equal(t, 2, f.asked(call))

				calendar = newTestCalendar(t, f, time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC))
				calendar.retry = time.Millisecond
				f.fail = map[string]int{call: 2}
				_, err = calendar.Events(context.Background(), []string{"USD"}, from, to)
				require.Error(t, err, "unavailable, not empty")

				events, err = calendar.Events(context.Background(), []string{"USD"}, from, to)
				require.NoError(t, err, "read again on the next read, not an hour later")
				require.Len(t, events, 3)
			})
		}
	}
}

// The initialized notification failing, the server forgetting the session
// meanwhile: the next call opens a new session, never sending the old one.
func TestMCPCalendar_InitializedFailing(t *testing.T) {
	f := newFakeMCP(t)
	f.failInitialized = 1
	calendar := newTestCalendar(t, f, time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC))
	from, to := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 25, 23, 0, 0, 0, time.UTC)

	_, err := calendar.Events(context.Background(), []string{"USD"}, from, to)
	var status *mcpHTTPError
	require.ErrorAs(t, err, &status)
	require.Equal(t, http.StatusInternalServerError, status.Status)

	for i := 0; i < 2; i++ {
		events, err := calendar.Events(context.Background(), []string{"USD"}, from, to)
		require.NoError(t, err, "read %d", i)
		require.Len(t, events, 3)
	}
	require.Equal(t, 2, f.opened, "a new session, once")
	require.Zero(t, f.staleInit, "never an initialize with a session")
}

// B25: the calls after a session failed to open (its initialized
// notification failing, the server forgetting it), concurrent, open one new
// session: the others wait for it, never opening one each.
func TestMCPCalendar_ConcurrentCallsOpenOneSession(t *testing.T) {
	f := newFakeMCP(t)
	f.failInitialized = 1
	f.initDelay = 50 * time.Millisecond
	calendar := newTestCalendar(t, f, time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC))
	from, to := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 25, 23, 0, 0, 0, time.UTC)

	_, err := calendar.Events(context.Background(), []string{"USD"}, from, to)
	require.Error(t, err, "the session failed to open")
	require.Equal(t, 1, f.opened)

	const calls = 8
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			events, err := calendar.Events(context.Background(), []string{"USD"}, from, to)
			if err == nil && len(events) != 3 {
				err = fmt.Errorf("%d events", len(events))
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	f.mtx.Lock()
	defer f.mtx.Unlock()
	require.Equal(t, 2, f.opened, "one session opened again, for all the calls")
	require.Zero(t, f.staleInit, "never an initialize with a session")
}

// A server holding the event stream open after the answer, as the spec
// allows: the answer is read as it comes, without waiting for the end of the
// stream.
func TestMCPCalendar_EventStreamHeldOpen(t *testing.T) {
	f := newFakeMCP(t)
	f.sse, f.keepOpen = true, true
	calendar := newTestCalendar(t, f, time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC))
	from, to := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 25, 23, 0, 0, 0, time.UTC)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	events, err := calendar.Events(ctx, []string{"USD"}, from, to)
	require.NoError(t, err)
	require.Len(t, events, 3)
	require.Less(t, time.Since(start), 2*time.Second, "not waiting for the end of the streams")
}

// An answer over maxMCPAnswer is refused, in JSON or in an event stream, an
// event stream without the answer read up to it only.
func TestMCPClient_AnswerTooLarge(t *testing.T) {
	big := strings.Repeat("x", maxMCPAnswer)
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sse %v", sse), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var message struct {
					ID int64 `json:"id"`
				}
				_ = json.NewDecoder(r.Body).Decode(&message)
				if sse {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"%s\"}\n\n", big)
					_, _ = fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{}}\n\n", message.ID)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"text":"%s"}}`, message.ID, big)
			}))
			defer server.Close()
			client := newMCPClient(server.URL, "", nil)
			err := client.post(context.Background(), "ping", nil, nil, false)
			require.ErrorContains(t, err, "over")
		})
	}
}

// A calendar whose tool fails is unavailable in the context, never an
// available calendar without any release.
func TestTrader_CalendarToolFailing(t *testing.T) {
	f := newFakeMCP(t)
	f.fail = map[string]int{"economic_calendar_list_events_by_currency:USD": 100}
	calendar, err := NewMCPCalendar(f.url(), fakeMCPToken)
	require.NoError(t, err)
	calendar.retry = time.Millisecond
	r := newTraderRunner(t, func(c *Config) {
		c.Calendar, c.CalendarCurrencies = calendar, []string{"USD"}
	}, 0)
	r.step(100, decide(ActionWait, 0.5, nil, nil, nil))
	in := r.provider.last()
	require.NotNil(t, in.Calendar)
	require.False(t, in.Calendar.Available)
}

// B16: the calendar of a pair of two currencies in the context: a currency
// it doesn't know (the argument refused) skipped, the other one's releases
// sent, available; a currency of the calendar failing otherwise, whatever the
// message, unavailable, as one currency failing would be.
func TestTrader_CalendarTwoCurrencies(t *testing.T) {
	for name, want := range map[string]bool{"EUR unknown": true, "EUR failing": false, "EUR not synced": false} {
		t.Run(name, func(t *testing.T) {
			f := newFakeMCP(t)
			switch name {
			case "EUR unknown":
				f.refusal, f.unknown = "rpc", map[string]bool{"EUR": true}
			case "EUR not synced":
				f.refusal = "Calendar data for currency EUR not available yet, the terminal is syncing"
				f.unknown = map[string]bool{"EUR": true}
			default:
				f.fail = map[string]int{"economic_calendar_list_events_by_currency:EUR": 100}
			}
			calendar, err := NewMCPCalendar(f.url(), fakeMCPToken)
			require.NoError(t, err)
			calendar.retry = time.Millisecond
			r := newTraderRunner(t, func(c *Config) {
				c.Calendar, c.CalendarCurrencies = calendar, []string{"USD", "EUR"}
			}, 0)
			r.step(100, decide(ActionWait, 0.5, nil, nil, nil))
			in := r.provider.last()
			require.NotNil(t, in.Calendar)
			require.Equal(t, want, in.Calendar.Available)
			require.Equal(t, 1, f.asked("economic_calendar_list_events_by_currency:USD"), "USD read")
		})
	}
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
	payload, err := mcpPayload("text/event-stream", strings.NewReader(stream), 2)
	require.NoError(t, err)
	require.JSONEq(t, `{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`, string(payload), "multi-line data")

	_, err = mcpPayload("text/event-stream", strings.NewReader(stream), 3)
	require.Error(t, err)

	payload, err = mcpPayload("application/json", strings.NewReader(`{"id":3}`), 3)
	require.NoError(t, err)
	require.Equal(t, `{"id":3}`, string(payload))
}

// An HTTP error of the server is logged: its error keeps the start of the
// body only, without the token of the client, a bearer token or an API key,
// eg: the page of a proxy echoing the request.
func TestMCPClient_HTTPErrorKeepsTheStartOfItsBody(t *testing.T) {
	const secretKey = "sk-proj-abcdef0123456789"
	body := "upstream failed: Authorization: Bearer " + fakeMCPToken + ", Bearer other.jwt-token, key " +
		secretKey + ", token " + fakeMCPToken + " " + strings.Repeat("x", 1<<20) + " the end"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	client := newMCPClient(server.URL, fakeMCPToken, nil)
	err := client.callTool(context.Background(), "economic_calendar_list_values", nil, nil)
	var status *mcpHTTPError
	require.ErrorAs(t, err, &status)
	require.Equal(t, http.StatusBadGateway, status.Status)

	text := err.Error()
	assert.Contains(t, text, "MCP server: HTTP 502 upstream failed: Authorization: Bearer [redacted]")
	assert.LessOrEqual(t, len(status.Body), maxMCPErrorBody+len("..."))
	assert.True(t, strings.HasSuffix(status.Body, "..."), "cut")
	for _, secret := range []string{fakeMCPToken, "other.jwt-token", secretKey, "the end"} {
		assert.NotContains(t, text, secret)
	}
}

// B16: a currency of the calendar of MT5 (eg: EUR) is never taken for one it
// doesn't know by the message of the tool, whatever it says, as it may be
// transient (eg: the terminal syncing), only by the server refusing the
// argument of the call (-32602); another code (eg: XTI of XTIUSD) is, whatever
// the error.
func TestMCPUnknownCurrency(t *testing.T) {
	for _, message := range []string{
		"Tool 'economic_calendar_list_events_by_currency' unknown currency code",
		"currency code is invalid",
		"Currency not found",
		"currency 'EUR' not found",
		"currency_code: invalid value",
		"EUR is not a valid currency",
		"Currency EUR unknown",
		"currency EUR not available",
		"Calendar data for currency EUR not available yet, the terminal is syncing",
		"currency data not available",
		"currency EUR is not available, try again later",
		"economic calendar not available for currency EUR",
		"Invalid currency data received from the terminal",
		"currency EUR temporarily not available",
		"Tool 'economic_calendar' timed out waiting for the terminal",
		"currency EUR: timeout",
		"ok false",
	} {
		err := &mcpToolError{Tool: "economic_calendar_list_events_by_currency", Message: message}
		require.False(t, unknownCurrency(err, "EUR"), "a currency of the calendar of MT5: %s", message)
		require.True(t, unknownCurrency(err, "XTI"), "not a currency of the calendar of MT5: %s", message)
	}
	busy := &mcpToolError{Tool: "economic_calendar_list_events_by_currency", Message: "error 5401"}
	for code := range mt5CalendarCurrencies {
		require.False(t, unknownCurrency(busy, code), code)
		require.False(t, unknownCurrency(busy, strings.ToLower(code)), code)
	}

	// the server refusing the argument of the call
	require.True(t, unknownCurrency(fmt.Errorf("llm: MCP tool x: %w", &mcpError{Code: mcpInvalidParams}), "EUR"),
		"the argument refused")
	for name, err := range map[string]error{
		"the session failing to open": &mcpOpenError{err: &mcpError{Code: mcpInvalidParams}},
		"the server failing":          &mcpError{Code: -32603, Message: "internal error"},
		"the server unreachable":      &mcpHTTPError{Status: http.StatusBadGateway},
		"another failure":             errors.New("unknown currency"),
	} {
		require.False(t, unknownCurrency(err, "EUR"), name)
		require.True(t, unknownCurrency(err, "XTI"), "not a currency of the calendar of MT5, whatever the error: %s",
			name)
	}
	require.False(t, unknownCurrency(busy, ""), "not a call for a currency")
	require.False(t, unknownCurrency(nil, "XTI"))
}

// testLog collects the lines logged, by any goroutine, until the end of a
// test, see captureLog.
type testLog struct {
	mtx   sync.Mutex
	lines strings.Builder
}

func (l *testLog) Write(p []byte) (int, error) {
	l.mtx.Lock()
	defer l.mtx.Unlock()
	return l.lines.Write(p)
}

func (l *testLog) String() string {
	l.mtx.Lock()
	defer l.mtx.Unlock()
	return l.lines.String()
}

// captureLog collects the lines logged, the warnings at least (another test
// may have left the level above), until the end of the test.
func captureLog(t *testing.T) *testLog {
	t.Helper()
	logs := &testLog{}
	level := logrus.GetLevel()
	logrus.SetOutput(logs)
	logrus.SetLevel(max(level, logrus.WarnLevel))
	t.Cleanup(func() {
		logrus.SetOutput(os.Stderr)
		logrus.SetLevel(level)
	})
	return logs
}

// B16: the calendar of a pair of two currencies: a currency it doesn't know
// (refused by the code of the error, or not a currency of the calendar of
// MT5, eg: XTI, whatever its error) is skipped, the releases of the other one
// read, the calendar available; a currency of the calendar failing otherwise,
// whatever the message (eg: the terminal busy, or its calendar not synced
// yet), leaves the calendar unavailable, never read in part, and is asked
// again on the next read.
func TestMCPCalendar_TwoCurrencies(t *testing.T) {
	from, to := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 25, 23, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)

	f := newFakeMCP(t)
	events, err := newTestCalendar(t, f, now).Events(context.Background(), []string{"USD", "EUR"}, from, to)
	require.NoError(t, err)
	require.Len(t, events, 4, "the releases of both")
	require.Equal(t, "EUR", events[0].Currency)

	for name, refused := range map[string]struct {
		currency, refusal string
	}{
		"by the code of the error":                 {"EUR", "rpc"},
		"not a currency of MT5":                    {"XTI", "Tool 'economic_calendar_list_events_by_currency' failed: 5402"},
		"not a currency of MT5, by its message":    {"XTI", ""},
		"not a currency of MT5, ok false":          {"XTI", `{"ok": false, "error": "currency 'XTI' not found"}`},
		"not a currency of MT5, the code of error": {"XTI", "rpc"},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLog(t)
			f := newFakeMCP(t)
			f.refusal, f.unknown = refused.refusal, map[string]bool{refused.currency: true}
			calendar := newTestCalendar(t, f, now)
			calendar.retry = time.Millisecond
			call := "economic_calendar_list_events_by_currency:" + refused.currency
			for i := 0; i < 2; i++ {
				events, err := calendar.Events(context.Background(), []string{"USD", refused.currency}, from, to)
				require.NoError(t, err, "the currency unknown skipped")
				require.Len(t, events, 3, "the releases of USD")
				for _, event := range events {
					require.Equal(t, "USD", event.Currency)
				}
				require.Equal(t, 1, f.asked(call), "neither tried again nor asked again within the hour")
			}
			require.Equal(t, 1, strings.Count(logs.String(), "the currency "+refused.currency+" skipped for 1h0m0s, "+
				"unknown to it"), "the skip logged, once")
		})
	}

	// EUR without any event read: skipped, asked again on the next read, the
	// skip logged once an hour
	logs := captureLog(t)
	f = newFakeMCP(t)
	f.refusal, f.unknown = `{"ok": true, "count": 0, "events": []}`, map[string]bool{"EUR": true}
	calendar := newTestCalendar(t, f, now)
	for i := 1; i <= 2; i++ {
		events, err := calendar.Events(context.Background(), []string{"USD", "EUR"}, from, to)
		require.NoError(t, err)
		require.Len(t, events, 3, "the releases of USD")
		require.Equal(t, i, f.asked("economic_calendar_list_events_by_currency:EUR"))
	}
	require.Equal(t, 1, strings.Count(logs.String(), "the currency EUR skipped, none of its events read"))

	// EUR failing for another reason: unavailable, read again on the next read
	f = newFakeMCP(t)
	calendar = newTestCalendar(t, f, now)
	calendar.retry = time.Millisecond
	f.fail = map[string]int{"economic_calendar_list_events_by_currency:EUR": 2}
	_, err = calendar.Events(context.Background(), []string{"USD", "EUR"}, from, to)
	require.ErrorContains(t, err, "timed out", "unavailable, not the releases of USD only")
	events, err = calendar.Events(context.Background(), []string{"USD", "EUR"}, from, to)
	require.NoError(t, err)
	require.Len(t, events, 4)
}

// B16: EURUSD, EUR refused by the message of the tool, whatever it says, eg:
// its calendar not synced yet in the terminal: the calendar unavailable, not
// the releases of USD only, EUR asked again on the next read rather than
// skipped for an hour.
func TestMCPCalendar_CurrencyRefusedByItsMessage(t *testing.T) {
	from, to := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 25, 23, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	for _, refusal := range []string{"Tool 'economic_calendar_list_events_by_currency' unknown currency code",
		"Calendar data for currency EUR not available yet, the terminal is syncing",
		"currency EUR not available", "Invalid currency data received from the terminal",
		`{"ok": false, "error": "currency 'EUR' not found"}`} {
		t.Run(refusal, func(t *testing.T) {
			f := newFakeMCP(t)
			f.refusal, f.unknown = refusal, map[string]bool{"EUR": true}
			calendar := newTestCalendar(t, f, now)
			calendar.retry = time.Millisecond
			call := "economic_calendar_list_events_by_currency:EUR"
			_, err := calendar.Events(context.Background(), []string{"USD", "EUR"}, from, to)
			require.Error(t, err, "unavailable, not the releases of USD only")
			require.Equal(t, 2, f.asked(call), "tried again at once")
			delete(f.unknown, "EUR") // the terminal synced
			events, err := calendar.Events(context.Background(), []string{"USD", "EUR"}, from, to)
			require.NoError(t, err)
			require.Len(t, events, 4, "the releases of both")
			require.Equal(t, 3, f.asked(call), "asked again on the next read, not an hour later")
		})
	}
}
