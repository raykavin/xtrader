package strategies

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

func newZScore(t *testing.T, configure func(*ZScoreReversionConfig)) *ZScoreReversion {
	t.Helper()
	cfg := DefaultZScoreReversionConfig()
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewZScoreReversion(cfg)
	require.NoError(t, err)
	return s
}

func TestNewZScoreReversion_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ZScoreReversionConfig)
		err       string
	}{
		{"timeframe", func(c *ZScoreReversionConfig) { c.Timeframe = "" }, "timeframe"},
		{"mean period", func(c *ZScoreReversionConfig) { c.MeanLen = 0 }, "mean period"},
		{"deviation period", func(c *ZScoreReversionConfig) { c.StdLen = 1 }, "standard deviation"},
		{"entry", func(c *ZScoreReversionConfig) { c.EntryZ = 0 }, "entry Z-Score"},
		{"exit beyond the entry", func(c *ZScoreReversionConfig) { c.ExitZ = 2 }, "exit Z-Score"},
		{"exit beyond the other entry", func(c *ZScoreReversionConfig) { c.ExitZ = -2.5 }, "exit Z-Score"},
		{"negative ADX period", func(c *ZScoreReversionConfig) { c.ADXLen = -1 }, "ADX period"},
		{"max ADX", func(c *ZScoreReversionConfig) { c.MaxADX = 0 }, "max ADX"},
		{"max ADX out of range", func(c *ZScoreReversionConfig) { c.MaxADX = 120 }, "max ADX"},
		{"the exits", func(c *ZScoreReversionConfig) { c.TakeProfitATR = 0 }, "take profit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultZScoreReversionConfig()
			tt.configure(&cfg)
			_, err := NewZScoreReversion(cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}

	// the max ADX is not read without the filter
	_, err := NewZScoreReversion(func() ZScoreReversionConfig {
		cfg := DefaultZScoreReversionConfig()
		cfg.ADXLen, cfg.MaxADX = 0, 0
		return cfg
	}())
	require.NoError(t, err)
}

func TestZScoreReversion_WarmupPeriod(t *testing.T) {
	// the ADX, smoothed twice over 14 candles, seeded, and the previous candle
	require.Equal(t, seedWarmup(28)+1, newZScore(t, nil).WarmupPeriod())
	require.Equal(t, seedWarmup(14)+1, newZScore(t, func(c *ZScoreReversionConfig) { c.ADXLen = 0 }).WarmupPeriod())
	require.Equal(t, 500+1, newZScore(t, func(c *ZScoreReversionConfig) { c.ADXLen, c.MeanLen = 0, 500 }).WarmupPeriod())
}

// newZSRunner feeds a ZScoreReversion with the ATR at 1 and the ADX at 20.
func newZSRunner(t *testing.T, configure func(*ZScoreReversionConfig)) *ivRunner {
	t.Helper()
	r := newIVRunner(t, newZScore(t, configure), 0)
	r.fixed["zs_atr"], r.fixed["zs_adx"] = 1, 20
	return r
}

// zs is a candle at 100 with the Z-Score z, holding position after it.
func zs(z, position float64) ivCandle {
	return ivCandle{close: 100, meta: map[string]float64{"zs_z": z}, position: position}
}

func TestZScoreReversion_Entries(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ZScoreReversionConfig)
		candles   []ivCandle
	}{
		{name: "buy on Z crossing down to -EntryZ", candles: []ivCandle{zs(-1.5, 0), zs(-2, 100)}},
		{name: "sell on Z crossing up to EntryZ", candles: []ivCandle{zs(1.5, 0), zs(2.1, -100)}},
		{name: "no entry inside the thresholds", candles: []ivCandle{zs(-1.5, 0), zs(-1.9, 0), zs(1.9, 0)}},
		{name: "no entry while Z stays beyond the threshold", candles: []ivCandle{zs(-2.5, 0), zs(-2.6, 0)}},
		{
			name:      "the threshold is configurable",
			configure: func(c *ZScoreReversionConfig) { c.EntryZ = 3 },
			candles:   []ivCandle{zs(-1, 0), zs(-2.5, 0), zs(-3.1, 100)},
		},
		{
			name: "the regime filter keeps a trending market out",
			candles: []ivCandle{
				zs(-1.5, 0),
				{close: 100, meta: map[string]float64{"zs_z": -2.2, "zs_adx": 30}},
			},
		},
		{
			name:      "the regime filter at its threshold",
			configure: func(c *ZScoreReversionConfig) { c.MaxADX = 30 },
			candles: []ivCandle{
				zs(-1.5, 0),
				{close: 100, meta: map[string]float64{"zs_z": -2.2, "zs_adx": 29.9}, position: 100},
			},
		},
		{
			name: "no entry without the ADX",
			candles: []ivCandle{
				zs(-1.5, 0),
				{close: 100, meta: map[string]float64{"zs_z": -2.2, "zs_adx": math.NaN()}},
			},
		},
		{
			name:      "no regime filter with ADXLen at 0",
			configure: func(c *ZScoreReversionConfig) { c.ADXLen = 0 },
			candles: []ivCandle{
				zs(-1.5, 0),
				{close: 100, meta: map[string]float64{"zs_z": -2.2, "zs_adx": 60}, position: 100},
			},
		},
		{name: "no entry without Z", candles: []ivCandle{zs(-1.5, 0), zs(math.NaN(), 0)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newZSRunner(t, tt.configure).feed(tt.candles...)
		})
	}
}

