package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The reads of the candles of a backtest at a bridge.
const (
	backtestReadTimeout = 2 * time.Minute // of a request to the bridge
	backtestReadLimit   = 256 << 20       // of its answer
	// backtestGapLeast is the least time between two candles counted as a
	// gap, besides three candles of the timeframe: the daily breaks of the
	// markets, eg: an hour for gold, aren't.
	backtestGapLeast = 2 * time.Hour
	// backtestGapsKept bounds the gaps told of a pair, the rest counted.
	backtestGapsKept = 20
)

// backtestTimeframe is a timeframe of the candles of a backtest: its name in
// the files, in MT5, and its length.
type backtestTimeframe struct {
	file, mt5 string
	length    time.Duration
}

// backtestTimeframes are the timeframes of the candles of a backtest, the
// MT5 name and the length, the finest first; see fileTimeframeOf.
var backtestTimeframes = []backtestTimeframe{{"1m", "M1", time.Minute}, {"5m", "M5", 5 * time.Minute}, {"15m", "M15", 15 * time.Minute},
	{"1h", "H1", time.Hour}}

// fileTimeframeOf returns the timeframe of the candles of a backtest of span,
// the finest whose candles stay under about 200 thousand by pair: the fills
// read candle by candle, the finer the fewer the candles where the stop and
// the target both are.
func fileTimeframeOf(span time.Duration) (file, mt5 string, length time.Duration) {
	for _, tf := range backtestTimeframes {
		if span/tf.length <= 200_000 {
			return tf.file, tf.mt5, tf.length
		}
	}
	last := backtestTimeframes[len(backtestTimeframes)-1]
	return last.file, last.mt5, last.length
}

// bridgeRate is a candle of GET /rates of the bridge, of the bid, its spread
// in points.
type bridgeRate struct {
	Time   int64   `json:"time"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"tick_volume"`
	Spread float64 `json:"spread"`
}

// bridgeSymbol is what GET /symbol of the bridge tells of a symbol for a
// backtest: its point, its volume step and least volume, in lots, its
// contract size (the units of a lot), and its base and profit currencies (the
// ones of the economic calendar of the pair).
type bridgeSymbol struct {
	Point          float64 `json:"point"`
	VolumeStep     float64 `json:"volume_step"`
	VolumeMin      float64 `json:"volume_min"`
	ContractSize   float64 `json:"trade_contract_size"`
	Digits         int     `json:"digits"`
	CurrencyBase   string  `json:"currency_base"`
	CurrencyProfit string  `json:"currency_profit"`
}

// candleData is the candles of a pair a backtest reads: the file, its
// timeframe, the symbol, its bounds and count, and the gaps found.
type candleData struct {
	File      string
	Timeframe string
	Symbol    bridgeSymbol
	First     time.Time
	Last      time.Time
	Count     int
	Gaps      []string
}

// candleCache keeps the candles of the backtests in dir, a file by symbol,
// timeframe and range, read once at a bridge whatever the runs that ask for
// them at once.
type candleCache struct {
	dir    string
	client *http.Client

	mu      sync.Mutex
	pending map[string]*sync.Mutex // by file, one read at a time
}

func newCandleCache(dir string) *candleCache {
	return &candleCache{dir: dir, client: &http.Client{Timeout: backtestReadTimeout},
		pending: make(map[string]*sync.Mutex)}
}

// lock returns the lock of the read of file.
func (c *candleCache) lock(file string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending[file] == nil {
		c.pending[file] = &sync.Mutex{}
	}
	return c.pending[file]
}

