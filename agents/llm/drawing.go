package llm

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

// The kinds, the biases and the states of the zones of a Drawing, see
// ui.Drawing.
const (
	DrawingZone   = ui.DrawingZone
	DrawingSupply = ui.DrawingSupply
	DrawingDemand = ui.DrawingDemand
	DrawingLevel  = ui.DrawingLevel
	DrawingMarker = ui.DrawingMarker

	BiasBullish = ui.BiasBullish
	BiasBearish = ui.BiasBearish
	BiasNeutral = ui.BiasNeutral

	ZoneFresh  = ui.ZoneFresh
	ZoneTested = ui.ZoneTested
	ZoneBroken = ui.ZoneBroken
)

// The limits of the drawings of a decision: how many are kept, and the length
// of their id and of their label, in characters.
const (
	maxDrawings     = 20
	maxDrawingID    = 32
	maxDrawingLabel = 24
)

// analysisLookBack are the last decisions of a pair read back from the store
// for the analysis drawn on its chart, see restoreAnalysis.
const analysisLookBack = 50

// Drawing is a mark of the analysis of the model on the chart, see ui.Drawing,
// in the form of the schema: its times in RFC 3339, as the candles of the
// context write them, and its prices null when they don't apply to its kind:
// Low and High for a zone, of supply or of demand too, Price for a level and a
// marker. To is null while the structure holds: drawn up to the last candle.
// State is the one of a supply or demand zone (fresh, tested or broken), null
// for the other kinds.
type Drawing struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Label     string   `json:"label"`
	Bias      string   `json:"bias"`
	Timeframe string   `json:"timeframe"`
	From      string   `json:"from"`
	To        *string  `json:"to"`
	Price     *float64 `json:"price"`
	Low       *float64 `json:"low"`
	High      *float64 `json:"high"`
	State     *string  `json:"state"`
}

// cleanDrawings returns the drawings of a decision on the context in that are
// drawn: of a known kind, with an id of their own and a label, on the
// timeframe of the decision or one of its higher timeframes, starting at a
// time of RFC 3339 not after the last candle, ending after it or not at all,
// and the prices of their kind positive (a zone with its low under its high,
// swapped when written the other way); a supply or demand zone with a state
// coherent with its end and with the last close of its timeframe (see
// cleanZone), once: the same zone drawn again under another id is left out;
// maxDrawings at most, their label cut to maxDrawingLabel. A wrong one only
// informs, so it is left out rather than the decision refused, with why in
// dropped.
func cleanDrawings(drawings []Drawing, in Input) (kept []Drawing, dropped []string) {
	timeframes := map[string]bool{strings.ToLower(in.Timeframe): true}
	// the last close of each timeframe, of its candles closed
	closes := make(map[string]float64)
	if n := len(in.Candles); n > 0 {
		closes[strings.ToLower(in.Timeframe)] = in.Candles[n-1].Close
	}
	for _, frame := range in.HigherTimeframes {
		timeframes[strings.ToLower(frame.Timeframe)] = true
		if n := len(frame.Candles); n > 0 {
			closes[strings.ToLower(frame.Timeframe)] = frame.Candles[n-1].Close
		}
	}
	latest := in.Time.UTC()
	if in.CandleInProgress != nil && in.CandleInProgress.Time.After(latest) {
		latest = in.CandleInProgress.Time.UTC()
	}
	ids := make(map[string]bool, len(drawings))
	zones := make(map[string]string) // the supply and demand zones kept, by their place, their id
	for i, d := range drawings {
		d, why := cleanDrawing(d, timeframes, latest)
		if why == "" && (d.Kind == DrawingSupply || d.Kind == DrawingDemand) {
			why = cleanZone(&d, closes)
		}
		place := ""
		if d.Low != nil && d.High != nil && (d.Kind == DrawingSupply || d.Kind == DrawingDemand) {
			place = fmt.Sprintf("%s|%s|%g|%g", d.Kind, d.Timeframe, *d.Low, *d.High)
		}
		switch {
		case why != "":
		case ids[d.ID]:
			why = "its id is the one of another drawing"
		case place != "" && zones[place] != "":
			why = "the same zone as " + zones[place]
		case len(kept) == maxDrawings:
			why = fmt.Sprintf("over the %d drawings drawn", maxDrawings)
		}
		if why != "" {
			name := d.ID
			if name == "" {
				name = fmt.Sprintf("#%d", i+1)
			}
			dropped = append(dropped, name+": "+why)
			continue
		}
		ids[d.ID] = true
		if place != "" {
			zones[place] = d.ID
		}
		kept = append(kept, d)
	}
	return kept, dropped
}

