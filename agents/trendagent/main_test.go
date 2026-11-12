package main

import (
	"flag"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/internal/runner"
	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/internal/seedtest"
)

// The default strategy of the agent names the comment of its orders on MT5,
// and so its magic number: it is the one the controller and the store compute
// for the kind trend (agentapi.DefaultStrategy), or the agent launched by the
// controller would refuse to trade (agentapi.Config.CheckMT5Book) and the
// store would not keep it apart from the other agents.
func TestAgent_DefaultStrategyOfItsKind(t *testing.T) {
	a := testAgent()
	require.NotEmpty(t, a.DefaultStrategy())
	require.Equal(t, agentapi.DefaultStrategy("trend"), a.DefaultStrategy())
	require.Equal(t, "nb:"+a.DefaultStrategy(), agentapi.NewMT5Book("trend", map[string]string{}, nil).Comment)
}

// testAgent is the agent of the tests, created once: its flags are defined on
// the command line.
var testAgent = sync.OnceValue(agent)

// trailing creates the strategy name of a with -trailing-atr at atr, on the
// timeframe of Run by default.
func trailing(a runner.Agent, name string, atr float64) error {
	for _, s := range a.Strategies {
		if s.Name == name {
			_, err := s.New(runner.Inputs{Set: map[string]bool{"trailing-atr": true}, TrailingATR: atr,
				Timeframe: "1h", Direction: strategies.RKDirectionBoth, MinValue: 10, Fraction: 1, Leverage: 1})
			return err
		}
	}
	return nil
}

// requireOwnFlags checks that every flag the agent defines is an input of one
// of its strategies: none is accepted and ignored.
func requireOwnFlags(t *testing.T, a runner.Agent) {
	t.Helper()
	flag.VisitAll(func(f *flag.Flag) {
		if strings.HasPrefix(f.Name, "test.") {
			return
		}
		require.True(t, slices.ContainsFunc(a.Strategies, func(s runner.Strategy) bool {
			return slices.Contains(s.Inputs, f.Name)
		}), "-%s taken by no strategy", f.Name)
	})
}

// requireTrailingATR checks -trailing-atr with each strategy of a: taken and
// applied (a negative one refused by the strategy) by the ones of takes,
// refused with the others with refused, and not applied by them.
func requireTrailingATR(t *testing.T, a runner.Agent, takes []string, refused string) {
	t.Helper()
	set := map[string]bool{"trailing-atr": true}
	for _, s := range a.Strategies {
		if slices.Contains(takes, s.Name) {
			require.Contains(t, s.Inputs, "trailing-atr", s.Name)
			require.NoError(t, a.CheckInputs(s.Name, set), s.Name)
			require.NoError(t, trailing(a, s.Name, 3), s.Name)
			require.ErrorContains(t, trailing(a, s.Name, -1), "trailing", "%s: applied", s.Name)
			continue
		}
		require.NotContains(t, s.Inputs, "trailing-atr", s.Name)
		require.ErrorContains(t, a.CheckInputs(s.Name, set), refused, s.Name)
		require.NoError(t, trailing(a, s.Name, -1), "%s: not applied", s.Name)
	}
}

// M12a: -trailing-atr as before the flags no strategy takes were refused:
// taken and applied by trendconfluence and matrend, and by fibonacci and
// consensus since, refused with emapullback and the signal strategies, none of
// which trails its stop loss by default.
func TestAgent_TrailingATR(t *testing.T) {
	a := testAgent()
	requireTrailingATR(t, a, []string{"trendconfluence", "matrend", "fibonacci", "consensus"},
		"-trailing-atr is an input of -strategy consensus and fibonacci and matrend and trendconfluence")
	require.ErrorContains(t, a.CheckInputs("matrend", map[string]bool{"take-profit-atr": true}),
		"-take-profit-atr is an input of -strategy")
	requireOwnFlags(t, a)
}

// fibonacci takes its own flags, refused with the other strategies, and the
// flags of the exits it uses, but not the stop loss and take profit in ATR,
// which the levels of the impulse replace.
func TestAgent_FibonacciInputs(t *testing.T) {
	a := testAgent()
	for _, name := range fibInputs {
		require.NoError(t, a.CheckInputs("fibonacci", map[string]bool{name: true}), name)
		require.ErrorContains(t, a.CheckInputs("matrend", map[string]bool{name: true}),
			"-"+name+" is an input of -strategy fibonacci")
	}
	for _, name := range []string{"max-bars", "min-atr-pct", "trailing-atr", "timeframe", "lot"} {
		require.NoError(t, a.CheckInputs("fibonacci", map[string]bool{name: true}), name)
	}
	for _, name := range []string{"stop-atr", "take-profit-atr", "stop-buffer-atr", "fast-ema"} {
		require.ErrorContains(t, a.CheckInputs("fibonacci", map[string]bool{name: true}), "-"+name+" is an input of",
			name)
	}
}

