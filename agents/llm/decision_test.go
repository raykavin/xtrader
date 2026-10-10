package llm

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

func ptr(v float64) *float64 {
	return &v
}

// testInput is a context at the price 100, the entry at most 1 from it and
// the levels at most 10.
func testInput(position *Position, actions ...Action) Input {
	nan := number(math.NaN())
	return Input{
		Symbol: "XAUUSD", Timeframe: "15m", Price: 100, Bid: nan, Ask: nan, Spread: nan, EntrySize: 1,
		Balance: 10000, Position: position,
		Rules: Rules{Actions: actions, MinConfidence: 0.6, MaxEntryDistance: 1, MaxLevelDistance: 10,
			ReasonLanguage: "English"},
	}
}

var flatActions = []Action{ActionWait, ActionEnterLong, ActionEnterShort}
var heldActions = []Action{ActionHold, ActionExit, ActionModifySL, ActionModifyTP}

// longPosition is a long entered at 98, its stop loss at 97 and its take
// profit at 104.
func longPosition() *Position {
	return &Position{Side: model.SideTypeBuy, Size: 1, Entry: 98, StopLoss: 97, TakeProfit: 104, PnL: 2, Candles: 3}
}

// shortPosition is a short entered at 102, its stop loss at 103 and its take
// profit at 96.
func shortPosition() *Position {
	return &Position{Side: model.SideTypeSell, Size: 1, Entry: 102, StopLoss: 103, TakeProfit: 96, PnL: 2,
		Candles: 3}
}

