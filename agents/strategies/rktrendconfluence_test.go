package strategies

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xhit/go-str2duration/v2"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/tools/log"
)

// btcDataframe loads the BTCUSDT 1h sample of the repository.
func btcDataframe(t *testing.T) *model.Dataframe {
	t.Helper()

	feed, err := exchange.NewCSVFeed("1h", exchange.PairFeed{
		Pair:      "BTCUSDT",
		File:      "../../testdata/btc-1h.csv",
		Timeframe: "1h",
	})
	require.NoError(t, err)

	candles, err := feed.CandlesByPeriod(context.Background(), "BTCUSDT", "1h", time.Time{}, time.Now())
	require.NoError(t, err)

	df := &model.Dataframe{Pair: "BTCUSDT", Metadata: make(map[string]model.Series[float64])}
	for _, candle := range candles {
		df.Time = append(df.Time, candle.Time)
		df.Open = append(df.Open, candle.Open)
		df.High = append(df.High, candle.High)
		df.Low = append(df.Low, candle.Low)
		df.Close = append(df.Close, candle.Close)
		df.Volume = append(df.Volume, candle.Volume)
	}
	return df
}

// window returns the candles [start, end) of df.
func window(df *model.Dataframe, start, end int) *model.Dataframe {
	return &model.Dataframe{
		Pair:     df.Pair,
		Time:     df.Time[start:end],
		Open:     df.Open[start:end],
		High:     df.High[start:end],
		Low:      df.Low[start:end],
		Close:    df.Close[start:end],
		Volume:   df.Volume[start:end],
		Metadata: make(map[string]model.Series[float64]),
	}
}

func newRKTrendConfluence(t *testing.T, configure func(*RKTrendConfluenceConfig)) *RKTrendConfluence {
	t.Helper()

	cfg := DefaultRKTrendConfluenceConfig()
	if configure != nil {
		configure(&cfg)
	}

	s, err := NewRKTrendConfluence(cfg)
	require.NoError(t, err)
	return s
}

// bullishSeries builds candles on which every gate lets a BUY through: price
// and slow EMA above a trend MA rising 0.5 ATR per bar, a strong ADX led by
// +DI, a positive MACD histogram and RSI at 60. The EMAs decide the trigger.
func bullishSeries(emaFast, emaSlow []float64) rkSeries {
	n := len(emaFast)
	s := rkSeries{
		emaFast:  emaFast,
		emaSlow:  emaSlow,
		close:    make([]float64, n),
		trendMA:  make([]float64, n),
		macd:     make([]float64, n),
		hist:     make([]float64, n),
		rsi:      make([]float64, n),
		plusDI:   make([]float64, n),
		minusDI:  make([]float64, n),
		adx:      make([]float64, n),
		atr:      make([]float64, n),
		volRatio: make([]float64, n),
	}
	for i := 0; i < n; i++ {
		s.close[i] = 110
		s.trendMA[i] = 90 + float64(i)
		s.macd[i] = 1
		s.hist[i] = 0.5
		s.rsi[i] = 60
		s.plusDI[i] = 30
		s.minusDI[i] = 10
		s.adx[i] = 25
		s.atr[i] = 2
		s.volRatio[i] = 1
	}
	return s
}

// crossUp is the fast EMA crossing the slow EMA up on the third candle.
func crossUp() rkSeries {
	return bullishSeries([]float64{99, 99, 106, 108}, []float64{100, 100, 105, 106})
}

// mirror returns the bearish mirror image of a series, prices mirrored about
// 100, so a BUY becomes a SELL.
func mirror(s rkSeries) rkSeries {
	flip := func(values []float64, fn func(float64) float64) []float64 {
		if values == nil {
			return nil
		}
		out := make([]float64, len(values))
		for i, v := range values {
			out[i] = fn(v)
		}
		return out
	}
	price := func(v float64) float64 { return 200 - v }
	negative := func(v float64) float64 { return -v }
	same := func(v float64) float64 { return v }

	return rkSeries{
		close:    flip(s.close, price),
		emaFast:  flip(s.emaFast, price),
		emaSlow:  flip(s.emaSlow, price),
		trendMA:  flip(s.trendMA, price),
		macd:     flip(s.macd, negative),
		hist:     flip(s.hist, negative),
		rsi:      flip(s.rsi, func(v float64) float64 { return 100 - v }),
		plusDI:   flip(s.minusDI, same),
		minusDI:  flip(s.plusDI, same),
		adx:      flip(s.adx, same),
		atr:      flip(s.atr, same),
		volRatio: flip(s.volRatio, same),
		htfBias:  flip(s.htfBias, negative),
	}
}

func TestNewRKTrendConfluence_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*RKTrendConfluenceConfig)
		err       string
	}{
		{"fast EMA not shorter", func(c *RKTrendConfluenceConfig) { c.EMAFastLen = 21 }, "fast EMA period"},
		{"MACD fast not shorter", func(c *RKTrendConfluenceConfig) { c.MACDFast = 26 }, "MACD fast length"},
		{"zero period", func(c *RKTrendConfluenceConfig) { c.TrendMALen = 0 }, "at least 1"},
		{"volatility ratio band", func(c *RKTrendConfluenceConfig) {
			c.UseATRFilter = true
			c.MinVolRatio = 3
		}, "min ATR / average ATR"},
		{"volatility percent band", func(c *RKTrendConfluenceConfig) {
			c.UseATRFilter = true
			c.ATRFilterMode = RKVolatilityPercent
			c.MinATRPct = 20
		}, "minimum ATR %"},
		{"RSI band", func(c *RKTrendConfluenceConfig) {
			c.UseRSIFilter = true
			c.RSIMidline = 70
		}, "momentum floor"},
		{"HTF multiple", func(c *RKTrendConfluenceConfig) {
			c.UseHTFFilter = true
			c.HTFMult = 1
		}, "HTF multiple"},
		{"HTF chart timeframe", func(c *RKTrendConfluenceConfig) {
			c.UseHTFFilter = true
			c.Timeframe = "1M"
		}, "invalid timeframe"},
		{"HTF manual timeframe", func(c *RKTrendConfluenceConfig) {
			c.UseHTFFilter = true
			c.HTFMode = RKHTFManual
			c.HTFManual = "60"
		}, "manual higher timeframe"},
		{"negative cooldown", func(c *RKTrendConfluenceConfig) { c.CooldownBars = -1 }, "cooldown"},
		{"negative stop", func(c *RKTrendConfluenceConfig) { c.StopATR = -1 }, "ATR multiples"},
		{"negative trailing stop", func(c *RKTrendConfluenceConfig) { c.TrailingATR = -2 }, "ATR multiples"},
		{"negative broker stop", func(c *RKTrendConfluenceConfig) { c.BrokerStopATR = -1 }, "ATR multiples"},
		{"negative fixed stop", func(c *RKTrendConfluenceConfig) { c.StopValue = -1 }, "fixed stop loss"},
		{"negative fixed take profit", func(c *RKTrendConfluenceConfig) { c.TakeProfitValue = -1 },
			"fixed stop loss"},
		{"min position value", func(c *RKTrendConfluenceConfig) { c.MinPositionValue = 0 }, "min position value"},
		{"no position fraction", func(c *RKTrendConfluenceConfig) { c.PositionFraction = 0 }, "position fraction"},
		{"position fraction above 1", func(c *RKTrendConfluenceConfig) { c.PositionFraction = 1.5 },
			"position fraction"},
		{"leverage below 1", func(c *RKTrendConfluenceConfig) { c.Leverage = 0.5 }, "leverage"},
		{"whole balance leveraged", func(c *RKTrendConfluenceConfig) { c.Leverage = 20 }, "below 1"},
		{"zero position size", func(c *RKTrendConfluenceConfig) { c.PositionSize = map[string]float64{"BTCUSDT": 0} },
			"position size of BTCUSDT"},
		{"NaN position size", func(c *RKTrendConfluenceConfig) { c.PositionSize = map[string]float64{"BTCUSDT": math.NaN()} },
			"position size of BTCUSDT"},
		{"direction", func(c *RKTrendConfluenceConfig) { c.Direction = "long" }, "invalid direction"},
		{"slope mode", func(c *RKTrendConfluenceConfig) { c.SlopeMode = "" }, "invalid slope mode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultRKTrendConfluenceConfig()
			tt.configure(&cfg)
			_, err := NewRKTrendConfluence(cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}
}

func TestRKTrendConfluence_WarmupPeriod(t *testing.T) {
	// trend MA and its slope lookback (205), the cooldown (1) and ten times the
	// longest recursive period, the MACD (35)
	require.Equal(t, 556, newRKTrendConfluence(t, nil).WarmupPeriod())

	// 53 closed 4h bars, the HTF trend MA and its previous value
	htf := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) { c.UseHTFFilter = true })
	require.Equal(t, 53*4+1+350, htf.WarmupPeriod())
}

