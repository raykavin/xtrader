package llm

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// pendingInput is testInput with the pending orders of kinds allowed, flat:
// the price 100, the levels at most 10 from it.
func pendingInput(kinds ...string) Input {
	in := testInput(nil, ActionWait, ActionEnterLong, ActionEnterShort, ActionBuyLimit, ActionSellLimit,
		ActionBuyStop, ActionSellStop)
	in.Rules.PendingOrders = kinds
	return in
}

// pendingDecision is a decision of action at entry, its stop loss and take
// profit.
func pendingDecision(action Action, entry, stop float64, profit *float64) Decision {
	return Decision{Action: action, Confidence: 0.8, Entry: ptr(entry), StopLoss: ptr(stop), TakeProfit: profit,
		Reason: "test"}
}

// The answer of a context with the pending orders and the drawings takes their
// actions and their drawings; the one of a context without them refuses them,
// as before.
func TestParseDecision_PendingOrdersAndDrawings(t *testing.T) {
	answer := `{"reason": "ok", "action": "BUY_LIMIT", "confidence": 0.7, "entry": 99, "stop_loss": 98,
		"take_profit": 103, "drawings": [{"id": "fvg-1", "kind": "zone", "label": "FVG",
		"bias": "bullish", "timeframe": "15m", "from": "2026-09-25T10:00:00Z", "to": null, "price": null,
		"low": 98.5, "high": 99.2}]}`

	_, err := ParseDecision([]byte(answer))
	require.ErrorContains(t, err, `unknown field "drawings"`, "the shape of before")
	_, err = parseDecision([]byte(strings.Replace(answer, `, "drawings": [`, `, "x": [`, 1)),
		decisionShape{pending: true, drawings: true})
	require.ErrorContains(t, err, `unknown field "x"`)
	noDrawings := `{"reason": "ok", "action": "BUY_LIMIT", "confidence": 0.7, "entry": 99, "stop_loss": 98,
		"take_profit": 103, "levels": []}`
	_, err = ParseDecision([]byte(noDrawings))
	require.ErrorContains(t, err, `unknown action "BUY_LIMIT"`, "the actions of before")

	d, err := parseDecision([]byte(answer), decisionShape{pending: true, drawings: true})
	require.NoError(t, err)
	require.Equal(t, ActionBuyLimit, d.Action)
	require.Equal(t, []Drawing{{ID: "fvg-1", Kind: "zone", Label: "FVG", Bias: "bullish", Timeframe: "15m",
		From: "2026-09-25T10:00:00Z", Low: ptr(98.5), High: ptr(99.2)}}, d.Drawings)
	require.Empty(t, d.Notes)

	d, err = parseDecision([]byte(noDrawings), decisionShape{pending: true, drawings: true})
	require.NoError(t, err, "drawings missing: none")
	require.Empty(t, d.Drawings)

	// with the drawings, the levels sent all the same left out, never drawn
	withLevels := strings.Replace(answer, `"take_profit": 103,`,
		`"take_profit": 103, "levels": [{"kind": "support", "price": 98}],`, 1)
	d, err = parseDecision([]byte(withLevels), decisionShape{pending: true, drawings: true})
	require.NoError(t, err)
	require.Empty(t, d.Levels)
	require.Len(t, d.Drawings, 1)
	require.Equal(t, []Note{{Kind: "text", Text: "levels left out: the prompt draws its drawings only"}}, d.Notes)

	// a drawing that isn't one of the schema left out, the decision kept
	bad := strings.Replace(answer, `"low": 98.5`, `"low": "low"`, 1)
	d, err = parseDecision([]byte(bad), decisionShape{pending: true, drawings: true})
	require.NoError(t, err)
	require.Empty(t, d.Drawings)
	require.Len(t, d.Notes, 1)
	require.Contains(t, d.Notes[0].Text, "drawing #1 left out")
	d, err = parseDecision([]byte(strings.Replace(noDrawings, `"levels": []`, `"levels": [], "drawings": 3`, 1)),
		decisionShape{drawings: true})
	require.ErrorContains(t, err, `unknown action "BUY_LIMIT"`, "the drawings without the pending orders")
	d, err = parseDecision([]byte(`{"reason": "ok", "action": "WAIT", "confidence": 0.5, "entry": null,
		"stop_loss": null, "take_profit": null, "drawings": 3}`), decisionShape{drawings: true})
	require.NoError(t, err)
	require.Contains(t, d.Notes[0].Text, "drawings left out: not a list")
}

