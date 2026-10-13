package main

import (
	"flag"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/internal/runner"
	"github.com/raykavin/xtrader/agents/strategies"
)

// The default strategy of the agent names the comment of its orders on MT5,
// and so its magic number: it is the one the controller and the store compute
// for the kind breakout (agentapi.DefaultStrategy), or the agent launched by the
// controller would refuse to trade (agentapi.Config.CheckMT5Book) and the
// store would not keep it apart from the other agents.
func TestAgent_DefaultStrategyOfItsKind(t *testing.T) {
	a := testAgent()
	require.NotEmpty(t, a.DefaultStrategy())
	require.Equal(t, agentapi.DefaultStrategy("breakout"), a.DefaultStrategy())
	require.Equal(t, "nb:"+a.DefaultStrategy(), agentapi.NewMT5Book("breakout", map[string]string{}, nil).Comment)
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
// taken and applied by donchianatr, which trails its stop loss by default,
// refused with atrbreakout and the other signal strategies.
func TestAgent_TrailingATR(t *testing.T) {
	a := testAgent()
	requireTrailingATR(t, a, []string{"donchianatr"}, "-trailing-atr is an input of -strategy donchianatr")
	requireOwnFlags(t, a)
}

// fimathe takes its own flags, refused with the other strategies, and
// -max-bars, but not the stop loss and the take profit in ATR nor the
// trailing in ATR: its stop loss and its take profit are the levels of its
// reference channel, its stop loss moved by its subcycle.
func TestAgent_FimatheInputs(t *testing.T) {
	a := testAgent()
	for _, name := range fimatheInputs {
		require.NoError(t, a.CheckInputs("fimathe", map[string]bool{name: true}), name)
		require.ErrorContains(t, a.CheckInputs("atrbreakout", map[string]bool{name: true}),
			"-"+name+" is an input of -strategy fimathe")
	}
	for _, name := range []string{"max-bars", "timeframe", "lot", "direction"} {
		require.NoError(t, a.CheckInputs("fimathe", map[string]bool{name: true}), name)
	}
	for _, name := range []string{"stop-atr", "take-profit-atr", "trailing-atr", "min-atr-pct", "buffer-atr"} {
		require.ErrorContains(t, a.CheckInputs("fimathe", map[string]bool{name: true}), "-"+name+" is an input of",
			name)
	}
}

// The strategy fimathe is created from its defaults, on its own timeframe
// unless -timeframe is set; a fixed stop loss is refused, the stop loss
// technical.
func TestAgent_Fimathe(t *testing.T) {
	a := testAgent()
	i := slices.IndexFunc(a.Strategies, func(s runner.Strategy) bool { return s.Name == "fimathe" })
	require.GreaterOrEqual(t, i, 0)
	in := runner.Inputs{Set: map[string]bool{}, Timeframe: "5m", Direction: strategies.RKDirectionBoth,
		MinValue: 10, Fraction: 1, Leverage: 1}
	trader, err := a.Strategies[i].New(in)
	require.NoError(t, err)
	require.Equal(t, "15m", trader.Timeframe())

	in.Set["timeframe"] = true
	trader, err = a.Strategies[i].New(in)
	require.NoError(t, err)
	require.Equal(t, "5m", trader.Timeframe())

	in.StopValue = 1
	_, err = a.Strategies[i].New(in)
	require.ErrorContains(t, err, "technical")
	in.StopValue = 0

	// the max risk, by -max-risk-pct or -fimathe-max-risk-pct, not both
	in.Set["max-risk-pct"], in.MaxRiskPct = true, -1
	_, err = a.Strategies[i].New(in)
	require.ErrorContains(t, err, "max risk", "-max-risk-pct applied")
	in.Set["fimathe-max-risk-pct"] = true
	_, err = a.Strategies[i].New(in)
	require.ErrorContains(t, err, "the same limit")
}
