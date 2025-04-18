package ui

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
)

// OrderController is the subset of *order.Controller used by the dashboard
// control panel: start/stop, the balance, manual orders, market or limit, with
// a stop loss and take profit, and the moves and cancels of the pending ones.
type OrderController interface {
	Status() order.Status
	Start()
	Stop()
	Account() (model.Account, error)
	Position(pair string) (asset, quote float64, err error)
	CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error)
	CreateOrderMarketQuote(side model.SideType, pair string, amount float64) (model.Order, error)
	CreateOrderLimit(side model.SideType, pair string, size, limit float64) (model.Order, error)
	CreateOrderMarketProtected(side model.SideType, pair string, size, stop, profit float64) ([]model.Order, error)
	CreateOrderLimitProtected(side model.SideType, pair string, size, limit, stop,
		profit float64) ([]model.Order, error)
	UpdateProtection(leg model.Order, level float64) (model.Order, error)
	UpdateLimit(order model.Order, limit float64) (model.Order, error)
	Cancel(order model.Order) error
}

// ChannelHeader is the header of the requests to the control panel telling the
// channel they come from, see Channel.
const ChannelHeader = "X-Xtrader-Channel"

// Channel is where the control panel of the bot is operated from: the trade
// panel of the web dashboard, which the requests without ChannelHeader come
// from, or the commands of the Telegram of the controller.
type Channel string

const (
	ChannelWeb      Channel = "web"
	ChannelTelegram Channel = "telegram"
)

// channelOf returns the channel of the request.
func channelOf(r *http.Request) Channel {
	if channel := Channel(r.Header.Get(ChannelHeader)); channel != "" {
		return channel
	}
	return ChannelWeb
}

// SetOrderController enables the bot control panel on the channels, all of
// them without any: start/stop and manual buy/sell orders, mirroring the
// Telegram commands. Call it before Start, typically with bot.Controller().
// Without a controller, or from another channel, the control endpoints report
// disabled and the panel is hidden; but the web dashboard starts and stops the
// bot of a controller whatever its channels, which gate its orders.
//
// Anyone with access to the dashboard port can operate the bot once this is
// set, the channel being a header of the requests; keep the port private
// (localhost, VPN or an authenticating proxy).
func (c *Chart) SetOrderController(controller OrderController, channels ...Channel) {
	if len(channels) == 0 {
		channels = []Channel{ChannelWeb, ChannelTelegram}
	}
	enabled := make(map[Channel]bool, len(channels))
	for _, channel := range channels {
		enabled[channel] = true
	}

	c.mu.Lock()
	c.controller, c.channels = controller, enabled
	c.mu.Unlock()
}

// SetChannels sets the channels the control panel is operated from, none
// disabling it, keeping the controller of SetOrderController: eg: as the
// controller turns the trade panel and the Telegram controls of the bot on and
// off while it runs. The dashboards are told at once.
func (c *Chart) SetChannels(channels ...Channel) {
	enabled := make(map[Channel]bool, len(channels))
	for _, channel := range channels {
		enabled[channel] = true
	}
	c.mu.Lock()
	c.channels = enabled
	c.mu.Unlock()
	c.publishControls()
}

// botControllerOf returns the controller whose bot the channel starts and
// stops, else nil: the web dashboard whatever its trade panel, which gates the
// orders only, Telegram with its controls on.
func (c *Chart) botControllerOf(channel Channel) OrderController {
	c.mu.Lock()
	defer c.mu.Unlock()
	if channel != ChannelWeb && !c.channels[channel] {
		return nil
	}
	return c.controller
}

// controllerOf returns the controller when the control panel is enabled on the
// channel, else nil.
func (c *Chart) controllerOf(channel Channel) OrderController {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.channels[channel] {
		return nil
	}
	return c.controller
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: message})
}

func (c *Chart) controlsResponse(channel Channel) ControlsResponse {
	var response ControlsResponse
	if bot := c.botControllerOf(channel); bot != nil {
		response.BotControl, response.Status = true, string(bot.Status())
	}
	if c.controllerOf(channel) != nil {
		response.Enabled, response.Reanalysis = true, c.reanalyzer() != nil
	}
	return response
}

