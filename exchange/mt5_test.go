package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

func TestMT5New(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)

	require.Equal(t, int64(12345678), m.Login())
	require.Equal(t, "BRL", m.AccountCurrency())
	require.Equal(t, m5fMagic, m.Magic())
	require.Equal(t, "nb:test", m.Comment())
	require.False(t, m.DataOnly())
	require.Equal(t, 2000.0, m.Leverage(m5fPair), "the unlimited leverage counts as 1:2000")

	asset, quote := SplitAssetQuote(m5fPair)
	require.Equal(t, "XAU", asset)
	require.Equal(t, "USD", quote)
	info := m.AssetsInfo(m5fPair)
	require.Equal(t, "XAU", info.BaseAsset)
	require.Equal(t, "USD", info.QuoteAsset)
	require.InDelta(t, 1, info.MinQuantity, 1e-9, "0.01 lot of 100 ounces")
	require.InDelta(t, 1, info.StepSize, 1e-9)
	require.InDelta(t, 20000, info.MaxQuantity, 1e-9, "200 lots")
	require.Equal(t, 0.001, info.TickSize)
	require.Equal(t, 3, info.QuotePrecision)
	require.Equal(t, 0, info.BaseAssetPrecision)

	size, ok := MT5ContractSize(m5fPair)
	require.True(t, ok)
	require.Equal(t, 100.0, size)
}

func TestMT5NewChecks(t *testing.T) {
	create := func(f *m5fFake, extra ...MT5Option) error {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		_, err := NewMT5(ctx, f.options(extra...)...)
		return err
	}

	t.Run("real account", func(t *testing.T) {
		f := newM5F(t)
		f.setAccount("trade_mode", mt5AccountReal)
		require.ErrorContains(t, create(f), "is real")
		require.ErrorContains(t, create(f, WithMT5DataOnly()), "is real", "even to read the market data")
		require.NoError(t, create(f, WithMT5Real()))
	})

	t.Run("hedging only", func(t *testing.T) {
		f := newM5F(t)
		f.setAccount("margin_mode", 0)
		require.ErrorContains(t, create(f), "only hedging accounts")
		require.NoError(t, create(f, WithMT5DataOnly()), "the market data of a netting account")
	})

	t.Run("unknown symbol", func(t *testing.T) {
		f := newM5F(t)
		err := create(f, WithMT5Symbols("XAUUSD"))
		require.ErrorIs(t, err, ErrInvalidAsset)
		require.ErrorContains(t, err, "no symbol XAUUSD")
	})

	t.Run("disabled symbol", func(t *testing.T) {
		f := newM5F(t)
		require.ErrorContains(t, create(f, WithMT5Symbols("USDBRLm")), "disabled")
		require.NoError(t, create(f, WithMT5Symbols("USDBRLm"), WithMT5DataOnly()))
	})

	t.Run("magic", func(t *testing.T) {
		f := newM5F(t)
		require.ErrorContains(t, create(f, WithMT5Magic(0)), "magic number")
		require.NoError(t, create(f, WithMT5Magic(0), WithMT5DataOnly()), "no magic to read the market data")
	})

	t.Run("comment", func(t *testing.T) {
		f := newM5F(t)
		require.ErrorContains(t, create(f, WithMT5Comment(strings.Repeat("x", 32))), "longer than 31")
		require.ErrorContains(t, create(f, WithMT5Comment("[sl]")), "starts with [")
		require.ErrorContains(t, create(f, WithMT5Comment("")), "empty")
		require.ErrorContains(t, create(f, WithMT5Comment("ação")), "printable ASCII")
	})

	t.Run("terminal", func(t *testing.T) {
		f := newM5F(t)
		f.setHealth("trade_allowed", false)
		require.NoError(t, create(f), "AlgoTrading off is a warning")
		f.setHealth("read_only", true)
		require.ErrorContains(t, create(f), "read-only")
		require.NoError(t, create(f, WithMT5DataOnly()))
		f.setHealth("connected", false)
		require.ErrorContains(t, create(f), "not connected")
	})

	t.Run("token", func(t *testing.T) {
		f := newM5F(t)
		f.mu.Lock()
		f.token = "s3cret"
		f.mu.Unlock()
		err := create(f)
		var mt5Err *MT5Error
		require.ErrorAs(t, err, &mt5Err)
		require.Equal(t, http.StatusUnauthorized, mt5Err.Status)
		require.NoError(t, create(f, WithMT5Token("s3cret")))
	})

	t.Run("bridge down", func(t *testing.T) {
		_, err := NewMT5(context.Background(), WithMT5BaseURL("http://127.0.0.1:1"), WithMT5Symbols(m5fPair),
			WithMT5Magic(1), withMT5RetryDelay(time.Millisecond))
		require.ErrorContains(t, err, "the bridge at http://127.0.0.1:1")
	})

	t.Run("no conversion", func(t *testing.T) {
		f := newM5F(t)
		f.setAccount("currency", "JPY")
		require.NoError(t, create(f), "the account currency is never converted: no symbol of JPY needed")
	})
}

