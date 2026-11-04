package entryfilter

import (
	"context"
	"errors"
	"flag"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/metalabel"
	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/ui"
)

const pair = "BTCUSDT"

// script is a strategy of the tests: on each candle, it runs on with the
// broker it is handed, if set.
type script struct {
	timeframe string
	warmup    int
	on        func(df *model.Dataframe, broker service.Broker)
	handed    []service.Broker
}

func (s *script) Timeframe() string { return s.timeframe }
func (s *script) WarmupPeriod() int { return s.warmup }
func (s *script) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	return []strategy.ChartIndicator{{GroupName: "script", Time: df.Time}}
}
func (s *script) OnCandle(df *model.Dataframe, broker service.Broker) {
	s.handed = append(s.handed, broker)
	if s.on != nil {
		s.on(df, broker)
	}
}

// hfScript is a script with partial candles.
type hfScript struct {
	script
	partials int
}

func (s *hfScript) OnPartialCandle(*model.Dataframe, service.Broker) { s.partials++ }

// newsScript is a script trading around the releases.
type newsScript struct {
	script
	source strategies.Releases
}

func (s *newsScript) SetReleases(source strategies.Releases) { s.source = source }

// hfNewsScript has partial candles and trades around the releases.
type hfNewsScript struct {
	hfScript
	source strategies.Releases
}

func (s *hfNewsScript) SetReleases(source strategies.Releases) { s.source = source }

// analysisScript is a script drawing an analysis.
type analysisScript struct{ script }

func (s *analysisScript) ChartAnalysis(pair string) (ui.Analysis, bool) {
	return ui.Analysis{Pair: pair}, true
}

// bench runs a strategy filtered on candles of 15m, trading a paper wallet
// through the order controller of the bot, as the engine does.
type bench struct {
	t      *testing.T
	s      strategy.Strategy
	wallet *exchange.PaperWallet
	ctl    *order.Controller
	df     *model.Dataframe
}

func newBench(t *testing.T, s strategy.Strategy, options ...exchange.PaperWalletOption) *bench {
	t.Helper()
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", append([]exchange.PaperWalletOption{
		exchange.WithPaperAsset("USDT", 100000)}, options...)...)
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	ctl := order.NewController(context.Background(), wallet, memory, order.NewOrderFeed())
	ctl.Start()
	t.Cleanup(ctl.Stop)
	return &bench{t: t, s: s, wallet: wallet, ctl: ctl,
		df: &model.Dataframe{Pair: pair, Metadata: map[string]model.Series[float64]{}}}
}

var day = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)

// feed feeds candles, each of its open, high, low and close.
func (b *bench) feed(bars ...[4]float64) {
	b.t.Helper()
	for _, bar := range bars {
		at := day.Add(time.Duration(len(b.df.Close)) * 15 * time.Minute)
		b.wallet.OnCandle(model.Candle{Pair: pair, Time: at, UpdatedAt: at, Open: bar[0], High: bar[1], Low: bar[2],
			Close: bar[3], Volume: 1, Complete: true})
		b.ctl.UpdateOrders()
		df := b.df
		df.Time = append(df.Time, at)
		df.Open = append(df.Open, bar[0])
		df.High = append(df.High, bar[1])
		df.Low = append(df.Low, bar[2])
		df.Close = append(df.Close, bar[3])
		df.Volume = append(df.Volume, 1)
		b.s.Indicators(df)
		b.s.OnCandle(df, b.ctl)
	}
}

// line returns n candles from price, each move up apart, 0.5 of range.
func line(n int, price, move float64) [][4]float64 {
	bars := make([][4]float64, n)
	for i := range bars {
		p := price + float64(i)*move
		bars[i] = [4]float64{p - move/2, p + 0.5, p - 0.5, p}
	}
	return bars
}

// zigzag returns n candles going up and down around price.
func zigzag(n int, price float64) [][4]float64 {
	bars := make([][4]float64, n)
	for i := range bars {
		p := price + float64(i%2)
		bars[i] = [4]float64{p, p + 0.5, p - 0.5, p}
	}
	return bars
}

// Off, the strategy is left as it is: the agents without a filter trade as
// they did.
func TestWrap_OffLeavesTheStrategy(t *testing.T) {
	s := &script{timeframe: "15m", warmup: 5}
	wrapped, err := Wrap(s, Config{})
	require.NoError(t, err)
	assert.Same(t, s, wrapped)

	cfg, err := RegisterFlags(flag.NewFlagSet("agent", flag.ContinueOnError))()
	require.NoError(t, err)
	assert.False(t, cfg.Enabled(), "every filter off by default")
	wrapped, err = Wrap(s, cfg)
	require.NoError(t, err)
	assert.Same(t, s, wrapped)
}

// The wrapper keeps the partial candles and the releases of the strategy only
// when it has them, its warmup the most of its own and the filters', its
// timeframe, its indicators, its analysis.
func TestWrap_KeepsTheInterfacesOfTheStrategy(t *testing.T) {
	cfg := Config{Regime: RegimeTrend}
	plain, err := Wrap(&script{timeframe: "15m", warmup: 5}, cfg)
	require.NoError(t, err)
	_, hf := plain.(strategy.HighFrequencyStrategy)
	_, releases := plain.(strategies.ReleasesUser)
	assert.False(t, hf, "no partial candles asked of a strategy without them")
	assert.False(t, releases)
	assert.Equal(t, "15m", plain.Timeframe())
	assert.Equal(t, DefaultERLen+1, max(plain.WarmupPeriod(), 5), "the regime needs 11 candles")
	_, drawn := plain.(ui.AnalysisSource).ChartAnalysis(pair)
	assert.False(t, drawn, "no analysis of its own")

	inner := &hfScript{script: script{timeframe: "1m", warmup: 300}}
	wrapped, err := Wrap(inner, cfg)
	require.NoError(t, err)
	fast, ok := wrapped.(strategy.HighFrequencyStrategy)
	require.True(t, ok)
	fast.OnPartialCandle(&model.Dataframe{}, nil)
	assert.Equal(t, 1, inner.partials, "forwarded")
	assert.Equal(t, 300, wrapped.WarmupPeriod(), "the strategy's, larger")

	news := &newsScript{script: script{timeframe: "5m"}}
	wrapped, err = Wrap(news, Config{MetaRecord: filepath.Join(t.TempDir(), "s.jsonl")})
	require.NoError(t, err)
	user, ok := wrapped.(strategies.ReleasesUser)
	require.True(t, ok)
	var source strategies.Releases = releasesOf(nil)
	user.SetReleases(source)
	assert.Equal(t, source, news.source, "forwarded")
	assert.Equal(t, metalabel.Window, wrapped.WarmupPeriod(), "the features need their window")

	both := &hfNewsScript{hfScript: hfScript{script: script{timeframe: "1m"}}}
	wrapped, err = Wrap(both, cfg)
	require.NoError(t, err)
	_, hf = wrapped.(strategy.HighFrequencyStrategy)
	_, releases = wrapped.(strategies.ReleasesUser)
	assert.True(t, hf && releases, "both kept")

	analysis, err := Wrap(&analysisScript{script{timeframe: "1h"}}, cfg)
	require.NoError(t, err)
	got, drawn := analysis.(ui.AnalysisSource).ChartAnalysis("XAUUSDm")
	assert.True(t, drawn)
	assert.Equal(t, "XAUUSDm", got.Pair)

	indicators := plain.Indicators(&model.Dataframe{Close: make(model.Series[float64], 20),
		Time: make([]time.Time, 20)})
	require.Len(t, indicators, 2, "the strategy's and the regime")
	assert.Equal(t, "Regime (er)", indicators[1].GroupName)
}

