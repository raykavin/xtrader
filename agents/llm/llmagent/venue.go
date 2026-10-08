package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/internal/flaglist"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/tools/log"
)

// venue is the exchange of the paper and live modes: MetaTrader 5.
type venue struct {
	leverage float64            // of the positions, 1 for none
	lots     map[string]float64 // the fixed sizes of the positions, by pair, if any
	mt5      mt5Venue
}

// mt5Venue holds the options of MetaTrader 5 in the paper and live modes.
type mt5Venue struct {
	bridge  string        // address of tools/mt5bridge
	token   string        // X-Bridge-Token of the bridge, if any
	magic   int64         // magic number of the bot's orders and positions
	comment string        // comment of the bot's orders
	live    bool          // a real account is allowed
	inLots  bool          // -lot is in MT5 lots, converted into units once connected
	poll    time.Duration // interval of the polls of the account and of the candles, the default when 0
}

// connect connects to MT5, authenticated when trading on it: the paper mode
// only needs its candles, so MT5 reads the market data only
// (exchange.WithMT5DataOnly) and never touches the positions of a live agent
// of the same magic number.
func (v venue) connect(ctx context.Context, pairs []string, trading bool) (service.Exchange, error) {
	options := []exchange.MT5Option{
		exchange.WithMT5BaseURL(v.mt5.bridge),
		exchange.WithMT5Symbols(pairs...),
		exchange.WithMT5Magic(v.mt5.magic),
		exchange.WithMT5Comment(v.mt5.comment),
	}
	if v.mt5.token != "" {
		options = append(options, exchange.WithMT5Token(v.mt5.token))
	}
	if v.mt5.live {
		options = append(options, exchange.WithMT5Real())
	}
	if v.mt5.poll > 0 {
		options = append(options, exchange.WithMT5PollInterval(v.mt5.poll))
	}
	if !trading {
		options = append(options, exchange.WithMT5DataOnly())
	}
	if v.leverage > 1 {
		options = append(options, exchange.WithMT5Leverage())
	}

	mt5, err := exchange.NewMT5(ctx, options...)
	if err != nil {
		return nil, err
	}
	// a position worth more than its margin allows would be rejected
	for _, pair := range pairs {
		if account := mt5.Leverage(pair); v.leverage > account {
			return nil, fmt.Errorf("mt5: the account trades %s at 1:%g at most, not 1:%g", pair, account,
				v.leverage)
		}
	}
	return mt5, nil
}

// backtestPairs registers the symbols of a backtest, which the adapter
// registers when connecting, and converts the MT5 lots into units with the
// contract sizes the package knows.
func (v venue) backtestPairs(pairs []string, currency string) error {
	for _, pair := range pairs {
		exchange.RegisterMT5Pair(pair, "", currency)
	}
	if v.lots == nil || !v.mt5.inLots {
		return nil
	}
	contracts := make(map[string]float64, len(pairs))
	for _, pair := range pairs {
		size, ok := exchange.MT5ContractSize(pair)
		if !ok {
			return fmt.Errorf("-lot: the contract size of %s is unknown, pass -lot in units with "+
				"-mt5-size unit", pair)
		}
		contracts[pair] = size
	}
	return lotsToUnits(v.lots, contracts)
}

// checkSizes converts the sizes of -lot read in MT5 lots into units of the
// base asset, with the contract sizes of the account, then checks them against
// the markets of exch. The strategy shares the map of the sizes, so it must run
// before the bot starts.
func (v venue) checkSizes(exch service.Exchange) error {
	if mt5, ok := exch.(*exchange.MT5); ok && v.lots != nil && v.mt5.inLots {
		contracts := make(map[string]float64, len(v.lots))
		for pair := range v.lots {
			contracts[pair] = mt5.ContractSize(pair)
		}
		if err := lotsToUnits(v.lots, contracts); err != nil {
			return err
		}
	}

	for pair, lot := range v.lots {
		info := exch.AssetsInfo(pair)
		switch {
		case lot < info.MinQuantity:
			return fmt.Errorf("-lot: %s takes positions of %g at least, not %g", pair, info.MinQuantity, lot)
		case info.MaxQuantity > 0 && lot > info.MaxQuantity:
			return fmt.Errorf("-lot: %s takes positions of %g at most, not %g", pair, info.MaxQuantity, lot)
		case info.StepSize > 0 && math.Abs(lot/info.StepSize-math.Round(lot/info.StepSize)) > 1e-6:
			return fmt.Errorf("-lot: %s takes positions in steps of %g, not %g", pair, info.StepSize, lot)
		}
		log.Infof("[SETUP] %s: positions of %g (min %g, step %g)", pair, lot, info.MinQuantity, info.StepSize)
	}
	return nil
}

// lotsToUnits converts in place the sizes of lots, in MT5 lots, into units
// of the base asset: units = lots x contract size.
func lotsToUnits(lots, contracts map[string]float64) error {
	for pair, lot := range lots {
		contract := contracts[pair]
		if contract <= 0 || math.IsNaN(contract) || math.IsInf(contract, 0) {
			return fmt.Errorf("-lot: the contract size of %s is unknown, so its lots can't be converted", pair)
		}
		lots[pair] = math.Round(lot*contract*1e9) / 1e9 // without the float noise, eg: 0.07 x 100
	}
	return nil
}

