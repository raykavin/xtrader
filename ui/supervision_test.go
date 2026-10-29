package ui

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
)

// fakeSupervised is the order controller of a bot as the supervisor operates
// it: its positions by pair, its entries, and the closes and cancels asked.
type fakeSupervised struct {
	mu        sync.Mutex
	status    order.Status
	positions map[string]float64
	blocked   bool
	reason    string
	revision  int64
	holds     int
	heldAt    []int // the holds as each position was closed
	closeErr  map[string]error
	closed    []string
	canceled  []int64
	cancelErr map[int64]error
	drained   int // the waits for the orders being sent
}

func (f *fakeSupervised) WaitIdle(time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drained++
	return true
}

func (f *fakeSupervised) Status() order.Status { return f.status }

func (f *fakeSupervised) Position(pair string) (float64, float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.positions[pair], 0, nil
}

func (f *fakeSupervised) EntriesBlocked() (bool, string, int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocked || f.holds > 0, f.reason, f.revision
}

func (f *fakeSupervised) BlockEntries(revision int64, reason string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if revision < f.revision {
		return false
	}
	f.blocked, f.reason, f.revision = true, reason, revision
	return true
}

func (f *fakeSupervised) AllowEntries(revision int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if revision < f.revision {
		return false
	}
	f.blocked, f.reason, f.revision = false, "", revision
	return true
}

func (f *fakeSupervised) HoldEntries(string) func() {
	f.mu.Lock()
	f.holds++
	f.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			f.holds--
			f.mu.Unlock()
		})
	}
}

func (f *fakeSupervised) ClosePosition(pair string) (model.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heldAt = append(f.heldAt, f.holds)
	if err := f.closeErr[pair]; err != nil {
		return model.Order{}, err
	}
	asset := f.positions[pair]
	if asset == 0 {
		return model.Order{}, exchange.ErrNothingToReduce
	}
	f.closed = append(f.closed, pair)
	delete(f.positions, pair)
	side := model.SideTypeSell
	if asset < 0 {
		side, asset = model.SideTypeBuy, -asset
	}
	return model.Order{Pair: pair, Side: side, Quantity: asset, Status: model.OrderStatusTypeFilled}, nil
}

func (f *fakeSupervised) Cancel(o model.Order) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.cancelErr[o.ID]; err != nil {
		return err
	}
	f.canceled = append(f.canceled, o.ID)
	return nil
}

// newSupervisedChart returns a chart of BTCUSDT and ETHUSDT operated by
// controller, served.
func newSupervisedChart(t *testing.T, controller *fakeSupervised) (*Chart, string) {
	t.Helper()
	c := newTestChart(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	c.OnCandle(candleAt("BTCUSDT", at, 100))
	c.OnCandle(candleAt("ETHUSDT", at, 10))
	c.SetSupervision(controller)
	return c, newTestServer(t, c).URL
}

var supervisorHeaders = []string{ChannelHeader, string(ChannelSupervisor), "Content-Type", "application/json"}

// Only the supervisor reaches the routes: the dashboard, Telegram and a
// request without a channel get a 404, as a bot without supervision does.
func TestSupervision_OnlyTheSupervisorChannel(t *testing.T) {
	controller := &fakeSupervised{status: order.StatusRunning, positions: map[string]float64{"BTCUSDT": 1}}
	_, url := newSupervisedChart(t, controller)

	for _, channel := range []string{"", string(ChannelWeb), string(ChannelTelegram)} {
		headers := []string{"Content-Type", "application/json"}
		if channel != "" {
			headers = append(headers, ChannelHeader, channel)
		}
		resp, _ := request(t, http.MethodGet, url+"/api/supervision", "", headers...)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, channel)
		resp, _ = request(t, http.MethodPost, url+"/api/supervision/close", "{}", headers...)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, channel)
		resp, _ = request(t, http.MethodPost, url+"/api/supervision/entries", `{"blocked":true}`, headers...)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, channel)
	}
	require.Empty(t, controller.closed)
	require.False(t, controller.blocked)

	unsupervised := newTestServer(t, newTestChart(t)).URL
	resp, _ := request(t, http.MethodGet, unsupervised+"/api/supervision", "", supervisorHeaders...)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "no supervision set")
}