func TestRKTrendConfluenceConfig_HigherTimeframe(t *testing.T) {
	tests := []struct {
		chart  string
		mode   RKHTFMode
		mult   int
		manual string
		want   time.Duration
	}{
		{"1h", RKHTFAuto, 4, "", 4 * time.Hour},
		{"4h", RKHTFAuto, 4, "", 16 * time.Hour},
		{"4h", RKHTFAuto, 7, "", rkDay}, // 28h, rounded down to whole days
		{"1d", RKHTFAuto, 4, "", 4 * rkDay},
		{"1d", RKHTFAuto, 10, "", rkWeek}, // 10 days, rounded down to whole weeks
		{"1w", RKHTFAuto, 5, "", rkMonth},
		{"1h", RKHTFManual, 4, "1d", rkDay},
		{"4h", RKHTFManual, 4, "1h", 16 * time.Hour}, // not higher than the chart: auto
	}

	for _, tt := range tests {
		t.Run(tt.chart+"-"+string(tt.mode)+"-"+tt.manual, func(t *testing.T) {
			chart, err := str2duration.ParseDuration(tt.chart)
			require.NoError(t, err)

			cfg := DefaultRKTrendConfluenceConfig()
			cfg.HTFMode, cfg.HTFMult, cfg.HTFManual = tt.mode, tt.mult, tt.manual
			got, err := cfg.higherTimeframe(chart)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestHTFBarStart(t *testing.T) {
	tests := []struct {
		at   string
		htf  time.Duration
		want string
	}{
		{"2021-03-10T13:00:00Z", 4 * time.Hour, "2021-03-10T12:00:00Z"},
		{"2021-03-10T13:00:00Z", 16 * time.Hour, "2021-03-10T00:00:00Z"},
		{"2021-03-10T17:00:00Z", 16 * time.Hour, "2021-03-10T16:00:00Z"}, // restarts at midnight
		{"2021-03-10T13:00:00Z", rkDay, "2021-03-10T00:00:00Z"},
		{"2021-03-10T13:00:00Z", rkWeek, "2021-03-08T00:00:00Z"}, // Monday
		{"2021-03-10T13:00:00Z", rkMonth, "2021-03-01T00:00:00Z"},
		{"2021-03-10T13:00:00Z", 3 * rkMonth, "2021-01-01T00:00:00Z"},
	}

	for _, tt := range tests {
		t.Run(tt.at+"-"+tt.htf.String(), func(t *testing.T) {
			at, err := time.Parse(time.RFC3339, tt.at)
			require.NoError(t, err)
			want, err := time.Parse(time.RFC3339, tt.want)
			require.NoError(t, err)
			require.Equal(t, want, htfBarStart(at, tt.htf))
		})
	}
}

func TestHTFBias(t *testing.T) {
	start := time.Date(2021, 3, 8, 0, 0, 0, 0, time.UTC)
	times := make([]time.Time, 24)
	rising, falling, flat := make([]float64, 24), make([]float64, 24), make([]float64, 24)
	for i := range times {
		times[i] = start.Add(time.Duration(i) * time.Hour)
		rising[i], falling[i], flat[i] = float64(100+i), float64(100-i), 100
	}

	// 4h bars of 1h candles with a 2 bar trend MA: the MA of the second and
	// third closed bars exists from the fourth 4h bar on, at 12:00, and the
	// third bar is only read once it is closed
	bias := htfBias(times, rising, 4*time.Hour, 2)
	for i := 0; i < 12; i++ {
		require.Truef(t, isNa(bias[i]), "index %d", i)
	}
	for i := 12; i < 24; i++ {
		require.Equalf(t, 1.0, bias[i], "index %d", i)
	}

	require.Equal(t, -1.0, htfBias(times, falling, 4*time.Hour, 2)[12])
	require.Equal(t, 0.0, htfBias(times, flat, 4*time.Hour, 2)[12])
}

func TestRKTrendConfluence_Gates(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*RKTrendConfluenceConfig)
		mutate    func(*rkSeries) // changes the candle of the cross, the third
		signal    bool
	}{
		{name: "every gate open", signal: true},
		{name: "price below the trend MA", mutate: func(s *rkSeries) { s.close[2] = 91 }},
		{name: "slow EMA below the trend MA", mutate: func(s *rkSeries) { s.emaSlow[2] = 91.5 }},
		{
			name:      "slow EMA below the trend MA, not required",
			configure: func(c *RKTrendConfluenceConfig) { c.RequireSlowTrend = false },
			mutate:    func(s *rkSeries) { s.emaSlow[2] = 91.5 },
			signal:    true,
		},
		{
			name:      "trend MA inside the regime buffer",
			configure: func(c *RKTrendConfluenceConfig) { c.RegimeBufferATR = 10 },
		},
		{name: "flat trend MA", mutate: func(s *rkSeries) { s.trendMA[2] = s.trendMA[1] }},
		{name: "weak slope", mutate: func(s *rkSeries) { s.trendMA[2] = s.trendMA[1] + 0.01 }},
		{
			name:      "slope filter off",
			configure: func(c *RKTrendConfluenceConfig) { c.UseSlopeFilter = false },
			mutate:    func(s *rkSeries) { s.trendMA[2] = s.trendMA[1] },
			signal:    true,
		},
		{
			name:      "percent slope",
			configure: func(c *RKTrendConfluenceConfig) { c.SlopeMode = RKSlopePercent },
			signal:    true, // 1 per bar is 1.1% of the trend MA
		},
		{name: "weak ADX", mutate: func(s *rkSeries) { s.adx[2] = 19 }},
		{name: "-DI leads", mutate: func(s *rkSeries) { s.plusDI[2] = 5 }},
		{
			name:      "-DI leads, direction not required",
			configure: func(c *RKTrendConfluenceConfig) { c.RequireDIDirection = false },
			mutate:    func(s *rkSeries) { s.plusDI[2] = 5 },
			signal:    true,
		},
		{
			name:      "ADX filter off",
			configure: func(c *RKTrendConfluenceConfig) { c.UseADXFilter = false },
			mutate:    func(s *rkSeries) { s.adx[2] = 5 },
			signal:    true,
		},
		{name: "negative histogram", mutate: func(s *rkSeries) { s.hist[2] = -0.1 }},
		{
			name:      "histogram not expanding",
			configure: func(c *RKTrendConfluenceConfig) { c.RequireHistExpansion = true },
		},
		{
			name:      "MACD on the wrong side of zero",
			configure: func(c *RKTrendConfluenceConfig) { c.RequireMACDZeroSide = true },
			mutate:    func(s *rkSeries) { s.macd[2] = -1 },
		},
		{
			name:      "RSI in the band",
			configure: func(c *RKTrendConfluenceConfig) { c.UseRSIFilter = true },
			signal:    true,
		},
		{
			name:      "RSI overbought",
			configure: func(c *RKTrendConfluenceConfig) { c.UseRSIFilter = true },
			mutate:    func(s *rkSeries) { s.rsi[2] = 75 },
		},
		{
			name:      "volatility in the band",
			configure: func(c *RKTrendConfluenceConfig) { c.UseATRFilter = true },
			signal:    true,
		},
		{
			name:      "volatility above the band",
			configure: func(c *RKTrendConfluenceConfig) { c.UseATRFilter = true },
			mutate:    func(s *rkSeries) { s.volRatio[2] = 3 },
		},
		{
			name: "volatility below the percent band",
			configure: func(c *RKTrendConfluenceConfig) {
				c.UseATRFilter = true
				c.ATRFilterMode = RKVolatilityPercent
				c.MinATRPct = 5 // the ATR is 1.8% of the price
			},
		},
		{name: "HTF agrees", mutate: func(s *rkSeries) { s.htfBias = []float64{1, 1, 1, 1} }, signal: true},
		{name: "HTF neutral", mutate: func(s *rkSeries) { s.htfBias = []float64{0, 0, 0, 0} }},
		{name: "HTF warming up", mutate: func(s *rkSeries) { s.htfBias = []float64{na, na, na, na} }},
		{
			name:      "EMAs too close",
			configure: func(c *RKTrendConfluenceConfig) { c.MinCrossSepATR = 1 }, // 0.5 ATR apart
		},
		{name: "fast EMA already above", mutate: func(s *rkSeries) { s.emaFast[0], s.emaFast[1] = 101, 101 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) {
				c.SlopeLookback = 1
				if tt.configure != nil {
					tt.configure(c)
				}
			})

			series := crossUp()
			if tt.mutate != nil {
				tt.mutate(&series)
			}
			none := []bool{false, false, false, false}
			signal := []bool{false, false, tt.signal, false}

			buy, sell := s.signals(series, nil, nil)
			require.Equal(t, signal, buy, "BUY")
			require.Equal(t, none, sell, "SELL")

			buy, sell = s.signals(mirror(series), nil, nil)
			require.Equal(t, none, buy, "mirrored BUY")
			require.Equal(t, signal, sell, "mirrored SELL")
		})
	}
}

