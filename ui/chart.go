// Package ui is the side of the web dashboard that runs in the bots: the Chart
// collects the state of a bot (candles, indicators, orders, equity) and serves
// it on an HTTP API, with its control panel. The controller package serves the
// single-page React application, and Telegram, for several bots, its agents,
// through that API, whose types are in dto.go.
package ui

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/StudioSol/set"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/internal/recovery"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

// CustomIndicator is an indicator rendered by the dashboard in addition to
// the ones exposed by the strategy. See the indicator subpackage.
type CustomIndicator interface {
	Name() string
	Overlay() bool
	Warmup() int
	Metrics() []IndicatorMetric
	Load(dataframe *model.Dataframe)
}

// IndicatorMetric is one series produced by a CustomIndicator.
type IndicatorMetric struct {
	Name   string
	Color  string
	Style  string
	Values model.Series[float64]
	Time   []time.Time
}

// Chart collects candles and orders from the bot and serves them on the API of
// the dashboard, which cmd/controller serves. It implements
// xtrader.CandleSubscriber and xtrader.OrderSubscriber, and service.Notifier:
// registered with xtrader.WithNotifier, the messages of the bot are sent on its
// events too.
//
// A panic computing what it shows of a candle, eg: the indicators of the
// strategy, leaves that out only, the candle kept: recovered, logged with its
// stack and sent as an error on its events (see recovery.Reporter), the chart
// and the bot, whose feed calls it, going on.
type Chart struct {
	mu sync.Mutex

	port        int
	candles     map[string][]Candle
	partial     map[string]Candle // the candle in progress, by pair, shown before it closes
	dataframe   map[string]*model.Dataframe
	orderIDs    map[string]*set.LinkedHashSetINT64
	orderByID   map[int64]model.Order
	indicators  []CustomIndicator
	paperWallet *exchange.PaperWallet
	strategy    strategy.Strategy
	decisions   DecisionSource // of the language model of the strategy, see DecisionSource
	controller  OrderController
	channels    map[Channel]bool // where the controller is operated from
	reanalysis  bool             // whether the language model may be asked again, see SetReanalysis
	lastUpdate  time.Time

	events *broker

	key            string                                   // of the requests of the controller, see WithAPIKey
	brokerPosition func(pair string) (BrokerPosition, bool) // see WithBrokerPositions
	reporter       service.AccountReporter                  // see WithAccountReporter
	currency       string                                   // of the account, once read
	askingCurrency bool                                     // in the background, see accountCurrency

	panics *recovery.Reporter // see Chart
	window int                // the candles of a pair kept, see WithCandleWindow

	// the timeframes shown other than the one of the strategy, see
	// WithCandleSource: the source of their candles, the prices received for
	// their candles in progress (under mu), and the candles read lately
	source    CandleSource
	ticks     map[string][]tick
	viewMu    sync.Mutex
	viewCache map[string]viewCached
	clock     func() time.Time // the wall clock of the prices received
}

// ChartCandles are the closed candles of a pair the chart keeps by default,
// see WithCandleWindow: the last ones, sent with the snapshot of the pair, the
// indicators drawn on them, eg: 3 days of M1, 17 of M5, 7 months of H1: more
// than the dashboard shows at a time, while the snapshot and the indicators,
// computed again on the closes and the snapshots, stay the same size as the
// bot runs for months.
const ChartCandles = 5000

// WithCandleWindow sets the closed candles of a pair the chart keeps, the last
// ones, ChartCandles by default, with the warmup of the indicators before them
// (the strategy's, and the longest of WithCustomIndicators), so the
// indicators are drawn on all of them; 0 keeps them all, eg: the chart of a
// backtest, served once it ends. The orders are kept all the same.
func WithCandleWindow(candles int) Option {
	return func(c *Chart) { c.window = max(candles, 0) }
}