// The state tells the entries, the positions and the pending orders, the
// entries apart from the legs.
func TestSupervision_State(t *testing.T) {
	controller := &fakeSupervised{status: order.StatusRunning, positions: map[string]float64{"BTCUSDT": -2},
		blocked: true, reason: "by the supervisor", revision: 3}
	c, url := newSupervisedChart(t, controller)
	group := int64(9)
	c.OnOrder(model.Order{ID: 1, Pair: "BTCUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeStopLoss,
		Status: model.OrderStatusTypeNew, Price: 110, Quantity: 2, GroupID: &group})
	c.OnOrder(model.Order{ID: 2, Pair: "ETHUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeLimit,
		Status: model.OrderStatusTypeNew, Price: 9, Quantity: 1})
	c.OnOrder(model.Order{ID: 3, Pair: "ETHUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeLimit,
		Status: model.OrderStatusTypeCanceled, Price: 8, Quantity: 1})

	resp, body := request(t, http.MethodGet, url+"/api/supervision", "", supervisorHeaders...)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var state SupervisionState
	require.NoError(t, json.Unmarshal([]byte(body), &state))
	require.True(t, state.EntriesBlocked)
	require.Equal(t, "by the supervisor", state.EntriesReason)
	require.EqualValues(t, 3, state.EntriesRevision)
	require.Equal(t, "running", state.Status)
	require.Equal(t, []SupervisedPosition{{Pair: "BTCUSDT", Side: "SELL", Quantity: 2}}, state.Positions)
	require.Len(t, state.Pending, 2, "the canceled one left out")
	byID := map[int64]SupervisedOrder{}
	for _, o := range state.Pending {
		byID[o.ID] = o
	}
	require.False(t, byID[1].Entry, "a leg of the short")
	require.True(t, byID[2].Entry, "a limit out of a group")
}

// The entries are blocked and allowed again, an older revision ignored.
func TestSupervision_Entries(t *testing.T) {
	controller := &fakeSupervised{status: order.StatusRunning}
	_, url := newSupervisedChart(t, controller)

	resp, body := request(t, http.MethodPost, url+"/api/supervision/entries",
		`{"blocked":true,"revision":4,"reason":"too much gold"}`, supervisorHeaders...)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var state SupervisionState
	require.NoError(t, json.Unmarshal([]byte(body), &state))
	require.True(t, state.EntriesBlocked)
	require.EqualValues(t, 4, state.EntriesRevision)

	resp, body = request(t, http.MethodPost, url+"/api/supervision/entries", `{"blocked":false,"revision":3}`,
		supervisorHeaders...)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &state))
	require.True(t, state.EntriesBlocked, "an older allowance ignored")

	resp, body = request(t, http.MethodPost, url+"/api/supervision/entries", `{"blocked":false,"revision":5}`,
		supervisorHeaders...)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &state))
	require.False(t, state.EntriesBlocked)
}

// The close blocks the entries when asked, holds them while each position is
// closed, and tells how each pair went: an outcome unknown is never sent
// again.
func TestSupervision_Close(t *testing.T) {
	controller := &fakeSupervised{status: order.StatusStopped,
		positions: map[string]float64{"BTCUSDT": 3},
		closeErr: map[string]error{"ETHUSDT": &exchange.OrderError{Err: exchange.ErrMT5OutcomeUnknown,
			Pair: "ETHUSDT"}}}
	_, url := newSupervisedChart(t, controller)

	resp, body := request(t, http.MethodPost, url+"/api/supervision/close",
		`{"block":true,"revision":2,"reason":"close all"}`, supervisorHeaders...)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var response SupervisionCloseResponse
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	require.Equal(t, []SupervisionCloseResult{
		{Pair: "BTCUSDT", Status: SupervisionClosed, Side: "SELL", Quantity: 3},
		{Pair: "ETHUSDT", Status: SupervisionUnknown, Error: response.Results[1].Error},
	}, response.Results)
	require.True(t, controller.blocked)
	require.Equal(t, "close all", controller.reason)
	require.Equal(t, []int{1, 1}, controller.heldAt, "held as each position closed")
	require.Zero(t, controller.holds, "released after, without hold_seconds")

	// a pair flat, another failing, an unknown one refused
	controller.closeErr = map[string]error{"ETHUSDT": errors.New("market closed")}
	resp, body = request(t, http.MethodPost, url+"/api/supervision/close", `{"pairs":["btcusdt","ETHUSDT"]}`,
		supervisorHeaders...)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	require.Equal(t, SupervisionFlat, response.Results[0].Status)
	require.Equal(t, "BTCUSDT", response.Results[0].Pair)
	require.Equal(t, SupervisionFailed, response.Results[1].Status)
	require.Equal(t, "market closed", response.Results[1].Error)
	resp, _ = request(t, http.MethodPost, url+"/api/supervision/close", `{"pairs":["XAUUSD"]}`, supervisorHeaders...)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	// held a while after
	resp, body = request(t, http.MethodPost, url+"/api/supervision/close", `{"hold_seconds":60}`,
		supervisorHeaders...)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.Equal(t, 1, controller.holds, "held for the minute asked")
}

