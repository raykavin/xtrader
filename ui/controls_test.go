package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/storage"
)

// fakeController records calls and returns canned balances / orders.
type fakeController struct {
	status       order.Status
	account      model.Account
	asset, quote float64
	positionErr  error
	orderErr     error

	started, stopped bool
	lastSide         model.SideType
	lastPair         string
	lastAmount       float64
	marketCalls      int
	quoteCalls       int

	// the orders with a price, the protected ones, the moves and the cancels
	calls                []string
	lastPrice            float64
	lastStop, lastProfit float64
	lastMoved            model.Order
	lastLevel            float64
	canceled             []int64
	moveErr, cancelErr   error
	legsErr              error // of a protected entry placed, its legs failing
}

// pointedController is a controller of an account valuing a move of 1 of the
// price of a unit at point in its currency, as MT5 on a BRL account.
type pointedController struct {
	fakeController
	point float64
}

func (p *pointedController) PointValue(string) (float64, error) { return p.point, nil }

func (f *fakeController) Status() order.Status { return f.status }
func (f *fakeController) Start()               { f.started = true; f.status = order.StatusRunning }
func (f *fakeController) Stop()                { f.stopped = true; f.status = order.StatusStopped }

func (f *fakeController) Account() (model.Account, error) { return f.account, f.positionErr }

func (f *fakeController) Position(string) (float64, float64, error) {
	return f.asset, f.quote, f.positionErr
}

func (f *fakeController) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	f.marketCalls++
	f.lastSide, f.lastPair, f.lastAmount = side, pair, size
	if f.orderErr != nil {
		return model.Order{}, f.orderErr
	}
	return model.Order{ID: 1, Pair: pair, Side: side, Quantity: size}, nil
}

func (f *fakeController) CreateOrderMarketQuote(side model.SideType, pair string, amount float64) (model.Order, error) {
	f.quoteCalls++
	f.lastSide, f.lastPair, f.lastAmount = side, pair, amount
	if f.orderErr != nil {
		return model.Order{}, f.orderErr
	}
	return model.Order{ID: 2, Pair: pair, Side: side, Quantity: amount}, nil
}

func (f *fakeController) CreateOrderLimit(side model.SideType, pair string, size, limit float64) (model.Order,
	error) {
	f.calls = append(f.calls, "limit")
	f.lastSide, f.lastPair, f.lastAmount, f.lastPrice = side, pair, size, limit
	return model.Order{ID: 3, Pair: pair, Side: side, Type: model.OrderTypeLimit, Quantity: size, Price: limit},
		f.orderErr
}

func (f *fakeController) CreateOrderMarketProtected(side model.SideType, pair string, size, stop,
	profit float64) ([]model.Order, error) {
	f.calls = append(f.calls, "market protected")
	f.lastSide, f.lastPair, f.lastAmount, f.lastStop, f.lastProfit = side, pair, size, stop, profit
	if f.orderErr != nil {
		return nil, f.orderErr
	}
	return []model.Order{{ID: 4, Pair: pair, Side: side, Type: model.OrderTypeMarket, Quantity: size}}, f.legsErr
}

func (f *fakeController) CreateOrderLimitProtected(side model.SideType, pair string, size, limit, stop,
	profit float64) ([]model.Order, error) {
	f.calls = append(f.calls, "limit protected")
	f.lastSide, f.lastPair, f.lastAmount, f.lastPrice = side, pair, size, limit
	f.lastStop, f.lastProfit = stop, profit
	if f.orderErr != nil {
		return nil, f.orderErr
	}
	return []model.Order{{ID: 5, Pair: pair, Side: side, Type: model.OrderTypeLimit, Quantity: size, Price: limit}},
		f.legsErr
}

func (f *fakeController) UpdateProtection(leg model.Order, level float64) (model.Order, error) {
	f.calls = append(f.calls, "move protection")
	f.lastMoved, f.lastLevel = leg, level
	leg.Price = level
	return leg, f.moveErr
}

func (f *fakeController) UpdateLimit(order model.Order, limit float64) (model.Order, error) {
	f.calls = append(f.calls, "move limit")
	f.lastMoved, f.lastLevel = order, limit
	order.Price = limit
	return order, f.moveErr
}

