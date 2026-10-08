package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/ui"
)

// An agent the controller launched answers the dashboard and the Telegram of
// the controller with the key of its start only, which the browser can't
// forge: another process of the machine reaching its port can't operate it.
func TestDashboard_KeyOfTheAgent(t *testing.T) {
	chart, err := ui.New(ui.WithAPIKey("the-key"))
	require.NoError(t, err)
	controller := &fakeController{status: order.StatusRunning}
	chart.SetOrderController(controller)
	chart.OnCandle(candleAt("ETHUSDT", testBase, 3000))
	bot := httptest.NewServer(chart.Handler())
	t.Cleanup(bot.Close)
	d, err := NewDashboard([]Agent{{Name: "bot", Address: bot.URL}})
	require.NoError(t, err)
	srv := httptest.NewServer(d.router(bundle{dir: t.TempDir(), version: "v1.0.0"}, nil))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	bots := func() *agent { return agentNamed(t, d, "bot") }

	// without it, eg: an agent of a controller before, refused
	resp, body := get(t, srv.URL+"/api/agents/bot/pairs")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, decodeError(t, body), "the controller that launched it")
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents/bot/pairs", "", nil, ui.KeyHeader, "the-key")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "the key of the browser dropped")
	assert.False(t, d.Online(ctx, "bot"))
	var apiErr *apiError
	require.ErrorAs(t, d.call(ctx, bots(), http.MethodGet, "pairs", nil, nil), &apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.Status)

	d.keys.set("bot", "the-key")
	resp, body = do(t, http.MethodGet, srv.URL+"/api/agents/bot/pairs", "", nil, ui.KeyHeader, "forged")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"pairs":["ETHUSDT"]}`, body)
	assert.True(t, d.Online(ctx, "bot"))
	require.NoError(t, d.call(ctx, bots(), http.MethodPost, "controls/stop", nil, nil), "the Telegram")
	assert.Equal(t, order.StatusStopped, controller.Status())
	eventsCtx, cancel := context.WithCancel(ctx)
	connected := false
	require.NoError(t, d.events(eventsCtx, bots(), func() { connected = true; cancel() }, nil))
	assert.True(t, connected, "the events")

	// the key of a start exited goes, not the one of the start after it
	d.keys.drop("bot", "the-key-before")
	assert.Equal(t, "the-key", d.keys.get("bot"))
	d.keys.drop("bot", "the-key")
	assert.Empty(t, d.keys.get("bot"))
	assert.False(t, d.Online(ctx, "bot"))
}
