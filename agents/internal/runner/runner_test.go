package runner

import (
	"hash/fnv"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
)

func TestList(t *testing.T) {
	require.Equal(t, []string{"GOLD", "BTCUSD"}, list("GOLD;BTCUSD"))
	require.Equal(t, []string{"GOLD", "BTCUSD"}, list("GOLD, BTCUSD"), "the comma of before")
	require.Equal(t, []string{"GOLD=0.01", "BTCUSD=0.03"}, list("GOLD=0.01;BTCUSD=0.03;"))
	require.Nil(t, list(" ; "))
}

func TestLotsByPair(t *testing.T) {
	pairs := []string{"GOLD", "BTCUSD"}

	lots, err := lotsByPair(pairs, nil)
	require.NoError(t, err)
	require.Nil(t, lots, "no -lot: the positions are a share of the balance")

	lots, err = lotsByPair(pairs, []string{"0.01"})
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"GOLD": 0.01, "BTCUSD": 0.01}, lots, "one for all the pairs")

	lots, err = lotsByPair(pairs, []string{"0.5", "0.01"})
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"GOLD": 0.5, "BTCUSD": 0.01}, lots, "one per pair, in their order")

	for _, sizes := range [][]string{{"0.5", "0.01", "1"}, {"0"}, {"-0.01"}, {"lot"}, {"NaN"}, {"Inf"}} {
		_, err := lotsByPair(pairs, sizes)
		require.Error(t, err, sizes)
	}

	lots, err = lotsByPair(pairs, []string{"BTCUSD=0.03", "GOLD=0.01"})
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"GOLD": 0.01, "BTCUSD": 0.03}, lots, "one by pair, in any order")
	for _, sizes := range [][]string{{"GOLD=0.01"}, {"GOLD=0.01", "0.03"}, {"GOLD=0.01", "EURUSD=0.1"},
		{"GOLD=0.01", "GOLD=0.02"}, {"GOLD=0.01", "BTCUSD=x"}} {
		_, err := lotsByPair(pairs, sizes)
		require.Error(t, err, sizes)
	}
}

// rulesExchange is an exchange whose markets take the sizes of info.
type rulesExchange struct {
	service.Exchange
	info model.AssetInfo
}

func (e rulesExchange) AssetsInfo(string) model.AssetInfo {
	return e.info
}

func TestCheckLots(t *testing.T) {
	exch := rulesExchange{info: model.AssetInfo{MinQuantity: 0.01, MaxQuantity: 100, StepSize: 0.01}}

	for _, lot := range []float64{0.01, 0.2, 0.25, 100} {
		require.NoError(t, checkLots(exch, map[string]float64{"GOLD": lot}, nil), lot)
	}
	require.NoError(t, checkLots(exch, nil, nil))

	for lot, err := range map[float64]string{
		0.001: "0.01 at least",
		150:   "100 at most",
		0.015: "steps of 0.01",
	} {
		require.ErrorContains(t, checkLots(exch, map[string]float64{"GOLD": lot}, nil), err, lot)
	}

	// a market without a maximum or an increment
	require.NoError(t, checkLots(rulesExchange{info: model.AssetInfo{MinQuantity: 0.001}},
		map[string]float64{"BTCUSD": 1234.5678}, nil))

	// MT5: the sizes in units, described with their lots
	exchange.RegisterMT5Pair("XAUUSDm", "XAU", "USD")
	mt5 := rulesExchange{info: model.AssetInfo{MinQuantity: 1, MaxQuantity: 20000, StepSize: 1}}
	contracts := map[string]float64{"XAUUSDm": 100}
	require.NoError(t, checkLots(mt5, map[string]float64{"XAUUSDm": 5}, contracts))
	require.ErrorContains(t, checkLots(mt5, map[string]float64{"XAUUSDm": 0.5}, contracts),
		"positions of 0.01 lot = 1 XAU at least, not 0.005 lot = 0.5 XAU")
}

