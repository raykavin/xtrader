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
// for the kind intraday (agentapi.DefaultStrategy), or the agent launched by the
// controller would refuse to trade (agentapi.Config.CheckMT5Book) and the
// store would not keep it apart from the other agents.
func TestAgent_DefaultStrategyOfItsKind(t *testing.T) {
	a := testAgent()
	require.NotEmpty(t, a.DefaultStrategy())
	require.Equal(t, agentapi.DefaultStrategy("intraday"), a.DefaultStrategy())
	require.Equal(t, "nb:"+a.DefaultStrategy(), agentapi.NewMT5Book("intraday", map[string]string{}, nil).Comment)
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

// M12a: no strategy of the agent trails its stop loss (BracketConfig has no
// trailing): -trailing-atr is refused with every one, rather than accepted and
// ignored as before.
func TestAgent_TrailingATR(t *testing.T) {
	a := testAgent()
	requireTrailingATR(t, a, nil, "-trailing-atr is an input of no strategy of the agent")
	for _, s := range a.Strategies {
		require.NoError(t, a.CheckInputs(s.Name, map[string]bool{"stop-atr": true, "lot": true}), s.Name)
	}
	require.ErrorContains(t, a.CheckInputs("vwap", map[string]bool{"max-trades": true}),
		"-max-trades is an input of -strategy asialondon and orb")
	requireOwnFlags(t, a)
}

// The seeds of the database create an agent of each strategy of the agent, and
// each agent of the kind intraday they create trades one of them.
func TestAgent_TheStrategiesOfTheSeeds(t *testing.T) {
	seeded, err := seedtest.Strategies(filepath.Join("..", ".."), "intraday")
	require.NoError(t, err)
	var known []string
	for _, s := range testAgent().Strategies {
		known = append(known, s.Name)
	}
	require.ElementsMatch(t, known, seeded)
}