// A pending order rests on the side of the market of its type, its levels on
// their sides of its entry, measured from its entry: the risk:reward and the
// room of the stop loss net of the spread, within the distances of the rules.
func TestDecision_ValidatePending(t *testing.T) {
	spread := func(in Input) Input { in.Spread = 0.4; return in }
	minimums := func(in Input) Input {
		in.Rules.MinRiskReward, in.Rules.MinStopDistance = 1.5, 1
		return in
	}
	for _, tc := range []struct {
		name  string
		in    Input
		d     Decision
		error string // empty when valid
	}{
		{name: "a buy limit below the price", in: pendingInput(PendingLimit),
			d: pendingDecision(ActionBuyLimit, 98, 97, ptr(102))},
		{name: "a buy limit above the price", in: pendingInput(PendingLimit),
			d:     pendingDecision(ActionBuyLimit, 100.5, 99, ptr(103)),
			error: "the entry 100.5 of a BUY_LIMIT must be below the ask, 100"},
		{name: "a buy limit under the ask, above the bid", in: spread(pendingInput(PendingLimit)),
			d: pendingDecision(ActionBuyLimit, 100.2, 99, ptr(103))},
		{name: "a sell limit above the price", in: pendingInput(PendingLimit),
			d: pendingDecision(ActionSellLimit, 102, 103, ptr(98))},
		{name: "a sell limit below the price", in: pendingInput(PendingLimit),
			d:     pendingDecision(ActionSellLimit, 99, 100.5, ptr(97)),
			error: "the entry 99 of a SELL_LIMIT must be above the bid, 100"},
		{name: "a buy stop above the ask", in: spread(pendingInput(PendingStop)),
			d: pendingDecision(ActionBuyStop, 101, 99, ptr(104))},
		{name: "a buy stop under the ask", in: spread(pendingInput(PendingStop)),
			d:     pendingDecision(ActionBuyStop, 100.3, 99, ptr(104)),
			error: "the entry 100.3 of a BUY_STOP must be above the ask, 100.4"},
		{name: "a sell stop below the bid", in: pendingInput(PendingStop),
			d: pendingDecision(ActionSellStop, 99, 100.5, ptr(96))},
		{name: "a sell stop above the bid", in: pendingInput(PendingStop),
			d:     pendingDecision(ActionSellStop, 100.5, 101, ptr(96)),
			error: "the entry 100.5 of a SELL_STOP must be below the bid, 100"},
		{name: "the stop loss on the wrong side of the entry", in: pendingInput(PendingLimit),
			d: pendingDecision(ActionBuyLimit, 98, 98.5, ptr(102)), error: "the stop loss 98.5 of a LONG must be below 98"},
		{name: "the take profit on the wrong side of the entry", in: pendingInput(PendingLimit),
			d:     pendingDecision(ActionSellLimit, 102, 103, ptr(102.5)),
			error: "the take profit 102.5 of a SHORT must be below 102"},
		{name: "far from the price", in: pendingInput(PendingLimit),
			d:     pendingDecision(ActionBuyLimit, 91, 89, ptr(95)),
			error: "the level 89 is 11 from the price 100: over the 10 allowed"},
		{name: "the risk:reward from the entry", in: minimums(pendingInput(PendingLimit)),
			d: pendingDecision(ActionBuyLimit, 98, 96, ptr(101))},
		{name: "the risk:reward under the minimum", in: minimums(pendingInput(PendingLimit)),
			d:     pendingDecision(ActionBuyLimit, 98, 96, ptr(100.9)),
			error: "the risk:reward 1.45 (reward 2.9, risk 2 from the entry 98) is under the 1.5 required"},
		{name: "the room of the stop net of the spread", in: spread(minimums(pendingInput(PendingLimit))),
			d:     pendingDecision(ActionBuyLimit, 98, 97.2, ptr(103)),
			error: "the stop loss 97.2 leaves 0.4 of room before it is hit once filled at 98"},
		{name: "a take profit required", in: pendingInput(PendingLimit),
			d:     pendingDecision(ActionBuyLimit, 98, 97, nil),
			error: "BUY_LIMIT needs an entry, a stop loss and a take profit"},
		{name: "a type not allowed", in: testInput(nil, ActionWait, ActionBuyLimit),
			d:     pendingDecision(ActionBuyStop, 101, 99, ptr(104)),
			error: "BUY_STOP is not an allowed action"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.d.Validate(tc.in)
			if tc.error == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrInvalidDecision)
			require.ErrorContains(t, err, tc.error)
		})
	}

	optional := pendingInput(PendingLimit)
	optional.Rules.TakeProfitOptional = true
	require.NoError(t, pendingDecision(ActionBuyLimit, 98, 97, nil).Validate(optional), "the stop loss alone")
	none := pendingInput(PendingLimit)
	none.Rules.NoTakeProfit = true
	require.ErrorContains(t, pendingDecision(ActionBuyLimit, 98, 97, ptr(102)).Validate(none),
		"BUY_LIMIT with the take profit 102")
}