// releasesOf is a source of releases of the tests.
type releasesOf []strategies.Release

func (r releasesOf) Releases(context.Context, string, time.Time, time.Time) ([]strategies.Release, error) {
	return r, nil
}

// The flags of the filter: off by default, each method with its defaults, a
// flag of a filter set without it or a value out of bounds refused.
func TestRegisterFlags(t *testing.T) {
	build := func(args ...string) (Config, error) {
		fs := flag.NewFlagSet("agent", flag.ContinueOnError)
		config := RegisterFlags(fs)
		require.NoError(t, fs.Parse(args))
		return config()
	}
	cfg, err := build("-regime=trend", "-regime-method=adx")
	require.NoError(t, err)
	assert.Equal(t, DefaultADXLen, cfg.RegimeLen)
	assert.Equal(t, float64(DefaultADXThreshold), cfg.RegimeThreshold)
	cfg, err = build("-regime=range", "-regime-method=hurst")
	require.NoError(t, err)
	assert.Equal(t, [2]float64{DefaultHurstLen, DefaultHurstThreshold}, [2]float64{float64(cfg.RegimeLen),
		cfg.RegimeThreshold})
	cfg, err = build("-regime=trend")
	require.NoError(t, err)
	assert.Equal(t, [2]float64{DefaultERLen, DefaultERThreshold}, [2]float64{float64(cfg.RegimeLen),
		cfg.RegimeThreshold})
	cfg, err = build("-intermarket-symbol=DXYm", "-intermarket-pairs=XAUUSDm;BTCUSDm", "-intermarket-mode=direct")
	require.NoError(t, err)
	assert.Equal(t, []string{"XAUUSDm", "BTCUSDm"}, cfg.IntermarketPairs)
	assert.Equal(t, []string{"DXYm"}, cfg.References())
	assert.Equal(t, DefaultIntermarketEMA, cfg.IntermarketEMA)
	cfg, err = build("-equity-curve=20", "-meta-record=s.jsonl", "-meta-max-bars=50")
	require.NoError(t, err)
	assert.True(t, cfg.Enabled())
	assert.Equal(t, [3]int{20, DefaultEquityMaxBars, 50}, [3]int{cfg.EquityCurve, cfg.EquityCurveMaxBars,
		cfg.MetaMaxBars})

	for _, args := range [][]string{
		{"-regime=up"}, {"-regime=trend", "-regime-method=rsi"}, {"-regime-len=20"},
		{"-regime-method=adx"}, {"-regime=trend", "-regime-threshold=1.5"},
		{"-regime=trend", "-regime-method=hurst", "-regime-len=20"},
		{"-regime=trend", "-regime-method=hurst", "-regime-threshold=1"},
		{"-regime=trend", "-regime-method=adx", "-regime-threshold=101"},
		{"-regime-max-atr-pct=101"}, {"-regime-max-atr-pct=-1"}, {"-regime-atr-window=50"},
		{"-regime-max-atr-pct=90", "-regime-atr-window=1"},
		{"-equity-curve=1"}, {"-equity-curve=-3"}, {"-equity-curve-max-bars=10"},
		{"-equity-curve=5", "-equity-curve-max-bars=-1"},
		{"-meta-threshold=0.6"}, {"-meta-model=m.json", "-meta-threshold=1"}, {"-meta-max-bars=10"},
		{"-intermarket-mode=direct"}, {"-intermarket-symbol=DXYm", "-intermarket-mode=sideways"},
		{"-intermarket-symbol=DXYm", "-intermarket-ema=1"}, {"-intermarket-pairs=XAUUSDm"},
		{"-intermarket-symbol=DXYm", "-intermarket-pairs=DXYm"},
	} {
		_, err := build(args...)
		assert.Error(t, err, "%v", args)
	}
}

// A model that can't be read fails the wrap: the agent doesn't start.
func TestWrap_ModelUnreadableFails(t *testing.T) {
	_, err := Wrap(&script{timeframe: "15m"}, Config{MetaModel: filepath.Join(t.TempDir(), "missing.json")})
	require.ErrorContains(t, err, "-meta-model")
	_, err = Wrap(&script{timeframe: "15m"}, Config{Regime: "up"})
	require.Error(t, err)
}

// buyer returns a script buying size on each candle, flat or not, its errors
// recorded.
func buyer(size float64, errs *[]error) *script {
	return &script{timeframe: "15m", warmup: 1, on: func(df *model.Dataframe, broker service.Broker) {
		_, err := broker.CreateOrderMarket(model.SideTypeBuy, df.Pair, size)
		*errs = append(*errs, err)
	}}
}

