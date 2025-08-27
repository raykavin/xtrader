package llm

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

// seedPattern matches a prompt of the seed of the database: its name, its
// description and its content, dollar-quoted.
var seedPattern = regexp.MustCompile(`(?s)\('([a-z0-9_.-]+)', \$d\$.*?\$d\$,\n\$prompt\$(.*?)\$prompt\$\)`)

// seededPrompts returns the prompts the database is seeded with, by name,
// the strategies the agents start with.
func seededPrompts(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "sql", "seeds", "0002_prompts.sql"))
	require.NoError(t, err)
	prompts := make(map[string]string)
	for _, match := range seedPattern.FindAllStringSubmatch(string(data), -1) {
		prompts[match[1]] = match[2]
	}
	require.GreaterOrEqual(t, len(prompts), 5)
	return prompts
}

// seededPrompt returns the seeded prompt of name.
func seededPrompt(t *testing.T, name string) string {
	t.Helper()
	strategy, ok := seededPrompts(t)[name]
	require.True(t, ok, "no prompt %s seeded", name)
	return strategy
}

// promptNames returns the names of prompts, sorted.
func promptNames(prompts map[string]string) []string {
	list := make([]string, 0, len(prompts))
	for name := range prompts {
		list = append(list, name)
	}
	sort.Strings(list)
	return list
}

func TestStripComments(t *testing.T) {
	require.Equal(t, "Setup: x.\n# not a header", stripComments("# name\n  # flags\n\nSetup: x.\n# not a header\n\n"))
	require.Equal(t, "Setup: x.", stripComments("Setup: x.\r\n"))
	require.Empty(t, stripComments("# only\n# comments\n"))
}

// Every prompt seeded is one the model takes, describes itself in its header
// and sends only its strategy, after the part the code relies on.
func TestPrompts(t *testing.T) {
	prompts := seededPrompts(t)
	for _, name := range promptNames(prompts) {
		t.Run(name, func(t *testing.T) {
			strategy := prompts[name]
			require.NoError(t, CheckPrompt(strategy))
			require.True(t, strings.HasPrefix(strategy, "# "+name+":"), "its header names it")
			require.Contains(t, strategy, "# Flags:", "its header gives the flags that fit it")

			prompt := systemPrompt(strategy)
			require.True(t, strings.HasPrefix(prompt, basePrompt+"\n\nStrategy:\n"))
			sent := strings.TrimPrefix(prompt, basePrompt)
			require.NotContains(t, sent, "\n#", "the header isn't sent")
			require.Contains(t, sent, "Setup")
			require.Contains(t, sent, "Stop loss and take profit:")
			require.Contains(t, sent, "Managing the position held")
		})
	}

	embedded, err := ReadStrategy("prompts/default.txt")
	require.NoError(t, err)
	require.Equal(t, embedded, defaultStrategy, "the default is the file")
	require.Equal(t, prompts["default"], defaultStrategy, "the default seeded is the embedded one")
	require.Equal(t, systemPrompt(defaultStrategy), defaultSystemPrompt)
}