// WithAccountReporter sets where the chart reads the account and the profits
// of the trades as the broker books them, in the currency of the account, eg:
// the MT5 adapter: the dashboard and the Telegram show them, while the core
// computes in the quote of the pairs.
func WithAccountReporter(reporter service.AccountReporter) Option {
	return func(c *Chart) { c.reporter = reporter }
}

// currencyReporter is an AccountReporter that knows the currency of its
// account without asking for it, eg: MT5, since it connected.
type currencyReporter interface {
	AccountCurrency() string
}

var _ currencyReporter = (*exchange.MT5)(nil)

// knownCurrency returns the currency of the account when known, without
// asking the broker. The caller holds c.mu.
func (c *Chart) knownCurrency() string {
	if c.currency == "" {
		if reporter, ok := c.reporter.(currencyReporter); ok {
			c.currency = reporter.AccountCurrency()
		}
	}
	return c.currency
}

// accountCurrency returns the currency of the account, empty without
// WithAccountReporter or until it is read: unless the broker tells it (see
// currencyReporter), the account is read in the background, never holding
// c.mu, as it is asked on the network (see readCurrency). The caller holds
// c.mu.
func (c *Chart) accountCurrency() string {
	currency := c.knownCurrency()
	if currency == "" && c.reporter != nil && !c.askingCurrency {
		c.askingCurrency = true
		go func() {
			c.readCurrency()
			c.mu.Lock()
			c.askingCurrency = false
			c.mu.Unlock()
		}()
	}
	return currency
}

// readCurrency reads the currency of the account from the broker, unless known
// already, see accountCurrency. It may ask the broker on the network: the
// caller doesn't hold c.mu.
func (c *Chart) readCurrency() {
	if c.reporter == nil {
		return
	}
	c.mu.Lock()
	known := c.knownCurrency() != ""
	c.mu.Unlock()
	if known {
		return
	}
	account, ok := c.reporter.AccountSummary()
	if !ok || account.Currency == "" {
		return
	}
	c.mu.Lock()
	c.currency = account.Currency
	c.mu.Unlock()
}

// accountProfit returns the profit the account booked for o, a close (a
// profit computed by the core), from the fill that opened the position it
// closes (see closeEntries, entries when computed already for its pair); nil
// when unknown. The caller holds c.mu.
func (c *Chart) accountProfit(o model.Order, entries map[int64]model.Order) *float64 {
	if c.reporter == nil || o.Status != model.OrderStatusTypeFilled || !settledClose(o) {
		return nil
	}
	if entries == nil {
		entries = c.closeEntries(o.Pair)
	}
	entry, ok := entries[o.ID]
	if !ok {
		return nil
	}
	profit, ok := c.reporter.ClosedProfit(o, entry)
	if !ok {
		return nil
	}
	return &profit
}

// settledClose reports whether the core settled the fill o as a close of (a
// part of) its position: it carries the profit of it.
func settledClose(o model.Order) bool {
	return o.Profit != 0 || o.ProfitValue != 0
}

// openLot is a fill that opened or added to a position, and its units still
// open.
type openLot struct {
	entry model.Order
	open  float64
}

// lotEpsilon is how many units of a fill are none, against the rounding of
// the sums of the quantities.
const lotEpsilon = 1e-9