// MODIFY_PENDING and CANCEL_PENDING need the pending order of the context, the
// new levels of a modification validated as the ones of its placement, with
// the levels it has.
func TestDecision_ValidateModifyPending(t *testing.T) {
	waiting := testInput(nil, ActionWait, ActionModifyPending, ActionCancelPending)
	waiting.PendingOrder = &PendingOrder{Type: ActionBuyLimit, Entry: 98, StopLoss: 97, TakeProfit: 102, Size: 1,
		PlacedBy: PlacedByModel}
	nan := number(math.NaN())

	require.NoError(t, pendingDecision(ActionModifyPending, 98.5, 97.5, ptr(102)).Validate(waiting))
	require.NoError(t, Decision{Action: ActionCancelPending, Confidence: 0.8, Reason: "x"}.Validate(waiting))
	require.ErrorContains(t, pendingDecision(ActionModifyPending, 100.5, 99, ptr(103)).Validate(waiting),
		"must be below the ask", "validated as a buy limit")
	require.ErrorContains(t, pendingDecision(ActionModifyPending, 98.5, 97.5, nil).Validate(waiting),
		"MODIFY_PENDING without the take profit of the pending order, 102")

	withoutProfit := waiting
	withoutProfit.Rules.TakeProfitOptional = true
	withoutProfit.PendingOrder = &PendingOrder{Type: ActionSellStop, Entry: 99, StopLoss: 100.5, TakeProfit: nan}
	require.NoError(t, pendingDecision(ActionModifyPending, 98, 100, nil).Validate(withoutProfit))
	require.ErrorContains(t, pendingDecision(ActionModifyPending, 98, 100, ptr(95)).Validate(withoutProfit),
		"the pending order has none, and none is added")

	flat := testInput(nil, ActionWait, ActionModifyPending, ActionCancelPending)
	require.ErrorContains(t, Decision{Action: ActionCancelPending, Confidence: 0.8, Reason: "x"}.Validate(flat),
		"CANCEL_PENDING without a pending order")
}

