package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/raykavin/xtrader/model"
)

// Action is what a Decision asks of the Trader.
type Action string

const (
	// ActionWait opens nothing: flat, without a clear opportunity.
	ActionWait Action = "WAIT"
	// ActionEnterLong buys at the market, the stop loss below the price and
	// the take profit above it.
	ActionEnterLong Action = "ENTER_LONG"
	// ActionEnterShort sells at the market, the stop loss above the price and
	// the take profit below it.
	ActionEnterShort Action = "ENTER_SHORT"
	// ActionHold keeps the position held as it is.
	ActionHold Action = "HOLD"
	// ActionExit closes the position held at the market.
	ActionExit Action = "EXIT"
	// ActionModifySL moves the stop loss of the position held.
	ActionModifySL Action = "MODIFY_SL"
	// ActionModifyTP moves the take profit of the position held.
	ActionModifyTP Action = "MODIFY_TP"

	// ActionBuyLimit places a pending buy at entry, below the ask, filled when
	// the price comes down to it, with its stop loss and take profit at the
	// broker, see Config.PendingOrders.
	ActionBuyLimit Action = "BUY_LIMIT"
	// ActionSellLimit places a pending sell at entry, above the bid, filled
	// when the price comes up to it.
	ActionSellLimit Action = "SELL_LIMIT"
	// ActionBuyStop places a pending buy at entry, above the ask, filled when
	// the price rises to it.
	ActionBuyStop Action = "BUY_STOP"
	// ActionSellStop places a pending sell at entry, below the bid, filled
	// when the price falls to it.
	ActionSellStop Action = "SELL_STOP"
	// ActionModifyPending moves the entry, the stop loss and the take profit
	// of the pending order waiting.
	ActionModifyPending Action = "MODIFY_PENDING"
	// ActionCancelPending cancels the pending order waiting.
	ActionCancelPending Action = "CANCEL_PENDING"
)

// Actions are the actions of a Decision, in the order of the schema.
var Actions = []Action{ActionWait, ActionEnterLong, ActionEnterShort, ActionHold, ActionExit, ActionModifySL,
	ActionModifyTP}

// PendingActions are the actions of the pending orders, in the order of the
// schema, after Actions, for the prompts that place them (see
// Config.PendingOrders).
var PendingActions = []Action{ActionBuyLimit, ActionSellLimit, ActionBuyStop, ActionSellStop,
	ActionModifyPending, ActionCancelPending}

// The types of the pending orders, see Config.PendingOrders.
const (
	PendingLimit = "limit"
	PendingStop  = "stop"
)

// PendingTypes are the types of the pending orders, in their order.
var PendingTypes = []string{PendingLimit, PendingStop}

// side returns the side of the position an entry opens, at the market or
// pending, none for the other actions.
func (a Action) side() model.SideType {
	switch a {
	case ActionEnterLong, ActionBuyLimit, ActionBuyStop:
		return model.SideTypeBuy
	case ActionEnterShort, ActionSellLimit, ActionSellStop:
		return model.SideTypeSell
	}
	return ""
}

// pendingKind returns the type of the pending order the action places, limit
// or stop, none for the other actions.
func (a Action) pendingKind() string {
	switch a {
	case ActionBuyLimit, ActionSellLimit:
		return PendingLimit
	case ActionBuyStop, ActionSellStop:
		return PendingStop
	}
	return ""
}

// pendingAction returns the action placing a pending order of kind on side.
func pendingAction(side model.SideType, kind string) Action {
	switch {
	case side == model.SideTypeBuy && kind == PendingLimit:
		return ActionBuyLimit
	case side == model.SideTypeSell && kind == PendingLimit:
		return ActionSellLimit
	case side == model.SideTypeBuy && kind == PendingStop:
		return ActionBuyStop
	case side == model.SideTypeSell && kind == PendingStop:
		return ActionSellStop
	}
	return ""
}