func TestMT5GetRetries(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour))

	// a GET failing with 5xx is sent again
	f.failNext("GET", "/tick", http.StatusBadGateway, `{"error":"terminal: timeout","retcode":-10005}`)
	f.clearRequests()
	price, err := m.LastQuote(context.Background(), m5fPair)
	require.NoError(t, err)
	require.Equal(t, 4285.897, price, "the bid, as the candles")
	require.Equal(t, 2, f.requestCount("GET", "/tick"))

	// a 4xx is not
	f.failNext("GET", "/rates", http.StatusBadRequest, `{"error":"invalid timeframe"}`)
	f.clearRequests()
	_, err = m.CandlesByLimit(context.Background(), m5fPair, "5m", 3)
	var mt5Err *MT5Error
	require.ErrorAs(t, err, &mt5Err)
	require.Equal(t, http.StatusBadRequest, mt5Err.Status)
	require.Equal(t, "invalid timeframe", mt5Err.Message)
	require.Equal(t, 1, f.requestCount("GET", "/rates"))
}

func TestMT5ErrorMapping(t *testing.T) {
	for code, target := range map[int]error{
		mt5CodeInvalidVolume: ErrInvalidQuantity,
		mt5CodeInvalidStops:  ErrInvalidProtection,
		mt5CodeNoMoney:       ErrInsufficientFunds,
	} {
		err := error(&MT5Error{Method: "POST", Path: "/order", Status: 200, Retcode: code, Message: "x"})
		require.ErrorIs(t, err, target, code)
	}
	readOnly := &MT5Error{Method: "POST", Path: "/order", Status: http.StatusForbidden, Message: "read-only"}
	require.ErrorIs(t, readOnly, ErrNotSupported)
	require.Equal(t, "mt5: POST /order: HTTP 403: read-only", readOnly.Error())
	stops := &MT5Error{Method: "POST", Path: "/order", Status: 200, Retcode: 10016, Message: "Invalid stops"}
	require.Equal(t, "mt5: POST /order: retcode 10016 (invalid stops): Invalid stops", stops.Error())
	require.NoError(t, errors.Unwrap(&MT5Error{Retcode: mt5CodeRequote}))
}

func TestMT5Sizes(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5Symbols(m5fPair, "BTCUSDm", "EURUSDm"))

	require.Equal(t, 100.0, m.ContractSize(m5fPair))
	require.Equal(t, 1.0, m.ContractSize("BTCUSDm"))
	require.Equal(t, 100000.0, m.ContractSize("EURUSDm"))
	require.Equal(t, 0.07, m.Lots(m5fPair, 7))
	require.Equal(t, 7.0, m.Units(m5fPair, 0.07), "without the float noise")
	require.Equal(t, 3000.0, m.Units("EURUSDm", 0.03))
	require.Zero(t, m.Lots("UNKNOWN", 1))

	btc := m.AssetsInfo("BTCUSDm")
	require.InDelta(t, 0.01, btc.MinQuantity, 1e-12)
	require.InDelta(t, 0.01, btc.StepSize, 1e-12)
	require.Equal(t, 2, btc.BaseAssetPrecision)
	require.Equal(t, 0.01, btc.TickSize)
	eur := m.AssetsInfo("EURUSDm")
	require.InDelta(t, 1000, eur.MinQuantity, 1e-9)
	require.Equal(t, 5, eur.QuotePrecision)

	lots, units, err := m.dealSize(m5fPair, 1.99)
	require.NoError(t, err)
	require.Equal(t, 0.01, lots, "floored to the volume step")
	require.Equal(t, 1.0, units)
	lots, units, err = m.dealSize("BTCUSDm", 0.123)
	require.NoError(t, err)
	require.Equal(t, 0.12, lots)
	require.Equal(t, 0.12, units)

	f.clearRequests()
	for _, size := range []float64{0.5, 20001, 0, -1} {
		_, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, size)
		require.ErrorIs(t, err, ErrInvalidQuantity, size)
		var orderErr *OrderError
		require.ErrorAs(t, err, &orderErr)
	}
	_, err = m.CreateOrderLimit(model.SideTypeBuy, "GOLD", 1, 4000)
	require.ErrorIs(t, err, ErrInvalidAsset)
	require.Zero(t, f.requestCount("POST", "/order"), "refused before any request")
}

