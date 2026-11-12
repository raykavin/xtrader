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
// ignored as before. The ones of the rules of the prompts set their own stop
// loss and take profit: -stop-atr is refused with them too. pairs places no
// stop loss and reads no ATR: -stop-atr and -min-atr-pct are refused with it.
func TestAgent_TrailingATR(t *testing.T) {
	a := testAgent()
	requireTrailingATR(t, a, nil, "-trailing-atr is an input of no strategy of the agent")
	for _, s := range a.Strategies {
		if !slices.Contains(s.Inputs, "stop-atr") {
			if s.Name == "pairs" {
				require.NoError(t, a.CheckInputs(s.Name, map[string]bool{"lot": true}), s.Name)
				require.ErrorContains(t, a.CheckInputs(s.Name, map[string]bool{"min-atr-pct": true}),
					"is an input of", s.Name)
			} else {
				require.NoError(t, a.CheckInputs(s.Name, map[string]bool{"min-atr-pct": true, "lot": true}), s.Name)
			}
			require.ErrorContains(t, a.CheckInputs(s.Name, map[string]bool{"stop-atr": true}), "is an input of",
				s.Name)
			continue
		}
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

// The filters of meanreversion set by their flags, its own only: the ones of
// its defaults unless set, each reaching the strategy (a value out of its
// bounds refused by it).
func TestAgent_MeanReversionFilters(t *testing.T) {
	a := testAgent()
	for _, name := range meanReversionInputs {
		require.NoError(t, a.CheckInputs("meanreversion", map[string]bool{name: true}), name)
		require.ErrorContains(t, a.CheckInputs("orb", map[string]bool{name: true}),
			"-"+name+" is an input of -strategy meanreversion")
	}
	i := slices.IndexFunc(a.Strategies, func(s runner.Strategy) bool { return s.Name == "meanreversion" })
	require.GreaterOrEqual(t, i, 0)
	in := runner.Inputs{Set: map[string]bool{}, Direction: strategies.RKDirectionBoth, MinValue: 10, Fraction: 1,
		Leverage: 1}
	_, err := a.Strategies[i].New(in)
	require.NoError(t, err, "the defaults")
	for name, c := range map[string]struct{ value, refused string }{
		"meanreversion-oversold":   {"80", "RSI levels"},
		"meanreversion-overbought": {"20", "RSI levels"},
		"meanreversion-max-adx":    {"0", "ADX of the range"},
		"meanreversion-flat-atr":   {"0", "flat EMAs"},
		"meanreversion-wick-pct":   {"2", "wick of the rejection"},
	} {
		before := flag.Lookup(name).Value.String()
		require.NoError(t, flag.Set(name, c.value))
		_, err := a.Strategies[i].New(in)
		require.ErrorContains(t, err, c.refused, name)
		require.NoError(t, flag.Set(name, before))
	}
	for name, value := range map[string]string{"meanreversion-oversold": "40", "meanreversion-overbought": "60",
		"meanreversion-max-adx": "25", "meanreversion-flat-atr": "0.5", "meanreversion-wick-pct": "0.3"} {
		require.NoError(t, flag.Set(name, value))
	}
	_, err = a.Strategies[i].New(in)
	require.NoError(t, err, "the looser filters taken")
	for name, value := range map[string]string{"meanreversion-oversold": "30", "meanreversion-overbought": "70",
		"meanreversion-max-adx": "20", "meanreversion-flat-atr": "0.25", "meanreversion-wick-pct": "0.5"} {
		require.NoError(t, flag.Set(name, value))
	}
}

// strategyOf returns the strategy name of a, appended after the default one.
func strategyOf(t *testing.T, a runner.Agent, name string) runner.Strategy {
	t.Helper()
	i := slices.IndexFunc(a.Strategies, func(s runner.Strategy) bool { return s.Name == name })
	require.Greater(t, i, 0, "%s: appended, never the default", name)
	return a.Strategies[i]
}

// requireOwnInputs checks that the flags own are taken by the strategy name
// only, refused with other, and that it takes the shared ones of takes and
// refuses the ones of refuses.
func requireOwnInputs(t *testing.T, a runner.Agent, name, other string, own, takes, refuses []string) {
	t.Helper()
	for _, flagName := range own {
		require.NoError(t, a.CheckInputs(name, map[string]bool{flagName: true}), flagName)
		require.ErrorContains(t, a.CheckInputs(other, map[string]bool{flagName: true}),
			"-"+flagName+" is an input of -strategy "+name)
	}
	for _, flagName := range takes {
		require.NoError(t, a.CheckInputs(name, map[string]bool{flagName: true}), "%s: -%s", name, flagName)
	}
	for _, flagName := range refuses {
		require.Error(t, a.CheckInputs(name, map[string]bool{flagName: true}), "%s: -%s", name, flagName)
	}
}

// outOfBounds is a value of a flag the strategy refuses, and what its error
// says.
type outOfBounds struct{ value, refused string }

// requireRefused sets each flag of cases to its value out of bounds, checks
// that s refuses it with in, and restores the flag.
func requireRefused(t *testing.T, s runner.Strategy, in runner.Inputs, cases map[string]outOfBounds) {
	t.Helper()
	for name, c := range cases {
		before := flag.Lookup(name).Value.String()
		require.NoError(t, flag.Set(name, c.value), name)
		_, err := s.New(in)
		require.ErrorContains(t, err, c.refused, name)
		require.NoError(t, flag.Set(name, before), name)
	}
}

// setFlags sets the flags of values, returning a func restoring the ones of
// before.
func setFlags(t *testing.T, values map[string]string) (restore func()) {
	t.Helper()
	before := map[string]string{}
	for name, value := range values {
		before[name] = flag.Lookup(name).Value.String()
		require.NoError(t, flag.Set(name, value), name)
	}
	return func() {
		for name, value := range before {
			require.NoError(t, flag.Set(name, value), name)
		}
	}
}

// testInputs are the inputs of Run by default, no flag set.
func testInputs() runner.Inputs {
	return runner.Inputs{Set: map[string]bool{}, Direction: strategies.RKDirectionBoth, MinValue: 10, Fraction: 1,
		Leverage: 1}
}

// The flags of newsbreakout, its own only: the ones of its defaults unless
// set, each reaching the strategy (a value out of its bounds refused by it).
// It takes the exits of the bracket, as orb.
func TestAgent_NewsBreakoutInputs(t *testing.T) {
	a := testAgent()
	requireOwnInputs(t, a, "newsbreakout", "orb", newsBreakoutInputs,
		append([]string{"timeframe", "lot", "max-risk-pct"}, runner.BracketInputs...),
		[]string{"trailing-atr", "buffer-atr", "max-range-atr", "session"})
	s := strategyOf(t, a, "newsbreakout")
	trader, err := s.New(testInputs())
	require.NoError(t, err, "the defaults")
	require.Equal(t, "5m", trader.Timeframe())
	_, ok := trader.(strategies.ReleasesUser)
	require.True(t, ok, "the releases given by the runner")
	requireRefused(t, s, testInputs(), map[string]outOfBounds{
		"newsbreakout-importance":    {"none", "-newsbreakout-importance: invalid importance"},
		"newsbreakout-wait":          {"0s", "the wait after a release must be a positive number of minutes"},
		"newsbreakout-expiry":        {"90s", "the expiry of the breakouts must be a positive number of minutes"},
		"newsbreakout-buffer-atr":    {"-1", "buffer ATR multiple can't be negative"},
		"newsbreakout-max-range-atr": {"-1", "max range ATR multiple can't be negative"},
	})
	restore := setFlags(t, map[string]string{"newsbreakout-importance": "medium", "newsbreakout-codes": "nfp, cpi",
		"newsbreakout-wait": "30m", "newsbreakout-expiry": "2h", "newsbreakout-buffer-atr": "0.1",
		"newsbreakout-max-range-atr": "3"})
	_, err = s.New(testInputs())
	require.NoError(t, err, "the flags taken")
	restore()
}

// The flags of seasonality, its own only: the ones of its defaults unless set,
// each reaching the strategy (a value out of its bounds refused by it).
func TestAgent_SeasonalityInputs(t *testing.T) {
	a := testAgent()
	requireOwnInputs(t, a, "seasonality", "orb", seasonalityInputs,
		append([]string{"timeframe", "lot", "max-risk-pct"}, runner.BracketInputs...),
		[]string{"trailing-atr", "timezone", "session"})
	s := strategyOf(t, a, "seasonality")
	trader, err := s.New(testInputs())
	require.NoError(t, err, "the defaults")
	require.Equal(t, "15m", trader.Timeframe())
	requireRefused(t, s, testInputs(), map[string]outOfBounds{
		"seasonality-windows":  {"10:30", "invalid window"},
		"seasonality-days":     {"10", "the days of a window measured must be at least its min days"},
		"seasonality-min-days": {"1", "the min days of a window must be 2 at least"},
		"seasonality-min-t":    {"-1", "the min t statistic can't be negative"},
	})
	restore := setFlags(t, map[string]string{"seasonality-windows": "08:00/120@Europe/London",
		"seasonality-days": "90", "seasonality-min-days": "30", "seasonality-min-t": "0"})
	_, err = s.New(testInputs())
	require.NoError(t, err, "the flags taken")
	restore()
}

// The flags of volumeprofile, its own only: the ones of its defaults unless
// set, each reaching the strategy (a value out of its bounds refused by it).
// Its target is the other edge of the value area, unless -take-profit-atr
// replaces it.
func TestAgent_VolumeProfileInputs(t *testing.T) {
	a := testAgent()
	requireOwnInputs(t, a, "volumeprofile", "orb", volumeProfileInputs,
		[]string{"stop-atr", "take-profit-atr", "max-bars", "min-atr-pct", "timeframe", "lot", "max-risk-pct"},
		[]string{"trailing-atr", "timezone", "anchor"})
	s := strategyOf(t, a, "volumeprofile")
	trader, err := s.New(testInputs())
	require.NoError(t, err, "the defaults")
	require.Equal(t, "30m", trader.Timeframe())
	requireRefused(t, s, testInputs(), map[string]outOfBounds{
		"volumeprofile-timezone":   {"Mars/Olympus", "invalid timezone"},
		"volumeprofile-anchor":     {"25:00", "invalid time of day"},
		"volumeprofile-bins":       {"1", "the profile needs 2 bins at least"},
		"volumeprofile-value-area": {"1.5", "the value area must be a share of the volume"},
		"volumeprofile-confirm":    {"0", "confirmation candles must be at least 1"},
	})
	restore := setFlags(t, map[string]string{"volumeprofile-timezone": "America/New_York",
		"volumeprofile-anchor": "17:00", "volumeprofile-bins": "100", "volumeprofile-value-area": "0.68",
		"volumeprofile-confirm": "3"})
	_, err = s.New(testInputs())
	require.NoError(t, err, "the flags taken")
	restore()
}

// The flags of cmegap, its own only: the ones of its defaults unless set, each
// reaching the strategy (a value out of its bounds refused by it). Its target
// is the reference, the gap filled, unless -take-profit-atr replaces it.
func TestAgent_CMEGapInputs(t *testing.T) {
	a := testAgent()
	requireOwnInputs(t, a, "cmegap", "orb", cmeGapInputs,
		[]string{"stop-atr", "take-profit-atr", "max-bars", "min-atr-pct", "timeframe", "lot", "max-risk-pct"},
		[]string{"trailing-atr", "timezone"})
	s := strategyOf(t, a, "cmegap")
	trader, err := s.New(testInputs())
	require.NoError(t, err, "the defaults")
	require.Equal(t, "1h", trader.Timeframe())
	requireRefused(t, s, testInputs(), map[string]outOfBounds{
		"cmegap-timezone":    {"Mars/Olympus", "invalid timezone"},
		"cmegap-close":       {"4pm", "invalid time of day"},
		"cmegap-open":        {"17", "invalid time of day"},
		"cmegap-min-gap-atr": {"-0.5", "the min gap ATR multiple can't be negative"},
		"cmegap-expiry":      {"0", "the expiry must be 1 hour at least"},
	})
	restore := setFlags(t, map[string]string{"cmegap-timezone": "America/New_York", "cmegap-close": "17:00",
		"cmegap-open": "18:00", "cmegap-min-gap-atr": "1", "cmegap-expiry": "48"})
	_, err = s.New(testInputs())
	require.NoError(t, err, "the flags taken")
	restore()
}

// The flags of pairs, its own only: the ones of its defaults unless set, each
// reaching the strategy (a value out of its bounds refused by it). It places
// no stop loss nor take profit and reads no ATR: the flags of the bracket are
// refused with it, and -stop-value, -take-profit-value and -max-risk-pct by it.
// Without -lot and -position-fraction, the two legs share the fraction of its
// default, below the whole balance of the default of Run.
func TestAgent_PairsInputs(t *testing.T) {
	a := testAgent()
	requireOwnInputs(t, a, "pairs", "zscore", pairsInputs,
		[]string{"timeframe", "lot", "position-fraction", "direction", "leverage"},
		append([]string{"trailing-atr", "entry-z", "exit-z"}, runner.BracketInputs...))
	s := strategyOf(t, a, "pairs")
	trader, err := s.New(testInputs())
	require.NoError(t, err, "the defaults, the fraction of DefaultPairsConfig")
	require.Equal(t, "1h", trader.Timeframe())
	in := testInputs()
	in.Set["position-fraction"] = true
	_, err = s.New(in)
	require.ErrorContains(t, err, "the two legs share the position fraction", "-position-fraction 1 set")
	in.Lots = map[string]float64{"XAUUSDm": 0.01, "XAGUSDm": 0.02}
	_, err = s.New(in)
	require.NoError(t, err, "a size per leg")
	for name, value := range map[string]float64{"stop-value": 5, "take-profit-value": 10, "max-risk-pct": 2} {
		in := testInputs()
		in.Set[name] = true
		switch name {
		case "stop-value":
			in.StopValue = value
		case "take-profit-value":
			in.TakeProfitValue = value
		default:
			in.MaxRiskPct = value
		}
		_, err := s.New(in)
		require.ErrorContains(t, err, "pairs places no stop loss", name)
	}
	requireRefused(t, s, testInputs(), map[string]outOfBounds{
		"pairs-hedge":    {"tls", `the hedge must be ratio or ols, not "tls"`},
		"pairs-len":      {"2", "the window of the spread must be at least 3 candles"},
		"pairs-entry-z":  {"0", "the entry Z-Score must be positive"},
		"pairs-exit-z":   {"2", "the exit Z-Score must be between -EntryZ and EntryZ"},
		"pairs-stop-z":   {"1.5", "the stop Z-Score must be 0 (off) or above the entry Z-Score"},
		"pairs-max-bars": {"-1", "max bars can't be negative"},
	})
	require.Equal(t, strategies.PairsHedgeRatio, flag.Lookup("pairs-hedge").DefValue, "the log of the ratio by default")
	restore := setFlags(t, map[string]string{"pairs-hedge": "ols", "pairs-len": "100", "pairs-entry-z": "2.5",
		"pairs-exit-z": "0.5", "pairs-stop-z": "4", "pairs-max-bars": "0"})
	_, err = s.New(testInputs())
	require.NoError(t, err, "the flags taken")
	in = testInputs()
	in.Lots = map[string]float64{"XAUUSDm": 0.1, "XAGUSDm": 0.01}
	_, err = s.New(in)
	require.NoError(t, err, "leg B in steps of its lot")
	restore()
}

// Every strategy of the agent takes the stop loss and the take profit in ATR
// (-stop-atr, -take-profit-atr), the ones placing levels of their own too,
// which they then replace; but pairs, whose two legs hedge each other.
func TestStrategies_TakeTheExitsInATR(t *testing.T) {
	a := testAgent()
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
