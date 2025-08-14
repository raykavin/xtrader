package llm

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// names returns the names of the series of the extras.
func names(extras []*extra) []string {
	var list []string
	for _, e := range extras {
		for _, s := range e.series {
			list = append(list, s.name)
		}
	}
	return list
}

func TestParseExtras(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Indicators = []string{"sma", "ema 8 13 21 34 55", "atr 14 50", "supertrend", "psar 0.02 0.2", "ichimoku",
		"stoch", "donchian 55", "keltner", "chandelier 22 3", "vwap 09:30 America/New_York", "roc", "macd 12 26 9",
		"bb 20 2.5", "adx 10", "sma 50", "rsi 7", "volume 30", "range 09:30 09:45 12:00 America/New_York"}
	extras, err := parseExtras(cfg)
	require.NoError(t, err)
	require.Len(t, extras, len(cfg.Indicators)-1, "the group of the Config alone isn't an extra")
	require.Equal(t, []string{
		"ema_8", "ema_13", "ema_21", "ema_34", "ema_55", "atr_14", "atr_14_sma_50", "supertrend_10_3",
		"supertrend_side_10_3", "psar_0.02_0.2", "psar_side_0.02_0.2", "ichimoku_tenkan_9", "ichimoku_kijun_26",
		"ichimoku_senkou_a", "ichimoku_senkou_b_52", "stoch_k_14_3", "stoch_d_3", "donchian_upper_55",
		"donchian_lower_55", "keltner_upper_20_10_2", "keltner_middle_20", "keltner_lower_20_10_2",
		"chandelier_stop_22_3", "chandelier_side_22_3", "vwap", "roc_10", "macd_12_26", "macd_signal_9",
		"macd_histogram_12_26_9", "bb_upper_20_2.5", "bb_middle_20", "bb_lower_20_2.5", "adx_10", "plus_di_10",
		"minus_di_10", "sma_50", "rsi_7", "volume_sma_30", "range_high", "range_low",
	}, names(extras))

	for spec, want := range map[string]string{
		"heikinashi":                     "unknown kind",
		"macd 12":                        "macd takes 3 to 3 parameters, not 1",
		"ema 0":                          "whole number of 1 at least",
		"ema 9":                          "ema_9 is sent by another one", // with the group ema
		"macd 26 12 9":                   "the fast average must be shorter",
		"psar 0.3 0.2":                   "is above the max",
		"bb 20 -2":                       "must be a positive number",
		"vwap 25:00":                     "must be HH:MM",
		"vwap 00:00 Mars/Olympus":        "the time zone",
		"range 09:45 09:30 12:00 UTC":    "the range starts before it ends",
		"range 09:30 09:45 12:00":        "range takes 4 to 4 parameters",
		"chandelier 22 3, supertrend 10": "chandelier takes 2 to 2 parameters", // a comma inside one
	} {
		cfg := DefaultConfig()
		cfg.Indicators = []string{"ema", spec}
		_, err := New(cfg, &fakeProvider{t: t})
		require.ErrorContains(t, err, want, spec)
	}

	require.Equal(t, "ema 20 50", NormalizeIndicator(" EMA+20+50 "))
	require.Equal(t, "vwap 09:30 America/New_York", NormalizeIndicator("vwap+09:30+America/New_York"))
	require.Empty(t, NormalizeIndicator(" "))
}