// m5fTestdataRates returns the candles of testdata, moved so that the last one
// is the one in progress now.
func m5fTestdataRates(t *testing.T) []mt5Rate {
	var rates []mt5Rate
	require.NoError(t, json.Unmarshal(m5fTestdata(t, "rates_XAUUSDm_M5.json"), &rates))
	current := time.Now().UTC().Truncate(5 * time.Minute)
	for i := range rates {
		rates[i].Time = current.Add(-time.Duration(len(rates)-1-i) * 5 * time.Minute).Unix()
	}
	return rates
}

func TestMT5CandlesByLimit(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5DataOnly())
	rates := m5fTestdataRates(t)
	f.setRates(m5fPair, "M5", rates)

	candles, err := m.CandlesByLimit(context.Background(), m5fPair, "5m", 5)
	require.NoError(t, err)
	require.Len(t, candles, 5)
	for i, candle := range candles {
		require.True(t, candle.Complete)
		require.Equal(t, time.Unix(rates[len(rates)-6+i].Time, 0).UTC(), candle.Time, "the partial one dropped")
		require.Equal(t, m5fPair, candle.Pair)
	}
	requests := f.requestsTo("GET", "/rates")
	require.Len(t, requests, 1)
	query, _ := url.ParseQuery(requests[0].query)
	require.Equal(t, "6", query.Get("count"), "one more for the candle in progress")
	require.Equal(t, "M5", query.Get("timeframe"))
	require.Equal(t, "0", query.Get("start"))

	last := candles[4]
	source := rates[len(rates)-2]
	require.Equal(t, source.Open, last.Open)
	require.Equal(t, source.Close, last.Close)
	require.Equal(t, source.TickVolume, last.Volume)
	require.Equal(t, 240.0, source.Spread)
	require.InDelta(t, 0.24, last.Metadata["spread"], 1e-9, "240 points of 0.001")
	require.InDelta(t, source.Close+0.24, last.Metadata["ask"], 1e-9)

	// all of them when fewer, the first ones of the testdata included
	candles, err = m.CandlesByLimit(context.Background(), m5fPair, "5m", 50)
	require.NoError(t, err)
	require.Len(t, candles, len(rates)-1)
	require.Equal(t, 4291.029, candles[0].Open)
	require.Equal(t, 4292.164, candles[0].High)

	// rounded to the digits of the symbol
	f.setRates(m5fPair, "M5", m5fCandles(4, 5*time.Minute, 4000))
	candles, err = m.CandlesByLimit(context.Background(), m5fPair, "5m", 3)
	require.NoError(t, err)
	require.Len(t, candles, 3)
	require.Equal(t, 4001.0, candles[0].Open)
	require.Equal(t, 4002.0, candles[0].Close)
	for _, candle := range candles {
		require.Contains(t, candle.Metadata, "ask")
		require.Contains(t, candle.Metadata, "spread")
	}

	_, err = m.CandlesByLimit(context.Background(), m5fPair, "7m", 3)
	require.ErrorContains(t, err, "unsupported timeframe")
	candles, err = m.CandlesByLimit(context.Background(), m5fPair, "5m", 0)
	require.NoError(t, err)
	require.Empty(t, candles)
}

func TestMT5Timeframes(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5DataOnly())
	for timeframe, name := range map[string]string{"1m": "M1", "3m": "M3", "5m": "M5", "10m": "M10", "15m": "M15",
		"30m": "M30", "1h": "H1", "2h": "H2", "4h": "H4", "1d": "D1", "1w": "W1", "1M": "MN1"} {
		f.clearRequests()
		_, err := m.CandlesByLimit(context.Background(), m5fPair, timeframe, 2)
		require.NoError(t, err, timeframe)
		requests := f.requestsTo("GET", "/rates")
		require.Len(t, requests, 1)
		query, _ := url.ParseQuery(requests[0].query)
		require.Equal(t, name, query.Get("timeframe"), timeframe)
	}

	// a month ends with the calendar
	month := mt5Timeframes["1M"]
	require.Equal(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		month.end(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)))
}

