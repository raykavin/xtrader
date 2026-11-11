package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/ui"
)

// recorder is an agent of the tests keeping the headers of the last request
// it got.
type recorder struct {
	*httptest.Server
	mu     sync.Mutex
	header http.Header
}

func newRecorder(t *testing.T, handler http.HandlerFunc) *recorder {
	t.Helper()
	r := &recorder{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.header = req.Header.Clone()
		r.mu.Unlock()
		handler(w, req)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *recorder) last() http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.header
}

// localBundle returns the directory of a bundle built locally, see WithUIDir.
func localBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log('ok')"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.css"), []byte("body{}"), 0o644))
	return dir
}

// browserHeaders are the headers of a browser, and of the proxies before the
// dashboard, the agent never gets: its credentials of the dashboard, where the
// request comes from, and a channel and a key forged.
var browserHeaders = []string{
	"Authorization", "Bearer xyz",
	"Proxy-Authorization", "Basic dXNlcjpwYXNz",
	"Origin", "https://dashboard.example",
	"Referer", "https://dashboard.example/?agent=bot",
	"Sec-Fetch-Site", "same-origin",
	"Sec-Fetch-Mode", "cors",
	"Sec-Fetch-Dest", "empty",
	"X-Forwarded-For", "203.0.113.7",
	"X-Forwarded-Host", "dashboard.example",
	"X-Forwarded-Proto", "https",
	"X-Forwarded-Port", "443",
	"Forwarded", "for=203.0.113.7;proto=https",
	"X-Real-Ip", "203.0.113.7",
	ui.ChannelHeader, string(ui.ChannelTelegram),
	ui.KeyHeader, "forged",
}

