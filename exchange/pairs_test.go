package exchange

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitAssetQuote(t *testing.T) {
	tt := []struct {
		Pair  string
		Asset string
		Quote string
	}{
		{"BTCUSDT", "BTC", "USDT"},
		{"ETHBTC", "ETH", "BTC"},
		{"BTCBUSD", "BTC", "BUSD"},
		{"1000SHIBBUSD", "1000SHIB", "BUSD"},
	}

	for _, tc := range tt {
		t.Run(tc.Pair, func(t *testing.T) {
			asset, quote := SplitAssetQuote(tc.Pair)
			require.Equal(t, tc.Asset, asset)
			require.Equal(t, tc.Quote, quote)
		})
	}
}

func TestRegisterPair(t *testing.T) {
	previous := maps.Clone(pairAssetQuoteMap)
	t.Cleanup(func() { pairAssetQuoteMap = previous })

	asset, quote := SplitAssetQuote("EURUSD")
	require.Empty(t, asset+quote, "not a pair of pairs.json")

	RegisterPair("EURUSD", "EUR", "USD")
	asset, quote = SplitAssetQuote("EURUSD")
	require.Equal(t, "EUR", asset)
	require.Equal(t, "USD", quote)
}

// The embedded pairs.json must stay parseable: init panics otherwise.
func TestEmbeddedPairs(t *testing.T) {
	var parsed map[string]AssetQuote
	require.NoError(t, json.Unmarshal(pairs, &parsed))
	require.NotEmpty(t, parsed)

	asset, quote := SplitAssetQuote("BTCUSDT")
	require.Equal(t, "BTC", asset)
	require.Equal(t, "USDT", quote)
}
