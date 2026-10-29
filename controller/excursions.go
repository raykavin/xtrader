package controller

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/ui"
)

// excursionTimeframes are the timeframes of the candles of the path of a
// trade, the finest whose candles over its duration are excursionMaxCandles at
// most, see timeframeOf.
var excursionTimeframes = []struct {
	name    string
	seconds int64
}{{"M1", 60}, {"M5", 300}, {"M15", 900}, {"H1", 3600}, {"H4", 4 * 3600}, {"D1", 86400}}

const (
	// excursionMaxCandles bounds the candles read for the path of a trade.
	excursionMaxCandles = 5000
	// excursionReads bounds the reads of candles of an account at once.
	excursionReads = 4
	// excursionTimeout bounds a read of the excursions of a range, the paths
	// of its trades not read yet then told unread.
	excursionTimeout = 2 * time.Minute
)

// excursions reads how far the price went for and against each trade of a
// range of the performance while it was open, from the candles of its symbol
// at the bridge of its account, see ui.PerformanceExcursionsResponse: a trade
// read once for good, as a closed trade never changes.
type excursions struct {
	performance *performance
	reads       rangeReads[ui.PerformanceExcursionsResponse]

	mu      sync.Mutex
	known   map[string]ui.PerformanceExcursion // by account/position, the ones read
	symbols map[string]excursionSymbol         // by bridge and symbol
}

func newExcursions(p *performance) *excursions {
	return &excursions{performance: p, reads: rangeReads[ui.PerformanceExcursionsResponse]{timeout: excursionTimeout},
		known: make(map[string]ui.PerformanceExcursion), symbols: make(map[string]excursionSymbol)}
}

// excursionSymbol is what GET /symbol tells of a symbol for its excursions:
// its point (the unit of the spread of its candles), and its tick value (of
// a lot, in the currency of the account) and tick size.
type excursionSymbol struct {
	Point     float64 `json:"point"`
	TickValue float64 `json:"trade_tick_value"`
	TickSize  float64 `json:"trade_tick_size"`
}

