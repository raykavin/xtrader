package llm

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
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