// requireController returns the controller enabled on the channel of the
// request or replies 404.
func (c *Chart) requireController(w http.ResponseWriter, r *http.Request) (OrderController, bool) {
	controller := c.controllerOf(channelOf(r))
	if controller == nil {
		writeJSONError(w, http.StatusNotFound, "bot controls are not enabled")
		return nil, false
	}
	return controller, true
}

func (c *Chart) handleControls(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, c.controlsResponse(channelOf(r)))
}

// requireBotController returns the controller whose bot the channel of the
// request starts and stops, or replies 404.
func (c *Chart) requireBotController(w http.ResponseWriter, r *http.Request) (OrderController, bool) {
	controller := c.botControllerOf(channelOf(r))
	if controller == nil {
		writeJSONError(w, http.StatusNotFound, "bot controls are not enabled")
		return nil, false
	}
	return controller, true
}

func (c *Chart) handleControlStart(w http.ResponseWriter, r *http.Request) {
	controller, ok := c.requireBotController(w, r)
	if !ok {
		return
	}

	controller.Start()
	c.publishControls()
	writeJSON(w, c.controlsResponse(channelOf(r)))
}

func (c *Chart) handleControlStop(w http.ResponseWriter, r *http.Request) {
	controller, ok := c.requireBotController(w, r)
	if !ok {
		return
	}

	controller.Stop()
	c.publishControls()
	writeJSON(w, c.controlsResponse(channelOf(r)))
}

// publishControls sends the state of the controls to the dashboards, as their
// trade panel sees it, whatever the channel of the change.
func (c *Chart) publishControls() {
	c.events.publish("controls", c.controlsResponse(ChannelWeb))
}

func (c *Chart) handleControlOrder(w http.ResponseWriter, r *http.Request) {
	controller, ok := c.requireController(w, r)
	if !ok {
		return
	}

	var request ControlOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	c.mu.Lock()
	pair, known := c.resolvePair(request.Pair)
	candles := c.candles[pair]
	c.mu.Unlock()
	if !known {
		writeJSONError(w, http.StatusNotFound, "unknown pair")
		return
	}

	if request.Amount <= 0 {
		writeJSONError(w, http.StatusBadRequest, "amount must be positive")
		return
	}
	if request.Percent && request.Amount > 100 {
		writeJSONError(w, http.StatusBadRequest, "percentage cannot exceed 100")
		return
	}
	if request.Percent && request.Lot {
		writeJSONError(w, http.StatusBadRequest, "an amount is a percentage or a size in lots, not both")
		return
	}
	switch request.Type = strings.ToLower(request.Type); {
	case request.Type != "" && request.Type != "market" && request.Type != "limit":
		writeJSONError(w, http.StatusBadRequest, `type must be "market" or "limit"`)
		return
	case request.Type == "limit" && request.Price <= 0:
		writeJSONError(w, http.StatusBadRequest, "a limit order needs its price")
		return
	case request.StopLoss < 0 || request.TakeProfit < 0:
		writeJSONError(w, http.StatusBadRequest, "the stop loss and take profit can't be negative")
		return
	}

	// the price the amount is converted at: the limit, else the last close
	reference := request.Price
	if request.Type != "limit" && len(candles) > 0 {
		reference = candles[len(candles)-1].Close
	}

	var side model.SideType
	switch strings.ToLower(request.Side) {
	case "buy":
		side = model.SideTypeBuy
	case "sell":
		side = model.SideTypeSell
	default:
		writeJSONError(w, http.StatusBadRequest, `side must be "buy" or "sell"`)
		return
	}

	created, err := createControlOrder(controller, side, pair, request, reference)
	if err != nil {
		writeControlError(w, err)
		return
	}

	c.mu.Lock()
	dto := c.toOrder(created, c.candles[pair])
	c.mu.Unlock()
	writeJSON(w, dto)
}