func TestMT5CandlesByPeriod(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5DataOnly())

	// 25000 minutes: three range requests of at most 10000 periods
	rates := m5fCandles(25000, time.Minute, 4000)
	f.setRates(m5fPair, "M1", rates)
	start := time.Unix(rates[0].Time, 0).UTC()
	candles, err := m.CandlesByPeriod(context.Background(), m5fPair, "1m", start, time.Time{})
	require.NoError(t, err)
	require.Len(t, candles, 24999, "the complete ones")
	for i := 1; i < len(candles); i++ {
		require.Equal(t, time.Minute, candles[i].Time.Sub(candles[i-1].Time))
	}
	requests := f.requestsTo("GET", "/rates")
	require.Len(t, requests, 3)
	for _, request := range requests {
		query, _ := url.ParseQuery(request.query)
		require.NotEmpty(t, query.Get("from"))
		require.NotEmpty(t, query.Get("to"))
		require.Empty(t, query.Get("count"))
	}

	// a window
	from := time.Unix(rates[100].Time, 0).UTC()
	to := time.Unix(rates[109].Time, 0).UTC()
	candles, err = m.CandlesByPeriod(context.Background(), m5fPair, "1m", from, to)
	require.NoError(t, err)
	require.Len(t, candles, 10)
	require.Equal(t, from, candles[0].Time)
	require.Equal(t, to, candles[9].Time)

	_, err = m.CandlesByPeriod(context.Background(), m5fPair, "1m", time.Time{}, to)
	require.Error(t, err)
}

// m5fNext reads the next candle of a subscription.
func m5fNext(t *testing.T, candles chan model.Candle) model.Candle {
	t.Helper()
	select {
	case candle, ok := <-candles:
		require.True(t, ok, "the subscription ended")
		return candle
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for a candle")
	}
	return model.Candle{}
}

func TestMT5CandlesSubscription(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5DataOnly())
	rates := m5fCandles(5, time.Minute, 4000)
	f.setRates(m5fPair, "M1", rates)

	preloaded, err := m.CandlesByLimit(context.Background(), m5fPair, "1m", 10)
	require.NoError(t, err)
	require.Len(t, preloaded, 4)

	ctx, cancel := context.WithCancel(context.Background())
	candles, errs := m.CandlesSubscription(ctx, m5fPair, "1m")
	go func() {
		for range errs {
		}
	}()

	// the candle in progress, with the ask of the tick
	partial := m5fNext(t, candles)
	require.False(t, partial.Complete)
	require.Equal(t, time.Unix(rates[4].Time, 0).UTC(), partial.Time)
	require.Equal(t, 4286.157, partial.Metadata["ask"])
	require.InDelta(t, 4286.157-partial.Close, partial.Metadata["spread"], 1e-9)

	// it changes: sent again
	rates[4].Close += 3
	f.setRates(m5fPair, "M1", rates)
	changed := m5fNext(t, candles)
	require.False(t, changed.Complete)
	require.Equal(t, partial.Close+3, changed.Close)

	// a new candle starts: the previous one complete, then the new one
	next := rates[4]
	next.Time += 60
	rates = append(rates, next)
	f.setRates(m5fPair, "M1", rates)
	complete := m5fNext(t, candles)
	require.True(t, complete.Complete)
	require.Equal(t, time.Unix(rates[4].Time, 0).UTC(), complete.Time)
	require.InDelta(t, 0.26, complete.Metadata["spread"], 1e-9, "the spread of the candle")
	started := m5fNext(t, candles)
	require.False(t, started.Complete)
	require.Equal(t, time.Unix(next.Time, 0).UTC(), started.Time)

	// no goroutine left: both channels closed
	cancel()
	require.Eventually(t, func() bool {
		select {
		case _, ok := <-candles:
			return !ok
		default:
			return false
		}
	}, 5*time.Second, 5*time.Millisecond)
}