// entry reports whether the action opens a position, at the market or with a
// pending order: the daily limits and the news block refuse it.
func (a Action) entry() bool {
	return a.side() != ""
}

// trades reports whether the action sends orders: all of them but WAIT and
// HOLD.
func (a Action) trades() bool {
	return a != ActionWait && a != ActionHold
}

var (
	// ErrInvalidResponse is returned for an answer of a provider that is not a
	// decision: not JSON, a field missing or unknown, a value out of the
	// schema, a refusal or an answer cut short.
	ErrInvalidResponse = errors.New("llm: invalid response")
	// ErrInvalidDecision is returned for a decision that breaks the rules of
	// its context, see Decision.Validate.
	ErrInvalidDecision = errors.New("llm: invalid decision")
)

// Decision is the answer of the model to the context of a candle, see the
// schema of prompt.go. Entry, StopLoss and TakeProfit are null when they don't
// apply to the action.
type Decision struct {
	Action     Action   `json:"action"`
	Confidence float64  `json:"confidence"` // from 0 to 1
	Entry      *float64 `json:"entry"`
	StopLoss   *float64 `json:"stop_loss"`
	TakeProfit *float64 `json:"take_profit"`
	Reason     string   `json:"reason"`

	// Sentiment is the reading of the market of the symbol by the model, from
	// 0 (extremely bearish) to 100 (extremely bullish), 50 neutral, whatever the
	// action: shown on the dashboard, it doesn't trade. Nil when the answer has
	// none, or one out of the range, left out with a note.
	Sentiment *float64 `json:"sentiment"`

	// Levels are the supports and resistances the analysis relies on, drawn
	// on the chart: what the model saw. They don't trade.
	Levels []Level `json:"levels"`

	// Drawings are the structures the analysis relies on, drawn on the chart
	// for a prompt that asks for them (see Config.Drawings): its zones, its
	// levels and its markers. They don't trade.
	Drawings []Drawing `json:"drawings,omitempty"`

	// Notes and Attachments are what the provider returned beside the
	// decision, kept with it (see Trader.SetDecisionStore): its notes, eg: the
	// text of the model beside the call of the decision, and its files, eg:
	// images. They aren't part of the schema: the providers set them.
	Notes       []Note       `json:"notes,omitempty"`
	Attachments []Attachment `json:"-"`
}

// The kinds of a Level.
const (
	LevelSupport    = "support"
	LevelResistance = "resistance"
)

// The range of Decision.Sentiment.
const (
	minSentiment = 0
	maxSentiment = 100
)

// maxLevels caps the levels of a decision kept.
const maxLevels = 8

// Level is a support or a resistance of a decision, at Price.
type Level struct {
	Kind  string  `json:"kind"`
	Price float64 `json:"price"`
}

// decisionFields are the fields of a Decision, all of them required by the
// schema; levels and sentiment may be missing from the answer of a provider
// without them, as they only inform.
var decisionFields = []string{"reason", "action", "confidence", "sentiment", "entry", "stop_loss", "take_profit",
	"levels"}

// decisionShape is the shape of the decision a context asks for, the one of
// its schema (see schemaOf): the actions of the pending orders and the
// drawings, for a prompt that has them.
type decisionShape struct {
	pending  bool // the actions of PendingActions, see Config.PendingOrders
	drawings bool // the drawings, see Config.Drawings
}

// shapeOf returns the shape of the decision the context in asks for.
func shapeOf(in Input) decisionShape {
	return decisionShape{pending: len(in.Rules.PendingOrders) > 0, drawings: in.drawings}
}

// fields returns the fields of a decision of the shape: with the drawings in
// place of the levels, the only part of the answer drawn then.
func (s decisionShape) fields() []string {
	if s.drawings {
		fields := slices.DeleteFunc(slices.Clone(decisionFields), func(name string) bool { return name == "levels" })
		return append(fields, "drawings")
	}
	return decisionFields
}