// The schema of a context without the pending orders nor the drawings is the
// one of before; with them, the same, the reason first, with their actions
// and the drawings, strict: every property required, none other.
func TestSchemaOf(t *testing.T) {
	require.Equal(t, decisionSchema, schemaOf(decisionShape{}))
	require.JSONEq(t, string(decisionSchema), string(extendedSchema(decisionShape{})), "the same template")

	for _, shape := range []decisionShape{{pending: true}, {drawings: true}, {pending: true, drawings: true}} {
		schema := schemaOf(shape)
		var parsed struct {
			Properties map[string]struct {
				Enum  []string        `json:"enum"`
				Items json.RawMessage `json:"items"`
			} `json:"properties"`
			Required []string `json:"required"`
			Extra    bool     `json:"additionalProperties"`
		}
		require.NoError(t, json.Unmarshal(schema, &parsed), "%+v", shape)
		require.Len(t, parsed.Required, len(parsed.Properties), "every property required")
		require.Contains(t, parsed.Required, "sentiment", "the reading of the market asked for in every shape")
		require.False(t, parsed.Extra)
		require.Less(t, strings.Index(string(schema), `"reason"`), strings.Index(string(schema), `"action"`),
			"the reason first")
		actions := parsed.Properties["action"].Enum
		require.Equal(t, shape.pending, slices.Contains(actions, string(ActionBuyLimit)), "%+v", shape)
		require.Equal(t, shape.pending, slices.Contains(actions, string(ActionCancelPending)))
		_, drawings := parsed.Properties["drawings"]
		require.Equal(t, shape.drawings, drawings)
		require.Equal(t, shape.drawings, slices.Contains(parsed.Required, "drawings"))
		_, levels := parsed.Properties["levels"]
		require.Equal(t, !shape.drawings, levels, "the levels but with the drawings, the only ones drawn")
	}
}

// The providers send the schema of the context and read its answer.
func TestProviders_SchemaOfTheContext(t *testing.T) {
	answer := `{"reason": "ok", "action": "BUY_LIMIT", "confidence": 0.7, "entry": 98, "stop_loss": 97,
		"take_profit": 102, "drawings": [{"id": "eq", "kind": "level", "label": "EQ",
		"bias": "neutral", "timeframe": "15m", "from": "2026-09-25T10:00:00Z", "to": null, "price": 99,
		"low": null, "high": null}]}`
	in := pendingInput(PendingLimit)
	in.drawings = true
	for _, p := range testProviders {
		t.Run(p.name, func(t *testing.T) {
			server, got := recording(t, p.answer(answer))
			provider, err := p.create(server.URL)
			require.NoError(t, err)
			d, err := provider.Analyze(context.Background(), in)
			require.NoError(t, err)
			require.Equal(t, ActionBuyLimit, d.Action)
			require.Len(t, d.Drawings, 1)

			sent := string(got.raw)
			require.Contains(t, sent, `"BUY_LIMIT"`)
			require.Contains(t, sent, `"drawings"`)
		})
	}
}