// TestMT5CandlesBackfill: the candles missed during an outage are sent, the
// ones beyond a request read by period.
func TestMT5CandlesBackfill(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5DataOnly())
	rates := m5fCandles(30, time.Minute, 4000)
	f.setRates(m5fPair, "M1", rates)
	frame := mt5Timeframes["1m"]

	// the last candle known is 20 minutes old, the last poll just now (a host
	// suspended): the 3 latest candles are read, then the ones in between
	last := time.Unix(rates[9].Time, 0).UTC()
	candles, err := m.latest(context.Background(), m5fPair, frame, last, time.Now())
	require.NoError(t, err)
	require.Len(t, candles, 20)
	require.Equal(t, last.Add(time.Minute), candles[0].Time)
	for i := 1; i < len(candles); i++ {
		require.Equal(t, time.Minute, candles[i].Time.Sub(candles[i-1].Time))
	}
	require.False(t, candles[len(candles)-1].Complete)

	// a subscription started after a preload older than the candles: every
	// candle missed, then the one in progress
	m.loadedMtx.Lock()
	m.loaded[m5fPair+"--1m"] = last
	m.loadedMtx.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, errs := m.CandlesSubscription(ctx, m5fPair, "1m")
	go func() {
		for range errs {
		}
	}()
	for i := 10; i < 29; i++ {
		candle := m5fNext(t, stream)
		require.True(t, candle.Complete, i)
		require.Equal(t, time.Unix(rates[i].Time, 0).UTC(), candle.Time, i)
	}
	require.False(t, m5fNext(t, stream).Complete)
}

func TestMT5DataOnly(t *testing.T) {
	f := newM5F(t)
	f.setAccount("margin_mode", 0) // the market data of a netting account
	m := newM5FAdapter(t, f, WithMT5DataOnly(), WithMT5Magic(0))
	require.True(t, m.DataOnly())
	f.clearRequests()

	calls := map[string]func() error{
		"Account":                func() error { _, err := m.Account(); return err },
		"Position":               func() error { _, _, err := m.Position(m5fPair); return err },
		"Order":                  func() error { _, err := m.Order(m5fPair, 1); return err },
		"OrderSince":             func() error { _, err := m.OrderSince(m5fPair, 1, time.Now()); return err },
		"CreateOrderMarket":      func() error { _, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1); return err },
		"CreateOrderMarketQuote": func() error { _, err := m.CreateOrderMarketQuote(model.SideTypeBuy, m5fPair, 100); return err },
		"CreateOrderLimit":       func() error { _, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 1, 4000); return err },
		"CreateOrderStop":        func() error { _, err := m.CreateOrderStop(m5fPair, 1, 4000); return err },
		"CreateOrderOCO":         func() error { _, err := m.CreateOrderOCO(model.SideTypeSell, m5fPair, 1, 4300, 4200, 4200); return err },
		"CreateOrderMarketProtected": func() error {
			_, err := m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 1, 4200, 4300)
			return err
		},
		"CreateOrderLimitProtected": func() error {
			_, err := m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 1, 4250, 4200, 4300)
			return err
		},
		"CreateOrderStopProtected": func() error {
			_, err := m.CreateOrderStopProtected(model.SideTypeBuy, m5fPair, 1, 4350, 4300, 4400)
			return err
		},
		"UpdateProtection": func() error { _, err := m.UpdateProtection(model.Order{ExchangeID: 1}, 4200); return err },
		"UpdateLimit":      func() error { _, err := m.UpdateLimit(model.Order{ExchangeID: 1}, 4200); return err },
		"Cancel":           func() error { return m.Cancel(model.Order{ExchangeID: 1}) },
	}
	for name, call := range calls {
		require.ErrorIs(t, call(), ErrNotSupported, name)
	}
	require.Empty(t, f.requestsTo("POST", "/order"))
	require.Empty(t, f.requestsTo("GET", "/positions"))
	require.Empty(t, f.requestsTo("GET", "/orders"))
	require.Empty(t, f.requestsTo("GET", "/tick"), "refused before any request")

	// the market data works
	f.setRates(m5fPair, "M5", m5fTestdataRates(t))
	candles, err := m.CandlesByLimit(context.Background(), m5fPair, "5m", 3)
	require.NoError(t, err)
	require.Len(t, candles, 3)
}