// A regime of trend refuses the entries of a market going nowhere and allows
// the ones of a trend; one of range the other way; not known yet, refused.
func TestFilter_Regime(t *testing.T) {
	var errs []error
	s := buyer(0.01, &errs)
	wrapped, err := Wrap(s, Config{Regime: RegimeTrend})
	require.NoError(t, err)
	b := newBench(t, wrapped)
	b.feed(zigzag(5, 100)...)
	require.Len(t, errs, 5)
	assert.ErrorIs(t, errs[0], order.ErrEntriesBlocked, "not known yet: refused")
	assert.ErrorContains(t, errs[0], "not known yet")
	b.feed(zigzag(10, 100)...)
	assert.ErrorIs(t, errs[len(errs)-1], order.ErrEntriesBlocked)
	assert.ErrorContains(t, errs[len(errs)-1], "not a trend market")
	asset, _, err := b.wallet.Position(pair)
	require.NoError(t, err)
	assert.Zero(t, asset)
	b.feed(line(12, 101, 1)...)
	assert.NoError(t, errs[len(errs)-1], "a straight line: a trend")
	asset, _, _ = b.wallet.Position(pair)
	assert.Positive(t, asset)
	_, isGated := s.handed[0].(gatedController)
	assert.True(t, isGated, "the order controller kept")
	_, protected := s.handed[0].(service.ProtectionBroker)
	_, legs := s.handed[0].(service.PositionLegs)
	_, points := s.handed[0].(service.PointValuer)
	assert.True(t, protected && legs && points, "its interfaces kept")

	errs = nil
	ranging, err := Wrap(buyer(0.01, &errs), Config{Regime: RegimeRange})
	require.NoError(t, err)
	r := newBench(t, ranging)
	r.feed(line(15, 100, 1)...)
	assert.ErrorContains(t, errs[len(errs)-1], "not a range market")
	r.feed(zigzag(15, 120)...)
	assert.NoError(t, errs[len(errs)-1])
}

// The volatility filter refuses the entries while the ATR is at an extreme
// percentile of its window.
func TestFilter_Volatility(t *testing.T) {
	var errs []error
	wrapped, err := Wrap(buyer(0.01, &errs), Config{RegimeMaxATRPct: 90, RegimeATRWindow: 20})
	require.NoError(t, err)
	assert.Equal(t, 11*atrLen, wrapped.WarmupPeriod())
	b := newBench(t, wrapped)
	b.feed(zigzag(60, 100)...)
	assert.ErrorContains(t, errs[len(errs)-1], "volatility", "every ATR the same: none below")
	calm := make([][4]float64, 0, 60)
	for i := range 60 {
		w := 0.5 + float64(i%7)*0.1
		calm = append(calm, [4]float64{100, 100 + w, 100 - w, 100})
	}
	b.feed(calm...)
	b.feed([4]float64{100, 100.2, 99.8, 100})
	assert.NoError(t, errs[len(errs)-1], "a quiet candle")
	b.feed([4]float64{100, 110, 90, 100})
	assert.ErrorContains(t, errs[len(errs)-1], "volatility: the ATR at the percentile 100")
}

// The exits always pass: an order reducing the position held, whatever the
// filter; a reversal refused sends its exit alone; the limit and stop orders
// protecting the position pass.
func TestFilter_ExitsPass(t *testing.T) {
	var errs []error
	var orders []model.Order
	step := 0
	s := &script{timeframe: "15m", warmup: 1}
	wrapped, err := Wrap(s, Config{Regime: RegimeTrend, RegimeLen: 2})
	require.NoError(t, err)
	b := newBench(t, wrapped)
	s.on = func(df *model.Dataframe, broker service.Broker) {
		step++
		var o model.Order
		var err error
		switch step {
		case 5: // a trend: the entry allowed
			o, err = broker.CreateOrderMarket(model.SideTypeBuy, pair, 1)
		case 8: // no trend: a limit and a stop protecting it pass, an entry doesn't
			o, err = broker.CreateOrderLimit(model.SideTypeSell, pair, 1, 200)
			errs, orders = append(errs, err), append(orders, o)
			o, err = broker.CreateOrderStop(pair, 1, 50)
			errs, orders = append(errs, err), append(orders, o)
			_, err = broker.CreateOrderLimit(model.SideTypeBuy, pair, 1, 90)
			errs, orders = append(errs, err), append(orders, model.Order{})
			for _, open := range orders[len(orders)-3 : len(orders)-1] {
				require.NoError(t, broker.Cancel(open))
			}
			return
		case 9: // a reversal refused: its exit alone
			o, err = broker.CreateOrderMarket(model.SideTypeSell, pair, 2)
		default:
			return
		}
		errs, orders = append(errs, err), append(orders, o)
	}
	b.feed(line(5, 100, 1)...)
	require.NoError(t, errs[0])
	asset, _, _ := b.wallet.Position(pair)
	require.InDelta(t, 1, asset, 1e-9)

	b.feed(zigzag(4, 104)...)
	require.Len(t, errs, 5)
	assert.NoError(t, errs[1], "a take profit: protects")
	assert.NoError(t, errs[2], "a stop: protects")
	assert.ErrorIs(t, errs[3], order.ErrEntriesBlocked, "a buy limit: an entry, refused")
	assert.ErrorIs(t, errs[4], order.ErrEntriesBlocked, "the reversal cut to its exit: done in part, told")
	assert.ErrorContains(t, errs[4], "read the position again")
	assert.InDelta(t, 1, orders[4].Quantity, 1e-9, "the order of the exit returned with the refusal")
	asset, _, _ = b.wallet.Position(pair)
	assert.InDelta(t, 0, asset, 1e-9, "flat, not short")
}