// actions returns the actions of a decision of the shape.
func (s decisionShape) actions() []Action {
	if s.pending {
		return append(slices.Clone(Actions), PendingActions...)
	}
	return Actions
}

// ParseDecision decodes the JSON of a decision as the schema describes it:
// every field present and no other, a known action, a confidence from 0 to 1,
// the prices null or positive, and a reason; a sentiment out of its range is
// left out with a note, the decision kept. Its errors wrap
// ErrInvalidResponse. It reads the decision of a prompt without pending
// orders nor drawings, see parseDecision.
func ParseDecision(data []byte) (Decision, error) {
	return parseDecision(data, decisionShape{})
}

// parseDecision decodes the JSON of a decision of shape, see ParseDecision:
// the actions of the pending orders known with them, and the drawings read
// one by one with them, may they be missing (none), a drawing that isn't one
// of the schema left out with a note.
func parseDecision(data []byte, shape decisionShape) (Decision, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Decision{}, invalidResponse("not a JSON object: %v", err)
	}
	var notes []Note
	if _, ok := fields["levels"]; ok && shape.drawings {
		// asked for the drawings only: the levels sent all the same left out,
		// never drawn, the decision kept
		delete(fields, "levels")
		notes = append(notes, Note{Kind: "text", Text: "levels left out: the prompt draws its drawings only"})
	}
	known := shape.fields()
	for _, name := range known {
		raw, ok := fields[name]
		switch {
		case !ok && (name == "levels" || name == "drawings" || name == "sentiment"):
		case !ok:
			return Decision{}, invalidResponse("missing field %q", name)
		case string(raw) == "null" && (name == "action" || name == "confidence" || name == "reason"):
			return Decision{}, invalidResponse("field %q is null", name)
		}
	}
	for name := range fields {
		if !slices.Contains(known, name) {
			return Decision{}, invalidResponse("unknown field %q", name)
		}
	}

	raw, ok := fields["drawings"]
	delete(fields, "drawings")
	rest, err := json.Marshal(fields)
	if err != nil {
		return Decision{}, invalidResponse("%v", err)
	}
	d, err := parseDecisionFields(rest, shape)
	if err != nil {
		return Decision{}, err
	}
	d.Notes = append(d.Notes, notes...)
	if !ok {
		return d, nil
	}
	var items []json.RawMessage
	if string(raw) != "null" {
		if err := json.Unmarshal(raw, &items); err != nil {
			d.Notes = append(d.Notes, Note{Kind: "text", Text: "drawings left out: not a list: " + err.Error()})
		}
	}
	for i, item := range items {
		var drawing Drawing
		if err := json.Unmarshal(item, &drawing); err != nil {
			d.Notes = append(d.Notes, Note{Kind: "text", Text: fmt.Sprintf("drawing #%d left out: %v", i+1, err)})
			continue
		}
		d.Drawings = append(d.Drawings, drawing)
	}
	return d, nil
}

// parseDecisionFields decodes the JSON of the fields of a decision of shape
// but its drawings, checked already, see parseDecision.
func parseDecisionFields(data []byte, shape decisionShape) (Decision, error) {
	var d Decision
	if err := json.Unmarshal(data, &d); err != nil {
		return Decision{}, invalidResponse("%v", err)
	}
	d.Reason = strings.TrimSpace(d.Reason)

	switch {
	case !slices.Contains(shape.actions(), d.Action):
		return Decision{}, invalidResponse("unknown action %q", d.Action)
	case math.IsNaN(d.Confidence) || d.Confidence < 0 || d.Confidence > 1:
		return Decision{}, invalidResponse("confidence %v out of [0, 1]", d.Confidence)
	case d.Reason == "":
		return Decision{}, invalidResponse("empty reason")
	}
	for name, price := range map[string]*float64{"entry": d.Entry, "stop_loss": d.StopLoss,
		"take_profit": d.TakeProfit} {
		if price != nil && (math.IsNaN(*price) || math.IsInf(*price, 0) || *price <= 0) {
			return Decision{}, invalidResponse("%s %v is not a positive price", name, *price)
		}
	}
	d.Levels = cleanLevels(d.Levels)
	if v := d.Sentiment; v != nil && (math.IsNaN(*v) || *v < minSentiment || *v > maxSentiment) {
		// it only informs: a wrong one is dropped rather than the decision refused
		d.Notes = append(d.Notes, Note{Kind: "text",
			Text: fmt.Sprintf("sentiment left out: %v out of [%d, %d]", *v, minSentiment, maxSentiment)})
		d.Sentiment = nil
	}
	return d, nil
}

