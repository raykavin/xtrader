package ui

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
)

// ChannelSupervisor is the channel of the supervisor of the controller: it
// only reduces the risk of the bot, its entries blocked, its positions closed
// without opening any, its pending entries canceled (see SetSupervision),
// whatever the trade panel and the Telegram controls. Only the controller
// sends it: its proxy sends the requests of the dashboards as ChannelWeb.
const ChannelSupervisor Channel = "supervisor"

// maxHold bounds how long the entries are held after a close, see
// SupervisionCloseRequest.HoldSeconds.
const maxHold = 24 * time.Hour

// SupervisedController is the order controller as the supervisor of the
// controller operates it, see SetSupervision, eg: *order.Controller.
type SupervisedController interface {
	Status() order.Status
	Position(pair string) (asset, quote float64, err error)
	EntriesBlocked() (blocked bool, reason string, revision int64)
	BlockEntries(revision int64, reason string) bool
	AllowEntries(revision int64) bool
	HoldEntries(reason string) (release func())
	ClosePosition(pair string) (model.Order, error)
	Cancel(order model.Order) error
}

// Protector is the protection of the positions of the bot (breakeven, trailing
// stop), see protect.Manager: how it stands, by pair, and the arms of the
// supervisor.
type Protector interface {
	Protection(pair string) *PositionProtection
	Protections() []PositionProtection
	Arm(request SupervisionProtectRequest) SupervisionProtectResponse
}

// SetProtection shows the protection of the positions of the bot with them,
// in the snapshots and the events, and lets the supervisor arm it (POST
// /api/supervision/protect). Call it before Start.
func (c *Chart) SetProtection(protector Protector) {
	c.protector = protector
}

// protection returns the protection of the position of pair, nil without one.
func (c *Chart) protection(pair string) *PositionProtection {
	if c.protector == nil {
		return nil
	}
	return c.protector.Protection(pair)
}

// SetSupervision enables the routes of the supervisor of the controller,
// /api/supervision, on the channel ChannelSupervisor only, apart from the
// channels of the control panel (see SetChannels): the state of the entries,
// the positions and the pending orders of the bot, its entries blocked or
// allowed, its positions closed and its pending entries canceled. Call it
// before Start, typically with bot.Controller().
func (c *Chart) SetSupervision(controller SupervisedController) {
	c.mu.Lock()
	c.supervised = controller
	c.mu.Unlock()
}

// requireSupervisor returns the controller the supervisor operates when the
// request comes from it, or replies 404: nothing tells the other channels the
// routes exist.
func (c *Chart) requireSupervisor(w http.ResponseWriter, r *http.Request) (SupervisedController, bool) {
	c.mu.Lock()
	controller := c.supervised
	c.mu.Unlock()
	if controller == nil || channelOf(r) != ChannelSupervisor {
		writeJSONError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return controller, true
}

// pairsOf returns the pairs of the chart among names, all of them without
// any, and the names it doesn't trade.
func (c *Chart) pairsOf(names []string) (pairs, unknown []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(names) == 0 {
		return c.pairs(), nil
	}
	for _, name := range names {
		pair, ok := c.resolvePair(name)
		switch {
		case !ok:
			unknown = append(unknown, name)
		case !slices.Contains(pairs, pair):
			pairs = append(pairs, pair)
		}
	}
	return pairs, unknown
}

// pendingOrders returns the open orders of the bot on pairs, as the order feed
// told them, the oldest first: an entry is a limit or stop order out of a
// group, the others protect a position.
func (c *Chart) pendingOrders(pairs []string) []model.Order {
	c.mu.Lock()
	defer c.mu.Unlock()
	var pending []model.Order
	for _, pair := range pairs {
		ids := c.orderIDs[pair]
		if ids == nil {
			continue
		}
		for id := range ids.Iter() {
			o := c.orderByID[id]
			if o.Status == model.OrderStatusTypeNew || o.Status == model.OrderStatusTypePartiallyFilled {
				pending = append(pending, o)
			}
		}
	}
	return pending
}

// pendingEntry reports whether the open order o would open a position: a limit
// or stop order out of an OCO group, the legs of the positions being in one.
func pendingEntry(o model.Order) bool {
	if o.GroupID != nil {
		return false
	}
	switch o.Type {
	case model.OrderTypeLimit, model.OrderTypeStopLoss, model.OrderTypeStopLossLimit:
		return true
	}
	return false
}

// handleSupervision answers the state of the bot for the supervisor, see
// SupervisionState.
func (c *Chart) handleSupervision(w http.ResponseWriter, r *http.Request) {
	controller, ok := c.requireSupervisor(w, r)
	if !ok {
		return
	}
	pairs, _ := c.pairsOf(nil)
	state := SupervisionState{Status: string(controller.Status()), Positions: []SupervisedPosition{},
		Pending: []SupervisedOrder{}}
	state.EntriesBlocked, state.EntriesReason, state.EntriesRevision = controller.EntriesBlocked()
	for _, pair := range pairs {
		asset, _, err := controller.Position(pair)
		if err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, "the position of "+pair+" can't be read now: "+
				err.Error())
			return
		}
		switch {
		case asset > 0:
			state.Positions = append(state.Positions, SupervisedPosition{Pair: pair, Side: "BUY", Quantity: asset})
		case asset < 0:
			state.Positions = append(state.Positions, SupervisedPosition{Pair: pair, Side: "SELL", Quantity: -asset})
		}
	}
	for _, o := range c.pendingOrders(pairs) {
		state.Pending = append(state.Pending, SupervisedOrder{ID: o.ID, Pair: o.Pair, Side: string(o.Side),
			Type: string(o.Type), Price: o.Price, Quantity: o.Quantity, Entry: pendingEntry(o)})
	}
	if c.protector != nil {
		state.Protections = c.protector.Protections()
	}
	writeJSON(w, state)
}