func TestSizesByPair(t *testing.T) {
	pairs := []string{"XAUUSDm", "BTCUSDm"}

	sizes, err := sizesByPair("-mt5-contract-size", pairs, []string{"100", "1"})
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"XAUUSDm": 100, "BTCUSDm": 1}, sizes)

	_, err = sizesByPair("-mt5-contract-size", pairs, []string{"0"})
	require.ErrorContains(t, err, "-mt5-contract-size: invalid size")
}

func TestParseMT5Size(t *testing.T) {
	for value, inLots := range map[string]bool{"lot": true, "lots": true, " LOT ": true, "unit": false,
		"units": false} {
		got, err := parseMT5Size(value)
		require.NoError(t, err, value)
		require.Equal(t, inLots, got, value)
	}
	for _, value := range []string{"", "oz", "contract"} {
		_, err := parseMT5Size(value)
		require.ErrorContains(t, err, "-mt5-size", value)
	}
}

func TestMT5Magic(t *testing.T) {
	magic := mt5Magic(0, mt5Comment("", "matrend"))
	require.Positive(t, magic)
	require.LessOrEqual(t, magic, int64(1<<31-1), "a positive int32")
	// the magic numbers of the bots trading already: a hash of "nb:" and the name
	hash := fnv.New32a()
	_, _ = hash.Write([]byte("nb:matrend"))
	require.Equal(t, int64(hash.Sum32()&math.MaxInt32), magic, "the one of before: the bots find their positions")
	require.Equal(t, magic, mt5Magic(0, mt5Comment("", "matrend")), "stable, so a restart finds its positions")
	require.NotEqual(t, magic, mt5Magic(0, mt5Comment("", "orb")), "one per strategy")
	require.NotEqual(t, mt5Magic(0, "nb:emacross-15m"), mt5Magic(0, "nb:emacross-1h"),
		"one per comment: two bots of a strategy on one account")
	require.Equal(t, int64(1001), mt5Magic(1001, "nb:matrend"), "the one of -mt5-magic")
}

func TestCheckInputs(t *testing.T) {
	list := []Strategy{{Name: "matrend", Inputs: []string{"max-distance", "trailing-atr"}},
		{Name: "emacross", Inputs: append([]string{"fast-ema"}, BracketInputs...)}}
	require.NoError(t, checkInputs(list, "matrend", map[string]bool{"max-distance": true, "pair": true}))
	require.ErrorContains(t, checkInputs(list, "matrend", map[string]bool{"stop-atr": true}),
		"-stop-atr is an input of -strategy emacross")
	require.NoError(t, checkInputs(list, "emacross", map[string]bool{"stop-atr": true, "fast-ema": true}))
}

// M12a: an input of Run that no strategy of the agent takes is refused rather
// than ignored, eg: -trailing-atr on intradayagent; the flags of every strategy
// (-pair, -lot...) are not inputs.
func TestCheckInputs_NoStrategyTakesIt(t *testing.T) {
	intraday := Agent{Name: "intradayagent", Strategies: []Strategy{
		{Name: "orb", Inputs: append([]string{"session"}, BracketInputs...)},
		{Name: "zscore", Inputs: append([]string{"entry-z"}, BracketInputs...)},
	}}
	require.ErrorContains(t, intraday.CheckInputs("orb", map[string]bool{"trailing-atr": true, "pair": true}),
		"-trailing-atr is an input of no strategy of the agent")
	require.NoError(t, intraday.CheckInputs("orb", map[string]bool{"stop-atr": true, "session": true, "lot": true}))

	trend := Agent{Name: "trendagent", Strategies: []Strategy{
		{Name: "matrend", Inputs: []string{"trailing-atr", "max-distance"}},
	}}
	for _, name := range []string{"take-profit-atr", "max-bars", "min-atr-pct", "stop-atr"} {
		require.ErrorContains(t, trend.CheckInputs("matrend", map[string]bool{name: true}),
			"-"+name+" is an input of no strategy of the agent")
	}
	require.NoError(t, trend.CheckInputs("matrend", map[string]bool{"trailing-atr": true, "direction": true}))
}