func TestRKTrendConfluence_EntryWindow(t *testing.T) {
	// the ADX only confirms the trend one candle after the cross
	series := crossUp()
	series.adx[2] = 15

	onCross := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) { c.SlopeLookback = 1 })
	buy, _ := onCross.signals(series, nil, nil)
	require.Equal(t, []bool{false, false, false, false}, buy)

	widened := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) {
		c.SlopeLookback = 1
		c.EntryWindow = 2
	})
	buy, _ = widened.signals(series, nil, nil)
	require.Equal(t, []bool{false, false, false, true}, buy)

	// the cooldown is raised to the entry window: one cross, one signal
	buy, _ = widened.signals(crossUp(), nil, nil)
	require.Equal(t, []bool{false, false, true, false}, buy)
}

func TestRKTrendConfluence_Cooldown(t *testing.T) {
	// crosses up on the third and on the sixth candle
	series := bullishSeries(
		[]float64{99, 99, 106, 106, 104, 106},
		[]float64{100, 100, 105, 105, 105, 105},
	)

	tests := []struct {
		cooldown int
		want     []bool
	}{
		{0, []bool{false, false, true, false, false, true}},
		{3, []bool{false, false, true, false, false, true}},
		{4, []bool{false, false, true, false, false, false}},
	}

	for _, tt := range tests {
		s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) {
			c.SlopeLookback = 1
			c.CooldownBars = tt.cooldown
		})
		buy, sell := s.signals(series, nil, nil)
		require.Equal(t, tt.want, buy, "cooldown %d", tt.cooldown)
		require.Equal(t, make([]bool, 6), sell, "the cross down is in a bullish regime")
	}
}

// flatBroker is an account without funds, on which the strategy never trades.
type flatBroker struct{}

func (flatBroker) Account() (model.Account, error)                   { return model.Account{}, nil }
func (flatBroker) Position(string) (asset, quote float64, err error) { return 0, 0, nil }
func (flatBroker) Order(string, int64) (model.Order, error)          { return model.Order{}, nil }
func (flatBroker) Cancel(model.Order) error                          { return nil }

func (flatBroker) CreateOrderOCO(model.SideType, string, float64, float64, float64, float64) ([]model.Order, error) {
	return nil, nil
}

func (flatBroker) CreateOrderLimit(model.SideType, string, float64, float64) (model.Order, error) {
	return model.Order{}, nil
}

func (flatBroker) CreateOrderMarket(model.SideType, string, float64) (model.Order, error) {
	return model.Order{}, nil
}

func (flatBroker) CreateOrderMarketQuote(model.SideType, string, float64) (model.Order, error) {
	return model.Order{}, nil
}

func (flatBroker) CreateOrderStop(string, float64, float64) (model.Order, error) {
	return model.Order{}, nil
}