// handleSupervisionProtect arms the protection of a position for the
// supervisor, see SupervisionProtectRequest: 404 without the protection, the
// answer of the arm else, see SupervisionProtectResponse.
func (c *Chart) handleSupervisionProtect(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireSupervisor(w, r); !ok {
		return
	}
	if c.protector == nil {
		writeJSONError(w, http.StatusNotFound, "the protection of the positions isn't run by this agent")
		return
	}
	var request SupervisionProtectRequest
	if !decodeSupervision(w, r, &request) {
		return
	}
	if request.ArmID <= 0 || strings.TrimSpace(request.Reason) == "" {
		writeJSONError(w, http.StatusBadRequest, "an arm needs its record and its reason")
		return
	}
	writeJSON(w, c.protector.Arm(request))
}

// decodeSupervision decodes the payload of a request of the supervisor into v,
// answering 400 when it can't.
func decodeSupervision(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON payload")
		return false
	}
	return true
}

// supervisionDrain bounds the wait for the orders being sent once the entries
// are blocked, see SupervisionEntriesRequest.Drain.
const supervisionDrain = 5 * time.Second

// idleWaiter waits for the orders being sent to end, see
// order.Controller.WaitIdle.
type idleWaiter interface {
	WaitIdle(d time.Duration) bool
}

// handleSupervisionEntries blocks the entries of the bot or allows them again,
// see SupervisionEntriesRequest, and answers the state of the entries.
func (c *Chart) handleSupervisionEntries(w http.ResponseWriter, r *http.Request) {
	controller, ok := c.requireSupervisor(w, r)
	if !ok {
		return
	}
	var request SupervisionEntriesRequest
	if !decodeSupervision(w, r, &request) {
		return
	}
	if request.Blocked {
		controller.BlockEntries(request.Revision, strings.TrimSpace(request.Reason))
		log.Warnf("[SUPERVISOR] the entries blocked (revision %d): %s", request.Revision, request.Reason)
		// the orders being sent ended, eg: before the pending entries are canceled
		if drainer, ok := controller.(idleWaiter); ok && request.Drain && !drainer.WaitIdle(supervisionDrain) {
			log.Warnf("[SUPERVISOR] orders still being sent after %s", supervisionDrain)
		}
	} else if controller.AllowEntries(request.Revision) {
		log.Infof("[SUPERVISOR] the entries allowed again (revision %d)", request.Revision)
	}
	var state SupervisionState
	state.Status = string(controller.Status())
	state.EntriesBlocked, state.EntriesReason, state.EntriesRevision = controller.EntriesBlocked()
	writeJSON(w, state)
}