func TestInputsBracket(t *testing.T) {
	cfg, timeframe := strategies.DefaultBracketConfig(), "5m"
	in := Inputs{Set: map[string]bool{"take-profit-atr": true}, Timeframe: "1h", Direction: strategies.RKDirectionBuyOnly,
		StopATR: 3, TakeProfitATR: 2.5, MinValue: 10, Fraction: 0.5, Leverage: 20}
	in.Bracket(&cfg, &timeframe)
	require.Equal(t, "5m", timeframe, "-timeframe unset: the strategy's")
	require.Equal(t, 1.0, cfg.StopATR, "-stop-atr unset: the strategy's")
	require.Equal(t, 2.5, cfg.TakeProfitATR)
	require.Equal(t, strategies.RKDirectionBuyOnly, cfg.Direction)
	require.Equal(t, 20.0, cfg.Leverage)

	in.Set["timeframe"], in.Set["stop-atr"] = true, true
	in.Bracket(&cfg, &timeframe)
	require.Equal(t, "1h", timeframe)
	require.Equal(t, 3.0, cfg.StopATR)

	// -max-risk-pct unset: the strategy's, eg: the 3% of fimathe
	cfg.MaxRiskPct, in.MaxRiskPct = 3, 1.5
	in.Bracket(&cfg, &timeframe)
	require.Equal(t, 3.0, cfg.MaxRiskPct)
	in.Set["max-risk-pct"] = true
	in.Bracket(&cfg, &timeframe)
	require.Equal(t, 1.5, cfg.MaxRiskPct)
}

func TestSignals(t *testing.T) {
	list := Signals("emacross", "donchianatr")
	require.Len(t, list, 2)
	require.Equal(t, "emacross", list[0].Name)
	require.NotEmpty(t, list[0].About)
	require.Equal(t, BracketInputs, list[0].Inputs)
	require.Contains(t, list[1].Inputs, "trailing-atr", "donchianatr trails its stop loss")
	require.NotContains(t, BracketInputs, "trailing-atr", "a copy extended")

	in := Inputs{Set: map[string]bool{"timeframe": true, "trailing-atr": true}, Timeframe: "4h",
		Direction: strategies.RKDirectionBoth, TrailingATR: 3, MinValue: 10, Fraction: 1, Leverage: 1}
	trader, err := list[1].New(in)
	require.NoError(t, err)
	require.Equal(t, "4h", trader.Timeframe())

	in.Set = map[string]bool{}
	trader, err = list[0].New(in)
	require.NoError(t, err)
	require.Equal(t, "15m", trader.Timeframe(), "-timeframe unset: the strategy's")

	in.Set = map[string]bool{"stop-atr": true}
	_, err = list[0].New(in)
	require.ErrorContains(t, err, "emacross", "-stop-atr 0: no stop loss")
	require.Panics(t, func() { Signals("nothing") })
}

// M12a: -trailing-atr is applied by the signal strategies taking it only (see
// TrailingSignals): set with emacross of Signals, which refuses it (see
// checkInputs), it changes nothing; TrailingSignals applies it to every one.
func TestSignals_TrailingATR(t *testing.T) {
	in := Inputs{Set: map[string]bool{"trailing-atr": true}, Direction: strategies.RKDirectionBoth,
		TrailingATR: -1, MinValue: 10, Fraction: 1, Leverage: 1}
	plain, trailing := Signals("emacross", "donchianatr"), TrailingSignals("emacross")
	require.Equal(t, BracketInputs, plain[0].Inputs)
	_, err := plain[0].New(in)
	require.NoError(t, err, "not applied")
	_, err = plain[1].New(in)
	require.ErrorContains(t, err, "trailing", "applied")

	require.Equal(t, append(slices.Clone(BracketInputs), "trailing-atr"), trailing[0].Inputs)
	_, err = trailing[0].New(in)
	require.ErrorContains(t, err, "trailing", "applied")
	in.TrailingATR = 2
	_, err = trailing[0].New(in)
	require.NoError(t, err)
	require.Panics(t, func() { TrailingSignals("nothing") })
}

