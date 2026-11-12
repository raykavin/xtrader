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
// for the kind momentum (agentapi.DefaultStrategy), or the agent launched by the
// controller would refuse to trade (agentapi.Config.CheckMT5Book) and the
// store would not keep it apart from the other agents.
func TestAgent_DefaultStrategyOfItsKind(t *testing.T) {
	a := testAgent()
	require.NotEmpty(t, a.DefaultStrategy())
	require.Equal(t, agentapi.DefaultStrategy("momentum"), a.DefaultStrategy())
	require.Equal(t, "nb:"+a.DefaultStrategy(), agentapi.NewMT5Book("momentum", map[string]string{}, nil).Comment)
}

// testAgent is the agent of the tests, created once: its flags are defined on
// the command line.
var testAgent = sync.OnceValue(agent)

// trailing creates the strategy name of a with -trailing-atr at atr.
func trailing(a runner.Agent, name string, atr float64) error {
	for _, s := range a.Strategies {
		if s.Name == name {
			_, err := s.New(runner.Inputs{Set: map[string]bool{"trailing-atr": true}, TrailingATR: atr,
				Direction: strategies.RKDirectionBoth, MinValue: 10, Fraction: 1, Leverage: 1})
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
// taken and applied by every strategy of the agent (runner.TrailingSignals),
// declared now.
func TestAgent_TrailingATR(t *testing.T) {
	a := testAgent()
	names := make([]string, 0, len(a.Strategies))
	for _, s := range a.Strategies {
		names = append(names, s.Name)
	}
	requireTrailingATR(t, a, names, "")
	requireOwnFlags(t, a)
}

// The seeds of the database create an agent of each strategy of the agent, and
// each agent of the kind momentum they create trades one of them.
func TestAgent_TheStrategiesOfTheSeeds(t *testing.T) {
	seeded, err := seedtest.Strategies(filepath.Join("..", ".."), "momentum")
	require.NoError(t, err)
	var known []string
	for _, s := range testAgent().Strategies {
		known = append(known, s.Name)
	}
	require.ElementsMatch(t, known, seeded)
}

// The levels of rsi2 set by their flags, its own only: the ones of its
// defaults (the book's 10 and 90) unless set, each reaching the strategy (a
// value out of its bounds refused by it).
func TestAgent_RSI2Levels(t *testing.T) {
	a := testAgent()
	for _, name := range rsi2Inputs {
		require.NoError(t, a.CheckInputs("rsi2", map[string]bool{name: true}), name)
		require.ErrorContains(t, a.CheckInputs("adxema", map[string]bool{name: true}),
			"-"+name+" is an input of -strategy rsi2")
	}
	for _, name := range []string{"stop-atr", "take-profit-atr", "max-bars", "min-atr-pct", "trailing-atr",
		"timeframe", "lot", "max-risk-pct"} {
		require.NoError(t, a.CheckInputs("rsi2", map[string]bool{name: true}), name)
	}
	i := slices.IndexFunc(a.Strategies, func(s runner.Strategy) bool { return s.Name == "rsi2" })
	require.Greater(t, i, 0, "appended, never the default")
	in := runner.Inputs{Set: map[string]bool{}, Timeframe: "1h", Direction: strategies.RKDirectionBoth,
		MinValue: 10, Fraction: 1, Leverage: 1}
	trader, err := a.Strategies[i].New(in)
	require.NoError(t, err, "the defaults")
	require.Equal(t, "1d", trader.Timeframe(), "the daily candles of the book unless -timeframe")
	// the warmup -print-warmup tells the controller: the SMA 200 and a margin,
	// the seed of the ATR inside it, held by a warm-up of a year of daily
	// candles of gold (about 250)
	require.Equal(t, 210, trader.WarmupPeriod())
	for name, value := range map[string]string{
		"rsi2-oversold":   "95", // above the overbought level
		"rsi2-overbought": "5",  // under the oversold level
	} {
		before := flag.Lookup(name).Value.String()
		require.NoError(t, flag.Set(name, value))
		_, err := a.Strategies[i].New(in)
		require.ErrorContains(t, err, "RSI levels", name)
		require.NoError(t, flag.Set(name, before))
	}
	require.NoError(t, flag.Set("rsi2-oversold", "5"))
	require.NoError(t, flag.Set("rsi2-overbought", "95"))
	_, err = a.Strategies[i].New(in)
	require.NoError(t, err, "the deeper levels taken")
	require.NoError(t, flag.Set("rsi2-oversold", "10"))
	require.NoError(t, flag.Set("rsi2-overbought", "90"))
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
