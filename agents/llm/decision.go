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
)

// Actions are the actions of a Decision, in the order of the schema.
var Actions = []Action{ActionWait, ActionEnterLong, ActionEnterShort, ActionHold, ActionExit, ActionModifySL,
	ActionModifyTP}

// side returns the side of the position an entry opens, none for the other
// actions.
func (a Action) side() model.SideType {
	switch a {
	case ActionEnterLong:
		return model.SideTypeBuy
	case ActionEnterShort:
		return model.SideTypeSell
	}
	return ""
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

	// Levels are the supports and resistances the analysis relies on, drawn
	// on the chart: what the model saw. They don't trade.
	Levels []Level `json:"levels"`

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

// maxLevels caps the levels of a decision kept.
const maxLevels = 8

// Level is a support or a resistance of a decision, at Price.
type Level struct {
	Kind  string  `json:"kind"`
	Price float64 `json:"price"`
}

// decisionFields are the fields of a Decision, all of them required by the
// schema; levels may be missing from the answer of a provider without it.
var decisionFields = []string{"reason", "action", "confidence", "entry", "stop_loss", "take_profit", "levels"}

// ParseDecision decodes the JSON of a decision as the schema describes it:
// every field present and no other, a known action, a confidence from 0 to 1,
// the prices null or positive, and a reason. Its errors wrap
// ErrInvalidResponse.
func ParseDecision(data []byte) (Decision, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Decision{}, invalidResponse("not a JSON object: %v", err)
	}
	for _, name := range decisionFields {
		raw, ok := fields[name]
		switch {
		case !ok && name == "levels":
		case !ok:
			return Decision{}, invalidResponse("missing field %q", name)
		case string(raw) == "null" && (name == "action" || name == "confidence" || name == "reason"):
			return Decision{}, invalidResponse("field %q is null", name)
		}
	}
	for name := range fields {
		if !slices.Contains(decisionFields, name) {
			return Decision{}, invalidResponse("unknown field %q", name)
		}
	}

	var d Decision
	if err := json.Unmarshal(data, &d); err != nil {
		return Decision{}, invalidResponse("%v", err)
	}
	d.Reason = strings.TrimSpace(d.Reason)

	switch {
	case !slices.Contains(Actions, d.Action):
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
//     sides of the price too.
//
// Its errors wrap ErrInvalidDecision.
func (d Decision) Validate(in Input) error {
	if !slices.Contains(in.Rules.Actions, d.Action) {
		return invalidDecision("%s is not an allowed action: %v", d.Action, in.Rules.Actions)
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