// cleanLevels returns the levels of a known kind and a positive price, each
// once, maxLevels at most: they only inform, so a wrong one is dropped rather
// than the decision refused.
func cleanLevels(levels []Level) []Level {
	var clean []Level
	for _, level := range levels {
		level.Kind = strings.ToLower(strings.TrimSpace(level.Kind))
		switch {
		case level.Kind != LevelSupport && level.Kind != LevelResistance:
		case math.IsNaN(level.Price) || math.IsInf(level.Price, 0) || level.Price <= 0:
		case slices.Contains(clean, level):
		case len(clean) < maxLevels:
			clean = append(clean, level)
		}
	}
	return clean
}

// Validate checks the decision against its context: an action of
// in.Rules.Actions, and the prices it needs on their side of the current
// price, within the distances of the rules:
//
//   - ENTER_LONG and ENTER_SHORT need an entry near the price, as they are
//     filled at the market, a stop loss on the losing side of both, and a
//     take profit on the winning side of both, unless the rules make it
//     optional: then without one, the risk:reward isn't checked; the rules
//     may also leave it out, no_take_profit: then an entry with one is
//     refused;
//   - MODIFY_SL needs a stop loss on the losing side of the price, not further
//     from it than the current one unless the rules let it widen;
//   - MODIFY_TP needs a take profit on the winning side of the price;
//   - HOLD may repeat the levels of the position, which must then be on their
//     sides of the price too;
//   - BUY_LIMIT, SELL_LIMIT, BUY_STOP and SELL_STOP need an entry on the side
//     of the price of their type, its stop loss and take profit on their sides
//     of the entry, see validatePending;
//   - MODIFY_PENDING needs the pending order of the context and its new
//     levels, validated as the ones of its placement, with the levels it has:
//     a stop loss, and a take profit when it has one, never one otherwise;
//   - CANCEL_PENDING needs the pending order of the context.
//
// Its errors wrap ErrInvalidDecision.
func (d Decision) Validate(in Input) error {
	if !slices.Contains(in.Rules.Actions, d.Action) {
		return invalidDecision("%s is not an allowed action: %v", d.Action, in.Rules.Actions)
	}

	switch d.Action {
	case ActionBuyLimit, ActionSellLimit, ActionBuyStop, ActionSellStop:
		return d.validatePending(in, d.Action)
	case ActionModifyPending, ActionCancelPending:
		pending := in.PendingOrder
		if pending == nil {
			return invalidDecision("%s without a pending order", d.Action)
		}
		if d.Action == ActionCancelPending {
			return nil
		}
		switch hasProfit := known(float64(pending.TakeProfit)); {
		case hasProfit && d.TakeProfit == nil:
			return invalidDecision("%s without the take profit of the pending order, %g: its levels are moved, "+
				"never removed", d.Action, float64(pending.TakeProfit))
		case !hasProfit && d.TakeProfit != nil:
			return invalidDecision("%s with the take profit %g: the pending order has none, and none is added",
				d.Action, *d.TakeProfit)
		}
		return d.validatePending(in, pending.Type)
	}

	price := float64(in.Price)
	if side := d.Action.side(); side != "" {
		if in.Rules.NoTakeProfit {
			switch {
			case d.Entry == nil || d.StopLoss == nil:
				return invalidDecision("%s needs an entry and a stop loss", d.Action)
			case d.TakeProfit != nil:
				return invalidDecision("%s with the take profit %g: the positions go without one, the stop loss "+
					"alone closes them", d.Action, *d.TakeProfit)
			}
		}
		if d.Entry == nil || d.StopLoss == nil || (d.TakeProfit == nil && !in.Rules.TakeProfitOptional &&
			!in.Rules.NoTakeProfit) {
			return invalidDecision("%s needs an entry, a stop loss and a take profit", d.Action)
		}
		entry, sign := *d.Entry, sideSign(side)
		if d.TakeProfit == nil {
			// the stop loss alone protects the position
			for _, ref := range []float64{price, entry} {
				if err := checkStop(side, *d.StopLoss, ref); err != nil {
					return err
				}
			}
			if limit := float64(in.Rules.MaxEntryDistance); known(limit) && math.Abs(entry-price) > limit {
				return invalidDecision("the entry %g is %g from the price %g, filled at the market: over the %g "+
					"allowed", entry, math.Abs(entry-price), price, limit)
			}
			if err := checkMinStop(in, side, *d.StopLoss); err != nil {
				return err
			}
			return checkDistances(in, *d.StopLoss)
		}
		if limit := float64(in.Rules.MaxEntryDistance); known(limit) && math.Abs(entry-price) > limit {
			return invalidDecision("the entry %g is %g from the price %g, filled at the market: over the %g "+
				"allowed", entry, math.Abs(entry-price), price, limit)
		}
		for _, ref := range []float64{price, entry} {
			if err := checkStop(side, *d.StopLoss, ref); err != nil {
				return err
			}
			if err := checkProfit(side, *d.TakeProfit, ref); err != nil {
				return err
			}
		}
		if sign*(*d.TakeProfit-*d.StopLoss) <= 0 {
			return invalidDecision("the take profit %g is behind the stop loss %g", *d.TakeProfit, *d.StopLoss)
		}
		if err := checkMinStop(in, side, *d.StopLoss); err != nil {
			return err
		}
		if err := checkRiskReward(in, side, *d.StopLoss, *d.TakeProfit); err != nil {
			return err
		}
		return checkDistances(in, *d.StopLoss, *d.TakeProfit)
	}

	if !d.Action.trades() && d.Action != ActionHold {
		return nil // WAIT: the prices, if any, are unused
	}
	position := in.Position
	if position == nil {
		return invalidDecision("%s without a position", d.Action)
	}

	switch d.Action {
	case ActionHold:
		if d.StopLoss != nil {
			if err := checkStop(position.Side, *d.StopLoss, price); err != nil {
				return err
			}
		}
		if d.TakeProfit != nil {
			if err := checkProfit(position.Side, *d.TakeProfit, price); err != nil {
				return err
			}
		}
	case ActionModifySL:
		if d.StopLoss == nil {
			return invalidDecision("%s needs a stop loss", d.Action)
		}
		stop := *d.StopLoss
		if err := checkStop(position.Side, stop, price); err != nil {
			return err
		}
		if current := float64(position.StopLoss); known(current) && !in.Rules.StopCanWiden &&
			sideSign(position.Side)*(stop-current) < 0 {
			return invalidDecision("moving the stop loss from %g to %g, away from the price, adds risk: not "+
				"allowed", current, stop)
		}
		return checkDistances(in, stop)
	case ActionModifyTP:
		if d.TakeProfit == nil {
			return invalidDecision("%s needs a take profit", d.Action)
		}
		if err := checkProfit(position.Side, *d.TakeProfit, price); err != nil {
			return err
		}
		return checkDistances(in, *d.TakeProfit)
	}
	return nil
}

