package exchange

import (
	_ "embed"
	"encoding/json"
	"sync"
)

type AssetQuote struct {
	Quote string
	Asset string
}

var (
	//go:embed pairs.json
	pairs             []byte
	pairsMtx          sync.RWMutex // guards pairAssetQuoteMap
	pairAssetQuoteMap = make(map[string]AssetQuote)
)

func init() {
	err := json.Unmarshal(pairs, &pairAssetQuoteMap)
	if err != nil {
		panic(err)
	}
}

func SplitAssetQuote(pair string) (asset string, quote string) {
	pairsMtx.RLock()
	data := pairAssetQuoteMap[pair]
	pairsMtx.RUnlock()
	return data.Asset, data.Quote
}

// RegisterPair makes SplitAssetQuote know a pair missing from the embedded
// list, pairs.json, the crypto pairs of the backtests and the tests. The MT5
// adapter registers its symbols when it is created, before the bot validates
// its pairs.
func RegisterPair(pair, asset, quote string) {
	pairsMtx.Lock()
	defer pairsMtx.Unlock()
	pairAssetQuoteMap[pair] = AssetQuote{Asset: asset, Quote: quote}
}