func (f *fakeController) Cancel(order model.Order) error {
	f.canceled = append(f.canceled, order.ID)
	return f.cancelErr
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

func decodeControls(t *testing.T, body string) ControlsResponse {
	t.Helper()
	var response ControlsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	return response
}

func TestControls_Disabled(t *testing.T) {
	c := newTestChart(t)
	srv := newTestServer(t, c)

	resp, body := get(t, srv.URL+"/api/controls")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, ControlsResponse{Enabled: false, Status: ""}, decodeControls(t, body))

	for _, path := range []string{"/controls/start", "/controls/stop", "/controls/order"} {
		resp, body := postJSON(t, srv.URL+"/api"+path, "{}")
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, path)
		assert.Contains(t, body, "Controles do agente desligados")
	}
}

func TestControls_StatusStartStop(t *testing.T) {
	c := newTestChart(t)
	controller := &fakeController{status: order.StatusRunning}
	c.SetOrderController(controller)
	srv := newTestServer(t, c)

	resp, body := get(t, srv.URL+"/api/controls")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, ControlsResponse{Enabled: true, Status: "running", BotControl: true}, decodeControls(t, body))

	resp, body = postJSON(t, srv.URL+"/api/controls/stop", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, controller.stopped)
	assert.Equal(t, ControlsResponse{Enabled: true, Status: "stopped", BotControl: true}, decodeControls(t, body))

	resp, body = postJSON(t, srv.URL+"/api/controls/start", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, controller.started)
	assert.Equal(t, ControlsResponse{Enabled: true, Status: "running", BotControl: true}, decodeControls(t, body))
}

func TestControls_OrderValidation(t *testing.T) {
	c := newTestChart(t)
	c.SetOrderController(&fakeController{})
	c.OnCandle(candleAt("BTCUSDT", time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC), 40000))
	srv := newTestServer(t, c)

	testCases := []struct {
		name, body, wantError string
		wantStatus            int
	}{
		{"invalid json", "{", "Pedido inválido", http.StatusBadRequest},
		{"unknown pair", `{"pair":"DOGEUSDT","side":"buy","amount":10}`, "Ativo desconhecido", http.StatusNotFound},
		{"zero amount", `{"pair":"BTCUSDT","side":"buy","amount":0}`, "A quantidade deve ser maior que zero", http.StatusBadRequest},
		{"percent above 100", `{"pair":"BTCUSDT","side":"buy","amount":150,"percent":true}`,
			"não pode passar de 100", http.StatusBadRequest},
		{"percent and lot", `{"pair":"BTCUSDT","side":"buy","amount":50,"percent":true,"lot":true}`,
			"não nos dois", http.StatusBadRequest},
		{"invalid side", `{"pair":"BTCUSDT","side":"hold","amount":10}`, "Lado da ordem inválido", http.StatusBadRequest},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := postJSON(t, srv.URL+"/api/controls/order", tc.body)
			assert.Equal(t, tc.wantStatus, resp.StatusCode)
			assert.Contains(t, body, tc.wantError)
		})
	}
}

func TestControls_OrderSemantics(t *testing.T) {
	base := time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC)

	testCases := []struct {
		name            string
		body            string
		wantSide        model.SideType
		wantAmount      float64
		wantMarketCalls int
		wantQuoteCalls  int
	}{
		{"buy quote amount", `{"pair":"btcusdt","side":"buy","amount":100}`,
			model.SideTypeBuy, 100, 0, 1},
		{"buy percent of quote balance", `{"pair":"BTCUSDT","side":"BUY","amount":50,"percent":true}`,
			model.SideTypeBuy, 500, 0, 1},
		{"sell quote amount", `{"pair":"BTCUSDT","side":"sell","amount":25}`,
			model.SideTypeSell, 25, 0, 1},
		{"sell percent of asset balance", `{"pair":"BTCUSDT","side":"sell","amount":50,"percent":true}`,
			model.SideTypeSell, 1, 1, 0},
		{"buy a size in lots", `{"pair":"BTCUSDT","side":"buy","amount":0.1,"lot":true}`,
			model.SideTypeBuy, 0.1, 1, 0},
		{"sell a size in lots, more than a lot too", `{"pair":"BTCUSDT","side":"sell","amount":150,"lot":true}`,
			model.SideTypeSell, 150, 1, 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestChart(t)
			controller := &fakeController{asset: 2, quote: 1000}
			c.SetOrderController(controller)
			c.OnCandle(candleAt("BTCUSDT", base, 40000))
			srv := newTestServer(t, c)

			resp, body := postJSON(t, srv.URL+"/api/controls/order", tc.body)
			require.Equal(t, http.StatusOK, resp.StatusCode, body)

			assert.Equal(t, tc.wantSide, controller.lastSide)
			assert.Equal(t, "BTCUSDT", controller.lastPair)
			assert.InDelta(t, tc.wantAmount, controller.lastAmount, 1e-9)
			assert.Equal(t, tc.wantMarketCalls, controller.marketCalls)
			assert.Equal(t, tc.wantQuoteCalls, controller.quoteCalls)

			var created Order
			require.NoError(t, json.Unmarshal([]byte(body), &created))
			assert.Equal(t, "BTCUSDT", created.Pair)
		})
	}
}