// validatePending checks the levels of d, the ones of a pending order placed by
// action (BUY_LIMIT, SELL_LIMIT, BUY_STOP or SELL_STOP), or moved to them,
// against the context in: the entry on its side of the market of now (a buy
// limit below the ask and a buy stop above it, a sell limit above the bid and
// a sell stop below it), the stop loss on the losing side of the entry and the
// take profit on its winning side (or none, as for the entries at the market,
// see Validate), all of them within rules.max_level_distance of the price. As
// the order is filled at its entry, the risk and the reward are measured from
// it: the reward over the risk at least rules.min_risk_reward, and the room of
// the stop loss, from the entry net of the spread once filled (the bid of a
// long, the ask of a short), at least rules.min_stop_distance.
func (d Decision) validatePending(in Input, action Action) error {
	side, kind := action.side(), action.pendingKind()
	switch {
	case in.Rules.NoTakeProfit && d.TakeProfit != nil:
		return invalidDecision("%s with the take profit %g: the positions go without one, the stop loss alone "+
			"closes them", d.Action, *d.TakeProfit)
	case d.Entry == nil || d.StopLoss == nil:
		return invalidDecision("%s needs an entry and a stop loss", d.Action)
	case d.TakeProfit == nil && !in.Rules.TakeProfitOptional && !in.Rules.NoTakeProfit:
		return invalidDecision("%s needs an entry, a stop loss and a take profit", d.Action)
	}
	entry, sign := *d.Entry, sideSign(side)

	bid, ask := float64(in.Price), float64(in.Price)
	if spread := float64(in.Spread); known(spread) && spread > 0 {
		ask += spread
	}
	if known(float64(in.Bid)) && known(float64(in.Ask)) {
		bid, ask = float64(in.Bid), float64(in.Ask)
	}
	market, where := ask, "below the ask"
	if side == model.SideTypeSell {
		market, where = bid, "above the bid"
	}
	if kind == PendingStop {
		where = map[model.SideType]string{model.SideTypeBuy: "above the ask", model.SideTypeSell: "below the bid"}[side]
	}
	// a limit rests on the side of the market the price comes back to, a stop
	// on the side it moves on to
	beyond := sign * (entry - market)
	if kind == PendingLimit && beyond >= 0 || kind == PendingStop && beyond <= 0 {
		return invalidDecision("the entry %g of a %s must be %s, %g", entry, action, where, market)
	}

	if err := checkStop(side, *d.StopLoss, entry); err != nil {
		return err
	}
	levels := []float64{entry, *d.StopLoss}
	if d.TakeProfit != nil {
		if err := checkProfit(side, *d.TakeProfit, entry); err != nil {
			return err
		}
		levels = append(levels, *d.TakeProfit)
	}
	if err := checkDistances(in, levels...); err != nil {
		return err
	}

	risk := sign * (entry - *d.StopLoss)
	if minimum := float64(in.Rules.MinStopDistance); known(minimum) && minimum > 0 {
		room := risk
		if spread := float64(in.Spread); known(spread) && spread > 0 {
			room -= spread // the bid of a long once filled at the ask, the ask of a short filled at the bid
		}
		if room < minimum {
			return invalidDecision("the stop loss %g leaves %.8g of room before it is hit once filled at %g (net of "+
				"the spread), under the %.8g required: inside the noise", *d.StopLoss, room, entry, minimum)
		}
	}
	if minimum := float64(in.Rules.MinRiskReward); d.TakeProfit != nil && known(minimum) && minimum > 0 {
		reward := sign * (*d.TakeProfit - entry)
		if ratio := reward / risk; ratio < minimum {
			return invalidDecision("the risk:reward %.2f (reward %.8g, risk %.8g from the entry %g) is under the %g "+
				"required", ratio, reward, risk, entry, minimum)
		}
	}
	return nil
}