// A gate alone (no filter on): the strategy wrapped only for it, its entries
// refused on an asset the gate refuses and allowed on the others; its exits and
// the orders protecting a position always pass; nothing else refused, not even
// before a candle.
func TestFilter_GateAlone(t *testing.T) {
	gate := &agentapi.PairGate{}
	s := &script{timeframe: "15m", warmup: 3}
	wrapped, err := Wrap(s, Config{Gate: gate})
	require.NoError(t, err)
	_, isFilter := wrapped.(*filter)
	require.True(t, isFilter, "wrapped for the gate")
	assert.Equal(t, 3, wrapped.WarmupPeriod())
	b := newBench(t, wrapped)
	var errs []error
	step := 0
	s.on = func(df *model.Dataframe, broker service.Broker) {
		step++
		switch step {
		case 1: // every asset: the entry allowed
			_, err := broker.CreateOrderMarket(model.SideTypeBuy, pair, 1)
			errs = append(errs, err)
			gate.Set(1, []string{"XAUUSDm"}, "Janela 08:00–10:00 só para o ativo XAUUSDm")
		case 2: // only XAUUSDm: an entry refused with why, the take profit and the exit pass
			_, err := broker.CreateOrderMarket(model.SideTypeBuy, pair, 1)
			errs = append(errs, err)
			o, err := broker.CreateOrderLimit(model.SideTypeSell, pair, 1, 200)
			errs = append(errs, err)
			require.NoError(t, broker.Cancel(o))
			_, err = broker.CreateOrderMarket(model.SideTypeSell, pair, 1)
			errs = append(errs, err)
		}
	}
	b.feed(line(2, 100, 1)...)
	require.Len(t, errs, 4)
	assert.NoError(t, errs[0])
	assert.ErrorIs(t, errs[1], order.ErrEntriesBlocked)
	assert.ErrorContains(t, errs[1], "Janela 08:00–10:00 só para o ativo XAUUSDm")
	assert.NoError(t, errs[2], "a take profit: protects")
	assert.NoError(t, errs[3], "the exit passes")
	asset, _, _ := b.wallet.Position(pair)
	assert.InDelta(t, 0, asset, 1e-9, "flat")
}

// The exposure limit alone (no filter on): the strategy wrapped only for it,
// each entry claimed of the controller with its asset and side and refused with
// why when not granted; the exits and the orders protecting a position never
// claimed.
func TestFilter_ClaimAlone(t *testing.T) {
	type claimed struct {
		pair string
		side model.SideType
	}
	var claims []claimed
	refuse := true
	s := &script{timeframe: "15m", warmup: 1}
	wrapped, err := Wrap(s, Config{Claim: func(pair string, side model.SideType) (bool, string) {
		claims = append(claims, claimed{pair, side})
		if refuse {
			return false, "Exposição máxima em BTCUSDT: 1 agente já está comprado (trend)"
		}
		return true, ""
	}})
	require.NoError(t, err)
	_, isFilter := wrapped.(*filter)
	require.True(t, isFilter, "wrapped for the claim")
	b := newBench(t, wrapped)
	var errs []error
	step := 0
	s.on = func(df *model.Dataframe, broker service.Broker) {
		step++
		switch step {
		case 1: // refused: the entry not sent, with why
			_, err := broker.CreateOrderMarket(model.SideTypeBuy, pair, 1)
			errs = append(errs, err)
			refuse = false
		case 2: // granted: the entry sent, then its take profit and its exit not claimed
			_, err := broker.CreateOrderMarket(model.SideTypeBuy, pair, 1)
			errs = append(errs, err)
			o, err := broker.CreateOrderLimit(model.SideTypeSell, pair, 1, 200)
			errs = append(errs, err)
			require.NoError(t, broker.Cancel(o))
			_, err = broker.CreateOrderMarket(model.SideTypeSell, pair, 1)
			errs = append(errs, err)
		}
	}
	b.feed(line(2, 100, 1)...)
	require.Len(t, errs, 4)
	assert.ErrorIs(t, errs[0], order.ErrEntriesBlocked)
	assert.ErrorContains(t, errs[0], "Exposição máxima em BTCUSDT: 1 agente já está comprado (trend)")
	assert.NoError(t, errs[1], "granted")
	assert.NoError(t, errs[2], "a take profit: protects")
	assert.NoError(t, errs[3], "the exit passes")
	assert.Equal(t, []claimed{{pair, model.SideTypeBuy}, {pair, model.SideTypeBuy}}, claims, "only the entries claimed")
	asset, _, _ := b.wallet.Position(pair)
	assert.InDelta(t, 0, asset, 1e-9, "flat")
}

// The exits are judged against the position the exchange holds too, the one
// the strategies size them from, not only the one the order controller
// follows, which lags it when a fill was dropped or its orders wiped: an exit
// of the exchange position passes whole while the entries are refused, never
// trimmed to the followed one; a reversal beyond the exchange position never
// asks for its exit part, which is sent alone, the refusal returned with it;
// the limit and stop orders protecting it pass.
func TestFilter_ExitsOfThePositionTheExchangeHolds(t *testing.T) {
	// the exchange holds 1 the order controller doesn't follow, and 1 it does
	var errs []error
	var orders []model.Order
	s := &script{timeframe: "15m", warmup: 1}
	wrapped, err := Wrap(s, Config{Regime: RegimeTrend}) // never ready here: every entry refused
	require.NoError(t, err)
	b := newBench(t, wrapped, exchange.WithPaperAsset("BTC", 1))
	b.feed([4]float64{100, 101, 99, 100})
	_, err = b.ctl.CreateOrderMarket(model.SideTypeBuy, pair, 1)
	require.NoError(t, err)
	followed, ok := b.ctl.HeldPosition(pair)
	require.True(t, ok)
	require.InDelta(t, 1, followed.Quantity, 1e-9, "the order controller follows 1")
	s.on = func(df *model.Dataframe, broker service.Broker) {
		asset, _, err := broker.Position(df.Pair)
		require.NoError(t, err)
		o, err := broker.CreateOrderMarket(model.SideTypeSell, df.Pair, asset)
		errs, orders = append(errs, err), append(orders, o)
	}
	b.feed([4]float64{100, 101, 99, 100})
	require.Len(t, errs, 1)
	assert.NoError(t, errs[0], "the exit of the 2 held at the exchange")
	assert.InDelta(t, 2, orders[0].Quantity, 1e-9, "whole, never trimmed to the 1 followed")
	asset, _, _ := b.wallet.Position(pair)
	assert.InDelta(t, 0, asset, 1e-9, "flat")

	// the exchange holds 2 the order controller doesn't follow: a reversal of 3
	errs, orders = nil, nil
	s = &script{timeframe: "15m", warmup: 1}
	wrapped, err = Wrap(s, Config{Regime: RegimeTrend})
	require.NoError(t, err)
	b = newBench(t, wrapped, exchange.WithPaperAsset("BTC", 2))
	s.on = func(df *model.Dataframe, broker service.Broker) {
		o, err := broker.CreateOrderMarket(model.SideTypeSell, df.Pair, 3)
		errs, orders = append(errs, err), append(orders, o)
	}
	b.feed([4]float64{100, 101, 99, 100})
	require.Len(t, errs, 1)
	assert.ErrorIs(t, errs[0], order.ErrEntriesBlocked, "its entry refused")
	assert.ErrorContains(t, errs[0], "the reversal cut to its exit, 2 of 3 sent")
	assert.InDelta(t, 2, orders[0].Quantity, 1e-9, "its exit sent alone")
	asset, _, _ = b.wallet.Position(pair)
	assert.InDelta(t, 0, asset, 1e-9, "flat, not short")

	// the exchange holds 1 the order controller doesn't follow: its protections
	errs = nil
	s = &script{timeframe: "15m", warmup: 1}
	wrapped, err = Wrap(s, Config{Regime: RegimeTrend})
	require.NoError(t, err)
	b = newBench(t, wrapped, exchange.WithPaperAsset("BTC", 1))
	s.on = func(df *model.Dataframe, broker service.Broker) {
		_, err := broker.CreateOrderLimit(model.SideTypeSell, df.Pair, 1, 200)
		errs = append(errs, err)
		_, err = broker.CreateOrderStop(df.Pair, 1, 50)
		errs = append(errs, err)
		_, err = broker.CreateOrderLimit(model.SideTypeSell, df.Pair, 2, 200)
		errs = append(errs, err)
	}
	b.feed([4]float64{100, 101, 99, 100})
	require.Len(t, errs, 3)
	assert.NoError(t, errs[0], "a take profit of the position at the exchange")
	assert.NoError(t, errs[1], "a stop of it")
	assert.ErrorIs(t, errs[2], order.ErrEntriesBlocked, "beyond it: an entry, refused")
}