// cleanZone checks the state of d, a supply or demand zone, and returns why it
// isn't drawn, empty when it is: fresh or tested, the zone holds, so it has no
// end, and the last close of its timeframe in closes, when known, isn't beyond
// its far edge (above a supply zone, below a demand one), which would have
// broken it; broken, it ends at the candle that broke it.
func cleanZone(d *Drawing, closes map[string]float64) string {
	state := ""
	if d.State != nil {
		state = strings.ToLower(strings.TrimSpace(*d.State))
	}
	switch state {
	case ZoneFresh, ZoneTested:
		if d.To != nil {
			return fmt.Sprintf("a %s zone %s, with an end: an ended zone is broken", d.Kind, state)
		}
		last, known := closes[d.Timeframe]
		switch {
		case !known:
		case d.Kind == DrawingSupply && last > *d.High:
			return fmt.Sprintf("a supply zone %s, the last close of %s, %g, above its high %g: broken", state,
				d.Timeframe, last, *d.High)
		case d.Kind == DrawingDemand && last < *d.Low:
			return fmt.Sprintf("a demand zone %s, the last close of %s, %g, below its low %g: broken", state,
				d.Timeframe, last, *d.Low)
		}
	case ZoneBroken:
		if d.To == nil {
			return "a zone broken needs its end, the candle that broke it"
		}
	default:
		return fmt.Sprintf("the state %q of a %s zone: fresh, tested or broken", state, d.Kind)
	}
	d.State = &state
	return ""
}

// cleanDrawing returns d with its text trimmed, its kind, bias and timeframe
// in lower case, its times in RFC 3339 UTC and only the prices of its kind,
// and why it isn't drawn, empty when it is: see cleanDrawings.
func cleanDrawing(d Drawing, timeframes map[string]bool, latest time.Time) (Drawing, string) {
	d.ID, d.Label = strings.TrimSpace(d.ID), strings.TrimSpace(d.Label)
	d.Kind, d.Bias = strings.ToLower(strings.TrimSpace(d.Kind)), strings.ToLower(strings.TrimSpace(d.Bias))
	d.Timeframe = strings.ToLower(strings.TrimSpace(d.Timeframe))
	if runes := []rune(d.Label); len(runes) > maxDrawingLabel {
		d.Label = string(runes[:maxDrawingLabel-1]) + "…"
	}
	if d.Bias != BiasBullish && d.Bias != BiasBearish {
		d.Bias = BiasNeutral
	}
	switch {
	case d.ID == "":
		return d, "no id"
	case utf8.RuneCountInString(d.ID) > maxDrawingID:
		return d, fmt.Sprintf("an id over %d characters", maxDrawingID)
	case d.Label == "":
		return d, "no label"
	case !timeframes[d.Timeframe]:
		return d, fmt.Sprintf("the timeframe %q, not one of the context", d.Timeframe)
	}

	from, err := time.Parse(time.RFC3339, strings.TrimSpace(d.From))
	if err != nil {
		return d, fmt.Sprintf("the start %q isn't a time of RFC 3339", d.From)
	}
	if from = from.UTC(); from.After(latest) {
		return d, fmt.Sprintf("the start %s is after the last candle, %s", from.Format(time.RFC3339),
			latest.Format(time.RFC3339))
	}
	d.From = from.Format(time.RFC3339)
	if d.To != nil {
		to, err := time.Parse(time.RFC3339, strings.TrimSpace(*d.To))
		switch {
		case err != nil:
			return d, fmt.Sprintf("the end %q isn't a time of RFC 3339", *d.To)
		case to.Before(from):
			return d, "the end is before the start"
		case to.After(latest):
			return d, "the end is after the last candle: null while the structure holds"
		}
		end := to.UTC().Format(time.RFC3339)
		d.To = &end
	}

	price := func(p *float64) bool { return p != nil && known(*p) && *p > 0 }
	if d.Kind != DrawingSupply && d.Kind != DrawingDemand {
		d.State = nil // the zones of supply and demand only
	}
	switch d.Kind {
	case DrawingZone, DrawingSupply, DrawingDemand:
		if !price(d.Low) || !price(d.High) {
			return d, "a zone needs its low and its high, positive"
		}
		if *d.Low > *d.High {
			d.Low, d.High = d.High, d.Low
		}
		if *d.Low == *d.High {
			return d, "a zone of no height: its low is its high"
		}
		d.Price = nil
	case DrawingLevel, DrawingMarker:
		if !price(d.Price) {
			return d, fmt.Sprintf("a %s needs its price, positive", d.Kind)
		}
		d.Low, d.High = nil, nil
	default:
		return d, fmt.Sprintf("the kind %q: zone, supply, demand, level or marker", d.Kind)
	}
	return d, ""
}