// checkStop fails when the stop loss of a position of side is not on its
// losing side of ref, the price or the entry: below it for a long, above it
// for a short.
func checkStop(side model.SideType, stop, ref float64) error {
	if sideSign(side)*(ref-stop) > 0 {
		return nil
	}
	where := "below"
	if side == model.SideTypeSell {
		where = "above"
	}
	return invalidDecision("the stop loss %g of a %s must be %s %g", stop, describeSide(side), where, ref)
}

// checkProfit fails when the take profit of a position of side is not on its
// winning side of ref: above it for a long, below it for a short.
func checkProfit(side model.SideType, profit, ref float64) error {
	if sideSign(side)*(profit-ref) > 0 {
		return nil
	}
	where := "above"
	if side == model.SideTypeSell {
		where = "below"
	}
	return invalidDecision("the take profit %g of a %s must be %s %g", profit, describeSide(side), where, ref)
}

// checkRiskReward fails when an entry of side at the market, with its stop
// loss and take profit, earns less than in.Rules.MinRiskReward times what it
// risks, both net of the spread: a long is filled at the ask and closed at the
// bid, the price of the context; a short is filled at the bid, its levels hit
// at the ask, so the spread is in them already.
func checkRiskReward(in Input, side model.SideType, stop, profit float64) error {
	minimum := float64(in.Rules.MinRiskReward)
	if !known(minimum) || minimum <= 0 {
		return nil
	}
	fill, sign := float64(in.Price), sideSign(side)
	if spread := float64(in.Spread); side == model.SideTypeBuy && known(spread) && spread > 0 {
		fill += spread
	}
	risk, reward := sign*(fill-stop), sign*(profit-fill)
	if risk <= 0 || reward <= 0 {
		return invalidDecision("the stop loss %g and the take profit %g leave no risk:reward from the fill %g",
			stop, profit, fill)
	}
	if ratio := reward / risk; ratio < minimum {
		return invalidDecision("the risk:reward %.2f (reward %g, risk %g from the fill %g, net of the spread) is "+
			"under the %g required", ratio, reward, risk, fill, minimum)
	}
	return nil
}