// The indicators the strategy declares are computed, sent and drawn; the ones
// of the Config it doesn't declare aren't, but the ATR of the rules.
func TestTrader_ExtraIndicators(t *testing.T) {
	declared := []string{"ema 20 50", "supertrend 10 3", "donchian 20", "stoch 14 3 3", "ichimoku 9 26 52",
		"keltner 20 10 2", "chandelier 22 3", "psar 0.02 0.2", "roc 10", "vwap 00:00 UTC", "atr 14 50"}
	r := newTraderRunner(t, func(c *Config) { c.Indicators = declared }, 0)
	require.GreaterOrEqual(t, r.trader.WarmupPeriod(), 5*50, "the EMA 50 seeded")
	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	in := r.provider.last()

	extras, err := parseExtras(r.trader.cfg)
	require.NoError(t, err)
	got := make([]string, 0, len(in.Indicators))
	for name, values := range in.Indicators {
		got = append(got, name)
		require.Len(t, values, r.trader.cfg.History, name)
		require.True(t, known(float64(values[len(values)-1])), "%s computed", name)
	}
	require.ElementsMatch(t, names(extras), got, "the ones declared only")
	require.Empty(t, in.Summary.Trend, "not the averages of the Config")
	require.True(t, known(float64(in.Rules.MaxEntryDistance)), "the ATR of the rules")
	for _, side := range []string{"supertrend_side_10_3", "chandelier_side_22_3", "psar_side_0.02_0.2"} {
		require.Contains(t, []float64{1, -1}, float64(in.Indicators[side][r.trader.cfg.History-1]), side)
	}

	drawn := r.trader.Indicators(r.df)
	require.True(t, drawn[0].Overlay)
	overlay := make([]string, 0, len(drawn[0].Metrics))
	for _, metric := range drawn[0].Metrics {
		overlay = append(overlay, metric.Name)
	}
	require.Contains(t, overlay, "supertrend_10_3")
	require.NotContains(t, overlay, "supertrend_side_10_3", "sent, not drawn")
	panes := make([]string, 0, len(drawn)-1)
	for _, pane := range drawn[1:] {
		panes = append(panes, pane.GroupName)
	}
	require.Equal(t, []string{"Stochastic 14 3 3", "ROC 10", "ATR 14, SMA 50"}, panes)

	// fewer candles than the indicators need, eg: the chart of a bot starting:
	// missing values rather than a failure of talib
	short := &model.Dataframe{Pair: testPair, Metadata: map[string]model.Series[float64]{},
		Time: r.df.Time[:5], Open: r.df.Open[:5], High: r.df.High[:5], Low: r.df.Low[:5], Close: r.df.Close[:5],
		Volume: r.df.Volume[:5]}
	r.trader.Indicators(short)
	require.True(t, math.IsNaN(short.Metadata["llm_x_ichimoku_senkou_a"].Last(0)))
}

func TestDonchian(t *testing.T) {
	high := []float64{10, 12, 11, 13, 9}
	low := []float64{8, 9, 7, 10, 6}
	upper, lower := donchian(high, low, 2)
	require.True(t, math.IsNaN(upper[1]), "the channel of the 2 candles before")
	require.Equal(t, 12.0, upper[2], "10 and 12, not the 11 of the candle")
	require.Equal(t, 8.0, lower[2])
	require.Equal(t, 13.0, upper[4])
	require.Equal(t, 7.0, lower[4])
}

func TestSideOf(t *testing.T) {
	side := sideOf([]float64{100, 100, 100, 100, 100}, []float64{0, 99, 101, 100, 98})
	require.True(t, math.IsNaN(side[0]), "talib's 0: unknown")
	require.Equal(t, []float64{1, -1, -1, 1}, side[1:], "a tie keeps the side")
}

func TestRateOfChange(t *testing.T) {
	roc := rateOfChange([]float64{100, 105, 110}, 2)
	require.True(t, math.IsNaN(roc[1]))
	require.InDelta(t, 10, roc[2], 1e-9)
}

func TestIchimokuSpans(t *testing.T) {
	high := []float64{10, 11, 12, 13, 14, 15}
	low := []float64{8, 9, 10, 11, 12, 13}
	tenkan, kijun, spanA, spanB := ichimoku(high, low, 2, 3, 4)
	require.Equal(t, 9.5, tenkan[1])
	require.Equal(t, 11.0, kijun[3])
	require.True(t, math.IsNaN(spanA[1]), "drawn 2 candles ahead")
	require.Equal(t, (tenkan[3]+kijun[3])/2, spanA[5])
	require.Equal(t, (high[3]+low[0])/2, spanB[5])
}

func TestChandelierSide(t *testing.T) {
	var high, low, closes []float64
	for i := range 30 {
		c := 100 + float64(i)
		closes, high, low = append(closes, c), append(high, c+0.5), append(low, c-0.5)
	}
	closes, high, low = append(closes, 110), append(high, 110.5), append(low, 109.5) // a drop

	side, stop := chandelier(high, low, closes, 3, 1)
	require.True(t, math.IsNaN(side[2]), "warming up")
	require.Equal(t, 1.0, side[29], "up along the rise")
	require.Less(t, stop[29], closes[29])
	require.Equal(t, -1.0, side[30], "the drop below the stop of the longs")
	require.Greater(t, stop[30], closes[30], "the stop of the shorts above")
}