// uiDrawings returns the drawings of a decision, cleaned (see cleanDrawings),
// in their API form; nil without any.
func uiDrawings(drawings []Drawing) []ui.Drawing {
	if len(drawings) == 0 {
		return nil
	}
	list := make([]ui.Drawing, 0, len(drawings))
	for _, d := range drawings {
		from, err := time.Parse(time.RFC3339, d.From)
		if err != nil {
			continue // cleaned: never
		}
		drawing := ui.Drawing{ID: d.ID, Kind: d.Kind, Label: d.Label, Bias: d.Bias, Timeframe: d.Timeframe,
			From: from.Unix(), Price: copyPrice(d.Price), Low: copyPrice(d.Low), High: copyPrice(d.High)}
		if d.State != nil {
			drawing.State = *d.State
		}
		if d.To != nil {
			if to, err := time.Parse(time.RFC3339, *d.To); err == nil {
				end := to.Unix()
				drawing.To = &end
			}
		}
		list = append(list, drawing)
	}
	return list
}

// modelDrawings returns drawings of the API in the form of the model, eg: the
// ones of the last analysis of a pair, read back from the store after a
// restart, for the context.
func modelDrawings(drawings []ui.Drawing) []Drawing {
	if len(drawings) == 0 {
		return nil
	}
	list := make([]Drawing, 0, len(drawings))
	for _, d := range drawings {
		drawing := Drawing{ID: d.ID, Kind: d.Kind, Label: d.Label, Bias: d.Bias, Timeframe: d.Timeframe,
			From: time.Unix(d.From, 0).UTC().Format(time.RFC3339), Price: copyPrice(d.Price), Low: copyPrice(d.Low),
			High: copyPrice(d.High)}
		if d.State != "" {
			state := d.State
			drawing.State = &state
		}
		if d.To != nil {
			end := time.Unix(*d.To, 0).UTC().Format(time.RFC3339)
			drawing.To = &end
		}
		list = append(list, drawing)
	}
	return list
}