// handleSupervisionClose closes the positions of the bot, never opening the
// other side, see SupervisionCloseRequest: the entries are held as they are
// closed, and the close of a pair whose outcome is unknown is never sent
// again, but told unknown, to check on the account. It goes on whatever the
// client, gone or not.
func (c *Chart) handleSupervisionClose(w http.ResponseWriter, r *http.Request) {
	controller, ok := c.requireSupervisor(w, r)
	if !ok {
		return
	}
	var request SupervisionCloseRequest
	if !decodeSupervision(w, r, &request) {
		return
	}
	pairs, unknown := c.pairsOf(request.Pairs)
	if len(unknown) > 0 {
		writeJSONError(w, http.StatusNotFound, "unknown pairs: "+strings.Join(unknown, ", "))
		return
	}
	reason := strings.TrimSpace(request.Reason)
	if request.Block {
		controller.BlockEntries(request.Revision, reason)
	}
	release := controller.HoldEntries(cmpOr(reason, "closing the positions"))
	defer func() {
		hold := min(time.Duration(max(request.HoldSeconds, 0))*time.Second, maxHold)
		if hold <= 0 {
			release()
			return
		}
		time.AfterFunc(hold, release)
	}()

	response := SupervisionCloseResponse{Results: make([]SupervisionCloseResult, 0, len(pairs))}
	if request.CancelPending { // first, held: an entry filled as it is canceled is closed below
		response.Canceled = c.cancelEntries(controller, pairs)
	}
	for _, pair := range pairs {
		result := SupervisionCloseResult{Pair: pair}
		closed, err := controller.ClosePosition(pair)
		switch {
		case err == nil:
			result.Status, result.Side, result.Quantity = SupervisionClosed, string(closed.Side), closed.Quantity
			log.Warnf("[SUPERVISOR] %s: the position closed, %s %v", pair, closed.Side, closed.Quantity)
		case errors.Is(err, exchange.ErrNothingToReduce):
			result.Status = SupervisionFlat
		case errors.Is(err, exchange.ErrMT5OutcomeUnknown):
			result.Status, result.Error = SupervisionUnknown, err.Error()
			log.Errorf("[SUPERVISOR] %s: the close of the position, its outcome unknown: %v", pair, err)
		default:
			result.Status, result.Error = SupervisionFailed, err.Error()
			log.Errorf("[SUPERVISOR] %s: the close of the position: %v", pair, err)
		}
		response.Results = append(response.Results, result)
	}
	writeJSON(w, response)
}

// handleSupervisionCancel cancels the pending entries of the bot, see
// SupervisionCancelRequest: the legs of its positions stay.
func (c *Chart) handleSupervisionCancel(w http.ResponseWriter, r *http.Request) {
	controller, ok := c.requireSupervisor(w, r)
	if !ok {
		return
	}
	var request SupervisionCancelRequest
	if !decodeSupervision(w, r, &request) {
		return
	}
	pairs, unknown := c.pairsOf(request.Pairs)
	if len(unknown) > 0 {
		writeJSONError(w, http.StatusNotFound, "unknown pairs: "+strings.Join(unknown, ", "))
		return
	}
	writeJSON(w, SupervisionCancelResponse{Results: c.cancelEntries(controller, pairs)})
}

// cancelEntries cancels the pending entries of the bot on pairs, the legs of
// its positions left, and returns how each cancel went.
func (c *Chart) cancelEntries(controller SupervisedController, pairs []string) []SupervisionCancelResult {
	results := []SupervisionCancelResult{}
	for _, o := range c.pendingOrders(pairs) {
		if !pendingEntry(o) {
			continue
		}
		result := SupervisionCancelResult{ID: o.ID, Pair: o.Pair, Status: SupervisionCanceled}
		if err := controller.Cancel(o); err != nil {
			switch {
			case errors.Is(err, exchange.ErrOrderNotOpen), errors.Is(err, exchange.ErrOrderNotFound):
				result.Status = SupervisionGone
			default:
				result.Status, result.Error = SupervisionFailed, err.Error()
			}
		}
		log.Warnf("[SUPERVISOR] %s: the pending entry %d %s", o.Pair, o.ID, result.Status)
		results = append(results, result)
	}
	return results
}

// cmpOr returns value, or fallback when empty.
func cmpOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