func TestControls_OrderErrors(t *testing.T) {
	base := time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC)

	c := newTestChart(t)
	controller := &fakeController{orderErr: order.ErrBotStopped}
	c.SetOrderController(controller)
	c.OnCandle(candleAt("BTCUSDT", base, 40000))
	srv := newTestServer(t, c)

	resp, body := postJSON(t, srv.URL+"/api/controls/order", `{"pair":"BTCUSDT","side":"buy","amount":10}`)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Contains(t, body, "O bot está parado")

	controller.orderErr = assert.AnError
	resp, body = postJSON(t, srv.URL+"/api/controls/order", `{"pair":"BTCUSDT","side":"buy","amount":10}`)
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.Contains(t, body, assert.AnError.Error())
}

// failingStorage fails every write, eg: a full disk.
type failingStorage struct{}

func (failingStorage) CreateOrder(*model.Order) error { return errors.New("no space left on device") }
func (failingStorage) UpdateOrder(*model.Order) error { return errors.New("no space left on device") }
func (failingStorage) Orders(...storage.OrderFilter) ([]*model.Order, error) {
	return nil, nil
}

// An order executed by the broker is never answered as an error, which would
// have it sent again: the storage failing to write it, or the legs of a
// protected entry failing, told by the notifications of the bot.
func TestControls_OrderExecutedIsNoError(t *testing.T) {
	base := time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC)

	t.Run("not stored", func(t *testing.T) {
		ctx := context.Background()
		wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 100000))
		wallet.OnCandle(candleAt("BTCUSDT", base, 40000))
		controller := order.NewController(ctx, wallet, failingStorage{}, order.NewOrderFeed())
		c := newTestChart(t)
		controller.SetNotifier(c)
		c.SetOrderController(controller)
		c.OnCandle(candleAt("BTCUSDT", base, 40000))
		srv := newTestServer(t, c)

		for _, body := range []string{
			`{"pair":"BTCUSDT","side":"buy","amount":0.1,"lot":true}`,
			`{"pair":"BTCUSDT","side":"buy","amount":0.1,"lot":true,"stop_loss":39000,"take_profit":42000}`,
			`{"pair":"BTCUSDT","side":"buy","amount":0.1,"lot":true,"type":"limit","price":39500}`,
		} {
			resp, got := postJSON(t, srv.URL+"/api/controls/order", body)
			require.Equal(t, http.StatusOK, resp.StatusCode, "%s: %s", body, got)
			var created ControlOrderResponse
			require.NoError(t, json.Unmarshal([]byte(got), &created))
			assert.Equal(t, "BTCUSDT", created.Pair, body)
			assert.Empty(t, created.Warning, body)
			assert.NotContains(t, got, `"warning"`, body)
		}
		asset, _, err := wallet.Position("BTCUSDT")
		require.NoError(t, err)
		assert.InDelta(t, 0.2, asset, 1e-9, "the market orders executed, once each")
	})

	t.Run("its legs failing", func(t *testing.T) {
		c := newTestChart(t)
		controller := &fakeController{asset: 2, quote: 1000, legsErr: exchange.ErrInvalidProtection}
		c.SetOrderController(controller)
		c.OnCandle(candleAt("BTCUSDT", base, 40000))
		srv := newTestServer(t, c)

		resp, body := postJSON(t, srv.URL+"/api/controls/order",
			`{"pair":"BTCUSDT","side":"buy","amount":0.1,"lot":true,"stop_loss":39000}`)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		assert.Contains(t, body, `"id":4`)
		assert.Equal(t, []string{"market protected"}, controller.calls)
		// B5: told in the answer that the position may be left unprotected
		var created ControlOrderResponse
		require.NoError(t, json.Unmarshal([]byte(body), &created))
		assert.Equal(t, int64(4), created.ID)
		assert.Contains(t, created.Warning, "só 0 das 1 ordens de stop loss e take profit")
		assert.Contains(t, created.Warning, exchange.ErrInvalidProtection.Error())
	})
}

