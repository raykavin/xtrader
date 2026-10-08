package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/ui"
)

func TestSizesByPair(t *testing.T) {
	sizes, err := sizesByPair("-lot", []string{"GOLD", "BTCUSD"}, nil)
	require.NoError(t, err)
	require.Nil(t, sizes, "no size: a share of the balance")

	sizes, err = sizesByPair("-lot", []string{"GOLD", "BTCUSD"}, []string{"0.5"})
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"GOLD": 0.5, "BTCUSD": 0.5}, sizes)

	sizes, err = sizesByPair("-lot", []string{"GOLD", "BTCUSD"}, []string{"0.5", "0.01"})
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"GOLD": 0.5, "BTCUSD": 0.01}, sizes)

	_, err = sizesByPair("-lot", []string{"GOLD", "BTCUSD"}, []string{"1", "2", "3"})
	require.ErrorContains(t, err, "one size for all the pairs or one per pair, not 3 for 2 pairs")
	for _, invalid := range []string{"0", "-1", "abc", "NaN", "Inf"} {
		_, err = sizesByPair("-lot", []string{"GOLD"}, []string{invalid})
		require.ErrorContains(t, err, "invalid size", invalid)
	}

	// by pair, in any order
	sizes, err = sizesByPair("-lot", []string{"BTCUSDm", "XAUUSDm"}, []string{"XAUUSDm=0.01", " BTCUSDm = 0.03 "})
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"BTCUSDm": 0.03, "XAUUSDm": 0.01}, sizes)
	for sizes, want := range map[string]string{
		"BTCUSDm=0.03":              "no size for XAUUSDm",
		"BTCUSDm=0.03,0.01":         "every size by its pair",
		"BTCUSDm=0.03,EURUSDm=0.01": `"EURUSDm" isn't a pair of -pair (BTCUSDm,XAUUSDm)`,
		"BTCUSDm=0.03,BTCUSDm=0.02": "BTCUSDm twice",
		"BTCUSDm=0,XAUUSDm=0.01":    `invalid size "0" for BTCUSDm`,
	} {
		_, err = sizesByPair("-lot", []string{"BTCUSDm", "XAUUSDm"}, list(sizes))
		require.ErrorContains(t, err, want, sizes)
	}
}

func TestList(t *testing.T) {
	require.Equal(t, []string{"BTCUSDm", "XAUUSDm"}, list("BTCUSDm;XAUUSDm"))
	require.Equal(t, []string{"BTCUSDm", "XAUUSDm"}, list(" BTCUSDm , XAUUSDm "), "the comma of before")
	require.Equal(t, []string{"a", "b", "c"}, list("a;b,c;;"), "both, the blanks dropped")
	require.Equal(t, []string{"GOLD", "BTCUSD"}, list(" GOLD, ,BTCUSD,"))
	require.Nil(t, list(""))
	sizes, err := sizesByPair("-lot", []string{"BTCUSDm", "XAUUSDm"}, list("BTCUSDm=0.03;XAUUSDm=0.01"))
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"BTCUSDm": 0.03, "XAUUSDm": 0.01}, sizes)
}

// -lot takes the lot of each pair by its name only, separated by ;.
func TestLotsByPair(t *testing.T) {
	pairs := []string{"BTCUSDm", "XAUUSDm"}
	lots, err := lotsByPair(pairs, "")
	require.NoError(t, err)
	require.Nil(t, lots, "no lot: a share of the balance")

	lots, err = lotsByPair(pairs, "BTCUSDm=0.01;XAUUSDm=0.02")
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"BTCUSDm": 0.01, "XAUUSDm": 0.02}, lots)
	lots, err = lotsByPair(pairs, " XAUUSDm = 0.02 ; BTCUSDm=0.01;")
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"BTCUSDm": 0.01, "XAUUSDm": 0.02}, lots, "in any order, the blanks dropped")
	lots, err = lotsByPair([]string{"XAUUSDm"}, "XAUUSDm=1.5")
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"XAUUSDm": 1.5}, lots, "one pair, by its name too")

	for value, want := range map[string]string{
		"BTCUSDm=0.01":              "no size for XAUUSDm",
		"BTCUSDm=0.01;EURUSDm=0.02": `"EURUSDm" isn't a pair of -pair (BTCUSDm,XAUUSDm)`,
		"BTCUSDm=0.01;BTCUSDm=0.02": "BTCUSDm twice",
		"BTCUSDm=0;XAUUSDm=0.02":    `invalid size "0" for BTCUSDm`,
		"BTCUSDm=abc;XAUUSDm=0.02":  `invalid size "abc" for BTCUSDm`,
		"BTCUSDm=0.01;0.02":         `"0.02" isn't PAIR=SIZE`,
		"BTCUSDm=0,01;XAUUSDm=0,02": "has a comma",
		"BTCUSDm=0.01,XAUUSDm=0.02": "has a comma",
		// the formats of before, refused: one lot for all, the order of -pair
		"0.01":      `"0.01" isn't PAIR=SIZE`,
		"0.03;0.01": `"0.03" isn't PAIR=SIZE`,
		"0.03,0.01": "has a comma",
		"1,5":       "has a comma",
	} {
		lots, err = lotsByPair(pairs, value)
		require.ErrorContains(t, err, "-lot: ", value)
		require.ErrorContains(t, err, want, value)
		require.ErrorContains(t, err, "eg: BTCUSDm=0.01;XAUUSDm=0.02", value)
		require.Nil(t, lots, value)
	}
}

