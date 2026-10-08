package runner

import (
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/exchange"
)

// exchanges are the names of -exchange: MetaTrader 5 only.
var exchanges = []string{"mt5"}

// mt5TokenEnv is the environment variable of the token of the bridge, read
// when -mt5-token is not set.
const mt5TokenEnv = "MT5_BRIDGE_TOKEN"

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

// options returns the options of the MT5 adapter but the leverage. Without
// trading (the paper mode) the adapter reads the market data only
// (exchange.WithMT5DataOnly): it never nets nor closes the positions of a live
// agent trading the account with the same magic number, and refuses the order
// methods.
func (v mt5Venue) options(pairs []string, trading bool) []exchange.MT5Option {
	options := []exchange.MT5Option{
		exchange.WithMT5BaseURL(v.bridge),
		exchange.WithMT5Symbols(pairs...),
		exchange.WithMT5Magic(v.magic),
		exchange.WithMT5Comment(v.comment),
	}
	if v.token != "" {
		options = append(options, exchange.WithMT5Token(v.token))
	}
	if v.live {
		options = append(options, exchange.WithMT5Real())
	}
	if v.poll > 0 {
		options = append(options, exchange.WithMT5PollInterval(v.poll))
	}
	if !trading {
		options = append(options, exchange.WithMT5DataOnly())
	}
	return options
}

// mt5Token returns the token of the bridge: the one of -mt5-token, or else the
// one of MT5_BRIDGE_TOKEN.
func mt5Token(token string) string {
	if token = strings.TrimSpace(token); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv(mt5TokenEnv))
}

// mt5Magic returns the magic number of the bot: the one of -mt5-magic, or a
// stable hash of its comment (see mt5Comment), as the controller computes it
// (see agentapi.MT5Magic), so that the same bot finds its positions again
// after a restart, and two bots of a strategy with comments of their own keep
// theirs apart.
func mt5Magic(magic int64, comment string) int64 {
	return agentapi.MT5Magic(magic, comment)
}

// mt5Comment returns the comment of the bot's orders: the one of -mt5-comment,
// or "nb:" followed by the name of the strategy, cut to the 31 characters MT5
// keeps, as the controller computes it (see agentapi.MT5Comment).
func mt5Comment(comment, strategy string) string {
	return agentapi.MT5Comment(comment, strategy)
}

// parseMT5Size reads -mt5-size: how -lot is read on MT5, in lots ("lot",
// converted into units of the base asset with the contract size of the pair)
// or in units of the base asset ("unit").
func parseMT5Size(value string) (inLots bool, err error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "lot", "lots":
		return true, nil
	case "unit", "units":
		return false, nil
	}
	return false, fmt.Errorf("-mt5-size: %q is neither lot nor unit", value)
}

// checkVenue checks the flags of the exchange against the mode: the contract
// sizes of MT5 in backtests only, the leverage of its margin live only.
func checkVenue(exchangeName, mode string, set map[string]bool, leverage float64) error {
	if !slices.Contains(exchanges, exchangeName) {
		return fmt.Errorf("invalid exchange %q: %s", exchangeName, strings.Join(exchanges, ", "))
	}
	switch {
	case set["mt5-contract-size"] && mode != "backtest":
		return errors.New("-mt5-contract-size is for backtests: the paper and live modes read the contract " +
			"sizes of the account")
	case leverage > 1 && mode != "live":
		return errors.New("-leverage needs the margin of the MT5 account: -mode live")
	}
	return nil
}

// mt5Contracts returns the contract sizes of the pairs for a backtest, the
// units of the base asset in a lot: the ones of -mt5-contract-size, one for
// all the pairs or one per pair in the same order, else the ones the package
// knows (see exchange.MT5ContractSize).
func mt5Contracts(pairs, sizes []string) (map[string]float64, error) {
	contracts, err := sizesByPair("-mt5-contract-size", pairs, sizes)
	if err != nil || contracts != nil {
		return contracts, err
	}

	contracts = make(map[string]float64, len(pairs))
	for _, pair := range pairs {
		size, ok := exchange.MT5ContractSize(pair)
		if !ok {
			return nil, fmt.Errorf("-mt5-contract-size: the contract size of %s is unknown, set it, eg: 100 "+
				"for XAUUSDm (ounces in a lot), or pass -lot in units with -mt5-size unit", pair)
		}
		contracts[pair] = size
	}
	return contracts, nil
}

// lotsToUnits converts in place the sizes of lots, in MT5 lots, into units of
// the base asset: units = lots x contract size. The strategies share the map,
// so they trade the converted sizes as long as the bot hasn't started.
func lotsToUnits(lots, contracts map[string]float64) error {
	for pair, lot := range lots {
		contract := contracts[pair]
		if contract <= 0 || math.IsNaN(contract) || math.IsInf(contract, 0) {
			return fmt.Errorf("-lot: the contract size of %s is unknown, so its lots can't be converted", pair)
		}
		lots[pair] = roundSize(lot * contract)
	}
	return nil
}

// roundSize drops the float noise of a size, eg: 0.07 x 100 = 7.000000000000001.
func roundSize(size float64) float64 {
	return math.Round(size*1e9) / 1e9
}

// describeSize describes a size of pair in units of its base asset, followed
// by the lots it is when its contract size is known, eg: "0.05 lot = 5 XAU".
func describeSize(pair string, units, contract float64) string {
	if contract <= 0 {
		return fmt.Sprintf("%g", units)
	}
	asset, _ := exchange.SplitAssetQuote(pair)
	if asset == "" {
		asset = "units"
	}
	return fmt.Sprintf("%g lot = %g %s", roundSize(units/contract), roundSize(units), asset)
}