// The deals and the orders of the history as the bridge answered them live.
func TestMT5HistoryParsing(t *testing.T) {
	var deals []mt5Deal
	require.NoError(t, json.Unmarshal(m5fTestdata(t, "deals.json"), &deals))
	require.Len(t, deals, 36)
	byTicket := make(map[int64]mt5Deal)
	for _, deal := range deals {
		byTicket[deal.Ticket] = deal
	}
	tp := byTicket[525915950]
	require.Equal(t, mt5ReasonTP, tp.Reason)
	require.Equal(t, mt5EntryOut, tp.Entry)
	require.True(t, tp.exit())
	require.Equal(t, int64(671024098), tp.PositionID)
	require.Equal(t, 4284.908, tp.Price)
	require.Equal(t, time.UnixMilli(1790355299000).UTC().Unix(), tp.at().Unix())
	sl := byTicket[525916090]
	require.Equal(t, mt5ReasonSL, sl.Reason)
	require.Equal(t, mt5TypeBuy, sl.Type, "the exit of a short is a buy")
	stopOut := byTicket[525825378]
	require.Equal(t, mt5ReasonSO, stopOut.Reason)
	closeBy := byTicket[525897978]
	require.Equal(t, mt5EntryOutBy, closeBy.Entry)
	require.True(t, closeBy.exit())
	require.Equal(t, 4287.345, closeBy.Price, "closed at the open price of the other position")

	var orders []mt5Order
	require.NoError(t, json.Unmarshal(m5fTestdata(t, "history_orders.json"), &orders))
	require.Len(t, orders, 34)
	var closeByOrder mt5Order
	for _, order := range orders {
		if order.Type == mt5TypeCloseBy {
			closeByOrder = order
		}
	}
	require.Equal(t, int64(671004267), closeByOrder.Ticket)
	require.Equal(t, int64(671003205), closeByOrder.PositionID)
	require.Equal(t, int64(671003798), closeByOrder.PositionByID)
	canceled := orders[1]
	require.Equal(t, mt5StateCanceled, canceled.State)
	require.Equal(t, model.OrderStatusTypeCanceled, mt5Status(canceled.State))
	require.Equal(t, time.Unix(1790352182, 637*int64(time.Millisecond)).UTC(), canceled.done())

	var account mt5Account
	require.NoError(t, json.Unmarshal(m5fTestdata(t, "account.json"), &account))
	require.Equal(t, mt5MarginHedging, account.MarginMode)
	require.Equal(t, 990.55, account.Balance)
	var symbol mt5Symbol
	require.NoError(t, json.Unmarshal(m5fTestdata(t, "symbol_XAUUSDm.json"), &symbol))
	require.Equal(t, mt5FillFOK, symbol.filling(), "filling_mode 3: fill or kill")
	require.InDelta(t, 5.1807, symbol.pointValue(), 1e-9, "BRL a unit for 1 of the price: 0.51807 / 0.001 / 100")
	require.Zero(t, mt5Symbol{TickSize: 0.01}.pointValue(), "no tick value")
	require.Equal(t, 127, symbol.OrderMode)
	require.NotZero(t, symbol.OrderMode&mt5OrderCloseBy)
	require.Equal(t, mt5FillIOC, mt5Symbol{FillingMode: 2}.filling())
	require.Equal(t, mt5FillReturn, mt5Symbol{}.filling())
	require.Equal(t, 4293.507, symbol.price(4293.5070000000005))
	require.True(t, math.Abs(mt5Clean(0.01*100)-1) == 0)
}

// Just opened, the terminal serves the candles it had cached, hours old: the
// preload waits for its history to reach its last tick, syncWait at most.
func TestMT5AwaitsTheHistory(t *testing.T) {
	f := newM5F(t)
	now := time.Now().Truncate(time.Minute)
	candles := func(end time.Time) []mt5Rate {
		var rates []mt5Rate
		for i := 4; i >= 0; i-- {
			at := end.Add(-time.Duration(i) * time.Minute).Unix()
			rates = append(rates, mt5Rate{Time: at, Open: 4290, High: 4291, Low: 4289, Close: 4290})
		}
		return rates
	}
	f.setRates(m5fPair, "M1", candles(now.Add(-5*time.Hour))) // the cache of the terminal
	m := newM5FAdapter(t, f, withMT5SyncWait(10*time.Second))

	go func() {
		time.Sleep(1500 * time.Millisecond)
		f.setRates(m5fPair, "M1", candles(now)) // synced
	}()
	started := time.Now()
	loaded, err := m.CandlesByLimit(context.Background(), m5fPair, "1m", 3)
	require.NoError(t, err)
	require.GreaterOrEqual(t, time.Since(started), time.Second, "waited for the history")
	require.Less(t, time.Since(started), 8*time.Second, "not for the whole wait")
	require.Equal(t, now.Add(-time.Minute).Unix(), loaded[len(loaded)-1].Time.Unix(), "the synced candles")

	// never synced: the candles it has, after the wait
	f.setRates(m5fPair, "M1", candles(now.Add(-5*time.Hour)))
	m = newM5FAdapter(t, f, withMT5SyncWait(1200*time.Millisecond))
	started = time.Now()
	loaded, err = m.CandlesByLimit(context.Background(), m5fPair, "1m", 3)
	require.NoError(t, err)
	require.GreaterOrEqual(t, time.Since(started), time.Second)
	require.NotEmpty(t, loaded)
}