// The strategy only receives the last WarmupPeriod candles: its signals must be
// the ones of the indicator computed over the whole history, as on a chart.
func TestRKTrendConfluence_WindowMatchesFullHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("long: left out by -short")
	}
	df := btcDataframe(t)

	tests := []struct {
		name      string
		configure func(*RKTrendConfluenceConfig)
	}{
		{name: "defaults"},
		{name: "every gate", configure: func(c *RKTrendConfluenceConfig) {
			c.UseRSIFilter = true
			c.UseATRFilter = true
			c.UseHTFFilter = true
			c.EntryWindow = 3
			c.CooldownBars = 12
		}},
		{
			// the cooldown chains signals further back than the window holds
			name: "long cooldown chain",
			configure: func(c *RKTrendConfluenceConfig) {
				c.EntryWindow = 20
				c.CooldownBars = 300
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newRKTrendConfluence(t, tt.configure)
			wantBuy, wantSell := s.signals(s.series(df), df.Time, nil)

			// the candles go through the strategy as the strategy controller
			// sends them
			warmup, signals := s.WarmupPeriod(), 0
			for end := warmup; end <= len(df.Close); end++ {
				w := window(df, end-warmup, end)
				s.Indicators(w)
				s.OnCandle(w, flatBroker{})

				buy, sell := w.Metadata["rk_buy"].Last(0) > 0, w.Metadata["rk_sell"].Last(0) > 0
				require.Equalf(t, wantBuy[end-1], buy, "BUY at %s", df.Time[end-1])
				require.Equalf(t, wantSell[end-1], sell, "SELL at %s", df.Time[end-1])
				if buy || sell {
					signals++
				}
			}
			require.Positive(t, signals, "the sample must produce signals")
		})
	}
}

// signalCandle is a dataframe whose last candle, closing at 100, has the signal
// of side, none when empty.
func signalCandle(side model.SideType) *model.Dataframe {
	buy, sell := 0.0, 0.0
	switch side {
	case model.SideTypeBuy:
		buy = 1
	case model.SideTypeSell:
		sell = 1
	}

	return &model.Dataframe{
		Pair:  "BTCUSDT",
		Close: model.Series[float64]{100},
		Metadata: map[string]model.Series[float64]{
			"rk_buy":  {buy},
			"rk_sell": {sell},
			"rk_atr":  {1},
		},
	}
}

func TestRKTrendConfluence_OnCandle(t *testing.T) {
	buy, sell := model.SideTypeBuy, model.SideTypeSell

	tests := []struct {
		direction RKDirection
		signals   []model.SideType
		positions []float64 // asset held after each signal
	}{
		{RKDirectionBoth, []model.SideType{buy, buy, sell, sell, buy}, []float64{100, 100, -100, -100, 100}},
		{RKDirectionBuyOnly, []model.SideType{sell, buy, sell, buy}, []float64{0, 100, 0, 100}},
		{RKDirectionSellOnly, []model.SideType{buy, sell, buy, sell}, []float64{0, -100, 0, -100}},
	}

	for _, tt := range tests {
		t.Run(string(tt.direction), func(t *testing.T) {
			wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000))
			wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: time.Now(), Close: 100, Complete: true})

			s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) { c.Direction = tt.direction })
			for i, side := range tt.signals {
				s.OnCandle(signalCandle(side), wallet)

				asset, _, err := wallet.Position("BTCUSDT")
				require.NoError(t, err)
				require.InDeltaf(t, tt.positions[i], asset, 1e-9, "after signal %d (%s)", i, side)
			}
		})
	}
}

// failingEntries fills the market quote orders but reports them as failed,
// like a request that timed out after the exchange executed it.
type failingEntries struct {
	*exchange.PaperWallet
}

func (b failingEntries) CreateOrderMarketQuote(side model.SideType, pair string, quote float64) (model.Order, error) {
	order, err := b.PaperWallet.CreateOrderMarketQuote(side, pair, quote)
	if err != nil {
		return order, err
	}
	return order, errors.New("request timed out")
}

// stopCandle is a closed candle of the stop tests and the asset held after it.
type stopCandle struct {
	close, high, low float64
	signal           model.SideType // BUY, SELL or none
	atr              float64        // 2 when zero
	position         float64
}