func TestMT5Comment(t *testing.T) {
	require.Equal(t, "nb:matrend", mt5Comment("", "matrend"), "the strategy name by default")
	require.Equal(t, "nb:orb-ny", mt5Comment(" nb:orb-ny ", "orb"))
	require.Len(t, mt5Comment("", strings.Repeat("x", 40)), 31, "cut to the 31 characters MT5 keeps")
}

func TestMT5Token(t *testing.T) {
	t.Setenv(mt5TokenEnv, "from-env")
	require.Equal(t, "from-env", mt5Token(""))
	require.Equal(t, "flag", mt5Token(" flag "))
	t.Setenv(mt5TokenEnv, "")
	require.Empty(t, mt5Token(""))
}

func TestCheckVenue(t *testing.T) {
	none := map[string]bool{}
	require.NoError(t, checkVenue("mt5", "backtest", map[string]bool{"mt5-contract-size": true}, 1))
	require.NoError(t, checkVenue("mt5", "live", map[string]bool{"mt5-magic": true, "mt5-live": true}, 20),
		"the leverage of the margin live")
	require.NoError(t, checkVenue("mt5", "paper", map[string]bool{"mt5-bridge": true}, 1))

	for _, c := range []struct {
		exchange, mode string
		set            map[string]bool
		leverage       float64
		err            string
	}{
		{"kraken", "live", none, 1, "invalid exchange"},
		{"binance", "live", none, 1, "invalid exchange \"binance\": mt5"},
		{"capital", "backtest", none, 1, "invalid exchange \"capital\": mt5"},
		{"mt5", "live", map[string]bool{"mt5-contract-size": true}, 1, "for backtests"},
		{"mt5", "paper", none, 20, "-leverage"},
		{"mt5", "backtest", none, 20, "-leverage"},
	} {
		err := checkVenue(c.exchange, c.mode, c.set, c.leverage)
		require.ErrorContains(t, err, c.err, c)
	}
}

func TestMT5Contracts(t *testing.T) {
	pairs := []string{"XAUUSDm", "BTCUSDm"}

	contracts, err := mt5Contracts(pairs, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"XAUUSDm": 100, "BTCUSDm": 1}, contracts, "the ones the package knows")

	contracts, err = mt5Contracts(pairs, []string{"10"})
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"XAUUSDm": 10, "BTCUSDm": 10}, contracts, "one for all the pairs")

	_, err = mt5Contracts([]string{"XAGUSDtest"}, nil)
	require.ErrorContains(t, err, "the contract size of XAGUSDtest is unknown")

	exchange.RegisterMT5Contract("XAGUSDtest", 5000)
	contracts, err = mt5Contracts([]string{"XAGUSDtest"}, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"XAGUSDtest": 5000}, contracts, "a registered one")
}

func TestLotsToUnits(t *testing.T) {
	lots := map[string]float64{"XAUUSDm": 0.07, "BTCUSDm": 0.01, "EURUSDm": 0.03}
	shared := lots // as the strategy holds it
	require.NoError(t, lotsToUnits(lots, map[string]float64{"XAUUSDm": 100, "BTCUSDm": 1, "EURUSDm": 100000}))
	require.Equal(t, map[string]float64{"XAUUSDm": 7, "BTCUSDm": 0.01, "EURUSDm": 3000}, shared,
		"converted in place, without the float noise")
	require.NoError(t, lotsToUnits(nil, nil))

	err := lotsToUnits(map[string]float64{"XAGUSDm": 0.01}, map[string]float64{"XAUUSDm": 100})
	require.ErrorContains(t, err, "the contract size of XAGUSDm is unknown")
	err = lotsToUnits(map[string]float64{"XAUUSDm": 0.01}, map[string]float64{"XAUUSDm": 0})
	require.Error(t, err)
}

