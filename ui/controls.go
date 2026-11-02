package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/service"
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
// bot of a controller whatever its channels, which gate its orders, and asks
// its language model again, its reanalysis allowed (see SetReanalysis).
//
// The channel is a header of the requests, which the API takes from its
// controller only (see guard): an agent the controller launched wants the key
// of its start (WithAPIKey), but one run on its own answers any process of its
// machine, so keep its port on the loopback.
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

// SetReanalysis allows the language model of the strategy to be asked again
// between the closes, see Reanalyzer, or forbids it, as it starts: from the
// channels the bot is started and stopped from (see botControllerOf), the web
// dashboard whatever its trade panel, Telegram with its controls on; eg: as
// the controller switches it while the bot runs, apart from the channels. The
// dashboards are told at once of a change. Forbidden until allowed.
func (c *Chart) SetReanalysis(allowed bool) {
	c.mu.Lock()
	changed := c.reanalysis != allowed
	c.reanalysis = allowed
	c.mu.Unlock()
	if changed {
		c.publishControls()
	}
}

// reanalysisAllowed reports whether the language model of the strategy may be
// asked again, see SetReanalysis.
func (c *Chart) reanalysisAllowed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reanalysis
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
		if c.reanalyzer() != nil {
			allowed := c.reanalysisAllowed()
			response.Reanalysis, response.ReanalysisDisabled = allowed, !allowed
		}
	}
	if c.controllerOf(channel) != nil {
		response.Enabled = true
	}
	return response
}

// requireController returns the controller enabled on the channel of the
// request or replies 404.
func (c *Chart) requireController(w http.ResponseWriter, r *http.Request) (OrderController, bool) {
	controller := c.controllerOf(channelOf(r))
	if controller == nil {
		writeJSONError(w, http.StatusNotFound, "Controles do agente desligados neste canal: ligue-os em "+
			"Configurações › Agentes.")
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
		writeJSONError(w, http.StatusNotFound, "Controles do agente desligados neste canal: ligue-os em "+
			"Configurações › Agentes.")
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
		writeJSONError(w, http.StatusBadRequest, "Pedido inválido.")
		return
	}

	c.mu.Lock()
	pair, known := c.resolvePair(request.Pair)
	candles := c.candles[pair]
	c.mu.Unlock()
	if !known {
		writeJSONError(w, http.StatusNotFound, "Ativo desconhecido.")
		return
	}

	if request.Amount <= 0 {
		writeJSONError(w, http.StatusBadRequest, "A quantidade deve ser maior que zero.")
		return
	}
	if request.Percent && request.Amount > 100 {
		writeJSONError(w, http.StatusBadRequest, "A quantidade em porcentagem não pode passar de 100.")
		return
	}
	if request.Percent && request.Lot {
		writeJSONError(w, http.StatusBadRequest, "Informe a quantidade em porcentagem ou em lotes, não nos dois.")
		return
	}
	switch request.Type = strings.ToLower(request.Type); {
	case request.Type != "" && request.Type != "market" && request.Type != "limit":
		writeJSONError(w, http.StatusBadRequest, `Tipo da ordem inválido: use "market" ou "limit".`)
		return
	case request.Type == "limit" && request.Price <= 0:
		writeJSONError(w, http.StatusBadRequest, "Informe o preço limite da ordem.")
		return
	case request.StopLoss < 0 || request.TakeProfit < 0:
		writeJSONError(w, http.StatusBadRequest, "O stop loss e o take profit não podem ser negativos.")
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
		writeJSONError(w, http.StatusBadRequest, `Lado da ordem inválido: use "buy" ou "sell".`)
		return
	}

	created, warning, err := createControlOrder(controller, side, pair, request, reference)
	if err != nil {
		writeControlError(w, err)
		return
	}

	c.mu.Lock()
	dto := c.toOrder(created, c.candles[pair])
	c.mu.Unlock()
	writeJSON(w, ControlOrderResponse{Order: dto, Warning: warning})
}

// manualMover is implemented by the controllers that move a leg for a user
// apart from the strategies, eg: order.Controller, whose stop guard never
// refuses a user: the trade panel may loosen a stop loss.
type manualMover interface {
	UpdateProtectionManual(leg model.Order, level float64) (model.Order, error)
}

// moveManually moves leg to level for a user, see manualMover.
func moveManually(controller OrderController, leg model.Order, level float64) (model.Order, error) {
	if manual, ok := controller.(manualMover); ok {
		return manual.UpdateProtectionManual(leg, level)
	}
	return controller.UpdateProtection(leg, level)
}