func TestAnchoredVWAP(t *testing.T) {
	start := time.Date(2026, 9, 25, 22, 0, 0, 0, time.UTC)
	var times []time.Time
	var high, low, closes, volume []float64
	for i := range 8 { // 22:00 to 03:30, every 30 minutes... up to 01:30
		times = append(times, start.Add(time.Duration(i)*30*time.Minute))
		c := 100 + float64(i)
		high, low, closes, volume = append(high, c), append(low, c), append(closes, c), append(volume, 1+float64(i%2))
	}
	vwap := anchoredVWAP(times, high, low, closes, volume, 0, time.UTC)
	for i := range 4 {
		require.True(t, math.IsNaN(vwap[i]), "the day before doesn't reach back to its anchor")
	}
	require.Equal(t, 104.0, vwap[4], "00:00: anchored again")
	require.InDelta(t, (104*1+105*2)/3.0, vwap[5], 1e-9, "weighted by the volume")
}

func TestSessionRange(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	// 15m candles from 09:00 New York (13:00 UTC, EDT) to 12:30
	start := time.Date(2026, 9, 25, 13, 0, 0, 0, time.UTC)
	var times []time.Time
	var high, low []float64
	for i := range 15 {
		times = append(times, start.Add(time.Duration(i)*15*time.Minute))
		high, low = append(high, 100+float64(i)), append(low, 90-float64(i))
	}
	highs, lows := sessionRange(times, high, low, 9*time.Hour+30*time.Minute, 10*time.Hour, 12*time.Hour, ny)
	for i := range 4 {
		require.True(t, math.IsNaN(highs[i]), "before the end of the range, 09:00 to 09:45")
	}
	require.Equal(t, 103.0, highs[4], "10:00: the candles of 09:30 and 09:45")
	require.Equal(t, 87.0, lows[4])
	require.Equal(t, 103.0, highs[11], "11:45, still traded")
	require.True(t, math.IsNaN(highs[12]), "12:00: over")

	// a window starting after the start of the range doesn't know it
	late, _ := sessionRange(times[3:], high[3:], low[3:], 9*time.Hour+30*time.Minute, 10*time.Hour, 12*time.Hour, ny)
	require.True(t, slices.ContainsFunc(late, func(v float64) bool { return math.IsNaN(v) }))
	require.True(t, math.IsNaN(late[1]))
}

// An entry without a take profit, when the rules allow it: the stop loss alone
// at the broker, and no risk:reward.
func TestTrader_TakeProfitOptional(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) { c.TakeProfitOptional, c.MinRiskReward = true, 3 }, 0)
	r.step(100, decide(ActionEnterLong, 0.8, ptr(100), ptr(98), nil))
	require.True(t, r.provider.last().Rules.TakeProfitOptional)
	require.InDelta(t, 10, r.position(), 1e-9)
	tr := r.trader.trades[testPair]
	require.Len(t, tr.legs, 1, "the stop loss alone")
	require.Equal(t, model.OrderTypeStopLoss, tr.legs[0].Type)

	r.step(100.5, decide(ActionHold, 0.7, nil, nil, nil))
	require.NotContains(t, r.provider.last().Rules.Actions, ActionModifyTP, "no take profit to move")

	required := newTraderRunner(t, nil, 0)
	required.step(100, decide(ActionEnterLong, 0.8, ptr(100), ptr(98), nil))
	require.Zero(t, required.position(), "a take profit required")
}

func TestDecision_ValidateWithoutTakeProfit(t *testing.T) {
	in := testInput(nil, flatActions...)
	long := Decision{Action: ActionEnterLong, Confidence: 0.8, Entry: ptr(100), StopLoss: ptr(98), Reason: "ok"}
	require.ErrorContains(t, long.Validate(in), "needs an entry, a stop loss and a take profit")

	in.Rules.TakeProfitOptional, in.Rules.MinRiskReward = true, 2
	require.NoError(t, long.Validate(in), "no risk:reward without a take profit")
	wrong := long
	wrong.StopLoss = ptr(101)
	require.ErrorContains(t, wrong.Validate(in), "must be below")
	far := long
	far.StopLoss = ptr(80)
	require.ErrorContains(t, far.Validate(in), "over the 10 allowed")
	in.Rules.MinStopDistance = 3
	require.ErrorContains(t, long.Validate(in), "inside the noise")
}
