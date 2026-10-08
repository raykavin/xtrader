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
	lastUpdate  time.Time

	events *broker

	key            string                                   // of the requests of the controller, see WithAPIKey
	brokerPosition func(pair string) (BrokerPosition, bool) // see WithBrokerPositions
	reporter       service.AccountReporter                  // see WithAccountReporter
	currency       string                                   // of the account, once read
}

// WithAccountReporter sets where the chart reads the account and the profits
// of the trades as the broker books them, in the currency of the account, eg:
// the MT5 adapter: the dashboard and the Telegram show them, while the core
// computes in the quote of the pairs.
func WithAccountReporter(reporter service.AccountReporter) Option {
	return func(c *Chart) { c.reporter = reporter }
}

// accountCurrency returns the currency of the account, read once, empty
// without WithAccountReporter. The caller holds c.mu.
func (c *Chart) accountCurrency() string {
	if c.reporter == nil {
		return ""
	}
	if c.currency == "" {
		if account, ok := c.reporter.AccountSummary(); ok {
			c.currency = account.Currency
		}
	}
	return c.currency
}

// accountProfit returns the profit the account booked for o, a close (a
// profit computed by the core), from the fill that opened the position it
// closes; nil when unknown. The caller holds c.mu.
func (c *Chart) accountProfit(o model.Order) *float64 {
	if c.reporter == nil || o.Status != model.OrderStatusTypeFilled || (o.Profit == 0 && o.ProfitValue == 0) {
		return nil
	}
	entry, ok := c.entryOf(o)
	if !ok {
		return nil
	}
	profit, ok := c.reporter.ClosedProfit(o, entry)
	if !ok {
		return nil
	}
	return &profit
}

// entryOf returns the fill that opened the position close closes, from the
// fills of its pair in the order they filled. The caller holds c.mu.
func (c *Chart) entryOf(close model.Order) (model.Order, bool) {
	ids := c.orderIDs[close.Pair]
	if ids == nil {
		return model.Order{}, false
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
	var net float64 // signed, positive long
	var entry model.Order
	for _, fill := range fills {
		signed := fill.Quantity
		if fill.Side == model.SideTypeSell {
			signed = -signed
		}
		if fill.ID == close.ID {
			return entry, net != 0 && math.Signbit(net) != math.Signbit(signed)
		}
		switch {
		case net == 0:
			entry = fill
		case math.Signbit(net) != math.Signbit(signed) && math.Abs(signed) > math.Abs(net)+1e-12:
			entry = fill // a reversal opens the rest
		}
		if net += signed; math.Abs(net) < 1e-12 {
			net = 0
		}
	}
	return model.Order{}, false
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
	}

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
	c.lastUpdate = time.Now()

	if c.events.hasSubscribers() {
		c.events.publish("candle", c.candleEvent(candle.Pair, dto))
	}
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
	if c.events.hasSubscribers() {
		c.events.publish("candle", CandleEvent{Pair: candle.Pair, Candle: dto, Indicators: []IndicatorUpdate{},
			Partial: true, Levels: c.levels(candle.Pair), Position: c.position(candle.Pair)})
	}
}

// levels returns the horizontal lines of the strategy on pair, nil when it
// draws none, see LevelsSource.
func (c *Chart) levels(pair string) []Level {
	source, ok := c.strategy.(LevelsSource)
	if !ok {
		return nil
	}
	levels := source.ChartLevels(pair)
	if levels == nil {
		levels = []Level{}
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

func (c *Chart) indicatorsByPair(pair string) []Indicator {
	indicators := make([]Indicator, 0)
	df := c.dataframe[pair]
	if df == nil {
		return indicators
	}

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

	if c.strategy != nil {
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

func (c *Chart) toOrder(o model.Order, candles []Candle) Order {
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
	if profit := c.accountProfit(o); profit != nil {
		order.AccountProfit, order.AccountCurrency = profit, c.accountCurrency()
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
	for id := range ids.Iter() {
		orders = append(orders, c.toOrder(c.orderByID[id], candles))
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