// The proxy of the trade panel hands the agent the request of the browser
// without the session of the user, its CSRF token, or any other credential:
// the agent, a process of its own, gets what its API needs only, the channel
// the dashboard's.
func TestAgentProxy_TheAgentGetsNoCredentials(t *testing.T) {
	agent := newRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"enabled":true}`)
	})
	d, err := NewDashboard([]Agent{{Name: "bot", Address: agent.URL}},
		WithUsers(newFakeUsers(testUsername, testPassword)), WithUIDir(localBundle(t)))
	require.NoError(t, err)
	srv := httptest.NewServer(d.Handler(context.Background()))
	t.Cleanup(srv.Close)
	resp, _ := login(t, srv, testUsername, testPassword)
	session, csrf := cookieOf(resp, sessionCookie), cookieOf(resp, csrfCookie)
	require.NotNil(t, session)
	require.NotNil(t, csrf)
	cookies := []*http.Cookie{session, csrf, {Name: "other", Value: "of-the-site"}}

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		headers := append([]string{csrfHeader, csrf.Value, "Accept", "application/json"}, browserHeaders...)
		resp, _ := do(t, method, srv.URL+"/api/agents/bot/controls/stop", "", cookies, headers...)
		require.Equal(t, http.StatusOK, resp.StatusCode, method)

		got := agent.last()
		for _, name := range []string{"Cookie", "Authorization", "Proxy-Authorization", csrfHeader, "Origin",
			"Referer", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest", "X-Forwarded-For", "X-Forwarded-Host",
			"X-Forwarded-Proto", "X-Forwarded-Port", "Forwarded", "X-Real-Ip", ui.KeyHeader} {
			assert.Empty(t, got.Values(name), "%s %s", method, name)
		}
		assert.Equal(t, string(ui.ChannelWeb), got.Get(ui.ChannelHeader), method)
		assert.Equal(t, "application/json", got.Get("Accept"), method)
		if method == http.MethodPost {
			assert.Equal(t, "application/json", got.Get("Content-Type"),
				"a change without a payload, as the web app sends start and stop")
		}
	}

	// a payload keeps its type, the agent refusing what isn't JSON
	resp, _ = do(t, http.MethodPost, srv.URL+"/api/agents/bot/controls/orders/1/move", "", cookies, csrfHeader,
		csrf.Value,
		"Content-Type", "text/plain")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/plain", agent.last().Get("Content-Type"))
}

// What the agent answers is served on the origin of the dashboard: its
// cookies never reach the browser, nor its headers acting on the dashboard,
// and only the types its API serves do, never sniffed, the others a 502: a
// page of the agent can't run on the origin of the dashboard.
func TestAgentProxy_AnswersOfTheAgent(t *testing.T) {
	answers := map[string]func(w http.ResponseWriter){
		"json": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		},
		"html": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<script>alert(document.cookie)</script>")
		},
		"untyped": func(w http.ResponseWriter) {
			w.Header()["Content-Type"] = nil // not sniffed by the agent
			_, _ = io.WriteString(w, "<html><script>alert(document.cookie)</script>")
		},
		"empty": func(w http.ResponseWriter) { w.WriteHeader(http.StatusAccepted) },
		"svg": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
		},
		"png": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = io.WriteString(w, "\x89PNG\r\n\x1a\n")
		},
		"octet": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, "<script>alert(1)</script>")
		},
		"download": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", "attachment")
			_, _ = io.WriteString(w, "data")
		},
		"csv": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/csv")
			w.Header().Set("Content-Disposition", "attachment;filename=history_X.csv")
			_, _ = io.WriteString(w, "a,b\n")
		},
		"text": func(w http.ResponseWriter) { http.Error(w, "unknown pair", http.StatusNotFound) },
		"javascript": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = io.WriteString(w, "alert(1)")
		},
		"xml": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<x:script xmlns:x="http://www.w3.org/1999/xhtml">alert(1)</x:script>`)
		},
	}
	agent := newRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "xtrader_session=fixated; Path=/")
		w.Header().Add("Set-Cookie", "csrf_token=fixated; Path=/")
		w.Header().Set("Access-Control-Allow-Origin", "https://evil.example")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		w.Header().Set("Clear-Site-Data", `"*"`)
		w.Header().Set("Refresh", "0; url=https://evil.example")
		w.Header().Set("Location", "https://evil.example")
		w.Header().Set("Link", "<https://evil.example/x.js>; rel=preload; as=script")
		w.Header().Set("X-Content-Type-Options", "sniff")
		w.Header().Set("Cache-Control", "no-cache")
		answers[strings.TrimPrefix(r.URL.Path, "/api/")](w)
	})
	srv := newDashboardServer(t, []Agent{{Name: "bot", Address: agent.URL}}, nil)

	for _, tc := range []struct {
		path   string
		status int
		body   string
	}{
		{"json", http.StatusOK, `{"ok":true}`},
		{"empty", http.StatusAccepted, ""},
		{"png", http.StatusOK, "\x89PNG\r\n\x1a\n"},
		{"download", http.StatusOK, "data"},
		{"csv", http.StatusOK, "a,b\n"},
		{"text", http.StatusNotFound, "unknown pair\n"},
		{"html", http.StatusBadGateway, ""},
		{"untyped", http.StatusBadGateway, ""},
		{"svg", http.StatusBadGateway, ""},
		{"octet", http.StatusBadGateway, ""},
		{"javascript", http.StatusBadGateway, ""},
		{"xml", http.StatusBadGateway, ""},
	} {
		resp, body := get(t, srv.URL+"/api/agents/bot/"+tc.path)
		assert.Equal(t, tc.status, resp.StatusCode, tc.path)
		if tc.status == http.StatusBadGateway {
			assert.Equal(t, "application/json", resp.Header.Get("Content-Type"), tc.path)
			assert.Contains(t, decodeError(t, body), "O agente bot respondeu com", tc.path)
			assert.NotContains(t, body, "alert(", tc.path)
		} else {
			assert.Equal(t, tc.body, body, tc.path)
			assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"), tc.path)
		}
		assert.Empty(t, resp.Cookies(), tc.path)
		assert.Empty(t, resp.Header.Values("Set-Cookie"), tc.path)
		assert.Equal(t, []string{"nosniff"}, resp.Header.Values("X-Content-Type-Options"), tc.path)
		for _, name := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials",
			"Strict-Transport-Security", "Clear-Site-Data", "Refresh", "Location", "Link"} {
			assert.Empty(t, resp.Header.Values(name), "%s %s", tc.path, name)
		}
	}
}

// The calls of the Telegram of the controller are JSON, even without a
// payload, eg: /start and /stop, and carry none of the headers of a browser.
func TestDashboard_CallIsJSON(t *testing.T) {
	c, bot := newAgent(t, nil)
	controller := &fakeController{status: order.StatusRunning}
	c.SetOrderController(controller)
	d, err := NewDashboard([]Agent{{Name: "bot", Address: bot.URL}})
	require.NoError(t, err)

	var controls ui.ControlsResponse
	require.NoError(t, d.call(context.Background(), agentNamed(t, d, "bot"), http.MethodPost, "controls/stop", nil,
		&controls))
	assert.Equal(t, "stopped", controls.Status)
	assert.Equal(t, order.StatusStopped, controller.Status())

	agent := newRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	})
	require.NoError(t, d.SetAgents([]Agent{{Name: "rec", Address: agent.URL}}))
	require.NoError(t, d.call(context.Background(), agentNamed(t, d, "rec"), http.MethodPost, "controls/start", nil,
		nil))
	got := agent.last()
	assert.Equal(t, "application/json", got.Get("Content-Type"))
	assert.Equal(t, string(ui.ChannelTelegram), got.Get(ui.ChannelHeader))
	for _, name := range []string{"Origin", "Referer", "Sec-Fetch-Site", "Cookie", "Authorization"} {
		assert.Empty(t, got.Values(name), name)
	}
}