func TestZScoreReversion_Exits(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ZScoreReversionConfig)
		candles   []ivCandle
	}{
		{name: "the long closes at the mean", candles: []ivCandle{zs(-1.5, 0), zs(-2, 100), zs(-0.5, 100), zs(0, 0)}},
		{name: "the short closes at the mean", candles: []ivCandle{zs(1.5, 0), zs(2, -100), zs(0.5, -100), zs(-0.1, 0)}},
		{
			name:      "the exit is configurable",
			configure: func(c *ZScoreReversionConfig) { c.ExitZ = 0.5 },
			candles:   []ivCandle{zs(-1.5, 0), zs(-2, 100), zs(-0.8, 100), zs(-0.5, 0)},
		},
		{
			name:      "past the mean with a negative exit",
			configure: func(c *ZScoreReversionConfig) { c.ExitZ = -0.5 },
			candles:   []ivCandle{zs(-1.5, 0), zs(-2, 100), zs(0.2, 100), zs(0.5, 0)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newZSRunner(t, tt.configure).feed(tt.candles...)
		})
	}
}

func TestZScoreReversion_Indicators(t *testing.T) {
	s := newZScore(t, func(c *ZScoreReversionConfig) { c.MeanLen, c.StdLen = 3, 4 })
	closes := []float64{10, 12, 11, 13, 13, 13, 13}
	df := &model.Dataframe{Pair: "BTCUSDT", Close: closes, High: closes, Low: closes,
		Metadata: map[string]model.Series[float64]{}}
	for i := range closes {
		df.Time = append(df.Time, time.Unix(int64(i)*300, 0))
	}
	s.Indicators(df)

	// Z = (close - SMA 3) / stdev 4, NaN until both are known and while the
	// deviation is 0
	mean, deviation := pineSMA(closes, 3), pineStdev(closes, 4)
	z := df.Metadata["zs_z"]
	requireSeries(t, []float64{na, na, na, (13 - mean[3]) / deviation[3], (13 - mean[4]) / deviation[4],
		(13 - mean[5]) / deviation[5], na}, z, 1e-12)
	require.InDelta(t, (13-12)/math.Sqrt(1.25), z[3], 1e-12, "the SMA of 12, 11, 13 and the stdev of 10, 12, 11, 13")
	require.Len(t, df.Metadata["zs_adx"], len(closes))
}