func TestRKTrendConfluence_Stops(t *testing.T) {
	buy, sell := model.SideTypeBuy, model.SideTypeSell

	tests := []struct {
		name          string
		configure     func(*RKTrendConfluenceConfig)
		holding       float64 // BTC held before the first candle, bought by someone else
		failedEntries bool    // entries are filled but reported as failed
		candles       []stopCandle
	}{
		{
			name:      "stop from the entry close",
			configure: func(c *RKTrendConfluenceConfig) { c.StopATR = 2 }, // 96
			candles: []stopCandle{
				{close: 100, high: 101, low: 99, signal: buy, position: 100},
				{close: 97, high: 98, low: 95, position: 100}, // a wick through the stop is not enough
				{close: 96, high: 97, low: 95, position: 0},
			},
		},
		{
			name:      "trailing stop from the highest high",
			configure: func(c *RKTrendConfluenceConfig) { c.TrailingATR = 3 }, // 101-6 = 95
			candles: []stopCandle{
				{close: 100, high: 101, low: 99, signal: buy, position: 100},
				{close: 108, high: 110, low: 100, position: 100},          // 110-6 = 104
				{close: 105, high: 106, low: 104, atr: 10, position: 100}, // a wider ATR never loosens it
				{close: 104, high: 105, low: 103, position: 0},
			},
		},
		{
			name:      "the stop of the previous candle applies to the close",
			configure: func(c *RKTrendConfluenceConfig) { c.TrailingATR = 3 },
			candles: []stopCandle{
				{close: 100, high: 101, low: 99, signal: buy, position: 100},
				{close: 108, high: 110, low: 100, position: 100}, // 110-6 = 104
				{close: 108, high: 115, low: 107, position: 100}, // under its own 115-6 = 109, above 104
				{close: 108.5, high: 109, low: 108, position: 0},
			},
		},
		{
			name: "the tighter of both stops",
			configure: func(c *RKTrendConfluenceConfig) {
				c.StopATR = 1     // 98
				c.TrailingATR = 3 // 95
			},
			candles: []stopCandle{
				{close: 100, high: 101, low: 99, signal: buy, position: 100},
				{close: 99, high: 100, low: 98, position: 100},
				{close: 98, high: 99, low: 97, position: 0},
			},
		},
		{
			name:      "trailing stop of a short from the lowest low",
			configure: func(c *RKTrendConfluenceConfig) { c.TrailingATR = 3 }, // 99+6 = 105
			candles: []stopCandle{
				{close: 100, high: 101, low: 99, signal: sell, position: -100},
				{close: 92, high: 95, low: 90, position: -100}, // 90+6 = 96
				{close: 96, high: 97, low: 95, position: 0},
			},
		},
		{
			name:      "stop of a short from the entry close",
			configure: func(c *RKTrendConfluenceConfig) { c.StopATR = 2 }, // 104
			candles: []stopCandle{
				{close: 100, high: 101, low: 99, signal: sell, position: -100},
				{close: 103, high: 104, low: 102, position: -100},
				{close: 104, high: 105, low: 103, position: 0},
			},
		},
		{
			name: "stop of a long only strategy",
			configure: func(c *RKTrendConfluenceConfig) {
				c.Direction = RKDirectionBuyOnly
				c.TrailingATR = 3 // 95
			},
			candles: []stopCandle{
				{close: 100, high: 101, low: 99, signal: buy, position: 100},
				{close: 90, high: 95, low: 89, signal: sell, position: 0}, // stopped, and no short
			},
		},
		{
			name:          "entry filled but reported as failed",
			configure:     func(c *RKTrendConfluenceConfig) { c.StopATR = 2 }, // 96
			failedEntries: true,
			candles: []stopCandle{
				{close: 100, high: 101, low: 99, signal: buy, position: 100},
				{close: 96, high: 97, low: 95, position: 0},
			},
		},
		{
			name:      "signal on the candle of a stop",
			configure: func(c *RKTrendConfluenceConfig) { c.TrailingATR = 3 },
			candles: []stopCandle{
				{close: 100, high: 101, low: 99, signal: buy, position: 100},
				{close: 90, high: 95, low: 89, signal: sell, position: -100}, // stopped, then short from 89+6 = 95
				{close: 95, high: 96, low: 94, position: 0},
			},
		},
		{
			name:      "an opposite signal restarts the stops",
			configure: func(c *RKTrendConfluenceConfig) { c.TrailingATR = 3 },
			candles: []stopCandle{
				{close: 100, high: 101, low: 99, signal: buy, position: 100},
				{close: 110, high: 112, low: 100, position: 100},                // 112-6 = 106
				{close: 107, high: 108, low: 106, signal: sell, position: -100}, // short from 106+6 = 112
				{close: 111, high: 112, low: 110, position: -100},
				{close: 112, high: 113, low: 111, position: 0},
			},
		},
		{
			name:      "position found without a trade",
			configure: func(c *RKTrendConfluenceConfig) { c.StopATR = 2 },
			holding:   1,
			candles: []stopCandle{
				{close: 50, high: 51, low: 49, position: 1}, // followed from here: 46
				{close: 47, high: 48, low: 46, position: 1},
				{close: 46, high: 47, low: 45, position: 0},
			},
		},
		{
			name:      "stop at the broker trailing the highest high",
			configure: func(c *RKTrendConfluenceConfig) { c.BrokerTrailingATR = 3 }, // 100-6 = 94
			candles: []stopCandle{
				{close: 100, high: 101, low: 99, signal: buy, position: 100},
				{close: 108, high: 110, low: 100, position: 100},          // 110-6 = 104, moved at the close
				{close: 106, high: 107, low: 105, atr: 10, position: 100}, // a wider ATR never loosens it
				{close: 105, high: 106, low: 103.5, position: 0},          // touched by a wick
			},
		},
		{
			name:      "stop at the broker trailing the lowest low of a short",
			configure: func(c *RKTrendConfluenceConfig) { c.BrokerTrailingATR = 3 }, // 100+6 = 106
			candles: []stopCandle{
				{close: 100, high: 101, low: 99, signal: sell, position: -100},
				{close: 92, high: 95, low: 90, position: -100}, // 90+6 = 96
				{close: 95, high: 96.5, low: 94, position: 0},
			},
		},
		{
			name:      "stop at the broker never moved beyond the close",
			configure: func(c *RKTrendConfluenceConfig) { c.BrokerTrailingATR = 1 }, // 100-2 = 98
			candles: []stopCandle{
				{close: 100, high: 101, low: 99, signal: buy, position: 100},
				{close: 101, high: 110, low: 100, position: 100}, // 110-2 = 108 is above the close: kept at 98
				{close: 99, high: 101, low: 98.5, position: 100},
				{close: 98, high: 99, low: 97, position: 0},
			},
		},
		{
			name: "stop at the broker from the entry and trailing, the tighter",
			configure: func(c *RKTrendConfluenceConfig) {
				c.BrokerStopATR = 1     // 98
				c.BrokerTrailingATR = 4 // 92, then 110-8 = 102
			},
			candles: []stopCandle{
				{close: 100, high: 101, low: 99, signal: buy, position: 100},
				{close: 99, high: 100, low: 98.5, position: 100},
				{close: 108, high: 110, low: 99, position: 100},
				{close: 104, high: 105, low: 101.5, position: 0},
			},
		},
		{
			name: "without stops only the signals close",
			candles: []stopCandle{
				{close: 100, high: 101, low: 99, signal: buy, position: 100},
				{close: 50, high: 51, low: 49, position: 100},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quote := 10000.0
			if tt.holding > 0 {
				quote = 0
			}
			wallet := exchange.NewPaperWallet(context.Background(), "USDT",
				exchange.WithPaperAsset("USDT", quote), exchange.WithPaperAsset("BTC", tt.holding))
			s := newRKTrendConfluence(t, tt.configure)

			var broker service.Broker = wallet
			if tt.failedEntries {
				broker = failingEntries{wallet}
			}

			start := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
			for i, candle := range tt.candles {
				wallet.OnCandle(model.Candle{
					Pair:     "BTCUSDT",
					Time:     start.Add(time.Duration(i) * time.Hour),
					Close:    candle.close,
					High:     candle.high,
					Low:      candle.low,
					Complete: true,
				})

				atr := candle.atr
				if atr == 0 {
					atr = 2
				}
				df := signalCandle(candle.signal)
				df.Close = model.Series[float64]{candle.close}
				df.High = model.Series[float64]{candle.high}
				df.Low = model.Series[float64]{candle.low}
				df.Metadata["rk_atr"] = model.Series[float64]{atr}
				s.OnCandle(df, broker)

				asset, _, err := wallet.Position("BTCUSDT")
				require.NoError(t, err)
				require.InDeltaf(t, candle.position, asset, 1e-9, "after candle %d", i)
			}
		})
	}
}

func TestRKTrendConfluence_Backtest(t *testing.T) {
	ctx := context.Background()
	s := newRKTrendConfluence(t, nil)

	csvFeed, err := exchange.NewCSVFeed(s.Timeframe(), exchange.PairFeed{
		Pair:      "BTCUSDT",
		File:      "../../testdata/btc-1h.csv",
		Timeframe: "1h",
	})
	require.NoError(t, err)

	store, err := storage.FromMemory()
	require.NoError(t, err)

	wallet := exchange.NewPaperWallet(
		ctx,
		"USDT",
		exchange.WithPaperAsset("USDT", 10000),
		exchange.WithPaperFee(0.001, 0.001),
		exchange.WithDataFeed(csvFeed),
	)

	bot, err := xtrader.NewBot(ctx, xtrader.Settings{Pairs: []string{"BTCUSDT"}}, wallet, s,
		xtrader.WithBacktest(wallet),
		xtrader.WithStorage(store),
		xtrader.WithLogLevel(log.ErrorLevel),
	)
	require.NoError(t, err)
	require.NoError(t, bot.Run(ctx))

	orders, err := store.Orders()
	require.NoError(t, err)
	require.NotEmpty(t, orders)
	for _, order := range orders {
		require.Equal(t, model.OrderStatusTypeFilled, order.Status, order.String())
	}
}

// sizingBroker has quote available, records the value of the orders at the
// market in quote and the size of the others, and counts every order created.
// pointedWallet is a paper wallet whose cash is in the currency of an account
// valuing a move of 1 of the price of a unit at point, as MT5 on a BRL account.
type pointedWallet struct {
	*exchange.PaperWallet
	point float64
}

func (w *pointedWallet) PointValue(string) (float64, error) { return w.point, nil }

// CreateOrderMarketQuote buys the units worth quote, in the account currency.
func (w *pointedWallet) CreateOrderMarketQuote(side model.SideType, pair string, quote float64) (model.Order,
	error) {
	price, err := w.LastQuote(context.Background(), pair)
	if err != nil {
		return model.Order{}, err
	}
	return w.CreateOrderMarket(side, pair, quote/(price*w.point))
}