// A strategy handed another broker than the order controller keeps the
// protected orders, its entries filtered.
func TestFilter_AnotherBroker(t *testing.T) {
	f := &filter{cfg: Config{Regime: RegimeTrend}.withDefaults(), pairs: map[string]*pairState{},
		tracker: newTracker(0, 0, nil)}
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 1000))
	gated := f.gate(wallet)
	protected, ok := gated.(service.ProtectionBroker)
	require.True(t, ok)
	_, err := protected.CreateOrderMarketProtected(model.SideTypeBuy, pair, 1, 90, 110)
	assert.ErrorIs(t, err, order.ErrEntriesBlocked, "no candle handed yet: not ready")
	_, ok = f.gate(plainBroker{}).(service.ProtectionBroker)
	assert.False(t, ok)
}

// plainBroker is a broker without the protected orders.
type plainBroker struct{ service.Broker }

// The equity curve of the theoretical trades: a run of losses pauses the
// entries once it is under its SMA, the trades refused still followed, and a
// recovery resumes them.
func TestFilter_EquityCurve(t *testing.T) {
	var errs []error
	s := &script{timeframe: "15m", warmup: 1}
	wrapped, err := Wrap(s, Config{EquityCurve: 3, EquityCurveMaxBars: 50})
	require.NoError(t, err)
	b := newBench(t, wrapped)
	f := wrapped.(*filter)
	s.on = func(df *model.Dataframe, broker service.Broker) {
		if asset, _, _ := broker.Position(df.Pair); asset != 0 || df.Close.Last(0) != 100 {
			return // an entry on the candles closing at 100 only, flat
		}
		_, err := broker.(service.ProtectionBroker).CreateOrderMarketProtected(model.SideTypeBuy, df.Pair, 0.01,
			98, 102)
		errs = append(errs, err)
	}
	// each entry stopped on the next candle: -1 R each
	for range 4 {
		b.feed([4]float64{100, 100.5, 99.5, 100}, [4]float64{100, 100.2, 97, 98})
	}
	assert.Equal(t, []float64{-1, -2, -3, -4}, f.tracker.curve[:4])
	assert.ErrorContains(t, errs[len(errs)-1], "equity curve", "under its SMA")
	refusedTrades := len(f.tracker.curve)
	// the theoretical trades of the entries refused win: the curve back above its SMA
	for range 3 {
		b.feed([4]float64{100, 100.5, 99.5, 100}, [4]float64{100, 103, 99.5, 102.5})
	}
	assert.Greater(t, len(f.tracker.curve), refusedTrades, "the refused entries followed")
	b.feed([4]float64{100, 100.5, 99.5, 100})
	assert.NoError(t, errs[len(errs)-1], "back above its SMA")
}

// The equity curve of a backtest starts the period empty, as it starts live:
// the theoretical trades decided in the warm-up, before RecordFrom, never
// enter it, however they end; the ones of the period do.
func TestFilter_EquityCurveFromRecordFrom(t *testing.T) {
	var errs []error
	s := &script{timeframe: "15m", warmup: 1}
	from := day.Add(8 * 15 * time.Minute) // the close of the eighth candle
	wrapped, err := Wrap(s, Config{EquityCurve: 3, EquityCurveMaxBars: 50, RecordFrom: from})
	require.NoError(t, err)
	b := newBench(t, wrapped)
	f := wrapped.(*filter)
	s.on = func(df *model.Dataframe, broker service.Broker) {
		if asset, _, _ := broker.Position(df.Pair); asset != 0 || df.Close.Last(0) != 100 {
			return
		}
		_, err := broker.(service.ProtectionBroker).CreateOrderMarketProtected(model.SideTypeBuy, df.Pair, 0.01,
			98, 102)
		errs = append(errs, err)
	}
	// the warm-up: four entries, each stopped on the next candle
	for range 4 {
		b.feed([4]float64{100, 100.5, 99.5, 100}, [4]float64{100, 100.2, 97, 98})
	}
	assert.Empty(t, f.tracker.curve, "the warm-up never counted")
	b.feed([4]float64{100, 100.5, 99.5, 100})
	assert.NoError(t, errs[len(errs)-1], "the first entry of the period: no curve yet")
	b.feed([4]float64{100, 100.2, 97, 98})
	assert.Equal(t, []float64{-1}, f.tracker.curve, "the trades of the period counted")
}