func TestDescribeSize(t *testing.T) {
	exchange.RegisterMT5Pair("XAUUSDm", "XAU", "USD")
	require.Equal(t, "0.05 lot = 5 XAU", describeSize("XAUUSDm", 5, 100))
	require.Equal(t, "0.07 lot = 7 XAU", describeSize("XAUUSDm", 7.000000000000001, 100))
	require.Equal(t, "0.5", describeSize("GOLD", 0.5, 0), "no contract size: the size alone")
	require.True(t, strings.HasSuffix(describeSize("UNKNOWNm", 3, 10), "= 3 units"), "an unregistered pair")
}

// The paper mode reads the market data of MT5 only: it must never net nor close
// the positions of a live agent trading the account with the same magic number.
func TestMT5VenueOptions(t *testing.T) {
	v := mt5Venue{bridge: "http://127.0.0.1:18899", token: "s3cret", magic: 1001, comment: "nb:matrend",
		poll: 2 * time.Second}
	apply := func(options []exchange.MT5Option) *exchange.MT5 {
		m := &exchange.MT5{}
		for _, option := range options {
			option(m)
		}
		return m
	}

	paper := apply(v.options([]string{"XAUUSDm"}, false))
	require.True(t, paper.DataOnly(), "the paper mode reads the market data only")
	require.Equal(t, int64(1001), paper.Magic())
	require.Equal(t, "nb:matrend", paper.Comment())

	live := apply(v.options([]string{"XAUUSDm"}, true))
	require.False(t, live.DataOnly(), "the live mode trades")
	require.Equal(t, int64(1001), live.Magic())
}

// Only the MT5 terminal reports the account: the other exchanges keep the
// profits of the core, in the quote.
func TestAccountOptions(t *testing.T) {
	require.Nil(t, accountOptions(&exchange.PaperWallet{}))
	require.Len(t, accountOptions(&exchange.MT5{}), 2)
}

func TestSizesOf_DecimalCommaRefused(t *testing.T) {
	pairs := []string{"XAUUSDm", "BTCUSDm"}
	for _, value := range []string{"1,5", "0,05", "XAUUSDm=0,05;BTCUSDm=1", "1,5;2"} {
		sizes, err := sizesOf("-lot", pairs, value)
		require.ErrorContains(t, err, "-lot: ", value)
		require.ErrorContains(t, err, "the comma may be a decimal one: separate the sizes with ; and write the "+
			"decimals with .", value)
		require.Nil(t, sizes, value)
	}
	_, err := sizesOf("-lot", []string{"XAUUSDm"}, "1,5")
	require.ErrorContains(t, err, "the comma may be a decimal one", "one pair")
	_, err = sizesOf("-mt5-contract-size", pairs, "100,1")
	require.ErrorContains(t, err, `-mt5-contract-size: "100,1" is ambiguous`)

	// the unambiguous ones, the comma of before among them
	for value, want := range map[string]map[string]float64{
		"1.5":                       {"XAUUSDm": 1.5, "BTCUSDm": 1.5},
		"0.03;0.01":                 {"XAUUSDm": 0.03, "BTCUSDm": 0.01},
		"0.03,0.01":                 {"XAUUSDm": 0.03, "BTCUSDm": 0.01},
		"1;5":                       {"XAUUSDm": 1, "BTCUSDm": 5},
		"BTCUSDm=0.03;XAUUSDm=0.01": {"XAUUSDm": 0.01, "BTCUSDm": 0.03},
		"BTCUSDm=0.03,XAUUSDm=0.01": {"XAUUSDm": 0.01, "BTCUSDm": 0.03},
	} {
		sizes, err := sizesOf("-lot", pairs, value)
		require.NoError(t, err, value)
		require.Equal(t, want, sizes, value)
	}
}
