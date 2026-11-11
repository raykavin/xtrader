package ui

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/order"
)

// request sends a request to the API with the headers, in pairs of a name and
// its value.
func request(t *testing.T, method, url, body string, headers ...string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	require.NoError(t, err)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(data)
}

// A page of another site in a browser of the machine reaches localhost: its
// requests, even the "simple" ones a form or a no-cors fetch sends without a
// preflight, are refused before they stop the bot or send an order. The
// controller, the client of the API, sends none of the headers of a browser.
func TestAPI_RefusesThePagesOfABrowser(t *testing.T) {
	c := newTestChart(t)
	c.OnCandle(candleAt("ETHUSDT", time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC), 3000))
	controller := &fakeController{status: order.StatusRunning}
	c.SetOrderController(controller)
	srv := newTestServer(t, c)

	for _, tc := range []struct {
		name    string
		headers []string
	}{
		{"a form of another site", []string{"Content-Type", "text/plain", "Origin", "https://evil.example",
			"Sec-Fetch-Site", "cross-site"}},
		{"JSON of another site", []string{"Content-Type", "application/json", "Origin", "https://evil.example"}},
		{"an opaque origin", []string{"Content-Type", "application/json", "Origin", "null"}},
		{"a browser without Origin", []string{"Content-Type", "application/json", "Sec-Fetch-Site", "cross-site"}},
		{"a site of the same domain", []string{"Content-Type", "application/json", "Sec-Fetch-Site", "same-site"}},
	} {
		resp, body := request(t, http.MethodPost, srv.URL+"/api/controls/stop", "{}", tc.headers...)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, tc.name)
		assert.Contains(t, body, "controller", tc.name)
		resp, _ = request(t, http.MethodPost, srv.URL+"/api/controls/orders/1/cancel", "{}", tc.headers...)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, tc.name)
	}
	resp, _ := request(t, http.MethodGet, srv.URL+"/api/controls/balance", "", "Origin", "https://evil.example")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "nor read")
	resp, _ = request(t, http.MethodOptions, srv.URL+"/api/controls/stop", "", "Origin", "https://evil.example",
		"Access-Control-Request-Method", "POST")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "a preflight")
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, order.StatusRunning, controller.Status(), "the bot not stopped")
	assert.Empty(t, controller.canceled, "no order canceled")

	// what the user opens himself, or a page of the API itself, is not a site
	// of another origin
	for _, site := range []string{"none", "same-origin"} {
		resp, _ := request(t, http.MethodGet, srv.URL+"/api/pairs", "", "Sec-Fetch-Site", site)
		assert.Equal(t, http.StatusOK, resp.StatusCode, site)
	}
	resp, _ = request(t, http.MethodPost, srv.URL+"/api/controls/stop", "", "Content-Type", "application/json")
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the controller")
	assert.Equal(t, order.StatusStopped, controller.Status())
}

// A name of another site resolved to the loopback (DNS rebinding) makes its
// pages of the same origin as the API, without an Origin on their reads: the
// API answers the hosts of the loopback only, the ones the controller asks
// for.
func TestAPI_RefusesTheHostsOffTheLoopback(t *testing.T) {
	c := newTestChart(t)
	c.OnCandle(candleAt("ETHUSDT", time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC), 3000))
	controller := &fakeController{status: order.StatusRunning}
	c.SetOrderController(controller)
	srv := newTestServer(t, c)
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]

	send := func(method, path, host string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, nil)
		require.NoError(t, err)
		req.Host = host
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp
	}
	for _, host := range []string{"rebind.evil.example:" + port, "rebind.evil.example", "10.0.0.5:" + port,
		"0.0.0.0:" + port, "localhost.evil.example:" + port, "[fe80::1]:" + port} {
		assert.Equal(t, http.StatusMisdirectedRequest, send(http.MethodGet, "/api/controls/balance", host).StatusCode,
			host)
		assert.Equal(t, http.StatusMisdirectedRequest, send(http.MethodPost, "/api/controls/stop", host).StatusCode,
			host)
	}
	assert.Equal(t, order.StatusRunning, controller.Status(), "the bot not stopped")

	for _, host := range []string{"localhost:" + port, "LOCALHOST", "127.0.0.1:" + port, "127.0.0.2", "[::1]:" + port,
		"[::1]"} {
		assert.Equal(t, http.StatusOK, send(http.MethodGet, "/api/pairs", host).StatusCode, host)
	}
}

// The requests that change something are JSON, even without a payload: a page
// of another site can't send JSON without a preflight, which the API refuses.
func TestAPI_ChangesAreJSON(t *testing.T) {
	c := newTestChart(t)
	c.OnCandle(candleAt("ETHUSDT", time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC), 3000))
	controller := &fakeController{status: order.StatusRunning}
	c.SetOrderController(controller)
	srv := newTestServer(t, c)

	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded",
		"multipart/form-data; boundary=x", "application/jsonx", "text/json"} {
		headers := []string{"Content-Type", contentType}
		resp, body := request(t, http.MethodPost, srv.URL+"/api/controls/stop", "", headers...)
		assert.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode, contentType)
		assert.Contains(t, body, "application/json", contentType)
		resp, _ = request(t, http.MethodPost, srv.URL+"/api/controls/orders/1/cancel", "{}", headers...)
		assert.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode, contentType)
	}
	assert.Equal(t, order.StatusRunning, controller.Status(), "the bot not stopped")
	assert.Empty(t, controller.canceled, "no order canceled")

	resp, _ := request(t, http.MethodPost, srv.URL+"/api/controls/stop", "",
		"Content-Type", "Application/JSON; charset=utf-8")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, controller.stopped, "its type taken whatever its case and parameters")
	resp, _ = request(t, http.MethodGet, srv.URL+"/api/controls", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the reads take no payload")
}

// An agent the controller launched answers it only, with the key of its start:
// another process of the machine can't operate it.
func TestAPI_Key(t *testing.T) {
	c := newTestChart(t, WithAPIKey("the-key"))
	controller := &fakeController{status: order.StatusRunning}
	c.SetOrderController(controller)
	srv := newTestServer(t, c)

	for _, key := range []string{"", "another", "THE-KEY", "the-key-and-more"} {
		resp, body := request(t, http.MethodGet, srv.URL+"/api/pairs", "", KeyHeader, key)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, key)
		assert.Contains(t, body, "controller", key)
		resp, _ = request(t, http.MethodPost, srv.URL+"/api/controls/stop", "", "Content-Type", "application/json",
			KeyHeader, key)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, key)
	}
	assert.Equal(t, order.StatusRunning, controller.Status())

	resp, _ := request(t, http.MethodGet, srv.URL+"/api/pairs", "", KeyHeader, "the-key")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = request(t, http.MethodPost, srv.URL+"/api/controls/stop", "", "Content-Type", "application/json",
		KeyHeader, "the-key")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, order.StatusStopped, controller.Status())

	// an agent run on its own has none
	resp, _ = request(t, http.MethodGet, newTestServer(t, newTestChart(t, WithAPIKey(""))).URL+"/api/pairs", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