// The theoretical trades: the stop, the target, both on one candle (the stop),
// the time, a pending entry filled or discarded, a trade the bot took ended
// as its position goes flat.
func TestTracker(t *testing.T) {
	at := day
	c := func(i int, o, h, l, cl float64) candle {
		return candle{time: at.Add(time.Duration(i) * time.Hour), open: o, high: h, low: l, close: cl}
	}
	var samples []metalabel.Sample
	k := newTracker(3, 3, func(s metalabel.Sample) { samples = append(samples, s) })
	flat := func(model.SideType) bool { return false }
	sample := &metalabel.Sample{Pair: "A"}

	k.add(&theoretical{pair: "A", side: model.SideTypeBuy, kind: kindMarket, price: 100, stop: 98, target: 104,
		at: at}, sample)
	k.onCandle("A", c(0, 100, 110, 90, 100), flat) // the candle of the entry: never
	k.onCandle("A", c(1, 100, 104.5, 99, 103), flat)
	require.Equal(t, []float64{2}, k.curve, "the target: 2 R")
	require.Len(t, samples, 1)
	assert.Equal(t, 1, samples[0].Label)
	assert.Equal(t, metalabel.OutcomeTarget, samples[0].Outcome)

	k.add(&theoretical{pair: "A", side: model.SideTypeSell, kind: kindMarket, price: 100, stop: 102, target: 96,
		at: c(2, 0, 0, 0, 0).time}, sample)
	k.onCandle("A", c(3, 100, 102.5, 95, 99), flat)
	assert.Equal(t, []float64{2, 1}, k.curve, "both: the stop")
	assert.Equal(t, metalabel.OutcomeStop, samples[1].Outcome)
	assert.Equal(t, 0, samples[1].Label)

	k.add(&theoretical{pair: "A", side: model.SideTypeBuy, kind: kindMarket, price: 100, stop: 98,
		at: c(4, 0, 0, 0, 0).time}, sample)
	for i := 5; i <= 7; i++ {
		k.onCandle("A", c(i, 100, 101, 99, 101), flat)
	}
	assert.Equal(t, []float64{2, 1, 1.5}, k.curve, "the time: the R of the move")
	assert.Equal(t, metalabel.OutcomeTime, samples[2].Outcome)
	assert.Equal(t, 1, samples[2].Label, "a positive result")

	k.add(&theoretical{pair: "A", side: model.SideTypeBuy, kind: kindLimit, price: 95, stop: 93, target: 99,
		at: c(8, 0, 0, 0, 0).time}, sample)
	k.onCandle("A", c(9, 100, 101, 96, 97), flat)
	k.onCandle("A", c(10, 97, 98, 94, 98), flat) // filled at 95, the target not on its candle
	k.onCandle("A", c(11, 98, 99.5, 97, 99), flat)
	assert.Equal(t, []float64{2, 1, 1.5, 3.5}, k.curve)
	assert.Equal(t, 95.0, samples[3].Entry)

	k.add(&theoretical{pair: "A", side: model.SideTypeBuy, kind: kindStop, price: 120, stop: 118,
		at: c(12, 0, 0, 0, 0).time}, sample)
	for i := 13; i <= 15; i++ {
		k.onCandle("A", c(i, 100, 101, 99, 100), flat)
	}
	assert.Len(t, k.curve, 4, "never filled: discarded")
	assert.Len(t, samples, 4)
	assert.Empty(t, k.trades)

	// a trade without a stop: the labels only
	k.add(&theoretical{pair: "A", side: model.SideTypeBuy, kind: kindMarket, price: 100,
		at: c(16, 0, 0, 0, 0).time}, sample)
	assert.False(t, k.counting("A"))
	k.add(&theoretical{pair: "A", side: model.SideTypeBuy, kind: kindMarket, price: 100, stop: 99,
		at: c(16, 0, 0, 0, 0).time, real: true}, nil)
	k.add(&theoretical{pair: "A", side: model.SideTypeBuy, kind: kindMarket, price: 100, stop: 99,
		at: c(16, 0, 0, 0, 0).time}, nil)
	require.Len(t, k.trades["A"], 2, "one counted at a time; the one without views dropped")
	holding := func(side model.SideType) bool { return side == model.SideTypeBuy }
	k.observe("A", c(16, 100, 100, 100, 100), holding)
	k.onCandle("A", c(17, 100, 100.5, 99.5, 100.5), holding)
	k.onCandle("A", c(18, 100.5, 100.8, 99.5, 100.2), flat)
	assert.Equal(t, []float64{2, 1, 1.5, 3.5, 3.7}, roundAll(k.curve), "flat: at the close, 0.2 R")
	assert.Len(t, samples, 4, "the one without a stop still open")
	k.onCandle("A", c(19, 100, 100.8, 99.5, 99), flat)
	require.Len(t, samples, 5)
	assert.Zero(t, samples[4].R, "no stop: no R")
	assert.Equal(t, 0, samples[4].Label, "under its entry")

	paused, equity, sma := k.paused(3)
	assert.False(t, paused)
	assert.InDelta(t, 3.7, equity, 1e-9)
	assert.InDelta(t, (1.5+3.5+3.7)/3, sma, 1e-9)
}

func roundAll(values []float64) []float64 {
	out := make([]float64, len(values))
	for i, v := range values {
		out[i] = math.Round(v*1e6) / 1e6
	}
	return out
}

// sourceOf is a source of candles of the tests: the candles of the reference,
// or err.
type sourceOf struct {
	candles []model.Candle
	err     error
	asked   int
}

func (s *sourceOf) CandlesByLimit(_ context.Context, symbol, period string, limit int) ([]model.Candle, error) {
	s.asked++
	if s.err != nil {
		return nil, s.err
	}
	return s.candles[max(len(s.candles)-limit, 0):], nil
}

// reference returns n candles of 15m of the reference from day, closing at
// close(i).
func reference(n int, close func(i int) float64) []model.Candle {
	candles := make([]model.Candle, n)
	for i := range candles {
		candles[i] = model.Candle{Pair: "DXYm", Time: day.Add(time.Duration(i) * 15 * time.Minute), Close: close(i),
			Complete: true}
	}
	return candles
}