func TestCorrelatedByPair(t *testing.T) {
	pairs := []string{"BTCUSDm", "XAUUSDm"}
	byPair, err := correlatedByPair(pairs, "")
	require.NoError(t, err)
	require.Nil(t, byPair)

	// the ones of each pair by its name, the pairs separated by ;
	byPair, err = correlatedByPair(pairs, "BTCUSDm=DXYm+XAUUSDm;XAUUSDm=DXYm+USTECm")
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"BTCUSDm": {"DXYm", "XAUUSDm"}, "XAUUSDm": {"DXYm", "USTECm"}}, byPair)
	byPair, err = correlatedByPair(pairs, "XAUUSDm=USTECm+DXYm; BTCUSDm = XAUUSDm + DXYm + USTECm ;")
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"XAUUSDm": {"USTECm", "DXYm"}, "BTCUSDm": {"XAUUSDm", "DXYm", "USTECm"}},
		byPair, "in any order, the blanks dropped")
	// a pair without any left out
	byPair, err = correlatedByPair(pairs, "XAUUSDm=DXYm")
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"XAUUSDm": {"DXYm"}}, byPair)

	for value, want := range map[string]string{
		"EURUSDm=DXYm":               `"EURUSDm" isn't a pair of -pair (BTCUSDm,XAUUSDm)`,
		"XAUUSDm=DXYm;XAUUSDm=USTEC": "XAUUSDm twice",
		"XAUUSDm=XAUUSDm+DXYm":       "XAUUSDm among its own markets",
		"XAUUSDm=":                   "no market for XAUUSDm",
		"XAUUSDm=DXYm=USTECm":        "isn't PAIR=MARKET+MARKET",
		"=DXYm":                      "isn't PAIR=MARKET+MARKET",
		// the formats of before, refused: one list for all the pairs, a comma
		"USTECm;XAUUSDm;DXYm":                              `"USTECm" isn't PAIR=MARKET+MARKET`,
		"USTECm, XAUUSDm,DXYm":                             "has a comma",
		"XAUUSDm=USTECm+DXYm, BTCUSDm=XAUUSDm+DXYm+USTECm": "has a comma",
		"XAUUSDm=DXYm,USTECm":                              "has a comma",
	} {
		_, err = correlatedByPair(pairs, value)
		require.ErrorContains(t, err, want, value)
	}
}

func TestParseMT5Size(t *testing.T) {
	for value, want := range map[string]bool{"lot": true, "Lots": true, " unit ": false, "units": false} {
		inLots, err := parseMT5Size(value)
		require.NoError(t, err, value)
		require.Equal(t, want, inLots, value)
	}
	_, err := parseMT5Size("ounce")
	require.ErrorContains(t, err, `"ounce" is neither lot nor unit`)
}

func TestMT5Magic(t *testing.T) {
	require.Equal(t, int64(42), mt5Magic(42, "nb:llm"), "the one of -mt5-magic")
	magic := mt5Magic(0, "nb:llm")
	require.Positive(t, magic)
	require.LessOrEqual(t, magic, int64(1<<31-1), "an int32")
	require.Equal(t, magic, mt5Magic(0, "nb:llm"), "stable across restarts")
	require.NotEqual(t, magic, mt5Magic(0, "nb:llm2"), "one per comment")
}

func TestMT5Token(t *testing.T) {
	t.Setenv(mt5TokenEnv, " from-env ")
	require.Equal(t, "from-flag", mt5Token(" from-flag "))
	require.Equal(t, "from-env", mt5Token(""))
}

func TestLotsToUnits(t *testing.T) {
	lots := map[string]float64{"XAUUSDm": 0.07, "BTCUSDm": 0.01}
	require.NoError(t, lotsToUnits(lots, map[string]float64{"XAUUSDm": 100, "BTCUSDm": 1}))
	require.Equal(t, map[string]float64{"XAUUSDm": 7, "BTCUSDm": 0.01}, lots, "without the float noise")

	err := lotsToUnits(map[string]float64{"XAGUSDm": 0.01}, map[string]float64{})
	require.ErrorContains(t, err, "the contract size of XAGUSDm is unknown")
}

