package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

func newTestServer(t *testing.T, c *Chart) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(c.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var sb strings.Builder
	_, err = bufio.NewReader(resp.Body).WriteTo(&sb)
	require.NoError(t, err)
	return resp, sb.String()
}

func TestServer_OnlyAPI(t *testing.T) {
	srv := newTestServer(t, newTestChart(t))

	// the dashboard is served apart, by cmd/controller
	resp, _ := get(t, srv.URL+"/")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = get(t, srv.URL+"/ui/v1.0.0/app.js")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestServer_API(t *testing.T) {
	c := newTestChart(t)
	base := time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC)
	c.OnCandle(candleAt("ETHUSDT", base, 3000))
	c.OnOrder(model.Order{ID: 7, Pair: "ETHUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Price: 3000, Quantity: 2, CreatedAt: base, UpdatedAt: base})
	srv := newTestServer(t, c)

	resp, body := get(t, srv.URL+"/api/health")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, body)

	resp, body = get(t, srv.URL+"/api/pairs")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	assert.JSONEq(t, `{"pairs":["ETHUSDT"]}`, body)

	resp, body = get(t, srv.URL+"/api/ethusdt/snapshot")
	assert.Equal(t, http.StatusOK, resp.StatusCode, "pair is case-insensitive")
	var snap Snapshot
	require.NoError(t, json.Unmarshal([]byte(body), &snap))
	assert.Equal(t, "ETHUSDT", snap.Pair)
	require.Len(t, snap.Candles, 1)
	require.Len(t, snap.Orders, 1)
	assert.Equal(t, int64(7), snap.Orders[0].ID)
	assert.Equal(t, base.Unix(), snap.Orders[0].CandleTime)
	assert.Contains(t, body, `"indicators":[]`, "empty slices are encoded as [] not null")
	assert.Contains(t, body, `"equity_values":[]`)

	resp, _ = get(t, srv.URL+"/api/BTCUSDT/snapshot")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp, body = get(t, srv.URL+"/api/ETHUSDT/orders.csv")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/csv", resp.Header.Get("Content-Type"))
	assert.Contains(t, resp.Header.Get("Content-Disposition"), "history_ETHUSDT.csv")
	lines := strings.Split(strings.TrimSpace(body), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, "created_at,status,side,id,type,quantity,price,total,fee,profit", lines[0])
	assert.Equal(t, "2021-09-26T20:00:00Z,FILLED,BUY,7,MARKET,2.000000,3000.000000,6000.00,0.0000,", lines[1])

	resp, _ = get(t, srv.URL+"/api/BTCUSDT/orders.csv")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// The symbols of MT5 end in a lower case suffix, eg: BTCUSDm: their pair is
// found whatever the case of the link.
func TestServer_MixedCasePair(t *testing.T) {
	c := newTestChart(t)
	base := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	c.OnCandle(candleAt("BTCUSDm", base, 84000))
	srv := newTestServer(t, c)

	for _, name := range []string{"BTCUSDm", "BTCUSDM", "btcusdm"} {
		resp, body := get(t, srv.URL+"/api/"+name+"/snapshot")
		require.Equal(t, http.StatusOK, resp.StatusCode, name)
		var snap Snapshot
		require.NoError(t, json.Unmarshal([]byte(body), &snap))
		assert.Equal(t, "BTCUSDm", snap.Pair, name)

		resp, _ = get(t, srv.URL+"/api/"+name+"/orders.csv")
		assert.Equal(t, http.StatusOK, resp.StatusCode, name)
	}
	resp, _ := get(t, srv.URL+"/api/ETHUSDm/snapshot")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestServer_HealthStale(t *testing.T) {
	c := newTestChart(t)
	c.lastUpdate = time.Now().Add(-2 * time.Hour)
	srv := newTestServer(t, c)

	resp, _ := get(t, srv.URL+"/api/health")
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestServer_Events(t *testing.T) {
	c := newTestChart(t)
	srv := newTestServer(t, c)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, ": connected\n", line)

	// wait for the subscription to be registered
	require.Eventually(t, c.events.hasSubscribers, time.Second, 10*time.Millisecond)

	base := time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC)
	c.OnCandle(candleAt("ETHUSDT", base, 3000))
	c.OnOrder(model.Order{ID: 1, Pair: "ETHUSDT", Side: model.SideTypeBuy, UpdatedAt: base})

	readEvent := func() (string, string) {
		var name, data string
		for {
			line, err := reader.ReadString('\n')
			require.NoError(t, err)
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "" && name != "":
				return name, data
			}
		}
	}

	name, data := readEvent()
	assert.Equal(t, "candle", name)
	var candleEvent CandleEvent
	require.NoError(t, json.Unmarshal([]byte(data), &candleEvent))
	assert.Equal(t, "ETHUSDT", candleEvent.Pair)
	assert.Equal(t, 3000.0, candleEvent.Candle.Close)
	assert.NotNil(t, candleEvent.Indicators)

	name, data = readEvent()
	assert.Equal(t, "order", name)
	var orderEvent OrderEvent
	require.NoError(t, json.Unmarshal([]byte(data), &orderEvent))
	assert.Equal(t, int64(1), orderEvent.Order.ID)
	assert.Equal(t, base.Unix(), orderEvent.Order.CandleTime)

	cancel()
	require.Eventually(t, func() bool { return !c.events.hasSubscribers() }, time.Second, 10*time.Millisecond)
}

func TestBroker_DropsSlowSubscribers(t *testing.T) {
	b := newBroker()
	ch := b.subscribe()
	defer b.unsubscribe(ch)

	for i := 0; i < cap(ch)+10; i++ {
		b.publish("candle", i) // must not block
	}
	assert.Len(t, ch, cap(ch))

	b.publish("x", make(chan int)) // unencodable payload is ignored
}

func TestWriteJSON_Unencodable(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeJSON(recorder, Point{Time: 1, Value: math.NaN()})

	assert.Equal(t, http.StatusInternalServerError, recorder.Code, "an error rather than an empty answer")
	var response ErrorResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Contains(t, response.Error, "unsupported value: NaN")
}