// writeControlError replies the error of an order of the control panel: 409
// when the bot or the order can't take it now, 400 for invalid levels or sizes,
// 422 when the broker doesn't support it.
func writeControlError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, order.ErrBotStopped), errors.Is(err, exchange.ErrOrderNotOpen):
		status = http.StatusConflict
	case errors.Is(err, exchange.ErrInvalidProtection), errors.Is(err, exchange.ErrInvalidQuantity):
		status = http.StatusBadRequest
	case errors.Is(err, exchange.ErrNotSupported):
		status = http.StatusUnprocessableEntity
	}
	writeJSONError(w, status, err.Error())
}

// createControlOrder creates the order of the control panel and returns it, the
// entry of a protected one. A market order without stop loss nor take profit
// mirrors the Telegram /buy and /sell semantics, see createMarketOrder; the
// others are sized at reference, see controlSize.
func createControlOrder(controller OrderController, side model.SideType, pair string,
	request ControlOrderRequest, reference float64) (model.Order, error) {

	protected := request.StopLoss > 0 || request.TakeProfit > 0
	if request.Type != "limit" && !protected {
		return createMarketOrder(controller, side, pair, request)
	}

	size, err := controlSize(controller, side, pair, request, reference)
	if err != nil {
		return model.Order{}, err
	}

	var orders []model.Order
	switch {
	case request.Type == "limit" && !protected:
		return controller.CreateOrderLimit(side, pair, size, request.Price)
	case request.Type == "limit":
		orders, err = controller.CreateOrderLimitProtected(side, pair, size, request.Price, request.StopLoss,
			request.TakeProfit)
	default:
		orders, err = controller.CreateOrderMarketProtected(side, pair, size, request.StopLoss, request.TakeProfit)
	}
	if err != nil {
		return model.Order{}, err
	}
	return orders[0], nil
}

// controlSize returns the size of an order at price: its lots, its amount in
// quote, or its share of the balance, of the quote for a buy and of the asset
// held for a sell.
func controlSize(controller OrderController, side model.SideType, pair string, request ControlOrderRequest,
	price float64) (float64, error) {

	switch {
	case request.Lot:
		return request.Amount, nil
	case price <= 0:
		return 0, errors.New("no price to size the order at")
	case request.Percent:
		asset, quote, err := controller.Position(pair)
		if err != nil {
			return 0, err
		}
		if side == model.SideTypeSell {
			return request.Amount * asset / 100.0, nil
		}
		return request.Amount * quote / 100.0 / price, nil
	}
	return request.Amount / price, nil
}

// createMarketOrder mirrors the Telegram /buy and /sell semantics: amounts are
// in quote currency, percentages consume the available balance — quote balance
// for buys, asset balance for sells — and lots are the size of the order.
func createMarketOrder(controller OrderController, side model.SideType, pair string,
	request ControlOrderRequest) (model.Order, error) {
	amount := request.Amount
	if request.Lot {
		return controller.CreateOrderMarket(side, pair, amount)
	}

	if request.Percent {
		asset, quote, err := controller.Position(pair)
		if err != nil {
			return model.Order{}, err
		}

		if side == model.SideTypeSell {
			return controller.CreateOrderMarket(side, pair, amount*asset/100.0)
		}
		amount = amount * quote / 100.0
	}

	return controller.CreateOrderMarketQuote(side, pair, amount)
}

// controlledOrder returns the pending order of the {id} of the request, known
// from the order events, or replies 404 when unknown and 409 when no longer
// pending.
func (c *Chart) controlledOrder(w http.ResponseWriter, r *http.Request) (model.Order, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	c.mu.Lock()
	pending, known := c.orderByID[id]
	c.mu.Unlock()

	switch {
	case err != nil || !known:
		writeJSONError(w, http.StatusNotFound, "unknown order")
		return model.Order{}, false
	case pending.Status != model.OrderStatusTypeNew:
		writeJSONError(w, http.StatusConflict, "the order is not pending")
		return model.Order{}, false
	}
	return pending, true
}