// read returns the candles of symbol at the bridge (with token) from from
// until before to on the timeframe mt5 (file the name of the backtest, of
// length): the file of the cache when it holds them, else read in parts of
// about 50 thousand candles and written; its gaps told (see gapsOf), none
// filled. ErrNoCandles without any.
func (c *candleCache) read(ctx context.Context, bridge, token, symbol, file, mt5 string, length time.Duration,
	from, to time.Time) (candleData, error) {
	data := candleData{Timeframe: file}
	if err := bridgeGet(ctx, c.client, 1<<20, bridge, "/symbol?"+url.Values{"symbol": {symbol}}.Encode(), token,
		&data.Symbol); err != nil {
		return data, fmt.Errorf("o símbolo %s: %w", symbol, err)
	}
	if err := os.MkdirAll(filepath.Join(c.dir, "candles"), 0o750); err != nil {
		return data, err
	}
	name := fmt.Sprintf("%s_%s_%d_%d.csv", safeName(symbol), file, from.Unix(), to.Unix())
	data.File = filepath.Join(c.dir, "candles", name)
	lock := c.lock(data.File)
	lock.Lock()
	defer lock.Unlock()

	var rates []bridgeRate
	if cached, err := readCandleFile(data.File); err == nil {
		rates = cached
	} else {
		step := length * 50_000
		for start := from; start.Before(to); start = start.Add(step) {
			end := start.Add(step)
			if end.After(to) {
				end = to
			}
			var part []bridgeRate
			path := "/rates?" + url.Values{"symbol": {symbol}, "timeframe": {mt5},
				"from": {strconv.FormatInt(start.Unix(), 10)},
				"to":   {strconv.FormatInt(end.Add(-time.Second).Unix(), 10)}}.Encode()
			if err := bridgeGet(ctx, c.client, backtestReadLimit, bridge, path, token, &part); err != nil {
				return data, fmt.Errorf("os candles de %s: %w", symbol, err)
			}
			for _, rate := range part {
				if n := len(rates); n > 0 && rate.Time <= rates[n-1].Time {
					continue // the edges of the parts once
				}
				rates = append(rates, rate)
			}
		}
		if len(rates) == 0 {
			return data, fmt.Errorf("%w: %s from %s to %s on %s", ErrNoCandles, symbol, from.UTC().Format(time.DateOnly),
				to.UTC().Format(time.DateOnly), mt5)
		}
		if err := writeCandleFile(data.File, rates); err != nil {
			return data, err
		}
	}
	data.Count = len(rates)
	data.First, data.Last = time.Unix(rates[0].Time, 0).UTC(), time.Unix(rates[len(rates)-1].Time, 0).UTC()
	data.Gaps = gapsOf(symbol, rates, length)
	return data, nil
}

// ErrNoCandles is the error of a range of candles the bridge has none of.
var ErrNoCandles = errors.New("no candles")

// gapsOf returns the gaps of rates of length: two candles more than three
// candles and backtestGapLeast apart, outside the weekend (from Friday 20:00
// to Monday 00:00 UTC, the markets of the metals and the currencies closed),
// backtestGapsKept of them, the rest counted. None is filled.
func gapsOf(symbol string, rates []bridgeRate, length time.Duration) []string {
	least := max(3*length, backtestGapLeast)
	var gaps []string
	n := 0
	for i := 1; i < len(rates); i++ {
		from, to := time.Unix(rates[i-1].Time, 0).UTC(), time.Unix(rates[i].Time, 0).UTC()
		if to.Sub(from) <= least || weekend(from, to) {
			continue
		}
		n++
		if len(gaps) < backtestGapsKept {
			gaps = append(gaps, fmt.Sprintf("%s: sem candles de %s a %s", symbol, from.Format("2006-01-02 15:04"),
				to.Format("2006-01-02 15:04 UTC")))
		}
	}
	if n > len(gaps) {
		gaps = append(gaps, fmt.Sprintf("%s: e mais %d lacunas", symbol, n-len(gaps)))
	}
	return gaps
}

// weekend reports whether from and to lie within one weekend: from Friday
// 20:00 to Monday 00:00 UTC.
func weekend(from, to time.Time) bool {
	days := (int(from.Weekday()) + 2) % 7 // days since Friday
	friday := time.Date(from.Year(), from.Month(), from.Day()-days, 20, 0, 0, 0, time.UTC)
	monday := friday.Add(52 * time.Hour)
	return !from.Before(friday) && !to.After(monday)
}

// safeName returns name with only letters, digits, dots, dashes and
// underscores, for a file.
func safeName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, name)
}

// writeCandleFile writes rates as a CSV of the backtests (exchange.CSVFeed),
// its spread in a column of its own, at once.
func writeCandleFile(path string, rates []bridgeRate) error {
	var b strings.Builder
	b.WriteString("time,open,close,low,high,volume,spread\n")
	for _, r := range rates {
		fmt.Fprintf(&b, "%d,%s,%s,%s,%s,%s,%s\n", r.Time, num(r.Open), num(r.Close), num(r.Low), num(r.High),
			num(r.Volume), num(r.Spread))
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// num writes a number of a candle without the noise of the floats.
func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// readCandleFile reads a CSV of writeCandleFile.
func readCandleFile(path string) ([]bridgeRate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		return nil, ErrNoCandles
	}
	rates := make([]bridgeRate, 0, len(lines)-1)
	for _, line := range lines[1:] {
		f := strings.Split(line, ",")
		if len(f) != 7 {
			return nil, fmt.Errorf("the candles of %s: a line of %d fields", path, len(f))
		}
		var r bridgeRate
		var err error
		if r.Time, err = strconv.ParseInt(f[0], 10, 64); err != nil {
			return nil, err
		}
		values := []*float64{&r.Open, &r.Close, &r.Low, &r.High, &r.Volume, &r.Spread}
		for i, v := range values {
			if *v, err = strconv.ParseFloat(f[i+1], 64); err != nil {
				return nil, err
			}
		}
		rates = append(rates, r)
	}
	return rates, nil
}