// closeEntries returns, by the ID of each fill of pair closing (a part of) a
// position, the fill that opened that position, from the fills of the pair in
// the order they filled, settled as the core settles them (see
// order.Controller):
//   - a fill on the side of the open ones opens or adds to the position;
//   - one on the other side the core settled as a close (see settledClose)
//     closes the open ones: first the one the account books it against when
//     the broker reports the profits of the trades (see
//     service.AccountReporter.ClosedProfit), else the oldest, then the others
//     from the oldest; its units beyond them reverse the position, opened by
//     it;
//   - one on the other side the core didn't settle as a close opens a
//     position anew: the one of the open fills was gone for the core, eg:
//     closed outside the bot (by hand, by a stop out), discarded without a
//     fill (see order.Controller.reconcile).
//
// The caller holds c.mu.
func (c *Chart) closeEntries(pair string) map[int64]model.Order {
	entries := make(map[int64]model.Order)
	ids := c.orderIDs[pair]
	if ids == nil {
		return entries
	}
	var fills []model.Order
	for id := range ids.Iter() {
		if order := c.orderByID[id]; order.Status == model.OrderStatusTypeFilled && order.Quantity > 0 {
			fills = append(fills, order)
		}
	}
	sort.SliceStable(fills, func(i, j int) bool {
		a, b := fills[i].UpdatedAt, fills[j].UpdatedAt
		if !a.Equal(b) {
			return a.Before(b)
		}
		return fills[i].ID < fills[j].ID
	})
	var lots []openLot
	var side model.SideType // of the open lots
	for _, fill := range fills {
		switch {
		case len(lots) == 0 || fill.Side == side:
			lots, side = append(lots, openLot{entry: fill, open: fill.Quantity}), fill.Side
		case !settledClose(fill):
			lots, side = []openLot{{entry: fill, open: fill.Quantity}}, fill.Side
		default:
			first := c.bookedLot(fill, lots)
			entries[fill.ID] = lots[first].entry
			lots, side = closeLots(lots, first, fill, side)
		}
	}
	return entries
}

// bookedLot returns the open lot the account books close against, see
// service.AccountReporter.ClosedProfit: the oldest when it books none, or
// without a reporter. The caller holds c.mu.
func (c *Chart) bookedLot(close model.Order, lots []openLot) int {
	if c.reporter == nil || len(lots) < 2 {
		return 0
	}
	for i, lot := range lots {
		if _, ok := c.reporter.ClosedProfit(close, lot.entry); ok {
			return i
		}
	}
	return 0
}

// closeLots returns the lots open on side after close, on the other side,
// closed them: the lot first first, then the others from the oldest; its
// units beyond them open a lot of its own, the side of the position reversed.
func closeLots(lots []openLot, first int, close model.Order, side model.SideType) ([]openLot, model.SideType) {
	left := close.Quantity
	take := func(i int) {
		units := math.Min(left, lots[i].open)
		lots[i].open -= units
		left -= units
	}
	take(first)
	for i := range lots {
		if i != first && left > lotEpsilon {
			take(i)
		}
	}
	open := make([]openLot, 0, len(lots))
	for _, lot := range lots {
		if lot.open > lotEpsilon {
			open = append(open, lot)
		}
	}
	if left > lotEpsilon {
		return []openLot{{entry: close, open: left}}, close.Side // a reversal opens the rest
	}
	return open, side
}

// WithBrokerPositions sets where the chart reads the open position of a pair
// as the broker reports it, its profit included, eg: MT5.OpenPosition: sent
// with the snapshot and every candle, the one in progress too, so the profit
// of the position follows the market. read must not block: a state polled
// already, not a request.
func WithBrokerPositions(read func(pair string) (BrokerPosition, bool)) Option {
	return func(c *Chart) { c.brokerPosition = read }
}

// position returns the open position of pair as the broker reports it, nil
// without WithBrokerPositions.
func (c *Chart) position(pair string) *BrokerPosition {
	if c.brokerPosition == nil {
		return nil
	}
	position, open := c.brokerPosition(pair)
	if !open {
		return &BrokerPosition{}
	}
	position.Open = true
	position.Quantity, position.Entry, position.Profit = orZero(position.Quantity), orZero(position.Entry),
		orZero(position.Profit)
	return &position
}

// Option configures a Chart.
type Option func(*Chart)

// WithPort sets the HTTP port of the API (default 5555), the dashboard of
// cmd/controller being on 8080.
func WithPort(port int) Option {
	return func(c *Chart) { c.port = port }
}

// WithStrategyIndicators renders the indicators returned by
// strategy.Indicators on the dashboard.
func WithStrategyIndicators(s strategy.Strategy) Option {
	return func(c *Chart) { c.strategy = s }
}