type sizingBroker struct {
	flatBroker
	quote  float64
	values []float64
	sizes  []float64
	orders int
}

func (b *sizingBroker) Position(string) (asset, quote float64, err error) { return 0, b.quote, nil }

func (b *sizingBroker) CreateOrderMarketQuote(_ model.SideType, _ string, value float64) (model.Order, error) {
	b.values = append(b.values, value)
	b.orders++
	return model.Order{}, nil
}

func (b *sizingBroker) CreateOrderMarket(_ model.SideType, _ string, size float64) (model.Order, error) {
	b.sizes = append(b.sizes, size)
	b.orders++
	return model.Order{}, nil
}

func (b *sizingBroker) CreateOrderLimit(model.SideType, string, float64, float64) (model.Order, error) {
	b.orders++
	return model.Order{}, nil
}

func (b *sizingBroker) CreateOrderStop(string, float64, float64) (model.Order, error) {
	b.orders++
	return model.Order{}, nil
}

func (b *sizingBroker) CreateOrderOCO(model.SideType, string, float64, float64, float64, float64) ([]model.Order,
	error) {
	b.orders++
	return nil, nil
}

func TestRKTrendConfluence_Sizing(t *testing.T) {
	tests := []struct {
		name               string
		fraction, leverage float64
		value              float64 // of the position opened with 10000 available
	}{
		{"all the balance, the default", 1, 1, 10000},
		{"a fraction of the balance", 0.25, 1, 2500},
		{"a fraction of the balance, leveraged", 0.1, 20, 20000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) {
				c.PositionFraction, c.Leverage = tt.fraction, tt.leverage
			})
			broker := &sizingBroker{quote: 10000}
			s.OnCandle(signalCandle(model.SideTypeSell), broker)
			require.Len(t, broker.values, 1)
			require.InDelta(t, tt.value, broker.values[0], 1e-9)
		})
	}

	t.Run("dust", func(t *testing.T) {
		s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) { c.PositionFraction = 0.001 })
		broker := &sizingBroker{quote: 5000} // a position of 5, under the minimum of 10
		s.OnCandle(signalCandle(model.SideTypeBuy), broker)
		require.Empty(t, broker.values)
	})

	t.Run("a lot, ordered by its size", func(t *testing.T) {
		s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) {
			c.PositionSize = map[string]float64{"BTCUSDT": 0.5}
		})
		broker := &sizingBroker{quote: 10000}
		s.OnCandle(signalCandle(model.SideTypeSell), broker)
		require.Empty(t, broker.values)
		require.Equal(t, []float64{0.5}, broker.sizes)
	})

	t.Run("a lot with the whole balance leveraged", func(t *testing.T) {
		// the size bounds the position: the fraction, unused, can stay at 1
		s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) {
			c.Leverage, c.PositionSize = 20, map[string]float64{"BTCUSDT": 1500}
		})
		broker := &sizingBroker{quote: 10000}
		s.OnCandle(signalCandle(model.SideTypeBuy), broker)
		require.Equal(t, []float64{1500}, broker.sizes, "150000 of 200000")
	})
}

func TestRKTrendConfluence_MaxRisk(t *testing.T) {
	// a long of all the balance of 10000 at 100, the ATR at 1
	candle := func() *model.Dataframe {
		df := signalCandle(model.SideTypeBuy)
		df.High, df.Low = model.Series[float64]{100}, model.Series[float64]{100}
		return df
	}
	for _, tc := range []struct {
		name      string
		configure func(*RKTrendConfluenceConfig)
		opened    bool
	}{
		// the stop 2 ATR away: 200 lost, 2%
		{"under the limit", func(c *RKTrendConfluenceConfig) { c.StopATR, c.MaxRiskPct = 2, 3 }, true},
		{"above the limit: skipped", func(c *RKTrendConfluenceConfig) { c.StopATR, c.MaxRiskPct = 2, 1 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newRKTrendConfluence(t, tc.configure)
			broker := &sizingBroker{quote: 10000}
			s.OnCandle(candle(), broker)
			require.Equal(t, tc.opened, len(broker.values) > 0)
		})
	}

	t.Run("the fixed stop loss measured, placed at the broker", func(t *testing.T) {
		// 50 lost at the fixed stop loss, 0.5% of the balance
		for maxRisk, want := range map[float64]float64{1: 100, 0.4: 0} {
			wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000))
			wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: time.Now(), Low: 100, High: 100, Close: 100,
				Complete: true})
			s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) { c.StopValue, c.MaxRiskPct = 50, maxRisk })
			s.OnCandle(candle(), wallet)
			asset, _, err := wallet.Position("BTCUSDT")
			require.NoError(t, err)
			require.InDelta(t, want, asset, 1e-9, "max risk %g%%", maxRisk)
		}
	})

	t.Run("in the account currency, by the point value", func(t *testing.T) {
		// a BRL account: 2 BRL a unit for 1 of the price; half the units, the
		// same 50 BRL lost at the stop loss
		for maxRisk, want := range map[float64]float64{1: 50, 0.4: 0} {
			wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000))
			wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: time.Now(), Low: 100, High: 100, Close: 100,
				Complete: true})
			broker := &pointedWallet{PaperWallet: wallet, point: 2}
			s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) { c.StopValue, c.MaxRiskPct = 50, maxRisk })
			s.OnCandle(candle(), broker)
			asset, _, err := wallet.Position("BTCUSDT")
			require.NoError(t, err)
			require.InDelta(t, want, asset, 1e-9, "max risk %g%%", maxRisk)
			if want > 0 {
				leg := s.protection["BTCUSDT"].leg
				require.Equal(t, model.OrderTypeStopLoss, leg.Type)
				require.InDelta(t, 99.5, *leg.Stop, 1e-9, "50 BRL / (50 units x 2 BRL)")
			}
		}
	})

	t.Run("the tightest of the stops", func(t *testing.T) {
		s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) { c.StopATR, c.BrokerStopATR = 3, 1.5 })
		require.InDelta(t, 98.5, s.entryStop(candle(), model.SideTypeBuy, 100, 1), 1e-9)
		s = newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) { c.StopATR, c.BrokerTrailingATR = 1, 2 })
		require.InDelta(t, 99, s.entryStop(candle(), model.SideTypeBuy, 100, 1), 1e-9)
		require.InDelta(t, 101, s.entryStop(candle(), model.SideTypeSell, 100, 1), 1e-9)
	})

	t.Run("a stop loss needed", func(t *testing.T) {
		cfg := DefaultRKTrendConfluenceConfig()
		cfg.MaxRiskPct = 2
		_, err := NewRKTrendConfluence(cfg)
		require.ErrorContains(t, err, "needs one")
		cfg.MaxRiskPct = -1
		_, err = NewRKTrendConfluence(cfg)
		require.ErrorContains(t, err, "max risk")
	})
}