// writeControlError replies the error of an order of the control panel: 409
// when the bot or the order can't take it now, 400 for invalid levels or sizes,
// 422 when the broker doesn't support it. See controlMessage for its message.
func writeControlError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, order.ErrBotStopped), errors.Is(err, order.ErrEntriesBlocked),
		errors.Is(err, exchange.ErrOrderNotOpen):
		status = http.StatusConflict
	case errors.Is(err, exchange.ErrInvalidProtection), errors.Is(err, exchange.ErrInvalidQuantity):
		status = http.StatusBadRequest
	case errors.Is(err, exchange.ErrNotSupported):
		status = http.StatusUnprocessableEntity
	}
	writeJSONError(w, status, controlMessage(err))
}

// controlMessage returns the message of err, an error of an order of the
// control panel, for the user: the errors of the bot and of the broker known
// in Portuguese, with their detail, if any. The entries blocked keep their
// text, the Telegram of the controller telling them apart by it, and any other
// error is told as it is.
func controlMessage(err error) string {
	switch {
	case errors.Is(err, order.ErrEntriesBlocked):
		return err.Error()
	case errors.Is(err, order.ErrBotStopped):
		return "O bot está parado: inicie-o antes."
	case errors.Is(err, exchange.ErrOrderNotOpen):
		return "A ordem não está mais aberta."
	case errors.Is(err, exchange.ErrInvalidProtection):
		return withDetail("Stop loss ou take profit inválido", err, exchange.ErrInvalidProtection)
	case errors.Is(err, exchange.ErrInvalidQuantity):
		return withDetail("Tamanho da ordem inválido", err, exchange.ErrInvalidQuantity)
	case errors.Is(err, exchange.ErrNotSupported):
		return "A corretora não aceita esta operação."
	}
	return err.Error()
}

// withDetail returns message with the detail of err, the text following the
// one of sentinel in it, eg: "min: 0.01 max: 100", when it has one.
func withDetail(message string, err, sentinel error) string {
	text := err.Error()
	if i := strings.Index(text, sentinel.Error()); i >= 0 {
		if detail := strings.TrimLeft(text[i+len(sentinel.Error()):], ": "); detail != "" {
			return message + ": " + detail
		}
	}
	return message + "."
}

// createControlOrder creates the order of the control panel and returns it, the
// entry of a protected one, with a warning when it was executed short of what
// was asked (see ControlOrderResponse). A market order without stop loss nor
// take profit mirrors the Telegram /buy and /sell semantics, see
// createMarketOrder; the others are sized at reference, see controlSize.
func createControlOrder(controller OrderController, side model.SideType, pair string,
	request ControlOrderRequest, reference float64) (model.Order, string, error) {

	protected := request.StopLoss > 0 || request.TakeProfit > 0
	if request.Type != "limit" && !protected {
		created, err := createMarketOrder(controller, side, pair, request)
		return created, "", err
	}

	size, err := controlSize(controller, side, pair, request, reference)
	if err != nil {
		return model.Order{}, "", err
	}

	var orders []model.Order
	switch {
	case request.Type == "limit" && !protected:
		created, err := controller.CreateOrderLimit(side, pair, size, request.Price)
		return created, "", err
	case request.Type == "limit":
		orders, err = controller.CreateOrderLimitProtected(side, pair, size, request.Price, request.StopLoss,
			request.TakeProfit)
	default:
		orders, err = controller.CreateOrderMarketProtected(side, pair, size, request.StopLoss, request.TakeProfit)
	}
	if len(orders) == 0 {
		if err == nil {
			err = errors.New("A corretora não criou nenhuma ordem.")
		}
		return model.Order{}, "", err
	}
	if err != nil {
		// the entry is on the exchange: an error would have it sent again
		asked := 0
		for _, level := range []float64{request.StopLoss, request.TakeProfit} {
			if level > 0 {
				asked++
			}
		}
		return orders[0], fmt.Sprintf("A ordem foi criada, mas só %d das %d ordens de stop loss e take profit: "+
			"a posição pode ter ficado sem elas. Confira: %v", len(orders)-1, asked, err), nil
	}
	return orders[0], "", nil
}