// The strategy fibonacci is created from its defaults, on its own timeframe
// unless -timeframe is set.
func TestAgent_Fibonacci(t *testing.T) {
	a := testAgent()
	i := slices.IndexFunc(a.Strategies, func(s runner.Strategy) bool { return s.Name == "fibonacci" })
	require.GreaterOrEqual(t, i, 0)
	in := runner.Inputs{Set: map[string]bool{}, Timeframe: "1h", Direction: strategies.RKDirectionBoth,
		MinValue: 10, Fraction: 1, Leverage: 1}
	trader, err := a.Strategies[i].New(in)
	require.NoError(t, err)
	require.Equal(t, "15m", trader.Timeframe())

	in.Set["timeframe"] = true
	trader, err = a.Strategies[i].New(in)
	require.NoError(t, err)
	require.Equal(t, "1h", trader.Timeframe())
}

// The seeds of the database create an agent of each strategy of the agent, and
// each agent of the kind trend they create trades one of them.
func TestAgent_TheStrategiesOfTheSeeds(t *testing.T) {
	seeded, err := seedtest.Strategies(filepath.Join("..", ".."), "trend")
	require.NoError(t, err)
	var known []string
	for _, s := range testAgent().Strategies {
		known = append(known, s.Name)
	}
	require.ElementsMatch(t, known, seeded)
}

// The rules, the quorum and the window of consensus set by their flags, its
// own only: the ones of its defaults unless set, each reaching the strategy (a
// value out of its bounds refused by it). It takes the exits of the bracket
// and -trailing-atr, as the signal strategies trailing their stop loss.
func TestAgent_ConsensusInputs(t *testing.T) {
	a := testAgent()
	for _, name := range consensusInputs {
		require.NoError(t, a.CheckInputs("consensus", map[string]bool{name: true}), name)
		require.ErrorContains(t, a.CheckInputs("ema200", map[string]bool{name: true}),
			"-"+name+" is an input of -strategy consensus")
	}
	for _, name := range []string{"stop-atr", "take-profit-atr", "max-bars", "min-atr-pct", "trailing-atr",
		"timeframe", "lot", "max-risk-pct"} {
		require.NoError(t, a.CheckInputs("consensus", map[string]bool{name: true}), name)
	}
	for _, name := range []string{"fib-swing", "max-distance", "fast-ema", "broker-stop-atr"} {
		require.ErrorContains(t, a.CheckInputs("consensus", map[string]bool{name: true}), "-"+name+" is an input of",
			name)
	}
	i := slices.IndexFunc(a.Strategies, func(s runner.Strategy) bool { return s.Name == "consensus" })
	require.Greater(t, i, 0, "appended, never the default")
	in := runner.Inputs{Set: map[string]bool{}, Timeframe: "1h", Direction: strategies.RKDirectionBoth,
		MinValue: 10, Fraction: 1, Leverage: 1}
	trader, err := a.Strategies[i].New(in)
	require.NoError(t, err, "the defaults")
	require.Equal(t, "15m", trader.Timeframe(), "the timeframe of the signal strategies unless -timeframe")
	for name, c := range map[string]struct{ value, refused string }{
		"consensus-rules":  {"ema200", "at least 2 rules"},
		"consensus-quorum": {"6", "the quorum must be between 1 and the number of rules"},
		"consensus-window": {"0", "the window must be at least 1 candle"},
	} {
		before := flag.Lookup(name).Value.String()
		require.NoError(t, flag.Set(name, c.value))
		_, err := a.Strategies[i].New(in)
		require.ErrorContains(t, err, c.refused, name)
		require.NoError(t, flag.Set(name, before))
	}
	require.NoError(t, flag.Set("consensus-rules", "ema200,supertrend,nosuchrule"))
	_, err = a.Strategies[i].New(in)
	require.ErrorContains(t, err, `invalid rule "nosuchrule"`)
	require.NoError(t, flag.Set("consensus-rules", "ema200,supertrend,donchian"))
	require.NoError(t, flag.Set("consensus-quorum", "2"))
	require.NoError(t, flag.Set("consensus-window", "1"))
	_, err = a.Strategies[i].New(in)
	require.NoError(t, err, "other rules, quorum and window taken")
	require.NoError(t, flag.Set("consensus-rules", "ema200,supertrend,macdtrend,adxema,donchian"))
	require.NoError(t, flag.Set("consensus-quorum", "3"))
	require.NoError(t, flag.Set("consensus-window", "3"))
}

// Every strategy of the agent takes the stop loss and the take profit in ATR
// (-stop-atr, -take-profit-atr), the ones placing levels of their own too,
// which they then replace; but pairs, whose two legs hedge each other.
func TestStrategies_TakeTheExitsInATR(t *testing.T) {
	a := agent()
	asked := map[string]bool{}
	for _, input := range runner.ATRExitInputs {
		asked[input] = true
	}
	for _, s := range a.Strategies {
		if s.Name == "pairs" {
			continue
		}
		require.NoError(t, a.CheckInputs(s.Name, asked), s.Name)
	}
}