// The intermarket filter: inverse, a buy only while the reference is below its
// EMA and a sell only while above; direct the other way; not read, too few
// candles or stale, both refused; only the candles closed by the candle of
// the pair count; a pair not filtered untouched.
func TestFilter_Intermarket(t *testing.T) {
	cfg := Config{IntermarketSymbol: "DXYm", IntermarketEMA: 5}.withDefaults()
	at := day.Add(29 * 15 * time.Minute)
	falling := reference(30, func(i int) float64 { return 110 - float64(i) })
	buy, sell := intermarketReasons(cfg, falling, nil, at, 15*time.Minute)
	assert.Empty(t, buy, "the dollar falling: gold bought")
	assert.Contains(t, sell, "no sell")

	direct := cfg
	direct.IntermarketMode = IntermarketDirect
	buy, sell = intermarketReasons(direct, falling, nil, at, 15*time.Minute)
	assert.Contains(t, buy, "no buy")
	assert.Empty(t, sell)

	// a candle opened after the one of the pair: not closed then, left out
	future := append(append([]model.Candle(nil), falling...), model.Candle{Time: at.Add(15 * time.Minute),
		Close: 1000, Complete: true})
	buy, _ = intermarketReasons(cfg, future, nil, at, 15*time.Minute)
	assert.Empty(t, buy, "never the future")

	for name, tc := range map[string]struct {
		refs []model.Candle
		err  error
		at   time.Time
	}{
		"not read": {nil, errors.New("down"), at},
		"too few":  {falling[:4], nil, at},
		"stale":    {falling, nil, at.Add(5 * 15 * time.Minute)},
		"incomplete": {func() []model.Candle {
			c := append([]model.Candle(nil), falling...)
			for i := range c {
				c[i].Complete = false
			}
			return c
		}(), nil, at},
	} {
		buy, sell := intermarketReasons(cfg, tc.refs, tc.err, tc.at, 15*time.Minute)
		assert.NotEmpty(t, buy, name)
		assert.Equal(t, buy, sell, name)
	}
	stillFresh := at.Add(4 * 15 * time.Minute)
	buy, _ = intermarketReasons(cfg, falling, nil, stillFresh, 15*time.Minute)
	assert.Empty(t, buy, "4 candles old: fresh enough")

	// a candle of a backtest file merged with the next one: opened with the one
	// of the pair, but holding data of after its close, left out; one updated
	// at its open, as MT5 tells them, kept
	merged := append([]model.Candle(nil), falling[:len(falling)-1]...)
	merged = append(merged, model.Candle{Time: at, UpdatedAt: at.Add(20 * time.Minute), Close: 1000, Complete: true})
	buy, sell = intermarketReasons(cfg, merged, nil, at, 15*time.Minute)
	assert.Empty(t, buy, "the merged candle left out: the dollar still falling")
	assert.Contains(t, sell, "no sell")
	merged[len(merged)-1].UpdatedAt = at
	buy, _ = intermarketReasons(cfg, merged, nil, at, 15*time.Minute)
	assert.Contains(t, buy, "no buy", "updated at its open: kept")

	// through the strategy: read once per candle, refused without a source
	var errs []error
	wrapped, err := Wrap(buyer(0.01, &errs), Config{IntermarketSymbol: "DXYm", IntermarketEMA: 5,
		IntermarketPairs: []string{pair}})
	require.NoError(t, err)
	assert.Equal(t, 25, wrapped.WarmupPeriod(), "the candles of the reference read")
	b := newBench(t, wrapped)
	b.feed([4]float64{100, 101, 99, 100})
	assert.ErrorContains(t, errs[0], "no source of the candles of DXYm")
	source := &sourceOf{candles: reference(40, func(i int) float64 { return 110 - float64(i) })}
	for i := range source.candles { // the last one opened with the second candle of the pair
		source.candles[i].Time = source.candles[i].Time.Add(-38 * 15 * time.Minute)
	}
	wrapped.(interface{ SetCandleSource(CandleSource) }).SetCandleSource(source)
	b.feed([4]float64{100, 101, 99, 100})
	assert.NoError(t, errs[1])
	assert.Equal(t, 1, source.asked)

	var others []error
	other, err := Wrap(buyer(0.01, &others), Config{IntermarketSymbol: "DXYm", IntermarketPairs: []string{"XAUUSDm"}})
	require.NoError(t, err)
	newBench(t, other).feed([4]float64{100, 101, 99, 100})
	assert.NoError(t, others[0], "a pair not filtered")
}

