package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// fxRateFor is how long a rate of the daily limits is kept.
const fxRateFor = time.Minute

// fxRates converts the results of the accounts in another currency into the
// one of the daily limits, at the mid price of the symbol converting them on
// the bridge of an account, as the MT5 adapter does (see exchange.MT5.rate):
// from+to, or to+from inverted, with the suffix of the symbols its agents
// trade (eg: USDBRLm inverted for BRL into USD); the rates kept fxRateFor, the
// symbols found kept for good.
type fxRates struct {
	now func() time.Time

	mu      sync.Mutex
	rates   map[string]fxRate          // by from+to
	symbols map[string]fxConversion    // by bridge+from+to
	suffix  map[string]map[string]bool // the suffixes of the symbols of a bridge
}

type fxRate struct {
	value float64
	at    time.Time
}

// fxConversion is the symbol converting one currency into another.
type fxConversion struct {
	symbol  string
	inverse bool
}

// fxSymbol is the answer of GET /symbol of the bridge.
type fxSymbol struct {
	Name           string `json:"name"`
	CurrencyBase   string `json:"currency_base"`
	CurrencyProfit string `json:"currency_profit"`
}

// fxTick is the answer of GET /tick of the bridge.
type fxTick struct {
	Bid float64 `json:"bid"`
	Ask float64 `json:"ask"`
}

func newFXRates(now func() time.Time) *fxRates {
	return &fxRates{now: now, rates: make(map[string]fxRate), symbols: make(map[string]fxConversion),
		suffix: make(map[string]map[string]bool)}
}

// rate returns the value in to of one unit of from, read with get on bridge,
// whose agents trade pairs (their suffixes tried): 1 for the same currency.
func (f *fxRates) rate(ctx context.Context, bridge string, pairs []string, from, to string,
	get func(path string, out any) error) (float64, error) {
	if from == to || from == "" || to == "" {
		return 1, nil
	}
	f.mu.Lock()
	cached, ok := f.rates[from+to]
	f.mu.Unlock()
	if ok && f.now().Sub(cached.at) < fxRateFor {
		return cached.value, nil
	}
	conversion, err := f.conversion(ctx, bridge, pairs, from, to, get)
	if err != nil {
		return 0, err
	}
	var tick fxTick
	if err := get("/tick?"+url.Values{"symbol": {conversion.symbol}}.Encode(), &tick); err != nil {
		return 0, fmt.Errorf("the price of %s: %w", conversion.symbol, err)
	}
	price := tick.Bid
	if tick.Bid > 0 && tick.Ask > 0 {
		price = (tick.Bid + tick.Ask) / 2
	} else if price <= 0 {
		price = tick.Ask
	}
	if price <= 0 {
		return 0, fmt.Errorf("no price for %s, converting %s into %s", conversion.symbol, from, to)
	}
	value := price
	if conversion.inverse {
		value = 1 / price
	}
	f.mu.Lock()
	f.rates[from+to] = fxRate{value: value, at: f.now()}
	f.mu.Unlock()
	return value, nil
}

// conversion finds the symbol converting from into to on bridge.
func (f *fxRates) conversion(ctx context.Context, bridge string, pairs []string, from, to string,
	get func(path string, out any) error) (fxConversion, error) {
	f.mu.Lock()
	conversion, ok := f.symbols[bridge+from+to]
	f.mu.Unlock()
	if ok {
		return conversion, nil
	}
	suffixes, err := f.suffixes(bridge, pairs, get)
	if err != nil {
		return fxConversion{}, err
	}
	var tried []string
	for _, suffix := range suffixes {
		for _, candidate := range []fxConversion{{symbol: from + to + suffix},
			{symbol: to + from + suffix, inverse: true}} {
			if ctx.Err() != nil {
				return fxConversion{}, ctx.Err()
			}
			tried = append(tried, candidate.symbol)
			var symbol fxSymbol
			err := get("/symbol?"+url.Values{"symbol": {candidate.symbol}}.Encode(), &symbol)
			var failed *bridgeFailure
			switch {
			case errors.As(err, &failed) && failed.status == http.StatusNotFound:
				continue
			case err != nil:
				return fxConversion{}, fmt.Errorf("the symbol %s: %w", candidate.symbol, err)
			}
			f.mu.Lock()
			f.symbols[bridge+from+to] = candidate
			f.mu.Unlock()
			return candidate, nil
		}
	}
	return fxConversion{}, fmt.Errorf("no symbol converts %s into %s, tried %s", from, to, strings.Join(tried, ", "))
}

// suffixes returns the suffixes of the symbols of bridge, after their
// currencies (eg: "m" for XAUUSDm), the ones of pairs read once, "" last.
func (f *fxRates) suffixes(bridge string, pairs []string, get func(path string, out any) error) ([]string, error) {
	f.mu.Lock()
	known := f.suffix[bridge]
	f.mu.Unlock()
	if known == nil {
		known = make(map[string]bool)
		for _, pair := range pairs {
			var symbol fxSymbol
			if err := get("/symbol?"+url.Values{"symbol": {pair}}.Encode(), &symbol); err != nil {
				return nil, fmt.Errorf("the symbol %s: %w", pair, err)
			}
			name := symbol.Name
			if name == "" {
				name = pair
			}
			if prefix := symbol.CurrencyBase + symbol.CurrencyProfit; prefix != "" && strings.HasPrefix(name, prefix) {
				known[name[len(prefix):]] = true
			}
		}
		f.mu.Lock()
		f.suffix[bridge] = known
		f.mu.Unlock()
	}
	list := make([]string, 0, len(known)+1)
	for suffix := range known {
		if suffix != "" {
			list = append(list, suffix)
		}
	}
	return append(list, ""), nil
}