// controlSize returns the size of an order at price: its lots, its amount in
// the currency of the cash (the account's at MT5, the quote in the paper
// wallet), or its share of the balance, of the cash for a buy and of the asset
// held for a sell; an amount turned into units by the point value of the pair
// (see service.PointValuer).
func controlSize(controller OrderController, side model.SideType, pair string, request ControlOrderRequest,
	price float64) (float64, error) {

	switch {
	case request.Lot:
		return request.Amount, nil
	case price <= 0:
		return 0, errors.New("Sem preço para calcular o tamanho da ordem.")
	}
	amount := request.Amount
	if request.Percent {
		asset, cash, err := controller.Position(pair)
		if err != nil {
			return 0, err
		}
		if side == model.SideTypeSell {
			return request.Amount * asset / 100.0, nil
		}
		amount = request.Amount * cash / 100.0
	}
	point, err := service.PointValue(controller, pair)
	if err != nil {
		return 0, err
	}
	if point <= 0 {
		return 0, fmt.Errorf("Sem o valor do ponto de %s para calcular o tamanho da ordem.", pair)
	}
	return amount / (price * point), nil
}

// createMarketOrder mirrors the Telegram /buy and /sell semantics: amounts are
// in the currency of the cash (the account's at MT5), percentages consume the
// available balance — the cash for buys, the asset for sells — and lots are
// the size of the order.
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
		writeJSONError(w, http.StatusNotFound, "Ordem não encontrada.")
		return model.Order{}, false
	case pending.Status != model.OrderStatusTypeNew:
		writeJSONError(w, http.StatusConflict, "A ordem não está mais pendente.")
		return model.Order{}, false
	}
	return pending, true
}

// handleControlBalance replies the balance of the account on the pairs of the
// bot, as the Telegram /balance command: their assets, valued at the last
// close, and the cash. On an account the broker reports (see
// WithAccountReporter), the cash is in its currency and the assets are valued
// in it by the point value of their pairs (see service.PointValuer), no rate
// of exchange read; elsewhere, eg: the paper wallet, in the quotes of the
// pairs.
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

	var summary service.AccountSummary
	reported := false
	if c.reporter != nil {
		summary, reported = c.reporter.AccountSummary()
	}

	response := BalanceResponse{Balances: make([]AssetBalance, 0, len(pairs)+1)}
	var quotes []AssetBalance
	for _, pair := range pairs {
		asset, quote := exchange.SplitAssetQuote(pair)
		point := 1.0
		if reported && summary.Currency != "" {
			quote = summary.Currency
			if point, err = service.PointValue(controller, pair); err != nil {
				writeControlError(w, err)
				return
			}
		}
		assetBalance, quoteBalance := account.Balance(asset, quote)
		quantity := assetBalance.Free + assetBalance.Lock
		response.Balances = append(response.Balances, AssetBalance{Asset: asset, Quantity: quantity,
			Value: quantity * closes[pair] * point, Quote: quote})

		if !slices.ContainsFunc(quotes, func(b AssetBalance) bool { return b.Asset == quote }) {
			quantity := quoteBalance.Free + quoteBalance.Lock
			quotes = append(quotes, AssetBalance{Asset: quote, Quantity: quantity, Value: quantity, Quote: quote})
		}
	}
	response.Balances = append(response.Balances, quotes...)
	for _, balance := range response.Balances {
		response.Total += balance.Value
	}
	if reported {
		response.Account = &AccountSummary{Currency: summary.Currency, Balance: summary.Balance,
			Equity: summary.Equity, Profit: summary.Profit, Margin: summary.Margin, FreeMargin: summary.FreeMargin}
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
		writeJSONError(w, http.StatusBadRequest, "Pedido inválido.")
		return
	}
	if request.Price <= 0 {
		writeJSONError(w, http.StatusBadRequest, "O preço deve ser maior que zero.")
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
		moved, err = moveManually(controller, pending, request.Price)
	default:
		writeJSONError(w, http.StatusUnprocessableEntity, "Só ordens limitadas, stop loss e take profit "+
			"podem ser movidos.")
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
	// unless the order events told the outcome meanwhile
	if current := c.orderByID[pending.ID]; current.Status == model.OrderStatusTypeNew {
		current.Status = model.OrderStatusTypePendingCancel
		c.orderByID[pending.ID] = current
		c.events.publish("order", OrderEvent{Pair: current.Pair, Order: c.toOrder(current, c.candles[current.Pair])})
	}
	dto := c.toOrder(c.orderByID[pending.ID], c.candles[pending.Pair])
	c.mu.Unlock()
	writeJSON(w, dto) // never holding c.mu, see snapshotOf
}