func TestControls_OrderTicket(t *testing.T) {
	base := time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC)

	testCases := []struct {
		name                     string
		body                     string
		wantCall                 string
		wantSize, wantPrice      float64
		wantStop, wantTakeProfit float64
	}{
		{"a limit order, its amount in quote at the limit",
			`{"pair":"BTCUSDT","side":"buy","amount":1000,"type":"limit","price":20000}`, "limit", 0.05, 20000, 0, 0},
		{"a limit order of a lot", `{"pair":"BTCUSDT","side":"sell","amount":0.1,"lot":true,"type":"limit",` +
			`"price":50000}`, "limit", 0.1, 50000, 0, 0},
		{"a limit order of a share of the quote balance", `{"pair":"BTCUSDT","side":"buy","amount":50,"percent":true,` +
			`"type":"LIMIT","price":25000}`, "limit", 0.02, 25000, 0, 0},
		{"a market order with its stop loss and take profit, sized at the last close",
			`{"pair":"BTCUSDT","side":"buy","amount":4000,"stop_loss":39000,"take_profit":42000}`,
			"market protected", 0.1, 0, 39000, 42000},
		{"a market order with its stop loss only, of a share of the asset held",
			`{"pair":"BTCUSDT","side":"sell","amount":50,"percent":true,"type":"market","stop_loss":41000}`,
			"market protected", 1, 0, 41000, 0},
		{"a limit order with its take profit", `{"pair":"BTCUSDT","side":"buy","amount":0.2,"lot":true,` +
			`"type":"limit","price":38000,"take_profit":45000}`, "limit protected", 0.2, 38000, 0, 45000},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestChart(t)
			controller := &fakeController{asset: 2, quote: 1000}
			c.SetOrderController(controller)
			c.OnCandle(candleAt("BTCUSDT", base, 40000))
			srv := newTestServer(t, c)

			resp, body := postJSON(t, srv.URL+"/api/controls/order", tc.body)
			require.Equal(t, http.StatusOK, resp.StatusCode, body)
			require.Equal(t, []string{tc.wantCall}, controller.calls)
			assert.InDelta(t, tc.wantSize, controller.lastAmount, 1e-9)
			assert.InDelta(t, tc.wantPrice, controller.lastPrice, 1e-9)
			assert.InDelta(t, tc.wantStop, controller.lastStop, 1e-9)
			assert.InDelta(t, tc.wantTakeProfit, controller.lastProfit, 1e-9)
			assert.Zero(t, controller.marketCalls+controller.quoteCalls)
		})
	}

	t.Run("an amount of the account currency, sized by the point value", func(t *testing.T) {
		// a BRL account: 5 BRL a unit for 1 of the price
		c := newTestChart(t)
		controller := &pointedController{fakeController: fakeController{asset: 2, quote: 100000}, point: 5}
		c.SetOrderController(controller)
		c.OnCandle(candleAt("BTCUSDT", base, 40000))
		srv := newTestServer(t, c)
		for body, size := range map[string]float64{
			`{"pair":"BTCUSDT","side":"buy","amount":1000,"type":"limit","price":20000}`:              0.01,
			`{"pair":"BTCUSDT","side":"buy","amount":50,"percent":true,"type":"limit","price":25000}`: 0.4,
			`{"pair":"BTCUSDT","side":"buy","amount":4000,"stop_loss":39000}`:                         0.02,
		} {
			controller.calls = nil
			resp, answer := postJSON(t, srv.URL+"/api/controls/order", body)
			require.Equal(t, http.StatusOK, resp.StatusCode, answer)
			assert.InDelta(t, size, controller.lastAmount, 1e-9, body)
		}
	})

	t.Run("refused", func(t *testing.T) {
		c := newTestChart(t)
		controller := &fakeController{}
		c.SetOrderController(controller)
		c.OnCandle(candleAt("BTCUSDT", base, 40000))
		srv := newTestServer(t, c)

		for body, wantError := range map[string]string{
			`{"pair":"BTCUSDT","side":"buy","amount":10,"type":"stop"}`:                             `Tipo da ordem inválido`,
			`{"pair":"BTCUSDT","side":"buy","amount":10,"type":"limit"}`:                            "Informe o preço limite",
			`{"pair":"BTCUSDT","side":"buy","amount":10,"stop_loss":-1}`:                            "não podem ser negativos",
			`{"pair":"BTCUSDT","side":"buy","amount":10,"type":"limit","price":1,"take_profit":-5}`: "não podem ser negativos",
		} {
			resp, got := postJSON(t, srv.URL+"/api/controls/order", body)
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
			assert.Contains(t, got, wantError, body)
		}
		assert.Empty(t, controller.calls)
	})

	t.Run("the errors of the broker", func(t *testing.T) {
		for _, tc := range []struct {
			err        error
			wantStatus int
			wantError  string
		}{
			{exchange.ErrInvalidProtection, http.StatusBadRequest, "Stop loss ou take profit inválido."},
			{fmt.Errorf("%w: min: 0.01 max: 100", exchange.ErrInvalidQuantity), http.StatusBadRequest,
				"Tamanho da ordem inválido: min: 0.01 max: 100"},
			{exchange.ErrNotSupported, http.StatusUnprocessableEntity, "A corretora não aceita esta operação."},
			{order.ErrBotStopped, http.StatusConflict, "O bot está parado: inicie-o antes."},
			{fmt.Errorf("%w: limite diário", order.ErrEntriesBlocked), http.StatusConflict,
				EntriesBlocked + ": limite diário"}, // the lead the Telegram tells them by
			{order.ErrEntriesBlocked, http.StatusConflict, EntriesBlocked + "."},
		} {
			c := newTestChart(t)
			c.SetOrderController(&fakeController{orderErr: tc.err})
			c.OnCandle(candleAt("BTCUSDT", base, 40000))
			srv := newTestServer(t, c)

			resp, body := postJSON(t, srv.URL+"/api/controls/order",
				`{"pair":"BTCUSDT","side":"buy","amount":100,"stop_loss":39000}`)
			assert.Equal(t, tc.wantStatus, resp.StatusCode, tc.err)
			assert.Contains(t, body, tc.wantError, tc.err)
		}
	})
}