// The header of a prompt asks for the pending orders, the drawings and the
// parts of the context; a value unknown refuses it.
func TestStrategyHeaders_SMC(t *testing.T) {
	strategy := "# x: y\n# Pending orders: Stop; limit\n# Drawings: ON\n# Context: smc\n\nSetup: z."
	kinds, err := StrategyPendingOrders(strategy)
	require.NoError(t, err)
	require.Equal(t, []string{PendingLimit, PendingStop}, kinds, "in their order")
	drawings, err := StrategyDrawings(strategy)
	require.NoError(t, err)
	require.True(t, drawings)
	contexts, err := StrategyContext(strategy)
	require.NoError(t, err)
	require.Equal(t, []string{ContextSMC}, contexts)

	kinds, err = StrategyPendingOrders("# x: y\n# Pending orders: none\n\nSetup: z.")
	require.NoError(t, err)
	require.Empty(t, kinds)
	_, err = StrategyPendingOrders("# x: y\n# Pending orders: limit, market\n\nSetup: z.")
	require.ErrorContains(t, err, `the type "market"`)
	_, err = StrategyDrawings("# x: y\n# Drawings: yes\n\nSetup: z.")
	require.ErrorContains(t, err, "on or off")
	_, err = StrategyContext("# x: y\n# Context: orderflow\n\nSetup: z.")
	require.ErrorContains(t, err, `the part "orderflow"`)

	// the instructions of the code, with the parts the header asks for
	// the levels left to the prompts without drawings: the drawings are the
	// only part of the answer drawn
	system := systemPrompt(strategy)
	require.Contains(t, basePrompt, levelsInstruction)
	require.True(t, strings.HasPrefix(system, strings.Replace(basePrompt, levelsInstruction, "", 1)+smcContextPrompt+
		"\n\n"+pendingOrdersPrompt+"\n\n"+drawingsPrompt+"\n\nStrategy:\nSetup: z."))
	require.NotContains(t, system, "List in levels")
	require.Equal(t, basePrompt+"\n\nStrategy:\nSetup: z.", systemPrompt("# x: y\n\nSetup: z."))

	// a prompt of a value unknown is refused, the one before kept
	r := newTraderRunner(t, nil, 0)
	_, err = r.trader.promptState(Prompt{Content: "# x: y\n# Drawings: maybe\n\nSetup: z."})
	require.ErrorContains(t, err, "# Drawings")
	st, err := r.trader.promptState(Prompt{Content: strategy})
	require.NoError(t, err)
	require.Equal(t, []string{PendingLimit, PendingStop}, st.pendingOrders)
	require.True(t, st.drawings)
	require.True(t, st.smc)
}

