package llm

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
)

// The context tells the model what its numbers mean, and leaves out the trend
// of the averages to a strategy that ignores them.
func TestTrader_Instrument(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) {
		c.TickVolume = true
		c.Indicators = []string{"atr", "volume"}
	}, 0)
	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	in := r.provider.last()
	require.Equal(t, tickVolume, in.Instrument.Volume)
	require.Equal(t, number(10), in.Instrument.ValuePerPriceUnit, "10 units: 10 of the quote for a move of 1")
	require.Equal(t, "USDT", in.Instrument.QuoteCurrency)
	require.Empty(t, in.Summary.Trend, "the averages ignored")
	data, err := json.Marshal(in)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &fields))
	for _, key := range []string{"structure", "session", "instrument", "memory"} {
		require.Contains(t, fields, key)
	}
	require.NotContains(t, fields, "higher_timeframes", "without a feeder")
	require.NotContains(t, string(fields["summary"]), `"trend"`)

	traded := newTraderRunner(t, nil, 0)
	traded.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	require.Equal(t, tradedVolume, traded.provider.last().Instrument.Volume)
	require.NotEmpty(t, traded.provider.last().Summary.Trend, "the averages used")
}

func TestNew_InvalidContext(t *testing.T) {
	for name, tc := range map[string]struct {
		configure func(*Config)
		want      string
	}{
		"structure":         {func(c *Config) { c.StructureCandles = 3 }, "the candles of the structure"},
		"memory":            {func(c *Config) { c.Memory = -1 }, "can't be negative"},
		"lower timeframe":   {func(c *Config) { c.HigherTimeframes = []string{"5m"} }, "isn't above 15m"},
		"invalid timeframe": {func(c *Config) { c.HigherTimeframes = []string{"x"} }, `invalid timeframe "x"`},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.configure(&cfg)
			_, err := New(cfg, &fakeProvider{t: t})
			require.ErrorContains(t, err, tc.want)
		})
	}

	cfg := DefaultConfig()
	cfg.HigherTimeframes = []string{}
	trader, err := New(cfg, &fakeProvider{t: t})
	require.NoError(t, err)
	require.Empty(t, trader.higher, "none")
	cfg.HigherTimeframes = nil
	trader, err = New(cfg, &fakeProvider{t: t})
	require.NoError(t, err)
	require.Equal(t, []string{"1h", "4h"}, trader.higher, "the ones of 15m")
}

// pointedWallet is a paper wallet whose cash is in the currency of an account
// valuing a move of 1 of the price of a unit at point, as MT5 on a BRL account.
type pointedWallet struct {
	*exchange.PaperWallet
	point float64
}

func (w *pointedWallet) PointValue(string) (float64, error) { return w.point, nil }

// On an account in another currency than the quote, eg: BRL trading BTCUSDT,
// the money of the context is the account's: the balance as the broker tells
// it, the entry sized and valued by the point value of the pair.
func TestTrader_AccountCurrency(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) { c.PositionSize, c.PositionFraction, c.Leverage = nil, 0.1, 1 }, 0)
	r.broker = &pointedWallet{PaperWallet: r.wallet, point: 5}
	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	in := r.provider.last()
	require.Equal(t, number(10000), in.Balance)
	require.Equal(t, number(2), in.EntrySize, "a tenth of 10000 over 100 x 5")
	require.Equal(t, number(10), in.Instrument.ValuePerPriceUnit, "2 units at 5 for a move of 1")
	require.Equal(t, "USDT", in.Instrument.Currency, "without an account, the quote of the wallet")

	// a fixed size, valued by the point value: 10 units at 100 x 5 is worth
	// 5000, within the balance
	fixed := newTraderRunner(t, nil, 0)
	fixed.broker = &pointedWallet{PaperWallet: fixed.wallet, point: 5}
	fixed.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	require.Equal(t, number(10), fixed.provider.last().EntrySize)
	require.Equal(t, number(50), fixed.provider.last().Instrument.ValuePerPriceUnit)
}