func TestControls_MoveAndCancel(t *testing.T) {
	base := time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC)
	group := int64(7)
	setup := func(t *testing.T) (*fakeController, string) {
		t.Helper()
		c := newTestChart(t)
		controller := &fakeController{}
		c.SetOrderController(controller)
		c.OnCandle(candleAt("BTCUSDT", base, 40000))
		for _, o := range []model.Order{
			{ID: 1, Pair: "BTCUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeLimit,
				Status: model.OrderStatusTypeNew, Price: 38000, Quantity: 0.1},
			{ID: 2, Pair: "BTCUSDT", Side: model.SideTypeSell, Type: model.OrderTypeLimitMaker,
				Status: model.OrderStatusTypeNew, Price: 45000, Quantity: 0.1, GroupID: &group},
			{ID: 3, Pair: "BTCUSDT", Side: model.SideTypeSell, Type: model.OrderTypeStopLoss,
				Status: model.OrderStatusTypeNew, Price: 36000, Quantity: 0.1, GroupID: &group},
			{ID: 4, Pair: "BTCUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
				Status: model.OrderStatusTypeFilled, Price: 40000, Quantity: 0.1},
			{ID: 5, Pair: "BTCUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
				Status: model.OrderStatusTypeNew, Price: 40000, Quantity: 0.1},
		} {
			c.OnOrder(o)
		}
		return controller, newTestServer(t, c).URL + "/api/controls/orders/"
	}

	t.Run("a limit order moves its price, a leg its level", func(t *testing.T) {
		for id, wantCall := range map[string]string{"1": "move limit", "2": "move protection", "3": "move protection"} {
			controller, url := setup(t)
			resp, body := postJSON(t, url+id+"/move", `{"price":39000}`)
			require.Equal(t, http.StatusOK, resp.StatusCode, body)
			require.Equal(t, []string{wantCall}, controller.calls, id)
			assert.Equal(t, id, strconv.FormatInt(controller.lastMoved.ID, 10))
			assert.InDelta(t, 39000, controller.lastLevel, 1e-9)

			var moved Order
			require.NoError(t, json.Unmarshal([]byte(body), &moved))
			assert.InDelta(t, 39000, moved.Price, 1e-9)
		}
	})

	t.Run("refused", func(t *testing.T) {
		controller, url := setup(t)
		for _, tc := range []struct {
			path, body, wantError string
			wantStatus            int
		}{
			{"9/move", `{"price":39000}`, "Ordem não encontrada", http.StatusNotFound},
			{"x/move", `{"price":39000}`, "Ordem não encontrada", http.StatusNotFound},
			{"4/move", `{"price":39000}`, "não está mais pendente", http.StatusConflict},
			{"5/move", `{"price":39000}`, "Só ordens limitadas", http.StatusUnprocessableEntity},
			{"1/move", `{"price":0}`, "O preço deve ser maior que zero", http.StatusBadRequest},
			{"1/move", `{`, "Pedido inválido", http.StatusBadRequest},
			{"9/cancel", ``, "Ordem não encontrada", http.StatusNotFound},
			{"4/cancel", ``, "não está mais pendente", http.StatusConflict},
		} {
			resp, body := postJSON(t, url+tc.path, tc.body)
			assert.Equal(t, tc.wantStatus, resp.StatusCode, tc.path)
			assert.Contains(t, body, tc.wantError, tc.path)
		}
		assert.Empty(t, controller.calls)
		assert.Empty(t, controller.canceled)

		controller.moveErr = exchange.ErrInvalidProtection
		resp, _ := postJSON(t, url+"1/move", `{"price":46000}`)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("canceled, shown as being canceled until confirmed", func(t *testing.T) {
		controller, url := setup(t)
		resp, body := postJSON(t, url+"2/cancel", ``)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		require.Equal(t, []int64{2}, controller.canceled)

		var canceling Order
		require.NoError(t, json.Unmarshal([]byte(body), &canceling))
		assert.Equal(t, string(model.OrderStatusTypePendingCancel), canceling.Status)

		resp, _ = postJSON(t, url+"2/cancel", ``)
		assert.Equal(t, http.StatusConflict, resp.StatusCode, "no longer pending")

		controller.cancelErr = exchange.ErrOrderNotOpen
		resp, _ = postJSON(t, url+"1/cancel", ``)
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
	})
}

func TestControls_Balance(t *testing.T) {
	c := newTestChart(t)
	base := time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC)
	c.OnCandle(candleAt("BTCUSDT", base, 20000))
	c.OnCandle(candleAt("ETHUSDT", base, 1000))
	srv := newTestServer(t, c)

	resp, _ := get(t, srv.URL+"/api/controls/balance")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "without the controls")

	c.SetOrderController(&fakeController{account: model.Account{Balances: []model.Balance{
		{Asset: "BTC", Free: 0.5, Lock: 0.1},
		{Asset: "ETH", Free: -2},
		{Asset: "USDT", Free: 1000, Lock: 500},
	}}})
	resp, body := get(t, srv.URL+"/api/controls/balance")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var balance BalanceResponse
	require.NoError(t, json.Unmarshal([]byte(body), &balance))
	assert.InDeltaSlice(t, []float64{12000, -2000, 1500}, []float64{balance.Balances[0].Value,
		balance.Balances[1].Value, balance.Balances[2].Value}, 1e-6)
	assert.Equal(t, []string{"BTC", "ETH", "USDT"}, []string{balance.Balances[0].Asset, balance.Balances[1].Asset,
		balance.Balances[2].Asset}, "the assets of the pairs, then their quote once")
	assert.InDelta(t, 0.6, balance.Balances[0].Quantity, 1e-9)
	assert.Equal(t, "USDT", balance.Balances[0].Quote)
	assert.InDelta(t, 11500, balance.Total, 1e-6)

	c.SetOrderController(&fakeController{positionErr: errors.New("exchange down")})
	resp, body = get(t, srv.URL+"/api/controls/balance")
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.Contains(t, body, "exchange down")
}