// The drawings of a decision are drawn once valid, the wrong ones left out
// with why: a decision is never refused for them.
func TestCleanDrawings(t *testing.T) {
	in := testInput(nil, ActionWait)
	in.Time = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	in.HigherTimeframes = []Frame{{Timeframe: "1h"}, {Timeframe: "4h"}}
	zone := func(id string) Drawing {
		return Drawing{ID: id, Kind: "zone", Label: "FVG", Bias: "bullish", Timeframe: "15m",
			From: "2026-09-25T10:00:00Z", Low: ptr(98), High: ptr(99)}
	}
	to := func(s string) *string { return &s }

	kept, dropped := cleanDrawings([]Drawing{
		zone("fvg-1"),
		{ID: " ob ", Kind: " ZONE ", Label: "OB", Bias: "Bearish", Timeframe: "4H", From: "2026-09-25T08:00:00+00:00",
			To: to("2026-09-25T11:00:00Z"), Low: ptr(103), High: ptr(102), Price: ptr(1)},
		{ID: "bsl", Kind: "level", Label: "BSL", Bias: "up", Timeframe: "1h", From: "2026-09-25T09:00:00Z",
			Price: ptr(104), Low: ptr(1)},
		{ID: "bos", Kind: "marker", Label: "a label far too long for the chart", Bias: "bearish", Timeframe: "15m",
			From: "2026-09-25T12:00:00Z", Price: ptr(100)},
		zone("fvg-1"),
		{ID: "", Kind: "level", Label: "x", Timeframe: "15m", From: "2026-09-25T09:00:00Z", Price: ptr(1)},
		{ID: "x1", Kind: "level", Label: "", Timeframe: "15m", From: "2026-09-25T09:00:00Z", Price: ptr(1)},
		{ID: "x2", Kind: "level", Label: "x", Timeframe: "1d", From: "2026-09-25T09:00:00Z", Price: ptr(1)},
		{ID: "x3", Kind: "level", Label: "x", Timeframe: "15m", From: "yesterday", Price: ptr(1)},
		{ID: "x4", Kind: "level", Label: "x", Timeframe: "15m", From: "2026-09-25T12:15:00Z", Price: ptr(1)},
		{ID: "x5", Kind: "zone", Label: "x", Timeframe: "15m", From: "2026-09-25T09:00:00Z", Low: ptr(1)},
		{ID: "x6", Kind: "zone", Label: "x", Timeframe: "15m", From: "2026-09-25T09:00:00Z", Low: ptr(1), High: ptr(1)},
		{ID: "x7", Kind: "marker", Label: "x", Timeframe: "15m", From: "2026-09-25T09:00:00Z", Price: ptr(-1)},
		{ID: "x8", Kind: "arrow", Label: "x", Timeframe: "15m", From: "2026-09-25T09:00:00Z", Price: ptr(1)},
		{ID: "x9", Kind: "level", Label: "x", Timeframe: "15m", From: "2026-09-25T09:00:00Z",
			To: to("2026-09-25T08:00:00Z"), Price: ptr(1)},
		{ID: "x10", Kind: "level", Label: "x", Timeframe: "15m", From: "2026-09-25T09:00:00Z",
			To: to("2026-09-25T13:00:00Z"), Price: ptr(1)},
	}, in)

	require.Equal(t, []Drawing{
		zone("fvg-1"),
		{ID: "ob", Kind: "zone", Label: "OB", Bias: "bearish", Timeframe: "4h", From: "2026-09-25T08:00:00Z",
			To: to("2026-09-25T11:00:00Z"), Low: ptr(102), High: ptr(103)},
		{ID: "bsl", Kind: "level", Label: "BSL", Bias: "neutral", Timeframe: "1h", From: "2026-09-25T09:00:00Z",
			Price: ptr(104)},
		{ID: "bos", Kind: "marker", Label: "a label far too long fo…", Bias: "bearish", Timeframe: "15m",
			From: "2026-09-25T12:00:00Z", Price: ptr(100)},
	}, kept)
	require.Equal(t, []string{
		"fvg-1: its id is the one of another drawing",
		"#6: no id",
		"x1: no label",
		`x2: the timeframe "1d", not one of the context`,
		`x3: the start "yesterday" isn't a time of RFC 3339`,
		"x4: the start 2026-09-25T12:15:00Z is after the last candle, 2026-09-25T12:00:00Z",
		"x5: a zone needs its low and its high, positive",
		"x6: a zone of no height: its low is its high",
		"x7: a marker needs its price, positive",
		`x8: the kind "arrow": zone, supply, demand, level or marker`,
		"x9: the end is before the start",
		"x10: the end is after the last candle: null while the structure holds",
	}, dropped)

	many := make([]Drawing, 0, maxDrawings+2)
	for i := range maxDrawings + 2 {
		many = append(many, zone(strings.Repeat("z", i+1)))
	}
	kept, dropped = cleanDrawings(many, in)
	require.Len(t, kept, maxDrawings)
	require.Len(t, dropped, 2)
	require.Contains(t, dropped[0], "over the 20 drawings drawn")

	// the candle in progress of a reanalysis: drawn up to it
	in.CandleInProgress = &CandleInProgress{Time: in.Time.Add(15 * time.Minute)}
	kept, _ = cleanDrawings([]Drawing{{ID: "m", Kind: "marker", Label: "x", Timeframe: "15m",
		From: "2026-09-25T12:15:00Z", Price: ptr(1)}}, in)
	require.Len(t, kept, 1)
}

// The drawings go to the API and back in the form of the model.
func TestDrawings_API(t *testing.T) {
	to := "2026-09-25T11:00:00Z"
	drawings := []Drawing{{ID: "ob", Kind: "zone", Label: "OB", Bias: "bearish", Timeframe: "4h",
		From: "2026-09-25T08:00:00Z", To: &to, Low: ptr(102), High: ptr(103)}}
	api := uiDrawings(drawings)
	require.Len(t, api, 1)
	require.Equal(t, time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC).Unix(), api[0].From)
	require.Equal(t, time.Date(2026, 9, 25, 11, 0, 0, 0, time.UTC).Unix(), *api[0].To)
	require.Equal(t, drawings, modelDrawings(api))
	require.Nil(t, uiDrawings(nil))
	require.Nil(t, modelDrawings(nil))
}