// excursionRate is a candle of GET /rates: its extremes, of the bid, and its
// spread, in points.
type excursionRate struct {
	Time   int64   `json:"time"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Spread float64 `json:"spread"`
}

// handle answers the excursions of the trades of the agents from the query
// from until before to, in Unix seconds, see ui.PerformanceExcursionsResponse.
func (e *excursions) handle(w http.ResponseWriter, r *http.Request) {
	window, err := closedRangeOf(r.URL.Query())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	response, err := e.reads.read(r.Context(), window, performanceTTL, e.readAll)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Errorf("ui: the excursions of the trades of the agents: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "the excursions of the trades can't be read now")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, response)
}

// readAll reads the excursions of the trades of window (see performance):
// the ones known already as they are, the others from the bridge of the
// account of their agent, excursionReads at once on an account.
func (e *excursions) readAll(ctx context.Context, window closedRange) (ui.PerformanceExcursionsResponse, error) {
	read, err := e.performance.reads.read(ctx, window, performanceTTL, e.performance.readAll)
	if err != nil {
		return ui.PerformanceExcursionsResponse{}, err
	}
	agents, err := e.performance.positions.store.Agents(ctx)
	if err != nil {
		return ui.PerformanceExcursionsResponse{}, err
	}
	accounts := liveAccounts(agents)
	accountOf := make(map[string]int) // by agent
	for i, account := range accounts {
		for _, a := range account.agents {
			accountOf[a.agent.Name] = i
		}
	}

	response := ui.PerformanceExcursionsResponse{From: read.From, To: read.To,
		Excursions: make([]ui.PerformanceExcursion, len(read.Trades)), Complete: read.Complete,
		Missing: read.Missing, At: time.Now().Unix()}
	pending := make(map[int][]int) // the trades to read, by account
	for i, trade := range read.Trades {
		key := trade.Account + "/" + strconv.FormatInt(trade.Position, 10)
		e.mu.Lock()
		known, ok := e.known[key]
		e.mu.Unlock()
		account, live := accountOf[trade.Agent]
		switch {
		case ok:
			response.Excursions[i] = known
		case trade.OpenedAt == 0:
			response.Excursions[i] = ui.PerformanceExcursion{Account: trade.Account, Position: trade.Position,
				Error: "abertura desconhecida"}
		case !live:
			response.Excursions[i] = ui.PerformanceExcursion{Account: trade.Account, Position: trade.Position,
				Error: "agente fora das contas lidas"}
		default:
			pending[account] = append(pending[account], i)
		}
	}

	var wg sync.WaitGroup
	for index, trades := range pending {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.readAccount(ctx, accounts[index], read.Trades, trades, response.Excursions)
		}()
	}
	wg.Wait()
	return response, nil
}

// readAccount reads the excursions of the trades of indexes of all, on
// account, into out at the same indexes, excursionReads at once, the ones read
// kept for good; a trade not read told with why.
func (e *excursions) readAccount(ctx context.Context, account liveAccount, all []ui.PerformanceTrade, indexes []int,
	out []ui.PerformanceExcursion) {
	p := e.performance.positions
	_, err := p.readBridge(ctx, account, "the excursions", func(token string) error {
		var mu sync.Mutex
		var refused error
		limit := make(chan struct{}, excursionReads)
		var wg sync.WaitGroup
		for _, i := range indexes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				limit <- struct{}{}
				defer func() { <-limit }()
				excursion, err := e.readTrade(ctx, account.bridge, token, all[i])
				var denied *refusedToken
				if errors.As(err, &denied) {
					mu.Lock()
					refused = err
					mu.Unlock()
					return
				}
				out[i] = excursion
				if err == nil {
					e.mu.Lock()
					e.known[excursion.Account+"/"+strconv.FormatInt(excursion.Position, 10)] = excursion
					e.mu.Unlock()
				}
			}()
		}
		wg.Wait()
		return refused // another token tried
	})
	if err != nil {
		for _, i := range indexes {
			if out[i].Account == "" {
				out[i] = ui.PerformanceExcursion{Account: all[i].Account, Position: all[i].Position,
					Error: "conta não lida"}
			}
		}
	}
}

// readTrade reads the candles of trade at the bridge with token and returns
// its excursion, see excursionOf; an error read into it, but a refused token.
func (e *excursions) readTrade(ctx context.Context, bridge, token string,
	trade ui.PerformanceTrade) (ui.PerformanceExcursion, error) {
	failed := func(err error, why string) (ui.PerformanceExcursion, error) {
		return ui.PerformanceExcursion{Account: trade.Account, Position: trade.Position, Error: why}, err
	}
	name, seconds := timeframeOf(trade.ClosedAt - trade.OpenedAt)
	query := "?" + url.Values{
		"symbol":    {trade.Symbol},
		"timeframe": {name},
		"from":      {strconv.FormatInt(trade.OpenedAt/seconds*seconds, 10)},
		"to":        {strconv.FormatInt(trade.ClosedAt, 10)},
	}.Encode()
	var rates []excursionRate
	if err := e.performance.positions.get(ctx, bridge, "/rates"+query, token, &rates); err != nil {
		return failed(err, "candles não lidos")
	}
	if len(rates) == 0 {
		return failed(errors.New("no candles"), "sem candles no período da operação")
	}
	var symbol excursionSymbol
	if trade.Side == "SELL" || trade.Exit == trade.Entry {
		var err error
		if symbol, err = e.symbol(ctx, bridge, token, trade.Symbol); err != nil {
			return failed(err, "símbolo não lido")
		}
	}
	excursion, ok := excursionOf(trade, rates, symbol)
	if !ok {
		return failed(errors.New("no value"), "sem valor por unidade de preço")
	}
	excursion.Timeframe = name
	return excursion, nil
}

// symbol returns the symbol of name at bridge, read with token once.
func (e *excursions) symbol(ctx context.Context, bridge, token, name string) (excursionSymbol, error) {
	key := bridge + "\x00" + name
	e.mu.Lock()
	symbol, ok := e.symbols[key]
	e.mu.Unlock()
	if ok {
		return symbol, nil
	}
	if err := e.performance.positions.get(ctx, bridge, "/symbol?"+url.Values{"symbol": {name}}.Encode(), token,
		&symbol); err != nil {
		return excursionSymbol{}, err
	}
	e.mu.Lock()
	e.symbols[key] = symbol
	e.mu.Unlock()
	return symbol, nil
}

// timeframeOf returns the timeframe of the candles of a trade of duration
// seconds, see excursionTimeframes, and its length in seconds.
func timeframeOf(duration int64) (string, int64) {
	for _, tf := range excursionTimeframes {
		if duration/tf.seconds <= excursionMaxCandles {
			return tf.name, tf.seconds
		}
	}
	last := excursionTimeframes[len(excursionTimeframes)-1]
	return last.name, last.seconds
}

// excursionOf returns the excursion of trade along rates, see
// ui.PerformanceExcursion: the extremes of the bid (of the ask for a short,
// the bid plus the spread of each candle in points of symbol), widened to its
// entry and exit, the candles at its ends may start before its opening or
// close after it; the most it was up and down from its entry, valued as the
// trade itself, its gross over its move, else (no move) its lots at the tick
// value of symbol. False without a value.
func excursionOf(trade ui.PerformanceTrade, rates []excursionRate, symbol excursionSymbol) (ui.PerformanceExcursion,
	bool) {
	sign := 1.0
	if trade.Side == "SELL" {
		sign = -1
	}
	high, low := math.Inf(-1), math.Inf(1)
	for _, rate := range rates {
		shift := 0.0
		if sign < 0 {
			shift = rate.Spread * symbol.Point
		}
		high, low = math.Max(high, rate.High+shift), math.Min(low, rate.Low+shift)
	}
	high = math.Max(high, math.Max(trade.Entry, trade.Exit))
	low = math.Min(low, math.Min(trade.Entry, trade.Exit))

	value := 0.0 // of a move of 1 of the price of the trade
	if move := sign * (trade.Exit - trade.Entry); math.Abs(move) > 1e-9 && trade.Gross/move > 0 {
		value = trade.Gross / move
	} else if symbol.TickSize > 0 {
		value = trade.Volume * symbol.TickValue / symbol.TickSize
	}
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return ui.PerformanceExcursion{}, false
	}
	favorable, adverse := high-trade.Entry, trade.Entry-low
	if sign < 0 {
		favorable, adverse = trade.Entry-low, high-trade.Entry
	}
	return ui.PerformanceExcursion{Account: trade.Account, Position: trade.Position, High: high, Low: low,
		MFE: round2(math.Max(favorable, 0) * value), MAE: round2(math.Max(adverse, 0) * value)}, true
}
