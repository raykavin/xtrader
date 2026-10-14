package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// updateGolden rewrites the golden files of the tests: go test -run
// TestPrompts_Interpretation ./agents/llm/ -update.
var updateGolden = flag.Bool("update", false, "rewrite the golden files")

// promptReading is how the agent reads a prompt, see TestPrompts_Interpretation.
type promptReading struct {
	Indicators    []string `json:"indicators"` // null without the line: all the groups
	TakeProfit    string   `json:"take_profit"`
	DailyGain     float64  `json:"daily_gain"`
	DailyLoss     float64  `json:"daily_loss"`
	DailyCurrency string   `json:"daily_currency"`
	Refused       string   `json:"refused"`      // why the prompt is refused, empty when taken
	HeaderLines   int      `json:"header_lines"` // the lines not sent to the model
	TextSHA256    string   `json:"text_sha256"`  // of the strategy sent to the model

	// the pending orders, the drawings and the parts of the context the
	// header asks for, absent without them
	PendingOrders []string `json:"pending_orders,omitempty"`
	Drawings      bool     `json:"drawings,omitempty"`
	Context       []string `json:"context,omitempty"`
}

// readPrompt returns how the agent reads strategy.
func readPrompt(strategy string) promptReading {
	var reading promptReading
	reading.Indicators, _ = StrategyIndicators(strategy)
	reading.TakeProfit = "required"
	switch {
	case StrategyWithoutTakeProfit(strategy):
		reading.TakeProfit = "none"
	case StrategyTakeProfitOptional(strategy):
		reading.TakeProfit = "optional"
	}
	gain, loss, currency, err := StrategyDailyLimits(strategy)
	if err == nil {
		err = CheckPrompt(strategy)
	}
	if err == nil {
		err = checkHeader(strategy)
	}
	reading.PendingOrders, _ = StrategyPendingOrders(strategy)
	reading.Drawings, _ = StrategyDrawings(strategy)
	reading.Context, _ = StrategyContext(strategy)
	if err != nil {
		reading.Refused = err.Error()
	}
	reading.DailyGain, reading.DailyLoss, reading.DailyCurrency = gain, loss, currency
	_, reading.HeaderLines = promptLines(strategy)
	sum := sha256.Sum256([]byte(stripComments(strategy)))
	reading.TextSHA256 = hex.EncodeToString(sum[:])
	return reading
}

// B21: how the agent reads every prompt seeded and the default one, its
// header (the indicators, the take profit, the daily limits) and the text
// sent to the model, is the one of testdata/prompts.golden.json: a change of
// the reading of the header shows here, prompt by prompt.
func TestPrompts_Interpretation(t *testing.T) {
	prompts := seededPrompts(t)
	embedded, err := ReadStrategy("prompts/default.txt")
	require.NoError(t, err)
	prompts["prompts/default.txt"] = embedded

	got := make(map[string]promptReading, len(prompts))
	for name, strategy := range prompts {
		got[name] = readPrompt(strategy)
	}
	data, err := json.MarshalIndent(got, "", "  ")
	require.NoError(t, err)
	data = append(data, '\n')

	path := filepath.Join("testdata", "prompts.golden.json")
	if *updateGolden {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, data, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "go test -run TestPrompts_Interpretation -update writes it")
	require.JSONEq(t, string(want), string(data))
	require.Len(t, got, 45, "the 8 prompts seeded, the 36 of the strategies of the code and the default one")
}