// handleControlBalance replies the balance of the account on the pairs of the
// bot, as the Telegram /balance command: their assets, valued at the last
// close, and their quotes.
func (c *Chart) handleControlBalance(w http.ResponseWriter, r *http.Request) {
	controller, ok := c.requireController(w, r)
	if !ok {
		return
	}
	account, err := controller.Account()
	if err != nil {
		writeControlError(w, err)
		return
	}

	c.mu.Lock()
	pairs := c.pairs()
	closes := make(map[string]float64, len(pairs))
	for _, pair := range pairs {
		if candles := c.candles[pair]; len(candles) > 0 {
			closes[pair] = candles[len(candles)-1].Close
		}
	}
	c.mu.Unlock()

	response := BalanceResponse{Balances: make([]AssetBalance, 0, len(pairs)+1)}
	var quotes []AssetBalance
	for _, pair := range pairs {
		asset, quote := exchange.SplitAssetQuote(pair)
		assetBalance, quoteBalance := account.Balance(asset, quote)
		quantity := assetBalance.Free + assetBalance.Lock
		response.Balances = append(response.Balances, AssetBalance{Asset: asset, Quantity: quantity,
			Value: quantity * closes[pair], Quote: quote})

		if !slices.ContainsFunc(quotes, func(b AssetBalance) bool { return b.Asset == quote }) {
			quantity := quoteBalance.Free + quoteBalance.Lock
			quotes = append(quotes, AssetBalance{Asset: quote, Quantity: quantity, Value: quantity, Quote: quote})
		}
	}
	response.Balances = append(response.Balances, quotes...)
	for _, balance := range response.Balances {
		response.Total += balance.Value
	}
	if c.reporter != nil {
		if account, ok := c.reporter.AccountSummary(); ok {
			response.Account = &AccountSummary{Currency: account.Currency, Balance: account.Balance,
				Equity: account.Equity, Profit: account.Profit, Margin: account.Margin,
				FreeMargin: account.FreeMargin}
		}
	}
	writeJSON(w, response)
}

// handleControlMove moves a pending order: the price of a limit order, or the
// level of a stop loss or take profit.
func (c *Chart) handleControlMove(w http.ResponseWriter, r *http.Request) {
	controller, ok := c.requireController(w, r)
	if !ok {
		return
	}

	var request ControlMoveRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	if request.Price <= 0 {
		writeJSONError(w, http.StatusBadRequest, "price must be positive")
		return
	}

	pending, ok := c.controlledOrder(w, r)
	if !ok {
		return
	}

	var moved model.Order
	var err error
	switch pending.Type {
	case model.OrderTypeLimit:
		moved, err = controller.UpdateLimit(pending, request.Price)
	case model.OrderTypeLimitMaker, model.OrderTypeStopLoss:
		moved, err = controller.UpdateProtection(pending, request.Price)
	default:
		writeJSONError(w, http.StatusUnprocessableEntity, "only limit orders, stop losses and take profits move")
		return
	}
	if err != nil {
		writeControlError(w, err)
		return
	}

	c.mu.Lock()
	dto := c.toOrder(moved, c.candles[moved.Pair])
	c.mu.Unlock()
	writeJSON(w, dto)
}

// handleControlCancel cancels a pending order, and the legs of its group with
// it. The order shows as being canceled until the exchange confirms it.
func (c *Chart) handleControlCancel(w http.ResponseWriter, r *http.Request) {
	controller, ok := c.requireController(w, r)
	if !ok {
		return
	}
	pending, ok := c.controlledOrder(w, r)
	if !ok {
		return
	}

	if err := controller.Cancel(pending); err != nil {
		writeControlError(w, err)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// unless the order events told the outcome meanwhile
	if current := c.orderByID[pending.ID]; current.Status == model.OrderStatusTypeNew {
		current.Status = model.OrderStatusTypePendingCancel
		c.orderByID[pending.ID] = current
		c.events.publish("order", OrderEvent{Pair: current.Pair, Order: c.toOrder(current, c.candles[current.Pair])})
	}
	writeJSON(w, c.toOrder(c.orderByID[pending.ID], c.candles[pending.Pair]))
}