// The supply and demand zones are drawn with a state coherent with their end
// and with the last close of their timeframe, once each: a zone fresh or
// tested holds (no end, no close beyond its far edge), a broken one ends at the
// candle that broke it; the state of the other kinds is left out.
func TestCleanDrawings_SupplyDemand(t *testing.T) {
	in := testInput(nil, ActionWait)
	in.Time = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	in.Candles = Candles{{Time: in.Time, Close: 100}}
	in.HigherTimeframes = []Frame{{Timeframe: "4h", Candles: Candles{{Time: in.Time.Add(-4 * time.Hour),
		Close: 103}}}}
	state := func(s string) *string { return &s }
	to := func(s string) *string { return &s }
	zone := func(id, kind, timeframe string, low, high float64, s *string) Drawing {
		return Drawing{ID: id, Kind: kind, Label: id, Bias: BiasNeutral, Timeframe: timeframe,
			From: "2026-09-25T08:00:00Z", Low: ptr(low), High: ptr(high), State: s}
	}

	broken := zone("d-broken", DrawingDemand, "15m", 100.5, 101, state("broken"))
	broken.To = to("2026-09-25T11:00:00Z")
	fresh4h := zone("s-4h", DrawingSupply, "4h", 102, 102.5, state("Fresh"))
	kept, dropped := cleanDrawings([]Drawing{
		zone("s1", DrawingSupply, "15m", 101, 102, state("fresh")),
		zone("d1", DrawingDemand, "15m", 98, 99, state("tested")),
		broken,
		zone("s-dup", DrawingSupply, "15m", 101, 102, state("tested")),
		zone("s-below", DrawingSupply, "15m", 98.5, 99.5, state("fresh")),
		zone("d-above", DrawingDemand, "15m", 100.5, 101.5, state("tested")),
		fresh4h,
		zone("s-nostate", DrawingSupply, "15m", 104, 105, nil),
		zone("s-odd", DrawingSupply, "15m", 104, 105, state("strong")),
		func() Drawing {
			d := zone("s-end", DrawingSupply, "15m", 106, 107, state("fresh"))
			d.To = broken.To
			return d
		}(),
		zone("d-noend", DrawingDemand, "15m", 90, 91, state("broken")),
		{ID: "fvg", Kind: DrawingZone, Label: "FVG", Timeframe: "15m", From: "2026-09-25T08:00:00Z", Low: ptr(97),
			High: ptr(97.5), State: state("fresh")},
	}, in)

	ids := make([]string, 0, len(kept))
	for _, d := range kept {
		ids = append(ids, d.ID)
	}
	require.Equal(t, []string{"s1", "d1", "d-broken", "fvg"}, ids)
	require.Equal(t, "fresh", *kept[0].State)
	require.Equal(t, "tested", *kept[1].State)
	require.Nil(t, kept[3].State, "the state of a zone of the structure left out")
	require.Equal(t, []string{
		"s-dup: the same zone as s1",
		"s-below: a supply zone fresh, the last close of 15m, 100, above its high 99.5: broken",
		"d-above: a demand zone tested, the last close of 15m, 100, below its low 100.5: broken",
		"s-4h: a supply zone fresh, the last close of 4h, 103, above its high 102.5: broken",
		`s-nostate: the state "" of a supply zone: fresh, tested or broken`,
		`s-odd: the state "strong" of a supply zone: fresh, tested or broken`,
		"s-end: a supply zone fresh, with an end: an ended zone is broken",
		"d-noend: a zone broken needs its end, the candle that broke it",
	}, dropped)

	// to the API and back, with its state
	api := uiDrawings(kept)
	require.Equal(t, "fresh", api[0].State)
	require.Empty(t, api[3].State)
	require.Equal(t, kept, modelDrawings(api))
}