// WithPaperWallet enables the equity / asset value charts and drawdown.
func WithPaperWallet(wallet *exchange.PaperWallet) Option {
	return func(c *Chart) { c.paperWallet = wallet }
}

// WithCustomIndicators adds indicators that are not part of the strategy.
func WithCustomIndicators(indicators ...CustomIndicator) Option {
	return func(c *Chart) { c.indicators = indicators }
}

// New creates the chart of a bot. Call Start, or mount Handler, to serve its
// API to the dashboard.
func New(options ...Option) (*Chart, error) {
	c := &Chart{
		port:      5555,
		candles:   make(map[string][]Candle),
		partial:   make(map[string]Candle),
		dataframe: make(map[string]*model.Dataframe),
		orderIDs:  make(map[string]*set.LinkedHashSetINT64),
		orderByID: make(map[int64]model.Order),
		events:    newBroker(),
		window:    ChartCandles,
		ticks:     make(map[string][]tick),
		viewCache: make(map[string]viewCached),
		clock:     time.Now,
	}
	c.panics = recovery.NewReporter(c.OnError)

	for _, option := range options {
		option(c)
	}
	if source, ok := c.strategy.(DecisionSource); ok {
		c.decisions = source
		source.ListenDecisions(c.onDecision)
	}

	return c, nil
}

// OnOrder stores an order and notifies connected dashboards.
func (c *Chart) OnOrder(order model.Order) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.orderIDs[order.Pair] == nil {
		c.orderIDs[order.Pair] = set.NewLinkedHashSetINT64()
	}
	c.orderIDs[order.Pair].Add(order.ID)
	// a fill published again keeps its time, the one the accounting periods
	// count the trade closed by, as the store does
	if was, ok := c.orderByID[order.ID]; ok && was.Status == model.OrderStatusTypeFilled &&
		order.Status == model.OrderStatusTypeFilled {
		order.UpdatedAt = was.UpdatedAt
	}
	c.orderByID[order.ID] = order

	c.events.publish("order", OrderEvent{
		Pair:  order.Pair,
		Order: c.toOrder(order, c.candles[order.Pair]),
	})
}

// Notify sends a message of the bot, eg: the result of a closed trade, to the
// connected dashboards.
func (c *Chart) Notify(message string) {
	c.events.publish("notification", NotificationEvent{Message: message})
}

// OnError sends an error of the bot to the connected dashboards.
func (c *Chart) OnError(err error) {
	message := err.Error()
	var orderError *exchange.OrderError
	if errors.As(err, &orderError) {
		message = fmt.Sprintf("Pair: %s\nQuantity: %.4f\n-----\n%s", orderError.Pair, orderError.Quantity,
			orderError.Err)
	}
	c.events.publish("notification", NotificationEvent{Message: message, Error: true})
}