// copyPrice returns a copy of a price that may be unset.
func copyPrice(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// noteAnalysis keeps d, a record kept, as the analysis of its pair drawn on the
// chart when the model answered it, whatever came of it (see ui.Analysis): a
// request failed, or an analysis skipped, leaves the one before.
func (t *Trader) noteAnalysis(d ui.Decision) {
	if d.Action == "" {
		return
	}
	t.analysisMtx.Lock()
	defer t.analysisMtx.Unlock()
	if t.analyses == nil {
		t.analyses = make(map[string]ui.Decision)
	}
	if current, ok := t.analyses[d.Pair]; ok && before(d, current) {
		return // an older one kept again
	}
	t.analyses[d.Pair] = d
}

// restoreAnalysis reads back from the store of the decisions the analysis of
// pair drawn on the chart before a restart, once, before its first analysis:
// the last decision of pair the model answered, among its analysisLookBack
// last ones. In the flow of the strategy: the store may ask the controller.
func (t *Trader) restoreAnalysis(pair string) {
	t.analysisMtx.Lock()
	if t.restored == nil {
		t.restored = make(map[string]bool)
	}
	_, known := t.analyses[pair]
	done := t.restored[pair]
	t.restored[pair] = true
	t.analysisMtx.Unlock()
	if known || done {
		return
	}
	decisions, err := t.store().Decisions(pair, analysisLookBack)
	if err != nil {
		log.Warnf("[LLM] %s | the decisions, for the analysis on the chart: %v", pair, err)
		return
	}
	for _, d := range decisions {
		if d.Action != "" {
			t.noteAnalysis(d)
			return
		}
	}
}

// ChartAnalysis returns the last analysis of the model on pair drawn on the
// chart, false without one yet. It implements ui.AnalysisSource.
func (t *Trader) ChartAnalysis(pair string) (ui.Analysis, bool) {
	t.analysisMtx.Lock()
	d, ok := t.analyses[pair]
	t.analysisMtx.Unlock()
	if !ok {
		return ui.Analysis{}, false
	}
	return analysisOf(d), true
}

// chartDrawings returns the drawings of the last analysis of pair on the chart,
// in the form of the model, for the context: nil without any.
func (t *Trader) chartDrawings(pair string) []Drawing {
	t.analysisMtx.Lock()
	defer t.analysisMtx.Unlock()
	return modelDrawings(t.analyses[pair].Drawings)
}

// analysisOf returns the analysis of the decision d drawn on the chart: its
// drawings and the entry it proposed without placing an order, if any.
func analysisOf(d ui.Decision) ui.Analysis {
	drawings := slices.Clone(d.Drawings)
	if drawings == nil {
		drawings = []ui.Drawing{}
	}
	return ui.Analysis{Pair: d.Pair, DecisionID: d.ID, Timeframe: d.Timeframe, Candle: d.Candle,
		DecidedAt: d.DecidedAt, Action: d.Action, Outcome: d.Outcome, Drawings: drawings, Intent: intentOf(d)}
}

// intentOf returns the entry the decision d proposed without placing an order,
// nil when it proposed none or placed it: an entry at the market or pending,
// or the new levels of a pending order (MODIFY_PENDING) refused or ignored,
// whose side the stop loss tells, below the entry for a buy, and whose type
// the entry, a buy limit below the price and a buy stop above it.
func intentOf(d ui.Decision) *ui.Intent {
	action := Action(d.Action)
	if len(d.OrderIDs) > 0 || d.Entry == nil {
		return nil
	}
	intent := &ui.Intent{Action: d.Action, Entry: copyPrice(d.Entry), StopLoss: copyPrice(d.StopLoss),
		TakeProfit: copyPrice(d.TakeProfit), Outcome: d.Outcome, Error: d.Error}
	side, kind := action.side(), action.pendingKind()
	switch {
	case action == ActionEnterLong || action == ActionEnterShort:
		kind = ui.IntentMarket
	case kind != "":
	case action == ActionModifyPending && d.Outcome != OutcomeApplied && d.StopLoss != nil:
		side = model.SideTypeBuy
		if *d.StopLoss > *d.Entry {
			side = model.SideTypeSell
		}
		kind = ui.IntentLimit
		if sideSign(side)*(*d.Entry-d.Price) > 0 {
			kind = ui.IntentStop // a buy above the price, a sell below it
		}
	default:
		return nil
	}
	if math.IsNaN(*d.Entry) {
		return nil
	}
	intent.Side, intent.OrderType = string(side), kind
	return intent
}
