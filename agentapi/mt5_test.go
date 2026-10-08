package agentapi

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/config"
)

func TestMT5Magic_TheOnesOfTheBotsTrading(t *testing.T) {
	// the magic numbers of the bots trading already, by their default comments:
	// a change of them would leave their positions behind
	for comment, magic := range map[string]int64{"nb:llm": 496707372, "nb:trendconfluence": 980556666,
		"nb:adxema": 854878241, "nb:atrbreakout": 1914336227, "nb:orb": 2127902278, "nb:matrend": 219323702} {
		require.Equal(t, magic, MT5Magic(0, comment), comment)
	}
	require.Equal(t, int64(42), MT5Magic(42, "nb:llm"), "the one of -mt5-magic")
	require.Equal(t, MT5Magic(0, "nb:llm"), MT5Magic(-1, "nb:llm"), "a hash of the comment unless above 0")
	require.NotEqual(t, MT5Magic(0, "nb:emacross-15m"), MT5Magic(0, "nb:emacross-1h"), "one per comment")
	magic := MT5Magic(0, strings.Repeat("x", 31))
	require.Positive(t, magic)
	require.LessOrEqual(t, magic, int64(1<<31-1), "a positive int32")
}

func TestMT5Comment_TheDefaultOfEachAgent(t *testing.T) {
	require.Equal(t, "nb:llm", MT5Comment("", "llm"), "an LLM agent")
	require.Equal(t, "nb:matrend", MT5Comment(" ", "matrend"), "the strategy by default")
	require.Equal(t, "nb:orb-ny", MT5Comment(" nb:orb-ny ", "orb"), "the one set, trimmed")
	require.Len(t, MT5Comment("", strings.Repeat("x", 40)), MT5MaxComment, "cut to the characters MT5 keeps")
}

func TestDefaultStrategy_OfEachKindOfTheRunner(t *testing.T) {
	for _, kind := range config.Kinds {
		if kind == "llm" {
			require.Empty(t, DefaultStrategy(kind))
			continue
		}
		require.NotEmpty(t, DefaultStrategy(kind), kind)
	}
	require.Equal(t, "trendconfluence", DefaultStrategy("trend"))
	require.Empty(t, DefaultStrategy("grid"))
}

func TestNewMT5Book_AsTheAgentsReadTheirFlags(t *testing.T) {
	magic := func(comment string) int64 { return MT5Magic(0, comment) }
	for _, tc := range []struct {
		name    string
		kind    string
		params  map[string]string
		account *Account
		want    MT5Book
	}{
		{"an LLM agent by default", "llm", nil, nil, MT5Book{Bridge: "http://127.0.0.1:18812", Magic: 496707372,
			Comment: "nb:llm", Pairs: []string{"XAUUSDm"}}},
		{"the strategy of its kind", "trend", map[string]string{"pair": "BTCUSDm; XAUUSDm,"},
			&Account{BridgeURL: "http://mt5:18812"}, MT5Book{Bridge: "http://mt5:18812", Magic: 980556666,
				Comment: "nb:trendconfluence", Pairs: []string{"BTCUSDm", "XAUUSDm"}}},
		{"its strategy", "intraday", map[string]string{"strategy": "vwap"}, nil, MT5Book{
			Bridge: "http://127.0.0.1:18812", Magic: magic("nb:vwap"), Comment: "nb:vwap", Pairs: []string{"XAUUSDm"}}},
		{"no strategy for an LLM agent", "llm", map[string]string{"strategy": "vwap"}, nil, MT5Book{
			Bridge: "http://127.0.0.1:18812", Magic: 496707372, Comment: "nb:llm", Pairs: []string{"XAUUSDm"}}},
		{"the comment of its account", "momentum", nil, &Account{BridgeURL: "http://mt5:18812",
			Comment: ptr(" nb:fast ")}, MT5Book{Bridge: "http://mt5:18812", Magic: magic("nb:fast"),
			Comment: "nb:fast", Pairs: []string{"XAUUSDm"}}},
		{"the magic number of its account", "breakout", nil, &Account{BridgeURL: "http://mt5:18812",
			Magic: ptr(int64(42))}, MT5Book{Bridge: "http://mt5:18812", Magic: 42, Comment: "nb:atrbreakout",
			Pairs: []string{"XAUUSDm"}}},
		// the flags of old settings among the params, the columns after them
		{"the flags among the params", "llm", map[string]string{"mt5-bridge": "http://old:1", "mt5-magic": "0x2A",
			"mt5-comment": "nb:old"}, nil, MT5Book{Bridge: "http://old:1", Magic: 42, Comment: "nb:old",
			Pairs: []string{"XAUUSDm"}}},
		{"the columns over the params", "llm", map[string]string{"mt5-bridge": "http://old:1", "mt5-magic": "42",
			"mt5-comment": "nb:old"}, &Account{BridgeURL: "http://new:2", Magic: ptr(int64(7)),
			Comment: ptr("nb:new")}, MT5Book{Bridge: "http://new:2", Magic: 7, Comment: "nb:new",
			Pairs: []string{"XAUUSDm"}}},
		{"a magic number of the params unread", "llm", map[string]string{"mt5-magic": "x"}, &Account{
			BridgeURL: ""}, MT5Book{Bridge: "http://127.0.0.1:18812", Magic: 496707372, Comment: "nb:llm",
			Pairs: []string{"XAUUSDm"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, NewMT5Book(tc.kind, tc.params, tc.account))
		})
	}
}