// On an account the broker reports, the balance is in its currency: the cash
// as the account holds it, the assets valued by the point value of their
// pairs, without any rate of exchange.
func TestControls_BalanceOfAnAccount(t *testing.T) {
	c := newTestChart(t, WithAccountReporter(&fakeReporter{}))
	c.OnCandle(candleAt("BTCUSDT", time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC), 20000))
	srv := newTestServer(t, c)
	c.SetOrderController(&pointedController{point: 5, fakeController: fakeController{account: model.Account{
		Balances: []model.Balance{{Asset: "BRL", Free: 900, Lock: 100}, {Asset: "BTC", Free: 0.01}}}}})

	resp, body := get(t, srv.URL+"/api/controls/balance")
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var balance BalanceResponse
	require.NoError(t, json.Unmarshal([]byte(body), &balance))
	require.Len(t, balance.Balances, 2)
	assert.Equal(t, AssetBalance{Asset: "BTC", Quantity: 0.01, Value: 1000, Quote: "BRL"}, balance.Balances[0],
		"0.01 x 20000 x 5 BRL")
	assert.Equal(t, AssetBalance{Asset: "BRL", Quantity: 1000, Value: 1000, Quote: "BRL"}, balance.Balances[1])
	assert.InDelta(t, 2000, balance.Total, 1e-9)
	require.NotNil(t, balance.Account)
	assert.Equal(t, "BRL", balance.Account.Currency)
}