func TestParseDecision(t *testing.T) {
	t.Run("the entries of the spec", func(t *testing.T) {
		d, err := ParseDecision([]byte(`{"action": "ENTER_LONG", "confidence": 0.82, "entry": 3852.60,
			"stop_loss": 3847.80, "take_profit": 3861.00, "reason": "Breakout confirmado..."}`))
		require.NoError(t, err)
		require.Equal(t, Decision{Action: ActionEnterLong, Confidence: 0.82, Entry: ptr(3852.6), StopLoss: ptr(3847.8),
			TakeProfit: ptr(3861), Reason: "Breakout confirmado..."}, d)
	})

	t.Run("nulls", func(t *testing.T) {
		d, err := ParseDecision([]byte(`{"action": "WAIT", "confidence": 0.42, "entry": null, "stop_loss": null,
			"take_profit": null, "reason": "  Sem confirmação suficiente. "}`))
		require.NoError(t, err)
		require.Equal(t, Decision{Action: ActionWait, Confidence: 0.42, Reason: "Sem confirmação suficiente."}, d)
	})

	t.Run("levels", func(t *testing.T) {
		d, err := ParseDecision([]byte(`{"action": "WAIT", "confidence": 0.5, "entry": null, "stop_loss": null,
			"take_profit": null, "reason": "ok", "levels": [{"kind": "support", "price": 84000},
			{"kind": " Resistance ", "price": 84500}, {"kind": "support", "price": 84000},
			{"kind": "pivot", "price": 84200}, {"kind": "support", "price": -1}]}`))
		require.NoError(t, err)
		require.Equal(t, []Level{{Kind: LevelSupport, Price: 84000}, {Kind: LevelResistance, Price: 84500}}, d.Levels,
			"the wrong and the repeated ones dropped, the decision kept")

		d, err = ParseDecision([]byte(`{"action": "WAIT", "confidence": 0.5, "entry": null, "stop_loss": null,
			"take_profit": null, "reason": "ok"}`))
		require.NoError(t, err, "missing: none")
		require.Nil(t, d.Levels)

		var many strings.Builder
		for i := range 12 {
			fmt.Fprintf(&many, `{"kind": "support", "price": %d},`, 100+i)
		}
		d, err = ParseDecision([]byte(`{"action": "WAIT", "confidence": 0.5, "entry": null, "stop_loss": null,
			"take_profit": null, "reason": "ok", "levels": [` + strings.TrimSuffix(many.String(), ",") + `]}`))
		require.NoError(t, err)
		require.Len(t, d.Levels, maxLevels)
	})

	t.Run("sentiment", func(t *testing.T) {
		wait := func(sentiment string) string {
			return `{"action": "WAIT", "confidence": 0.5, "entry": null, "stop_loss": null, "take_profit": null,
				"reason": "ok"` + sentiment + `}`
		}
		d, err := ParseDecision([]byte(wait(`, "sentiment": 72.5`)))
		require.NoError(t, err)
		require.Equal(t, ptr(72.5), d.Sentiment)
		for _, edge := range []float64{minSentiment, maxSentiment} {
			d, err = ParseDecision([]byte(wait(fmt.Sprintf(`, "sentiment": %v`, edge))))
			require.NoError(t, err)
			require.Equal(t, ptr(edge), d.Sentiment, "its ends included")
		}

		for _, absent := range []string{"", `, "sentiment": null`} {
			d, err = ParseDecision([]byte(wait(absent)))
			require.NoError(t, err, "missing or null: none, the decision kept")
			require.Nil(t, d.Sentiment)
			require.Empty(t, d.Notes)
		}

		for _, out := range []string{"-1", "100.5", "0.72e3"} {
			d, err = ParseDecision([]byte(wait(`, "sentiment": ` + out)))
			require.NoError(t, err, "out of its range: left out, the decision kept")
			require.Nil(t, d.Sentiment, out)
			require.Len(t, d.Notes, 1)
			require.Contains(t, d.Notes[0].Text, "sentiment left out")
		}

		_, err = ParseDecision([]byte(wait(`, "sentiment": "bullish"`)))
		require.ErrorIs(t, err, ErrInvalidResponse, "not a number: the answer isn't the schema")
	})

	t.Run("every action", func(t *testing.T) {
		for _, action := range Actions {
			d, err := ParseDecision([]byte(`{"action": "` + string(action) + `", "confidence": 1, "entry": null,
				"stop_loss": 3850.2, "take_profit": 3864, "reason": "ok"}`))
			require.NoError(t, err, action)
			require.Equal(t, action, d.Action)
		}
	})

	valid := map[string]string{
		"reason": `"ok"`, "action": `"ENTER_LONG"`, "confidence": `0.8`, "entry": `100`, "stop_loss": `98`,
		"take_profit": `104`,
	}
	// with returns the JSON of the valid decision with the field name set to
	// value, removed when empty
	with := func(name, value string) string {
		fields := make([]string, 0, len(valid)+1)
		for _, field := range decisionFields {
			v := valid[field]
			if field == name {
				v = value
			}
			if v != "" {
				fields = append(fields, `"`+field+`": `+v)
			}
		}
		if _, ok := valid[name]; !ok && name != "" {
			fields = append(fields, `"`+name+`": `+value)
		}
		return "{" + strings.Join(fields, ", ") + "}"
	}

	tests := []struct {
		name string
		data string
		want string // in the error
	}{
		{"not JSON", `the market looks bullish`, "not a JSON object"},
		{"JSON cut short", `{"action": "WAIT", "confidence": 0.5`, "not a JSON object"},
		{"a JSON array", `[]`, "not a JSON object"},
		{"JSON null", `null`, "missing field"},
		{"text after the JSON", with("", "") + ` I hope it helps`, "not a JSON object"},
		{"empty", ``, "not a JSON object"},
		{"unknown field", with("size", "1"), `unknown field "size"`},
		{"unknown action", with("action", `"BUY"`), `unknown action "BUY"`},
		{"action in lowercase", with("action", `"enter_long"`), "unknown action"},
		{"empty action", with("action", `""`), "unknown action"},
		{"null action", with("action", `null`), `"action" is null`},
		{"action not a string", with("action", `1`), "cannot unmarshal"},
		{"confidence above 1", with("confidence", `1.5`), "confidence 1.5 out of [0, 1]"},
		{"negative confidence", with("confidence", `-0.1`), "out of [0, 1]"},
		{"confidence in percent", with("confidence", `82`), "out of [0, 1]"},
		{"confidence not a number", with("confidence", `"high"`), "cannot unmarshal"},
		{"null confidence", with("confidence", `null`), `"confidence" is null`},
		{"zero stop loss", with("stop_loss", `0`), "stop_loss 0 is not a positive price"},
		{"negative stop loss", with("stop_loss", `-98`), "stop_loss -98 is not a positive price"},
		{"stop loss not a number", with("stop_loss", `"98"`), "cannot unmarshal"},
		{"zero take profit", with("take_profit", `0`), "take_profit 0 is not a positive price"},
		{"negative take profit", with("take_profit", `-104`), "take_profit -104 is not a positive price"},
		{"negative entry", with("entry", `-1`), "entry -1 is not a positive price"},
		{"empty reason", with("reason", `"  "`), "empty reason"},
		{"null reason", with("reason", `null`), `"reason" is null`},
	}
	for _, field := range decisionFields {
		if field == "levels" || field == "sentiment" {
			continue // optional: see "levels" and "sentiment"
		}
		tests = append(tests, struct{ name, data, want string }{"missing " + field, with(field, ""),
			`missing field "` + field + `"`})
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDecision([]byte(tc.data))
			require.ErrorIs(t, err, ErrInvalidResponse)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestDecision_Validate(t *testing.T) {
	enter := func(action Action, entry, stop, profit float64) Decision {
		return Decision{Action: action, Confidence: 0.8, Entry: ptr(entry), StopLoss: ptr(stop),
			TakeProfit: ptr(profit), Reason: "ok"}
	}
	levels := func(action Action, stop, profit *float64) Decision {
		return Decision{Action: action, Confidence: 0.8, StopLoss: stop, TakeProfit: profit, Reason: "ok"}
	}
	unlimited := func(in Input) Input {
		in.Rules.MaxEntryDistance, in.Rules.MaxLevelDistance = number(math.NaN()), number(math.NaN())
		return in
	}
	widening := func(in Input) Input {
		in.Rules.StopCanWiden = true
		return in
	}

	flat := testInput(nil, flatActions...)
	long := testInput(longPosition(), heldActions...)
	short := testInput(shortPosition(), heldActions...)
	adopted := testInput(&Position{Side: model.SideTypeBuy, Size: 1, Entry: number(math.NaN()),
		StopLoss: number(math.NaN()), TakeProfit: number(math.NaN()), PnL: number(math.NaN())},
		ActionHold, ActionExit)

	tests := []struct {
		name     string
		input    Input
		decision Decision
		want     string // in the error, none when valid
	}{
		{"wait", flat, Decision{Action: ActionWait, Confidence: 0.4, Reason: "ok"}, ""},
		{"wait with prices, unused", flat, enter(ActionWait, 1, 2, 3), ""},
		{"long", flat, enter(ActionEnterLong, 100.2, 98, 104), ""},
		{"long without limits", unlimited(flat), enter(ActionEnterLong, 150, 1, 1000), ""},
		{"short", flat, enter(ActionEnterShort, 99.5, 102, 96), ""},
		{"long without entry", flat, levels(ActionEnterLong, ptr(98), ptr(104)), "needs an entry"},
		{"long without stop loss", flat, Decision{Action: ActionEnterLong, Confidence: 0.8, Entry: ptr(100),
			TakeProfit: ptr(104), Reason: "ok"}, "needs an entry, a stop loss and a take profit"},
		{"long without take profit", flat, Decision{Action: ActionEnterLong, Confidence: 0.8, Entry: ptr(100),
			StopLoss: ptr(98), Reason: "ok"}, "needs an entry, a stop loss and a take profit"},
		{"long entry away from the price", flat, enter(ActionEnterLong, 101.5, 98, 104), "the entry 101.5 is 1.5"},
		{"long stop loss above the price", flat, enter(ActionEnterLong, 100, 101, 104),
			"the stop loss 101 of a LONG must be below 100"},
		{"long stop loss at the price", flat, enter(ActionEnterLong, 100, 100, 104), "must be below 100"},
		{"long stop loss above the entry", flat, enter(ActionEnterLong, 99.5, 99.7, 104),
			"the stop loss 99.7 of a LONG must be below 99.5"},
		{"long take profit below the price", flat, enter(ActionEnterLong, 100, 98, 99),
			"the take profit 99 of a LONG must be above 100"},
		{"long take profit below the entry", flat, enter(ActionEnterLong, 100.5, 98, 100.3),
			"the take profit 100.3 of a LONG must be above 100.5"},
		{"long stop loss too far", flat, enter(ActionEnterLong, 100, 85, 104), "the level 85 is 15 from the price"},
		{"long take profit too far", flat, enter(ActionEnterLong, 100, 98, 111), "the level 111 is 11"},
		{"short stop loss below the price", flat, enter(ActionEnterShort, 100, 99, 96),
			"the stop loss 99 of a SHORT must be above 100"},
		{"short take profit above the price", flat, enter(ActionEnterShort, 100, 102, 101),
			"the take profit 101 of a SHORT must be below 100"},
		{"short levels swapped", flat, enter(ActionEnterShort, 100, 96, 102), "must be above"},
		{"short when only longs open", testInput(nil, ActionWait, ActionEnterLong),
			enter(ActionEnterShort, 100, 102, 96), "ENTER_SHORT is not an allowed action"},
		{"hold when flat", flat, levels(ActionHold, nil, nil), "HOLD is not an allowed action"},
		{"exit when flat", flat, levels(ActionExit, nil, nil), "EXIT is not an allowed action"},
		{"move when flat", flat, levels(ActionModifySL, ptr(98), nil), "MODIFY_SL is not an allowed action"},
		{"entry with a position", long, enter(ActionEnterLong, 100, 98, 104), "ENTER_LONG is not an allowed"},
		{"wait with a position", long, levels(ActionWait, nil, nil), "WAIT is not an allowed action"},

		{"hold", long, levels(ActionHold, nil, nil), ""},
		{"hold repeating the levels", long, levels(ActionHold, ptr(97), ptr(104)), ""},
		{"hold with a stop loss above the price", long, levels(ActionHold, ptr(101), nil),
			"the stop loss 101 of a LONG must be below 100"},
		{"hold with a take profit below the price", long, levels(ActionHold, nil, ptr(99)),
			"the take profit 99 of a LONG must be above 100"},
		{"hold a short", short, levels(ActionHold, ptr(103), ptr(96)), ""},
		{"exit", long, levels(ActionExit, nil, nil), ""},
		{"exit a position followed without legs", adopted, levels(ActionExit, nil, nil), ""},

		{"tighten the stop loss of a long", long, levels(ActionModifySL, ptr(99), nil), ""},
		{"stop loss of a long above the price", long, levels(ActionModifySL, ptr(100.5), nil),
			"the stop loss 100.5 of a LONG must be below 100"},
		{"widen the stop loss of a long", long, levels(ActionModifySL, ptr(96), nil),
			"moving the stop loss from 97 to 96, away from the price, adds risk"},
		{"widen the stop loss of a long, allowed", widening(long), levels(ActionModifySL, ptr(96), nil), ""},
		{"stop loss too far", widening(long), levels(ActionModifySL, ptr(89), nil), "the level 89 is 11"},
		{"move the stop loss without one", long, levels(ActionModifySL, nil, ptr(105)), "MODIFY_SL needs a stop loss"},
		{"tighten the stop loss of a short", short, levels(ActionModifySL, ptr(101.5), nil), ""},
		{"widen the stop loss of a short", short, levels(ActionModifySL, ptr(104), nil),
			"moving the stop loss from 103 to 104"},
		{"stop loss of a short below the price", short, levels(ActionModifySL, ptr(99), nil),
			"the stop loss 99 of a SHORT must be above 100"},
		{"move the stop loss of a position without legs", adopted, levels(ActionModifySL, ptr(99), nil),
			"MODIFY_SL is not an allowed action"},

		{"move the take profit of a long", long, levels(ActionModifyTP, nil, ptr(106)), ""},
		{"take profit of a long below the price", long, levels(ActionModifyTP, nil, ptr(99)),
			"the take profit 99 of a LONG must be above 100"},
		{"take profit too far", long, levels(ActionModifyTP, nil, ptr(120)), "the level 120 is 20"},
		{"move the take profit without one", long, levels(ActionModifyTP, ptr(99), nil),
			"MODIFY_TP needs a take profit"},
		{"move the take profit of a short", short, levels(ActionModifyTP, nil, ptr(95)), ""},
		{"take profit of a short above the price", short, levels(ActionModifyTP, nil, ptr(100.5)),
			"the take profit 100.5 of a SHORT must be below 100"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.decision.Validate(tc.input)
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrInvalidDecision)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestDecisionSchema(t *testing.T) {
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
		Additional bool                       `json:"additionalProperties"`
	}
	require.NoError(t, json.Unmarshal(decisionSchema, &schema))

	// strict: every property required, none other, as ParseDecision reads it
	require.Equal(t, "object", schema.Type)
	require.False(t, schema.Additional)
	require.Equal(t, decisionFields, schema.Required)
	require.Len(t, schema.Properties, len(decisionFields))
	for _, field := range decisionFields {
		require.Contains(t, schema.Properties, field)
	}

	var action struct {
		Enum []Action `json:"enum"`
	}
	require.NoError(t, json.Unmarshal(schema.Properties["action"], &action))
	require.Equal(t, Actions, action.Enum)

	// the reason comes first, so that the model writes the evidence first
	require.Less(t, strings.Index(string(decisionSchema), `"reason"`), strings.Index(string(decisionSchema), `"action"`))
}

func TestInput_JSON(t *testing.T) {
	in := testInput(&Position{Side: model.SideTypeSell, Size: 2, Entry: 101.25, StopLoss: 103,
		TakeProfit: number(math.NaN()), PnL: 2.5, Candles: 4, InitialStop: 103, RMultiple: number(math.NaN())},
		heldActions...)
	in.Candles = Candles{{Open: 100, High: 101.5, Low: 99.25, Close: 100.123456789, Volume: 1234567890}}
	in.Indicators = map[string][]number{"rsi_14": {55.123456789, number(math.NaN())}}

	data, err := json.Marshal(in)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, "XAUUSD", got["symbol"])
	require.Nil(t, got["bid"], "unknown numbers are null")
	require.Equal(t, map[string]any{"side": "SHORT", "size": 2.0, "entry_price": 101.25, "stop_loss": 103.0,
		"take_profit": nil, "unrealized_pnl": 2.5, "candles_held": 4.0, "initial_stop_loss": 103.0,
		"r_multiple": nil}, got["position"])
	require.Equal(t, map[string]any{
		"columns": []any{"time", "open", "high", "low", "close", "volume"},
		"rows":    []any{[]any{"0001-01-01T00:00:00Z", 100.0, 101.5, 99.25, 100.12346, 1.2345679e+09}},
	}, got["candles"], "the candles are rows, their numbers of 8 significant digits")
	require.Equal(t, []any{55.123457, nil}, got["indicators"].(map[string]any)["rsi_14"])
	require.Equal(t, []any{"HOLD", "EXIT", "MODIFY_SL", "MODIFY_TP"},
		got["rules"].(map[string]any)["allowed_actions"])
}