func TestConfig_MT5Book(t *testing.T) {
	cfg := Config{Name: "gold", Kind: "llm", Params: map[string]string{"pair": "XAUUSDm;BTCUSDm"},
		Account: &Account{BridgeURL: "http://mt5:18812", Comment: ptr("nb:gold")},
		LLM:     &LLM{Params: map[string]string{"llm-min-stop-atr": "1"}}}
	book := cfg.MT5Book()
	require.Equal(t, MT5Book{Bridge: "http://mt5:18812", Magic: MT5Magic(0, "nb:gold"), Comment: "nb:gold",
		Pairs: []string{"XAUUSDm", "BTCUSDm"}}, book)
	require.Equal(t, map[string]string{"pair": "XAUUSDm;BTCUSDm"}, cfg.Params, "left as it was")

	require.NoError(t, cfg.CheckMT5Book(book))
	for _, change := range []func(b *MT5Book){
		func(b *MT5Book) { b.Magic = 1 },
		func(b *MT5Book) { b.Comment = "nb:llm" },
		func(b *MT5Book) { b.Bridge = "http://127.0.0.1:18812" },
		func(b *MT5Book) { b.Pairs = []string{"XAUUSDm"} },
	} {
		other := book
		change(&other)
		err := cfg.CheckMT5Book(other)
		require.ErrorContains(t, err, "the agent gold trades")
		require.ErrorContains(t, err, fmt.Sprintf("its settings give the bridge http://mt5:18812, the magic number "+
			"%d (comment \"nb:gold\") and XAUUSDm;BTCUSDm", book.Magic))
	}
}

func TestSameMT5Bridge(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"http://127.0.0.1:18812", "http://127.0.0.1:18812", true},
		{"http://127.0.0.1:18812", "http://localhost:18812/", true},
		{"http://LOCALHOST:18812", "http://[::1]:18812", true},
		{"http://0.0.0.0:18812", "http://127.0.0.2:18812", true},
		{"http://mt5:18812", "HTTP://MT5:18812/api", true},
		{"http://mt5", "http://mt5:80", true},
		{"https://mt5", "http://mt5:443", true},
		{"http://127.0.0.1:18812", "http://127.0.0.1:18813", false},
		{"http://mt5:18812", "http://mt5-2:18812", false},
		{"http://mt5:18812", "http://127.0.0.1:18812", false},
		{" bridge ", "BRIDGE", true},
	} {
		assert.Equal(t, c.same, SameMT5Bridge(c.a, c.b), "%s and %s", c.a, c.b)
	}
}

func TestMT5Book_Overlaps(t *testing.T) {
	gold := MT5Book{Bridge: "http://mt5:18812", Magic: 7, Comment: "nb:gold", Pairs: []string{"XAUUSDm"}}
	for _, c := range []struct {
		name     string
		edit     func(b *MT5Book)
		overlaps bool
	}{
		{"the same", func(b *MT5Book) {}, true},
		{"a pair in common, case aside", func(b *MT5Book) { b.Pairs = []string{"BTCUSDm", "xauusdm"} }, true},
		{"no pair known", func(b *MT5Book) { b.Pairs = nil }, true},
		{"another comment of the same magic number", func(b *MT5Book) { b.Comment = "nb:other" }, true},
		{"another pair", func(b *MT5Book) { b.Pairs = []string{"BTCUSDm"} }, false},
		{"another magic number", func(b *MT5Book) { b.Magic = 8 }, false},
		{"another bridge", func(b *MT5Book) { b.Bridge = "http://mt5-2:18812" }, false},
	} {
		other := gold
		other.Pairs = slices.Clone(gold.Pairs)
		c.edit(&other)
		assert.Equal(t, c.overlaps, gold.Overlaps(other), c.name)
		assert.Equal(t, c.overlaps, other.Overlaps(gold), c.name+", both ways")
	}
	require.Equal(t, []string{"XAUUSDm"}, gold.Common(MT5Book{Pairs: []string{"xauusdm", "BTCUSDm"}}))
	require.Equal(t, []string{"XAUUSDm"}, gold.Common(MT5Book{}), "the ones of both, none known")
	unknown := MT5Book{Bridge: gold.Bridge, Magic: gold.Magic}
	require.True(t, unknown.Overlaps(unknown), "no pair known on either")
}

func ptr[T any](value T) *T { return &value }