func TestControls_Channels(t *testing.T) {
	c := newTestChart(t)
	c.OnCandle(candleAt("ETHUSDT", time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC), 3000))
	controller := &fakeController{status: order.StatusRunning}
	c.SetOrderController(controller, ChannelTelegram)
	srv := newTestServer(t, c)
	events := c.events.subscribe()
	defer c.events.unsubscribe(events)

	send := func(method, path string, channel Channel) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, nil)
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
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp, string(body)
	}

	// the dashboard, the requests without the header: its trade panel off,
	// the orders refused, but the bot started and stopped
	_, body := send(http.MethodGet, "/api/controls", "")
	assert.JSONEq(t, `{"enabled":false,"status":"running","bot_control":true}`, body)
	resp, _ := send(http.MethodGet, "/api/controls/balance", "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, body = send(http.MethodPost, "/api/controls/stop", ChannelWeb)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"enabled":false,"status":"stopped","bot_control":true}`, body)
	assert.True(t, controller.stopped)
	event := <-events
	assert.Equal(t, "controls", event.name)
	assert.JSONEq(t, `{"enabled":false,"status":"stopped","bot_control":true}`, string(event.data))
	resp, _ = send(http.MethodPost, "/api/controls/start", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, controller.started)
	<-events

	// Telegram, its controls on
	_, body = send(http.MethodGet, "/api/controls", ChannelTelegram)
	assert.JSONEq(t, `{"enabled":true,"status":"running","bot_control":true}`, body)
	resp, body = send(http.MethodPost, "/api/controls/stop", ChannelTelegram)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"enabled":true,"status":"stopped","bot_control":true}`, body)

	// the dashboards get the state the dashboard sees
	event = <-events
	assert.Equal(t, "controls", event.name)
	assert.JSONEq(t, `{"enabled":false,"status":"stopped","bot_control":true}`, string(event.data))

	// all the channels without any
	c.SetOrderController(controller)
	for _, channel := range []Channel{"", ChannelWeb, ChannelTelegram} {
		_, body = send(http.MethodGet, "/api/controls", channel)
		assert.JSONEq(t, `{"enabled":true,"status":"stopped","bot_control":true}`, body, channel)
	}

	// switched while the bot runs: the trade panel alone, then none, the
	// dashboards told at once; Telegram off, its start and stop too
	c.SetChannels(ChannelWeb)
	event = <-events
	assert.Equal(t, "controls", event.name)
	assert.JSONEq(t, `{"enabled":true,"status":"stopped","bot_control":true}`, string(event.data))
	_, body = send(http.MethodGet, "/api/controls", ChannelTelegram)
	assert.JSONEq(t, `{"enabled":false,"status":""}`, body)
	c.SetChannels()
	event = <-events
	assert.JSONEq(t, `{"enabled":false,"status":"stopped","bot_control":true}`, string(event.data))
	resp, _ = send(http.MethodPost, "/api/controls/start", ChannelTelegram)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "not from Telegram without its controls")
	for _, channel := range []Channel{"", ChannelWeb} {
		resp, _ = send(http.MethodPost, "/api/controls/start", channel)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "the dashboard whatever its trade panel")
		resp, _ = send(http.MethodGet, "/api/controls/balance", channel)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "but not the orders")
	}
}