// OnCandle stores a closed candle and notifies connected dashboards.
func (c *Chart) OnCandle(candle model.Candle) {
	if !candle.Complete {
		c.onPartialCandle(candle)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	candles := c.candles[candle.Pair]
	if len(candles) > 0 && candle.Time.Unix() <= candles[len(candles)-1].Time {
		return
	}
	if partial, ok := c.partial[candle.Pair]; ok && partial.Time <= candle.Time.Unix() {
		delete(c.partial, candle.Pair)
	}

	dto := Candle{
		Time:   candle.Time.Unix(),
		Open:   candle.Open,
		High:   candle.High,
		Low:    candle.Low,
		Close:  candle.Close,
		Volume: candle.Volume,
	}
	c.candles[candle.Pair] = append(candles, dto)
	c.recordTick(candle.Pair, candle.Close)

	df := c.dataframe[candle.Pair]
	if df == nil {
		df = &model.Dataframe{
			Pair:     candle.Pair,
			Metadata: make(map[string]model.Series[float64]),
		}
		c.dataframe[candle.Pair] = df
	}
	if c.orderIDs[candle.Pair] == nil {
		c.orderIDs[candle.Pair] = set.NewLinkedHashSetINT64()
	}

	df.Close = append(df.Close, candle.Close)
	df.Open = append(df.Open, candle.Open)
	df.High = append(df.High, candle.High)
	df.Low = append(df.Low, candle.Low)
	df.Volume = append(df.Volume, candle.Volume)
	df.Time = append(df.Time, candle.Time)
	df.LastUpdate = candle.Time
	for k, v := range candle.Metadata {
		df.Metadata[k] = append(df.Metadata[k], v)
	}
	c.trim(candle.Pair)
	c.lastUpdate = time.Now()

	if c.events.hasSubscribers() {
		if event, ok := c.safely(candle, func() CandleEvent { return c.candleEvent(candle.Pair, dto) }); ok {
			c.events.publish("candle", event)
		}
	}
}

// trim drops the oldest candles of pair beyond the ones kept, see
// WithCandleWindow, from the candles and the dataframe of the indicators, in
// step. The series are resliced, append copying the candles kept once out of
// room. The caller holds c.mu.
func (c *Chart) trim(pair string) {
	if c.window <= 0 {
		return
	}
	keep := c.window + c.warmup()
	if drop := len(c.candles[pair]) - keep; drop > 0 {
		c.candles[pair] = c.candles[pair][drop:]
	}
	df := c.dataframe[pair]
	drop := len(df.Time) - keep
	if drop <= 0 {
		return
	}
	df.Close, df.Open, df.High, df.Low = df.Close[drop:], df.Open[drop:], df.High[drop:], df.Low[drop:]
	df.Volume, df.Time = df.Volume[drop:], df.Time[drop:]
	for key, values := range df.Metadata {
		df.Metadata[key] = values[min(drop, len(values)):]
	}
}

// warmup returns the candles the indicators of the chart take before their
// first value: the warmup of the strategy, or of the longest of the custom
// indicators.
func (c *Chart) warmup() int {
	warmup := 0
	if c.strategy != nil {
		warmup = c.strategy.WarmupPeriod()
	}
	for _, indicator := range c.indicators {
		warmup = max(warmup, indicator.Warmup())
	}
	return max(warmup, 0)
}

// safely returns event(), the event of candle, false when it panics, eg: the
// strategy drawing its levels: recovered and reported, see Chart. The caller
// holds c.mu.
func (c *Chart) safely(candle model.Candle, event func() CandleEvent) (CandleEvent, bool) {
	var built CandleEvent
	recovered := recovery.Call("the candle event of the chart of "+candle.Pair, candle.Time,
		func() { built = event() })
	if recovered != nil {
		c.panics.Report(recovered, "the candle left out of the events of the dashboard, its snapshot shows it")
		return CandleEvent{}, false
	}
	return built, true
}

// onPartialCandle keeps the candle in progress of its pair, shown by the
// snapshot and sent on the SSE stream as it changes (Partial), the indicators
// left to its close: the chart follows the market between the closes.
func (c *Chart) onPartialCandle(candle model.Candle) {
	c.mu.Lock()
	defer c.mu.Unlock()

	candles := c.candles[candle.Pair]
	if len(candles) == 0 || candle.Time.Unix() <= candles[len(candles)-1].Time {
		return // before the history, or a candle closed already
	}
	dto := Candle{
		Time:   candle.Time.Unix(),
		Open:   candle.Open,
		High:   candle.High,
		Low:    candle.Low,
		Close:  candle.Close,
		Volume: candle.Volume,
	}
	if c.partial[candle.Pair] == dto {
		return
	}
	c.partial[candle.Pair] = dto
	c.recordTick(candle.Pair, candle.Close)
	if c.events.hasSubscribers() {
		event, ok := c.safely(candle, func() CandleEvent {
			return CandleEvent{Pair: candle.Pair, Candle: dto, Indicators: []IndicatorUpdate{}, Partial: true,
				Levels: c.levels(candle.Pair), Position: c.position(candle.Pair)}
		})
		if ok {
			c.events.publish("candle", event)
		}
	}
}

// levels returns the horizontal lines of the strategy on pair, nil when it
// draws none, see LevelsSource.
func (c *Chart) levels(pair string) []Level {
	source, ok := c.strategy.(LevelsSource)
	if !ok {
		return nil
	}
	levels := make([]Level, 0)
	for _, level := range source.ChartLevels(pair) {
		if finite(level.Price) {
			levels = append(levels, level)
		}
	}
	return levels
}

func (c *Chart) candleEvent(pair string, candle Candle) CandleEvent {
	event := CandleEvent{
		Pair:       pair,
		Candle:     candle,
		Indicators: make([]IndicatorUpdate, 0),
		Levels:     c.levels(pair),
		Position:   c.position(pair),
	}

	for _, indicator := range c.indicatorsByPair(pair) {
		update := IndicatorUpdate{Name: indicator.Name, Metrics: make([]MetricUpdate, 0, len(indicator.Metrics))}
		for _, metric := range indicator.Metrics {
			if len(metric.Points) == 0 {
				continue
			}
			update.Metrics = append(update.Metrics, MetricUpdate{
				Name:  metric.Name,
				Point: metric.Points[len(metric.Points)-1],
			})
		}
		event.Indicators = append(event.Indicators, update)
	}

	assetValues, equityValues := c.equityValuesByPair(pair)
	if len(equityValues) > 0 {
		last := equityValues[len(equityValues)-1]
		event.Equity = &last
	}
	if len(assetValues) > 0 {
		last := assetValues[len(assetValues)-1]
		event.Asset = &last
	}

	return event
}

func (c *Chart) pairs() []string {
	pairs := make([]string, 0, len(c.candles))
	for pair := range c.candles {
		pairs = append(pairs, pair)
	}
	sort.Strings(pairs)
	return pairs
}

func (c *Chart) snapshot(pair string) Snapshot {
	asset, quote := exchange.SplitAssetQuote(pair)
	assetValues, equityValues := c.equityValuesByPair(pair)

	candles := make([]Candle, len(c.candles[pair]), len(c.candles[pair])+1)
	copy(candles, c.candles[pair])
	if partial, ok := c.partial[pair]; ok && (len(candles) == 0 || partial.Time > candles[len(candles)-1].Time) {
		candles = append(candles, partial) // in progress: the last one
	}

	return Snapshot{
		Pair:            pair,
		Asset:           asset,
		Quote:           quote,
		Candles:         candles,
		Indicators:      c.indicatorsByPair(pair),
		Orders:          c.ordersByPair(pair),
		EquityValues:    equityValues,
		AssetValues:     assetValues,
		MaxDrawdown:     c.maxDrawdown(),
		Levels:          c.levels(pair),
		Position:        c.position(pair),
		AccountCurrency: c.accountCurrency(),
		LLM:             c.llm(),
		Analysis:        c.analysis(pair),
		Timeframe:       c.executionTimeframe(),
		Timeframes:      c.viewTimeframes(),
	}
}

func (c *Chart) maxDrawdown() *Drawdown {
	if c.paperWallet == nil {
		return nil
	}
	value, start, end := c.paperWallet.MaxDrawdown()
	if start.IsZero() || !finite(value) {
		return nil
	}
	return &Drawdown{Value: value, Start: start.Unix(), End: end.Unix()}
}

func (c *Chart) equityValuesByPair(pair string) (asset []Point, equity []Point) {
	asset = make([]Point, 0)
	equity = make([]Point, 0)

	if c.paperWallet == nil {
		return asset, equity
	}

	assetName, _ := exchange.SplitAssetQuote(pair)
	for _, value := range c.paperWallet.AssetValues(assetName) {
		if finite(value.Value) {
			asset = append(asset, Point{Time: value.Time.Unix(), Value: value.Value})
		}
	}
	for _, value := range c.paperWallet.EquityValues() {
		if finite(value.Value) {
			equity = append(equity, Point{Time: value.Time.Unix(), Value: value.Value})
		}
	}

	return asset, equity
}

// toPoints pairs the values with their times, leaving out the values that
// aren't finite: the gaps of the indicators, as the na of Pine, which JSON
// can't carry and the dashboard doesn't draw.
func toPoints(times []time.Time, values []float64) []Point {
	n := len(values)
	if len(times) < n {
		n = len(times)
	}
	points := make([]Point, 0, n)
	for i := 0; i < n; i++ {
		if finite(values[i]) {
			points = append(points, Point{Time: times[i].Unix(), Value: values[i]})
		}
	}
	return points
}

// finite reports whether value is neither NaN nor infinite.
func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// orZero returns value, or 0 when it isn't finite: JSON can't carry it, and
// the whole answer would fail.
func orZero(value float64) float64 {
	if !finite(value) {
		return 0
	}
	return value
}

// indicatorsByPair returns the indicators of pair: the custom ones, then the
// ones of the strategy; the ones that panic left out, see Chart. The caller
// holds c.mu.
func (c *Chart) indicatorsByPair(pair string) []Indicator {
	indicators := make([]Indicator, 0)
	df := c.dataframe[pair]
	if df == nil {
		return indicators
	}

	custom := recovery.Call("the custom indicators of the chart of "+pair, df.LastUpdate, func() {
		indicators = append(indicators, c.customIndicators(df)...)
	})
	if custom != nil {
		c.panics.Report(custom, "left out of the chart")
	}
	if c.strategy != nil {
		var drawn []Indicator
		recovered := recovery.Call("Indicators of the strategy on the chart of "+pair, df.LastUpdate, func() {
			drawn = c.strategyIndicators(df)
		})
		if recovered != nil {
			c.panics.Report(recovered, "left out of the chart")
		}
		indicators = append(indicators, drawn...)
	}
	return indicators
}

// customIndicators returns the indicators of WithCustomIndicators on df.
func (c *Chart) customIndicators(df *model.Dataframe) []Indicator {
	indicators := make([]Indicator, 0, len(c.indicators))
	for _, i := range c.indicators {
		i.Load(df)
		indicator := Indicator{
			Name:    i.Name(),
			Overlay: i.Overlay(),
			Metrics: make([]Metric, 0),
		}
		for _, metric := range i.Metrics() {
			indicator.Metrics = append(indicator.Metrics, Metric{
				Name:   metric.Name,
				Color:  metric.Color,
				Style:  metric.Style,
				Points: toPoints(metric.Time, metric.Values),
			})
		}
		indicators = append(indicators, indicator)
	}
	return indicators
}

// strategyIndicators returns the indicators of the strategy on df.
func (c *Chart) strategyIndicators(df *model.Dataframe) []Indicator {
	indicators := make([]Indicator, 0)
	warmup := c.strategy.WarmupPeriod()
	for _, i := range c.strategy.Indicators(df) {
		indicator := Indicator{
			Name:    i.GroupName,
			Overlay: i.Overlay,
			Metrics: make([]Metric, 0),
		}
		// Values before the warmup period are not meaningful (usually zero)
		// and would distort the price scale, so they are dropped. An
		// indicator that tells where its values start is trusted, the
		// others wait for the whole warmup of the strategy.
		start := i.Warmup
		if start == 0 {
			start = warmup
		}
		for _, metric := range i.Metrics {
			if len(metric.Values) < start || len(i.Time) < start {
				continue
			}
			indicator.Metrics = append(indicator.Metrics, Metric{
				Name:   metric.Name,
				Color:  metric.Color,
				Style:  string(metric.Style),
				Points: toPoints(i.Time[start:], metric.Values[start:]),
			})
		}
		indicators = append(indicators, indicator)
	}
	return indicators
}

// candleTimeFor returns the open time of the candle that contains t, or the
// last candle when t is after the last known candle. Zero when no candles.
func candleTimeFor(candles []Candle, t time.Time) int64 {
	if len(candles) == 0 {
		return 0
	}
	ts := t.Unix()
	idx := sort.Search(len(candles), func(i int) bool { return candles[i].Time > ts })
	if idx == 0 {
		return candles[0].Time
	}
	return candles[idx-1].Time
}

// toOrder returns the DTO of o, its numbers that aren't finite, eg: the profit
// of a fill without a price, sent as 0: JSON can't carry them, and the snapshot
// of its pair and its events would fail. The caller holds c.mu.
func (c *Chart) toOrder(o model.Order, candles []Candle) Order {
	return c.toOrderOf(o, candles, nil)
}

// toOrderOf is toOrder, entries the fills that opened the positions the
// closes of the pair of o close, when computed already (see closeEntries).
// The caller holds c.mu.
func (c *Chart) toOrderOf(o model.Order, candles []Candle, entries map[int64]model.Order) Order {
	o, _ = o.Finite()
	order := Order{
		ID:          o.ID,
		ExchangeID:  o.ExchangeID,
		Pair:        o.Pair,
		Side:        string(o.Side),
		Type:        string(o.Type),
		Status:      string(o.Status),
		Price:       o.Price,
		Quantity:    o.Quantity,
		Fee:         o.Fee,
		CreatedAt:   o.CreatedAt.Unix(),
		UpdatedAt:   o.UpdatedAt.Unix(),
		CandleTime:  candleTimeFor(candles, o.UpdatedAt),
		Stop:        o.Stop,
		GroupID:     o.GroupID,
		RefPrice:    o.RefPrice,
		Profit:      o.Profit,
		ProfitValue: o.ProfitValue,
		DecisionID:  c.decisionOf(o.ID),
	}
	// in the currency of the account: unknown until it is read
	if profit := c.accountProfit(o, entries); profit != nil && finite(*profit) {
		if currency := c.accountCurrency(); currency != "" {
			order.AccountProfit, order.AccountCurrency = profit, currency
		}
	}
	return order
}

func (c *Chart) ordersByPair(pair string) []Order {
	orders := make([]Order, 0)
	ids := c.orderIDs[pair]
	if ids == nil {
		return orders
	}
	candles := c.candles[pair]
	var entries map[int64]model.Order // of the closes, read once for all of them
	if c.reporter != nil {
		entries = c.closeEntries(pair)
	}
	for id := range ids.Iter() {
		orders = append(orders, c.toOrderOf(c.orderByID[id], candles, entries))
	}
	return orders
}

func (c *Chart) orderRowsByPair(pair string) [][]string {
	rows := make([][]string, 0)
	ids := c.orderIDs[pair]
	if ids == nil {
		return rows
	}
	for id := range ids.Iter() {
		o := c.orderByID[id]
		var profit string
		if o.Profit != 0 {
			profit = fmt.Sprintf("%.2f", o.Profit)
		}
		rows = append(rows, []string{
			o.CreatedAt.Format(time.RFC3339),
			string(o.Status),
			string(o.Side),
			fmt.Sprintf("%d", o.ID),
			string(o.Type),
			fmt.Sprintf("%f", o.Quantity),
			fmt.Sprintf("%f", o.Price),
			fmt.Sprintf("%.2f", o.Quantity*o.Price),
			fmt.Sprintf("%.4f", o.Fee),
			profit,
		})
	}
	return rows
}

// Start serves the API on the configured port of the loopback, for the
// dashboard of cmd/controller, whose database lists it, which reaches its
// agents there only. It blocks until the server stops.
func (c *Chart) Start() error {
	fmt.Printf("Dashboard API available at http://localhost:%d/api, open it with cmd/controller\n", c.port)
	return NewHTTPServer(fmt.Sprintf("localhost:%d", c.port), c.Handler()).ListenAndServe()
}

// Handler returns the HTTP handler of the API, under /api, for users who want
// to mount it in their own server.
func (c *Chart) Handler() http.Handler {
	return c.router()
}