func TestBacktestPairs(t *testing.T) {
	v := venue{lots: map[string]float64{"XAUUSDm": 0.05}, mt5: mt5Venue{inLots: true}}
	require.NoError(t, v.backtestPairs([]string{"XAUUSDm"}, "USD"))
	require.Equal(t, map[string]float64{"XAUUSDm": 5}, v.lots, "100 ounces in a lot")

	v = venue{lots: map[string]float64{"ZZZUSDm": 1}, mt5: mt5Venue{inLots: true}}
	require.ErrorContains(t, v.backtestPairs([]string{"ZZZUSDm"}, "USD"), "-mt5-size unit")

	v = venue{lots: map[string]float64{"ZZZUSDm": 1}}
	require.NoError(t, v.backtestPairs([]string{"ZZZUSDm"}, "USD"), "in units")
	require.Equal(t, map[string]float64{"ZZZUSDm": 1}, v.lots)
}

func TestCheckVenue(t *testing.T) {
	tests := []struct {
		name, exchange, mode string
		leverage             float64
		want                 string // in the error, none when valid
	}{
		{"backtest", "mt5", "backtest", 1, ""},
		{"paper", "mt5", "paper", 1, ""},
		{"live with leverage", "mt5", "live", 20, ""},
		{"unknown exchange", "kraken", "live", 1, `invalid exchange "kraken": mt5`},
		{"binance is gone", "binance", "paper", 1, `invalid exchange "binance"`},
		{"capital is gone", "capital", "live", 1, `invalid exchange "capital"`},
		{"leverage in paper", "mt5", "paper", 20, "-leverage needs the margin"},
		{"leverage in a backtest", "mt5", "backtest", 2, "-leverage needs the margin"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkVenue(tc.exchange, tc.mode, tc.leverage)
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestCalendarConfig(t *testing.T) {
	defaults := llm.DefaultConfig()
	configure := func(set map[string]bool, url, token, importance, blockImportance string) (llm.Config, error) {
		cfg := llm.DefaultConfig()
		err := calendarConfig(&cfg, set, url, token, []string{"usd"}, 3*time.Hour, time.Hour, importance, 0,
			10*time.Minute, 20*time.Minute, blockImportance)
		return cfg, err
	}

	cfg, err := configure(map[string]bool{}, "", "", "medium", "high")
	require.NoError(t, err)
	require.Nil(t, cfg.Calendar, "no calendar without -calendar-mcp")
	require.Equal(t, defaults.NewsBlockBefore, cfg.NewsBlockBefore)

	_, err = configure(map[string]bool{"news-block-before": true}, "", "", "medium", "high")
	require.ErrorContains(t, err, "-news-block-before needs the calendar of -calendar-mcp")

	_, err = configure(map[string]bool{}, "http://127.0.0.1:22346/mcp", " ", "medium", "high")
	require.ErrorContains(t, err, calendarTokenEnv)

	_, err = configure(map[string]bool{}, "http://127.0.0.1:22346/mcp", "token", "urgent", "high")
	require.ErrorContains(t, err, "-calendar-importance")
	_, err = configure(map[string]bool{}, "http://127.0.0.1:22346/mcp", "token", "low", "never")
	require.ErrorContains(t, err, "-news-block-importance")

	cfg, err = configure(map[string]bool{}, "http://127.0.0.1:22346/mcp", "token", "low", "medium")
	require.NoError(t, err)
	require.NotNil(t, cfg.Calendar)
	require.Equal(t, []string{"USD"}, cfg.CalendarCurrencies)
	require.Equal(t, llm.ImportanceLow, cfg.CalendarImportance)
	require.Equal(t, llm.ImportanceMedium, cfg.NewsBlockImportance)
	require.Equal(t, 3*time.Hour, cfg.CalendarAhead)
	require.Equal(t, time.Hour, cfg.CalendarBack)
	require.Equal(t, 10*time.Minute, cfg.NewsBlockBefore)
	require.Equal(t, 20*time.Minute, cfg.NewsBlockAfter)
}

// Launched by the controller, the secrets are the ones of its settings.
func TestSecretsOf(t *testing.T) {
	require.Equal(t, secrets{}, secretsOf(agentapi.Config{}))
	cfg := agentapi.Config{Account: &agentapi.Account{BridgeToken: "bridge"},
		LLM: &agentapi.LLM{APIKey: "key", CalendarToken: "calendar"}}
	require.Equal(t, secrets{provider: "key", bridge: "bridge", calendar: "calendar"}, secretsOf(cfg))
}

func TestPromptNames(t *testing.T) {
	require.Equal(t, "trend_following", promptName("agents/llm/prompts/trend_following.txt"))
	require.Equal(t, "scalp", promptName("scalp"))
	require.Equal(t, "trend_following v3", describePrompt(llm.Prompt{Ref: ui.DecisionPrompt{ID: 1,
		Name: "trend_following", Version: 3}}))
	require.Equal(t, "default", describePrompt(llm.DefaultPrompt()))
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