// checkMinStop fails when the stop loss of an entry of side leaves less room
// than in.Rules.MinStopDistance before it is hit: from the bid, the price, for
// a long; from the ask, the price plus the spread, for a short. A stop inside
// the noise of the candles is hit by it.
func checkMinStop(in Input, side model.SideType, stop float64) error {
	minimum := float64(in.Rules.MinStopDistance)
	if !known(minimum) || minimum <= 0 {
		return nil
	}
	trigger := float64(in.Price) // the bid, for a long
	if side == model.SideTypeSell {
		if spread := float64(in.Spread); known(spread) && spread > 0 {
			trigger += spread // the ask
		}
	}
	if room := sideSign(side) * (trigger - stop); room < minimum {
		return invalidDecision("the stop loss %g leaves %g of room before it is hit (from %g), under the %g "+
			"required: inside the noise", stop, room, trigger, minimum)
	}
	return nil
}

// checkDistances fails when a level is further from the price than
// in.Rules.MaxLevelDistance.
func checkDistances(in Input, levels ...float64) error {
	limit, price := float64(in.Rules.MaxLevelDistance), float64(in.Price)
	if !known(limit) {
		return nil
	}
	for _, level := range levels {
		if distance := math.Abs(level - price); distance > limit {
			return invalidDecision("the level %g is %g from the price %g: over the %g allowed", level, distance,
				price, limit)
		}
	}
	return nil
}

// sideSign is 1 for a long and -1 for a short, so that prices compare the same
// way on both sides.
func sideSign(side model.SideType) float64 {
	if side == model.SideTypeSell {
		return -1
	}
	return 1
}

// describeSide names the position of side as the context does: LONG or SHORT.
func describeSide(side model.SideType) string {
	if side == model.SideTypeSell {
		return "SHORT"
	}
	return "LONG"
}

func invalidResponse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidResponse, fmt.Sprintf(format, args...))
}

func invalidDecision(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidDecision, fmt.Sprintf(format, args...))
}