func TestRKSizing(t *testing.T) {
	lot := map[string]float64{"BTCUSDT": 0.5}
	tests := []struct {
		name        string
		sizing      rkSizing
		quote       float64 // available, at a price of 100
		size, value float64 // 0 when no position opens
		point       float64 // the point value, 1 when 0
	}{
		{"a share of the balance", rkSizing{fraction: 0.5, leverage: 1, minValue: 10}, 10000, 50, 5000, 0},
		{"a share of the balance, leveraged", rkSizing{fraction: 0.1, leverage: 20, minValue: 10}, 10000, 200, 20000, 0},
		{"a share worth less than the minimum", rkSizing{fraction: 0.5, leverage: 1, minValue: 10}, 10, 0, 0, 0},
		{"a lot", rkSizing{sizes: lot, fraction: 1, leverage: 1, minValue: 10}, 10000, 0.5, 50, 0},
		{"a lot with little balance", rkSizing{sizes: lot, fraction: 1, leverage: 1, minValue: 10}, 50, 0.5, 50, 0},
		{"a lot worth more than the balance", rkSizing{sizes: lot, fraction: 1, leverage: 1, minValue: 10}, 49, 0, 0, 0},
		{"a lot within the leveraged balance", rkSizing{sizes: lot, fraction: 1, leverage: 2, minValue: 10}, 25, 0.5,
			50, 0},
		{"a lot taken for dust", rkSizing{sizes: lot, fraction: 1, leverage: 1, minValue: 51}, 10000, 0, 0, 0},
		{"no lot for the pair", rkSizing{sizes: map[string]float64{"ETHUSDT": 1}, fraction: 1, leverage: 1,
			minValue: 10}, 10000, 0, 0, 0},
		// a BRL account: 5 BRL a unit for 1 of the price
		{"a share of a BRL balance", rkSizing{fraction: 0.5, leverage: 1, minValue: 10}, 10000, 10, 5000, 5},
		{"a lot in BRL", rkSizing{sizes: lot, fraction: 1, leverage: 1, minValue: 10}, 10000, 0.5, 250, 5},
		{"a lot in BRL worth more than the balance", rkSizing{sizes: lot, fraction: 1, leverage: 1, minValue: 10},
			249, 0, 0, 5},
		{"a lot in BRL taken for dust", rkSizing{sizes: lot, fraction: 1, leverage: 1, minValue: 251}, 10000, 0, 0,
			5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			point := tt.point
			if point == 0 {
				point = 1
			}
			size, value, ok := tt.sizing.position("BTCUSDT", rkCash{free: tt.quote, point: point}, 100)
			require.Equal(t, tt.size > 0, ok)
			if ok {
				require.InDelta(t, tt.size, size, 1e-9)
				require.InDelta(t, tt.value, value, 1e-9)
			}
		})
	}

	t.Run("the money of the stops and of the risk in the account currency", func(t *testing.T) {
		// 5.5 BRL a unit for 1 of the price: 55 BRL lost 10 below for 1 unit
		require.InDelta(t, 90, rkLossLevel(model.SideTypeBuy, 100, 1, 5.5, 55), 1e-9)
		require.InDelta(t, 120, rkProfitLevel(model.SideTypeBuy, 100, 1, 5.5, 110), 1e-9)
		require.InDelta(t, 110, rkLossLevel(model.SideTypeSell, 100, 1, 5.5, 55), 1e-9)
		// 55 BRL at the stop: over 0.5% of 10000 BRL, within 1%
		cash := rkCash{free: 10000, point: 5.5}
		require.True(t, rkSizing{maxRisk: 0.5}.risky("BTCUSDT", 100, 90, 1, cash))
		require.False(t, rkSizing{maxRisk: 1}.risky("BTCUSDT", 100, 90, 1, cash))
		// the point value of the quote (1) gives the levels of before
		require.InDelta(t, 99.5, rkLossLevel(model.SideTypeBuy, 100, 100, 1, 50), 1e-9)
	})

	t.Run("a share of the balance is ordered by its value, a lot by its size", func(t *testing.T) {
		broker := &sizingBroker{}
		require.NoError(t, rkSizing{fraction: 1}.open(broker, model.SideTypeBuy, "BTCUSDT", 50, 5000))
		require.NoError(t, rkSizing{sizes: lot}.open(broker, model.SideTypeBuy, "BTCUSDT", 0.5, 50))
		require.Equal(t, []float64{5000}, broker.values)
		require.Equal(t, []float64{0.5}, broker.sizes)
	})
}

func TestRKTrendConfluence_BrokerStop(t *testing.T) {
	start := time.Now()
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000))
	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: start, Low: 100, High: 100, Close: 100, Complete: true})
	s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) { c.BrokerStopATR = 2 })

	// the long opens with its stop at the broker, 2 ATR below the close
	s.OnCandle(signalCandle(model.SideTypeBuy), wallet)
	asset, _, err := wallet.Position("BTCUSDT")
	require.NoError(t, err)
	require.InDelta(t, 100, asset, 1e-9)
	long := s.protection["BTCUSDT"].leg
	require.Equal(t, model.OrderTypeStopLoss, long.Type)
	require.Equal(t, model.SideTypeSell, long.Side)
	require.InDelta(t, 98, *long.Stop, 1e-9)

	// the opposite signal cancels it before closing the long, and the short
	// gets its own
	s.OnCandle(signalCandle(model.SideTypeSell), wallet)
	asset, _, err = wallet.Position("BTCUSDT")
	require.NoError(t, err)
	require.InDelta(t, -100, asset, 1e-9, "reversed, not closed twice")
	canceled, err := wallet.Order("BTCUSDT", long.ExchangeID)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeCanceled, canceled.Status)
	short := s.protection["BTCUSDT"].leg
	require.Equal(t, model.SideTypeBuy, short.Side)
	require.InDelta(t, 102, *short.Stop, 1e-9)

	// the broker closes the short when the price touches the stop, a wick
	// included
	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: start.Add(time.Hour), Low: 99, High: 102.5, Close: 101,
		Complete: true})
	asset, _, err = wallet.Position("BTCUSDT")
	require.NoError(t, err)
	require.InDelta(t, 0, asset, 1e-9)
	filled, err := wallet.Order("BTCUSDT", short.ExchangeID)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeFilled, filled.Status)
}

func TestRKTrendConfluence_BrokerStopNotSupported(t *testing.T) {
	s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) { c.BrokerStopATR = 2 })
	broker := &sizingBroker{quote: 10000}

	s.OnCandle(signalCandle(model.SideTypeBuy), broker)
	require.Zero(t, broker.orders, "no position without its stop")
}

// fillingBroker fills the legs it is asked to cancel: the price touched them at
// the broker while their cancel was in flight.
type fillingBroker struct {
	*exchange.PaperWallet
}

func (b fillingBroker) Cancel(leg model.Order) error {
	if err := b.PaperWallet.Cancel(leg); err != nil {
		return err
	}
	asset, _, err := b.Position(leg.Pair)
	if err != nil {
		return err
	}
	if _, err := b.CreateOrderMarket(leg.Side, leg.Pair, math.Abs(asset)); err != nil {
		return err
	}
	return exchange.ErrOrderNotOpen
}