// The meta-labeling: a model refuses an entry under its threshold, allows one
// at or above it; the samples of the entries attempted are recorded once
// labeled, refused or not.
func TestFilter_MetaModelAndSamples(t *testing.T) {
	dir := t.TempDir()
	model1 := &metalabel.Model{Version: metalabel.ModelVersion, Features: []string{"side"}, Mean: []float64{0},
		Std: []float64{1}, Weights: []float64{3}, Bias: 0, Threshold: 0.5}
	path := filepath.Join(dir, "meta.json")
	require.NoError(t, model1.Save(path))
	record := filepath.Join(dir, "samples", "s.jsonl")

	var errs []error
	side := model.SideTypeSell
	s := &script{timeframe: "15m", warmup: 1}
	s.on = func(df *model.Dataframe, broker service.Broker) {
		if n := len(df.Close); n != metalabel.Window && n != metalabel.Window+1 {
			return // a sell on the first candle with the features, a buy on the next
		}
		price := df.Close.Last(0)
		sign := 1.0
		if side == model.SideTypeSell {
			sign = -1
		}
		_, err := broker.(service.ProtectionBroker).CreateOrderMarketProtected(side, df.Pair, 0.01,
			price-sign*3, price+sign*3)
		errs = append(errs, err)
	}
	wrapped, err := Wrap(s, Config{MetaModel: path, MetaRecord: record, MetaMaxBars: 5, Agent: "gold",
		Strategy: "script"})
	require.NoError(t, err)
	assert.Equal(t, metalabel.Window, wrapped.WarmupPeriod())
	_, statErr := os.Stat(record)
	assert.True(t, os.IsNotExist(statErr), "nothing written before a sample")
	b := newBench(t, wrapped)
	rng := rand.New(rand.NewPCG(1, 2))
	price := 100.0
	bars := make([][4]float64, metalabel.Window)
	for i := range bars {
		price += rng.NormFloat64() * 0.3
		bars[i] = [4]float64{price, price + 0.6, price - 0.6, price}
	}
	b.feed(bars...)
	require.Len(t, errs, 1)
	assert.ErrorContains(t, errs[0], "meta-labeling: a probability of 0.05", "a sell: sigmoid(-3)")
	side = model.SideTypeBuy
	b.feed([4]float64{price, price + 0.6, price - 0.6, price})
	require.Len(t, errs, 2)
	assert.NoError(t, errs[1], "a buy: sigmoid(3)")
	b.feed([4]float64{price, price + 3.5, price - 0.5, price + 3}) // the target of the buy, the stop of the sell

	file, err := os.Open(record)
	require.NoError(t, err)
	defer func() { _ = file.Close() }()
	samples, err := metalabel.ReadSamples(file)
	require.NoError(t, err)
	require.Len(t, samples, 2)
	bySide := map[string]metalabel.Sample{}
	for _, s := range samples {
		bySide[s.Side] = s
	}
	buyer := bySide["BUY"]
	assert.Equal(t, "gold", buyer.Agent)
	assert.Equal(t, "script", buyer.Strategy)
	assert.Equal(t, metalabel.OutcomeTarget, buyer.Outcome)
	assert.Equal(t, 1, buyer.Label)
	assert.InDelta(t, 1, buyer.R, 1e-9)
	assert.Len(t, buyer.Features, len(metalabel.FeatureNames()))
	seller := bySide["SELL"]
	assert.Equal(t, metalabel.OutcomeStop, seller.Outcome, "refused, still labeled")
	assert.Equal(t, 0, seller.Label)
	assert.Equal(t, -1.0, seller.R)
	assert.Greater(t, seller.ExitTime, seller.Time)
	assert.Equal(t, buyer.ExitTime, seller.ExitTime, "both on the same candle")
	require.NoError(t, wrapped.(interface{ Close() error }).Close())
}

// The Hurst exponent by rescaled range tells a persistent series from an
// anti-persistent one, a random walk between; too short, unknown.
func TestHurst(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 5))
	walk := []float64{100}
	alternating := []float64{100}
	trend := []float64{100}
	step := 0.0
	for i := range 400 {
		walk = append(walk, walk[len(walk)-1]*math.Exp(0.01*rng.NormFloat64()))
		sign := 1.0
		if i%2 == 0 {
			sign = -1
		}
		alternating = append(alternating, alternating[len(alternating)-1]*math.Exp(0.01*sign+0.001*rng.NormFloat64()))
		step = 0.95*step + 0.01*rng.NormFloat64()
		trend = append(trend, trend[len(trend)-1]*math.Exp(step))
	}
	hw, ha, ht := hurst(walk), hurst(alternating), hurst(trend)
	assert.Less(t, ha, 0.3, "anti-persistent")
	assert.Greater(t, ht, 0.8, "persistent")
	assert.Greater(t, hw, ha)
	assert.Less(t, hw, ht)
	assert.True(t, math.IsNaN(hurst(walk[:20])), "fewer than two sizes")
	t.Logf("hurst: walk %.3f, alternating %.3f, persistent %.3f", hw, ha, ht)
}

// Corrected for small samples, the Hurst exponent of random walks centres on
// 0.5 at the window of the filter, where the plain rescaled range gives about
// 0.6: the threshold of 0.5 parts persistent from anti-persistent series.
func TestHurst_RandomWalksCentreOnHalf(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 7))
	for _, window := range []int{100, 200, 400} {
		var sum float64
		var above int
		const walks = 1000
		for range walks {
			closes := []float64{100}
			for range window {
				closes = append(closes, closes[len(closes)-1]*math.Exp(0.01*rng.NormFloat64()))
			}
			h := hurst(closes)
			sum += h
			if h > 0.5 {
				above++
			}
		}
		mean := sum / walks
		t.Logf("window %d: mean H %.3f, above 0.5 in %.0f%%", window, mean, float64(above)/walks*100)
		assert.InDelta(t, 0.5, mean, 0.03, "window %d", window)
		assert.InDelta(t, 0.5, float64(above)/walks, 0.12, "window %d: as often above as below", window)
	}
	// the expected R/S of a random walk grows about as √n, both branches
	assert.InDelta(t, 1, expectedRS(341)/expectedRS(340)*math.Sqrt(340.0/341), 0.01)
	assert.Less(t, expectedRS(8), expectedRS(16))
}

// No sample is recorded of an entry decided before RecordFrom, the warm-up of
// a backtest: the run of the segment before records it.
func TestFilter_SamplesFromRecordFrom(t *testing.T) {
	record := filepath.Join(t.TempDir(), "s.jsonl")
	s := &script{timeframe: "15m", warmup: 1}
	s.on = func(df *model.Dataframe, broker service.Broker) {
		if n := len(df.Close); n == metalabel.Window || n == metalabel.Window+1 {
			price := df.Close.Last(0)
			_, _ = broker.(service.ProtectionBroker).CreateOrderMarketProtected(model.SideTypeSell, df.Pair, 0.01,
				price+1, price-1)
		}
	}
	from := day.Add(time.Duration(metalabel.Window+1) * 15 * time.Minute) // the close of the second one
	wrapped, err := Wrap(s, Config{MetaRecord: record, MetaMaxBars: 2, RecordFrom: from,
		Source: metalabel.SourceBacktest, Run: 42, Segment: metalabel.SegmentValidation})
	require.NoError(t, err)
	b := newBench(t, wrapped)
	b.feed(zigzag(metalabel.Window+4, 100)...)
	file, err := os.Open(record)
	require.NoError(t, err)
	defer func() { _ = file.Close() }()
	samples, err := metalabel.ReadSamples(file)
	require.NoError(t, err)
	require.Len(t, samples, 1, "the one decided at the start of the period only")
	assert.Equal(t, from.Add(-15*time.Minute).Unix(), samples[0].Time)
	assert.Equal(t, [3]any{metalabel.SourceBacktest, int64(42), metalabel.SegmentValidation},
		[3]any{samples[0].Source, samples[0].Run, samples[0].Segment}, "where it was recorded")
}