// The cancel cancels the pending entries only, the legs of the positions
// staying.
func TestSupervision_Cancel(t *testing.T) {
	controller := &fakeSupervised{status: order.StatusRunning,
		cancelErr: map[int64]error{3: &exchange.OrderError{Err: exchange.ErrOrderNotOpen}}}
	c, url := newSupervisedChart(t, controller)
	group := int64(7)
	c.OnOrder(model.Order{ID: 1, Pair: "BTCUSDT", Side: model.SideTypeSell, Type: model.OrderTypeLimitMaker,
		Status: model.OrderStatusTypeNew, Price: 120, Quantity: 1, GroupID: &group})
	c.OnOrder(model.Order{ID: 2, Pair: "BTCUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeLimit,
		Status: model.OrderStatusTypeNew, Price: 90, Quantity: 1})
	c.OnOrder(model.Order{ID: 3, Pair: "ETHUSDT", Side: model.SideTypeSell, Type: model.OrderTypeStopLoss,
		Status: model.OrderStatusTypeNew, Price: 9, Quantity: 1})

	resp, body := request(t, http.MethodPost, url+"/api/supervision/cancel", `{}`, supervisorHeaders...)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var response SupervisionCancelResponse
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	require.ElementsMatch(t, []SupervisionCancelResult{
		{ID: 2, Pair: "BTCUSDT", Status: SupervisionCanceled},
		{ID: 3, Pair: "ETHUSDT", Status: SupervisionGone},
	}, response.Results)
	require.Equal(t, []int64{2}, controller.canceled, "the leg of the position stays")
}

// fakeProtector is the protection of the positions of a bot: its state, and
// the arms asked.
type fakeProtector struct {
	mu    sync.Mutex
	state PositionProtection
	arms  []SupervisionProtectRequest
}

func (p *fakeProtector) Protection(pair string) *PositionProtection {
	if pair != p.state.Pair {
		return nil
	}
	state := p.state
	return &state
}

func (p *fakeProtector) Protections() []PositionProtection { return []PositionProtection{p.state} }

func (p *fakeProtector) Arm(request SupervisionProtectRequest) SupervisionProtectResponse {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.arms = append(p.arms, request)
	return SupervisionProtectResponse{Status: ProtectArmed, Previous: 98, Detail: "armado: aguardando o gatilho"}
}

// The supervisor arms the protection of a position on its channel only; the
// state tells the protection of the positions; the snapshot shows it on the
// dashboard; without the protection the route is a 404.
func TestSupervision_Protect(t *testing.T) {
	controller := &fakeSupervised{status: order.StatusRunning, positions: map[string]float64{"BTCUSDT": 1}}
	c, url := newSupervisedChart(t, controller)
	arm := `{"arm_id":4,"pair":"BTCUSDT","position_ref":"mt5:1","feature":"trailing","reason":"devolução"}`

	resp, _ := request(t, http.MethodPost, url+"/api/supervision/protect", arm, supervisorHeaders...)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "without the protection")

	protector := &fakeProtector{state: PositionProtection{Pair: "BTCUSDT", PositionRef: "mt5:1", Side: "BUY",
		Entry: 100, StopLoss: 98, Trailing: ProtectionFeature{State: ProtectionWaiting, Remaining: 0.4}}}
	c.SetProtection(protector)
	for _, channel := range []string{string(ChannelWeb), string(ChannelTelegram)} {
		resp, _ := request(t, http.MethodPost, url+"/api/supervision/protect", arm, ChannelHeader, channel,
			"Content-Type", "application/json")
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, channel)
	}
	require.Empty(t, protector.arms)

	resp, body := request(t, http.MethodPost, url+"/api/supervision/protect", arm, supervisorHeaders...)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var armed SupervisionProtectResponse
	require.NoError(t, json.Unmarshal([]byte(body), &armed))
	require.Equal(t, ProtectArmed, armed.Status)
	require.Len(t, protector.arms, 1)
	require.Equal(t, "mt5:1", protector.arms[0].PositionRef)

	resp, _ = request(t, http.MethodPost, url+"/api/supervision/protect", `{"pair":"BTCUSDT"}`,
		supervisorHeaders...)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "without its record nor its reason")

	resp, body = request(t, http.MethodGet, url+"/api/supervision", "", supervisorHeaders...)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var state SupervisionState
	require.NoError(t, json.Unmarshal([]byte(body), &state))
	require.Len(t, state.Protections, 1)
	require.Equal(t, ProtectionWaiting, state.Protections[0].Trailing.State)

	resp, body = request(t, http.MethodGet, url+"/api/BTCUSDT/snapshot", "", ChannelHeader, string(ChannelWeb))
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var snapshot Snapshot
	require.NoError(t, json.Unmarshal([]byte(body), &snapshot))
	require.NotNil(t, snapshot.Protection, "on the dashboard")
	require.Equal(t, 98.0, snapshot.Protection.StopLoss)
}

// Blocked with drain, the entries wait for the orders being sent to end before
// answering; without it, or allowed, they don't.
func TestSupervision_EntriesDrain(t *testing.T) {
	controller := &fakeSupervised{status: order.StatusRunning}
	_, url := newSupervisedChart(t, controller)
	for _, body := range []string{`{"blocked":true,"revision":1,"reason":"limite diário"}`,
		`{"blocked":true,"revision":2,"reason":"limite diário","drain":true}`, `{"blocked":false,"revision":3,"drain":true}`} {
		resp, answer := request(t, http.MethodPost, url+"/api/supervision/entries", body, supervisorHeaders...)
		require.Equal(t, http.StatusOK, resp.StatusCode, answer)
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	require.Equal(t, 1, controller.drained)
}