func TestRKTrendConfluence_ReversalAfterTheBrokerStopFilled(t *testing.T) {
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000))
	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: time.Now(), Low: 100, High: 100, Close: 100, Complete: true})
	s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) { c.BrokerStopATR = 2 })
	s.OnCandle(signalCandle(model.SideTypeBuy), wallet)

	// the long is closed by its stop as it is canceled: the short is the
	// entry, with its stop, not the close of a long already closed
	s.OnCandle(signalCandle(model.SideTypeSell), fillingBroker{wallet})
	asset, _, err := wallet.Position("BTCUSDT")
	require.NoError(t, err)
	require.InDelta(t, -100, asset, 1e-9)
	require.Contains(t, s.protection, "BTCUSDT")
	short := s.protection["BTCUSDT"].leg
	require.Equal(t, model.SideTypeBuy, short.Side)
	require.InDelta(t, 102, *short.Stop, 1e-9)
}

func TestRKClose(t *testing.T) {
	tests := []struct {
		name     string
		held     float64 // BTC, when closing a long
		flat     bool
		position float64 // after the close
	}{
		{"closes what is held", 100, true, 0},
		{"only what is left", 40, true, 0},
		{"nothing, closed by a leg", 0, true, 0},
		{"nothing, dust", 0.05, true, 0.05},
		{"nothing, reversed by someone else", -50, false, -50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000))
			wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: time.Now(), Low: 100, High: 100, Close: 100,
				Complete: true})
			if tt.held != 0 {
				_, err := wallet.CreateOrderMarket(rkHeld(tt.held, 0), "BTCUSDT", math.Abs(tt.held))
				require.NoError(t, err)
			}

			df := &model.Dataframe{Pair: "BTCUSDT", Close: model.Series[float64]{100}}
			require.Equal(t, tt.flat, rkClose(df, wallet, model.SideTypeBuy, 10))
			asset, _, err := wallet.Position("BTCUSDT")
			require.NoError(t, err)
			require.InDelta(t, tt.position, asset, 1e-9)
		})
	}
}

// movesRecorder records the levels the broker stops are moved to.
type movesRecorder struct {
	*exchange.PaperWallet
	moves []float64
}

func (b *movesRecorder) UpdateProtection(leg model.Order, level float64) (model.Order, error) {
	b.moves = append(b.moves, level)
	return b.PaperWallet.UpdateProtection(leg, level)
}

func TestRKTrendConfluence_BrokerTrailingMoves(t *testing.T) {
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000))
	broker := &movesRecorder{PaperWallet: wallet}
	s := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) { c.BrokerTrailingATR = 1 }) // 100-2 = 98

	start := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, candle := range []stopCandle{
		{close: 100, high: 101, low: 99, signal: model.SideTypeBuy},
		{close: 101, high: 110, low: 100}, // 110-2 = 108 is above the close: not sent
		{close: 99.5, high: 100, low: 99}, // nothing better
		{close: 109, high: 109.5, low: 108.5},
	} {
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: start.Add(time.Duration(i) * time.Hour),
			Close: candle.close, High: candle.high, Low: candle.low, Complete: true})
		df := signalCandle(candle.signal)
		df.Close = model.Series[float64]{candle.close}
		df.High = model.Series[float64]{candle.high}
		df.Low = model.Series[float64]{candle.low}
		df.Metadata["rk_atr"] = model.Series[float64]{2}
		s.OnCandle(df, broker)
	}

	require.Equal(t, []float64{108}, broker.moves, "moved once the close is above it: 110-2")
	require.InDelta(t, 108, s.protection["BTCUSDT"].stop, 1e-9)
}

func TestRKTrendConfluence_FixedStops(t *testing.T) {
	start := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	// feed runs the candles, with an ATR of 2, and checks the position after each
	feed := func(t *testing.T, s *RKTrendConfluence, broker *movesRecorder, candles ...stopCandle) {
		t.Helper()
		for i, candle := range candles {
			broker.OnCandle(model.Candle{Pair: "BTCUSDT", Time: start.Add(time.Duration(i) * time.Hour),
				Close: candle.close, High: candle.high, Low: candle.low, Complete: true})
			df := signalCandle(candle.signal)
			df.Close = model.Series[float64]{candle.close}
			df.High = model.Series[float64]{candle.high}
			df.Low = model.Series[float64]{candle.low}
			df.Metadata["rk_atr"] = model.Series[float64]{2}
			s.OnCandle(df, broker)

			asset, _, err := broker.Position("BTCUSDT")
			require.NoError(t, err)
			require.InDeltaf(t, candle.position, asset, 1e-9, "after candle %d", i)
		}
	}
	newBroker := func() *movesRecorder {
		return &movesRecorder{PaperWallet: exchange.NewPaperWallet(context.Background(), "USDT",
			exchange.WithPaperAsset("USDT", 10000))}
	}
	// 100 bought at 100: a loss of 50 is 0.5 away and a profit of 100 is 1 away;
	// the stops in ATR are replaced, the broker trailing included
	fixed := func(c *RKTrendConfluenceConfig) {
		c.StopValue, c.TakeProfitValue = 50, 100
		c.StopATR, c.TrailingATR, c.BrokerStopATR, c.BrokerTrailingATR = 0.1, 0.1, 1, 1
	}

	t.Run("the stop loss at the broker, not trailed", func(t *testing.T) {
		s, broker := newRKTrendConfluence(t, fixed), newBroker()
		feed(t, s, broker,
			stopCandle{close: 100, high: 100, low: 100, signal: model.SideTypeBuy, position: 100},
			stopCandle{close: 99.8, high: 100.6, low: 99.7, position: 100}, // under the stop in ATR: 100-0.1*2
		)
		leg := s.protection["BTCUSDT"].leg
		require.Equal(t, model.OrderTypeStopLoss, leg.Type)
		require.InDelta(t, 99.5, *leg.Stop, 1e-9)
		require.Empty(t, broker.moves, "not trailed with the ATR")

		// the broker closes the long on a wick through it
		feed(t, s, broker, stopCandle{close: 99.9, high: 100, low: 99.4})
	})

	t.Run("the take profit at the broker", func(t *testing.T) {
		s, broker := newRKTrendConfluence(t, fixed), newBroker()
		feed(t, s, broker,
			stopCandle{close: 100, high: 100, low: 100, signal: model.SideTypeBuy, position: 100},
			stopCandle{close: 100.5, high: 101.2, low: 100.1},
		)
		_, quote, err := broker.Position("BTCUSDT")
		require.NoError(t, err)
		require.InDelta(t, 10100, quote, 1e-9, "100 earned at 101")
	})

	t.Run("the take profit only, with the stop in ATR at the broker", func(t *testing.T) {
		s, broker := newRKTrendConfluence(t, func(c *RKTrendConfluenceConfig) {
			c.TakeProfitValue, c.BrokerStopATR = 100, 1
		}), newBroker()
		feed(t, s, broker, stopCandle{close: 100, high: 100, low: 100, signal: model.SideTypeBuy, position: 100})
		leg := s.protection["BTCUSDT"].leg
		require.Equal(t, model.OrderTypeStopLoss, leg.Type, "the stop, which the take profit precedes")
		require.InDelta(t, 98, *leg.Stop, 1e-9, "1 ATR of 2")
	})
}