func TestReadStrategy_Errors(t *testing.T) {
	_, err := ReadStrategy(filepath.Join(t.TempDir(), "missing.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)

	empty := filepath.Join(t.TempDir(), "empty.txt")
	require.NoError(t, os.WriteFile(empty, []byte("# a header only\n\n"), 0o644))
	_, err = ReadStrategy(empty)
	require.ErrorContains(t, err, "is empty")

	binary := filepath.Join(t.TempDir(), "binary.txt")
	require.NoError(t, os.WriteFile(binary, []byte{0xff, 0xfe, 0x00}, 0o644))
	_, err = ReadStrategy(binary)
	require.ErrorContains(t, err, "isn't UTF-8")
}

func TestWithStrategy(t *testing.T) {
	strategy := seededPrompt(t, "scalp_1m")

	server, got := recording(t, openAIBody(longDecision))
	provider, err := NewOpenAI("gpt-test", testKey, WithBaseURL(server.URL), WithStrategy(strategy))
	require.NoError(t, err)
	_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
	require.NoError(t, err)
	messages := got.body["messages"].([]any)
	system := messages[0].(map[string]any)["content"].(string)
	require.Equal(t, systemPrompt(strategy), system)
	require.Contains(t, system, "scalping on 1 minute candles")
	require.NotContains(t, system, "trend_pullback: trade only", "not the default")

	server, got = recording(t, anthropicBody(longDecision))
	anthropic, err := NewAnthropic("claude-test", testKey, WithBaseURL(server.URL), WithStrategy(strategy))
	require.NoError(t, err)
	_, err = anthropic.Analyze(context.Background(), testInput(nil, flatActions...))
	require.NoError(t, err)
	require.Equal(t, systemPrompt(strategy), got.body["system"])

	_, err = NewOpenAI("gpt-test", testKey, WithStrategy("# a header only"))
	require.ErrorContains(t, err, "the strategy of the prompt is empty")
}

// The prompts declare the indicators they use, drawn and sent: all known.
func TestStrategyIndicators(t *testing.T) {
	strategy := seededPrompt(t, "support_resistance")
	groups, ok := StrategyIndicators(strategy)
	require.True(t, ok)
	require.Equal(t, []string{"atr", "volume"}, groups)

	_, ok = StrategyIndicators(defaultStrategy)
	require.False(t, ok, "the default: all of them")

	// every prompt makes a Trader, its indicators and its take profit as its
	// header declares them, within the warmup of an agent of the controller
	for name, strategy := range seededPrompts(t) {
		cfg := DefaultConfig()
		cfg.Warmup = 1000
		trader, err := New(cfg, &fakeProvider{t: t}, WithPrompt(Prompt{Ref: ui.DecisionPrompt{Name: name},
			Content: strategy}, PromptOverrides{}))
		require.NoError(t, err, name)
		require.Equal(t, 1000, trader.WarmupPeriod(), name)
	}

	groups, ok = StrategyIndicators("# x: y\n# Indicators: ATR , bb\n\nSetup: z.\n# Indicators: rsi")
	require.True(t, ok)
	require.Equal(t, []string{"atr", "bb"}, groups, "the header only, in lower case")

	groups, ok = StrategyIndicators("# x: y\n# Indicators: EMA  20 50, range+09:30+09:45+12:00+America/New_York " +
		"(a note)\n\nSetup: z.")
	require.True(t, ok)
	require.Equal(t, []string{"ema 20 50", "range 09:30 09:45 12:00 America/New_York"}, groups,
		"the kind in lower case, the time zone as it is")
}

func TestStrategyTakeProfitOptional(t *testing.T) {
	require.True(t, StrategyTakeProfitOptional("# x: y\n# Take profit: Optional\n\nSetup: z."))
	require.False(t, StrategyTakeProfitOptional("# x: y\n# Take profit: required\n\nSetup: z."))
	require.False(t, StrategyTakeProfitOptional("# x: y\n\nSetup: z.\n# Take profit: optional"), "the header only")
	require.False(t, StrategyTakeProfitOptional(defaultStrategy))
}

// indicatorName matches the names of the indicators of the context in a
// prompt, eg: ema_20, psar_side_0.02_0.2, vwap, range_high.
var indicatorName = regexp.MustCompile(`\b(?:(?:ema|sma|rsi|atr|adx|plus_di|minus_di|macd|bb|volume_sma|supertrend|` +
	`psar|ichimoku|stoch|donchian|keltner|chandelier|roc|range)_[a-z0-9_.]*[a-z0-9]|vwap)\b`)

// Each indicator a prompt names is one it declares, computed and sent to the
// model once the warmup is done, but the range of a session, sent only while
// it trades: the model is never told to read what the context lacks.
func TestPrompts_IndicatorsSent(t *testing.T) {
	prompts := seededPrompts(t)
	for _, name := range promptNames(prompts) {
		t.Run(name, func(t *testing.T) {
			strategy := prompts[name]
			declared, ok := StrategyIndicators(strategy)
			if !ok {
				return // all the groups
			}
			r := newTraderRunner(t, func(c *Config) {
				c.Indicators, c.History = declared, 6
				c.TakeProfitOptional = StrategyTakeProfitOptional(strategy)
				c.NoTakeProfit = StrategyWithoutTakeProfit(strategy)
			}, 0)
			r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
			sent := r.provider.last().Indicators
			for name, values := range sent {
				if !strings.HasPrefix(name, "range_") {
					require.True(t, known(float64(values[len(values)-1])), "%s computed", name)
				}
			}
			body := stripComments(strategy)
			for _, at := range indicatorName.FindAllStringIndex(body, -1) {
				if at[0] > 0 && body[at[0]-1] == '.' {
					continue // a field of the context, eg: summary.atr_pct
				}
				require.Contains(t, sent, body[at[0]:at[1]], "named by the prompt, not sent")
			}
		})
	}
}