// sizesOf reads the flag name of a list of sizes (see flaglist.Sizes) and maps
// them to the pairs (see sizesByPair).
func sizesOf(name string, pairs []string, value string) (map[string]float64, error) {
	sizes, err := flaglist.Sizes(name, value)
	if err != nil {
		return nil, err
	}
	return sizesByPair(name, pairs, sizes)
}

// lotsByPair reads -lot: the lot of every pair of -pair by its name, PAIR=SIZE,
// the pairs separated by ;, eg: BTCUSDm=0.01;XAUUSDm=0.02, the decimals with a
// dot. A size for all the pairs, sizes in the order of -pair and a comma are
// refused. It returns nil without any: a share of the balance.
func lotsByPair(pairs []string, value string) (map[string]float64, error) {
	const want = "the lot of each pair by its name, separated by ;, eg: BTCUSDm=0.01;XAUUSDm=0.02"
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if strings.Contains(value, ",") {
		return nil, fmt.Errorf("-lot: %q has a comma, the decimals go with a dot: %s", value, want)
	}
	var sizes []string
	for _, item := range strings.Split(value, ";") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		if !strings.Contains(item, "=") {
			return nil, fmt.Errorf("-lot: %q isn't PAIR=SIZE: %s", item, want)
		}
		sizes = append(sizes, item)
	}
	lots, err := sizesByPair("-lot", pairs, sizes)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, want)
	}
	return lots, nil
}

// sizesByPair maps the positive sizes of the flag name to the pairs: one for
// all of them, one per pair in the same order, or one by pair, each PAIR=SIZE,
// eg: BTCUSDm=0.03,XAUUSDm=0.01, every pair with its own. It returns nil
// without any.
func sizesByPair(name string, pairs, sizes []string) (map[string]float64, error) {
	if len(sizes) == 0 {
		return nil, nil
	}
	parse := func(pair, size string) (float64, error) {
		value, err := strconv.ParseFloat(strings.TrimSpace(size), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			return 0, fmt.Errorf("%s: invalid size %q for %s", name, size, pair)
		}
		return value, nil
	}

	values := make(map[string]float64, len(pairs))
	named := 0
	for _, size := range sizes {
		if strings.Contains(size, "=") {
			named++
		}
	}
	if named > 0 {
		if named != len(sizes) {
			return nil, fmt.Errorf("%s: every size by its pair, PAIR=SIZE, or none", name)
		}
		for _, item := range sizes {
			pair, size, _ := strings.Cut(item, "=")
			pair = strings.TrimSpace(pair)
			switch _, seen := values[pair]; {
			case !slices.Contains(pairs, pair):
				return nil, fmt.Errorf("%s: %q isn't a pair of -pair (%s)", name, pair, strings.Join(pairs, ","))
			case seen:
				return nil, fmt.Errorf("%s: %s twice", name, pair)
			}
			value, err := parse(pair, size)
			if err != nil {
				return nil, err
			}
			values[pair] = value
		}
		for _, pair := range pairs {
			if _, ok := values[pair]; !ok {
				return nil, fmt.Errorf("%s: no size for %s", name, pair)
			}
		}
		return values, nil
	}

	if len(sizes) != 1 && len(sizes) != len(pairs) {
		return nil, fmt.Errorf("%s: one size for all the pairs or one per pair, not %d for %d pairs", name,
			len(sizes), len(pairs))
	}
	for i, pair := range pairs {
		size := sizes[0]
		if len(sizes) > 1 {
			size = sizes[i]
		}
		value, err := parse(pair, size)
		if err != nil {
			return nil, err
		}
		values[pair] = value
	}
	return values, nil
}

// parseMT5Size reads -mt5-size: how -lot is read on MT5, in lots (converted
// into units of the base asset with the contract size of the pair) or in units
// of the base asset.
func parseMT5Size(value string) (inLots bool, err error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "lot", "lots":
		return true, nil
	case "unit", "units":
		return false, nil
	}
	return false, fmt.Errorf("-mt5-size: %q is neither lot nor unit", value)
}

// mt5TokenEnv is the environment variable of the token of the bridge, read
// when -mt5-token is not set.
const mt5TokenEnv = "MT5_BRIDGE_TOKEN"

// mt5Token returns the token of the bridge: the one of -mt5-token, or else the
// one of MT5_BRIDGE_TOKEN.
func mt5Token(token string) string {
	if token = strings.TrimSpace(token); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv(mt5TokenEnv))
}

// mt5Magic returns the magic number of the bot: the one of -mt5-magic, or a
// stable hash of the comment, a positive int32, so that the bot finds its
// positions again after a restart, as the controller computes it (see
// agentapi.MT5Magic).
func mt5Magic(magic int64, comment string) int64 {
	return agentapi.MT5Magic(magic, comment)
}

// mt5Comment returns the comment of the bot's orders: the one of -mt5-comment,
// trimmed, or nb:llm, as the controller computes it (see agentapi.MT5Comment).
func mt5Comment(comment string) string {
	return agentapi.MT5Comment(comment, "llm")
}
