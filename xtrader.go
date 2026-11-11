package xtrader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/internal/recovery"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/tools/metrics"

	"github.com/aybabtme/uniplot/histogram"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/schollz/progressbar/v3"
)

const defaultDatabase = "xtrader.db"

func init() {
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04",
	})
}

type OrderSubscriber interface {
	OnOrder(model.Order)
}

type CandleSubscriber interface {
	OnCandle(model.Candle)
}

// CandleHook is told every candle the bot processes live, and of a backtest,
// the ones in progress and the closed ones, once the strategy ran on it and,
// on a closed one, the orders were settled (see processCandle): never the
// candles of the preload. A panic of it is recovered and logged. It runs on the
// goroutine of the candles, so it must not wait long: eg: the protection of
// the positions moving a stop loss.
type CandleHook interface {
	OnCandle(model.Candle)
}

// WithCandleHook sets the hook of the candles, see CandleHook.
func WithCandleHook(hook CandleHook) Option {
	return func(bot *XTrader) {
		bot.candleHook = hook
	}
}

// ErrBacktestCanceled is the error of a backtest whose context ended before
// its last candle.
var ErrBacktestCanceled = errors.New("backtest canceled")

// WithBacktestProgress tells progress, after each candle of a backtest, how
// many it processed of all of them, in place of the progress bar.
func WithBacktestProgress(progress func(done, total int)) Option {
	return func(bot *XTrader) {
		bot.backtestProgress = progress
	}
}

// WithBacktestClock tells clock the time of each candle of a backtest before
// it is processed: the end of the data known then (its UpdatedAt, else its
// open time), the simulated now, eg: of a feed answering until then only.
func WithBacktestClock(clock func(time.Time)) Option {
	return func(bot *XTrader) {
		bot.backtestClock = clock
	}
}

// SetCandleHook sets the hook of the candles, see CandleHook: before Run.
func (n *XTrader) SetCandleHook(hook CandleHook) {
	n.candleHook = hook
}

type XTrader struct {
	storage  storage.Storage
	settings model.Settings
	exchange service.Exchange
	strategy strategy.Strategy
	notifier service.Notifier

	orderController       *order.Controller
	priorityQueueCandle   *model.PriorityQueue
	strategiesControllers map[string]*strategy.Controller
	orderFeed             *order.Feed
	dataFeed              *exchange.DataFeedSubscription
	paperWallet           *exchange.PaperWallet

	backtest bool

	candleHook       CandleHook            // see CandleHook
	backtestProgress func(done, total int) // see WithBacktestProgress
	backtestClock    func(time.Time)       // see WithBacktestClock

	// hooked: the candles processed go to candleHook, live after the preload;
	// set before the goroutine of the candles starts, read on it
	hooked bool
}

type Option func(*XTrader)

func NewBot(
	ctx context.Context,
	settings model.Settings,
	exch service.Exchange,
	str strategy.Strategy,
	options ...Option,
) (*XTrader, error) {
	bot := &XTrader{
		settings:              settings,
		exchange:              exch,
		strategy:              str,
		orderFeed:             order.NewOrderFeed(),
		dataFeed:              exchange.NewDataFeed(exch),
		strategiesControllers: make(map[string]*strategy.Controller),
		priorityQueueCandle:   model.NewPriorityQueue(nil),
	}

	for _, pair := range settings.Pairs {
		asset, quote := exchange.SplitAssetQuote(pair)
		if asset == "" || quote == "" {
			return nil, fmt.Errorf("invalid pair: %s", pair)
		}
	}

	for _, option := range options {
		option(bot)
	}

	var err error
	if bot.storage == nil {
		bot.storage, err = storage.FromFile(defaultDatabase)
		if err != nil {
			return nil, err
		}
	}

	bot.orderController = order.NewController(ctx, exch, bot.storage, bot.orderFeed)

	// the notifier of WithNotifier can only be wired once the order controller
	// exists
	if bot.notifier != nil {
		bot.orderController.SetNotifier(bot.notifier)
		bot.SubscribeOrder(bot.notifier)
	}

	return bot, nil
}

// WithBacktest sets the bot to run in backtest mode, it is required for backtesting environments
// Backtest mode optimize the input read for CSV and deal with race conditions
func WithBacktest(wallet *exchange.PaperWallet) Option {
	return func(bot *XTrader) {
		bot.backtest = true
		opt := WithPaperWallet(wallet)
		opt(bot)
	}
}

// WithStorage sets the storage for the bot, by default it uses a local file called xtrader.db
func WithStorage(storage storage.Storage) Option {
	return func(bot *XTrader) {
		bot.storage = storage
	}
}

// WithLogLevel sets the log level. eg: log.DebugLevel, log.InfoLevel, log.WarnLevel, log.ErrorLevel, log.FatalLevel
func WithLogLevel(level log.Level) Option {
	return func(_ *XTrader) {
		log.SetLevel(level)
	}
}

// WithNotifier registers a notifier to the bot, currently only email and telegram are supported.
// The notifier is wired to the order controller by NewBot, once it exists.
func WithNotifier(notifier service.Notifier) Option {
	return func(bot *XTrader) {
		bot.notifier = notifier
	}
}

// WithCandleSubscription subscribes a given struct to the candle feed
func WithCandleSubscription(subscriber CandleSubscriber) Option {
	return func(bot *XTrader) {
		bot.SubscribeCandle(subscriber)
	}
}

// WithPaperWallet sets the paper wallet for the bot (used for backtesting and live simulation)
func WithPaperWallet(wallet *exchange.PaperWallet) Option {
	return func(bot *XTrader) {
		bot.paperWallet = wallet
	}
}

func (n *XTrader) SubscribeCandle(subscriptions ...CandleSubscriber) {
	for _, pair := range n.settings.Pairs {
		for _, subscription := range subscriptions {
			n.dataFeed.Subscribe(pair, n.strategy.Timeframe(), subscription.OnCandle, false)
		}
	}
}

func WithOrderSubscription(subscriber OrderSubscriber) Option {
	return func(bot *XTrader) {
		bot.SubscribeOrder(subscriber)
	}
}

func (n *XTrader) SubscribeOrder(subscriptions ...OrderSubscriber) {
	for _, pair := range n.settings.Pairs {
		for _, subscription := range subscriptions {
			n.orderFeed.Subscribe(pair, subscription.OnOrder, false)
		}
	}
}

func (n *XTrader) Controller() *order.Controller {
	return n.orderController
}

// Summary function displays all trades, accuracy and some bot metrics in stdout
// To access the raw data, you may access `bot.Controller().Results`
func (n *XTrader) Summary() {
	var (
		total  float64
		wins   int
		loses  int
		volume float64
		sqn    float64
	)

	buffer := bytes.NewBuffer(nil)
	table := tablewriter.NewTable(buffer,
		tablewriter.WithRenderer(renderer.NewBlueprint(tw.Rendition{
			Symbols: tw.NewSymbols(tw.StyleASCII),
		})),
		tablewriter.WithHeaderAutoFormat(tw.Off),
		tablewriter.WithHeaderAlignment(tw.AlignCenter),
		tablewriter.WithRowAlignmentConfig(tw.CellAlignment{
			PerColumn: []tw.Align{
				tw.AlignLeft, tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight,
				tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight,
			},
		}),
		tablewriter.WithFooterAlignmentConfig(tw.CellAlignment{Global: tw.AlignRight}),
	)
	table.Header([]string{"PAIR", "TRADES", "WIN", "LOSS", "% WIN", "PAYOFF", "PR FACT.", "SQN", "PROFIT", "VOLUME"})
	avgPayoff := 0.0
	avgProfitFactor := 0.0

	// Pre-calculate total returns size to avoid reallocations
	totalReturns := 0
	for _, summary := range n.orderController.Results {
		totalReturns += len(summary.WinPercent()) + len(summary.LosePercent())
	}
	returns := make([]float64, 0, totalReturns)

	for _, summary := range n.orderController.Results {
		avgPayoff += summary.Payoff() * float64(len(summary.Win())+len(summary.Lose()))
		avgProfitFactor += summary.ProfitFactor() * float64(len(summary.Win())+len(summary.Lose()))
		err := table.Append([]string{
			summary.Pair,
			strconv.Itoa(len(summary.Win()) + len(summary.Lose())),
			strconv.Itoa(len(summary.Win())),
			strconv.Itoa(len(summary.Lose())),
			fmt.Sprintf("%.1f %%", summary.WinPercentage()),
			fmt.Sprintf("%.3f", summary.Payoff()),
			fmt.Sprintf("%.3f", summary.ProfitFactor()),
			fmt.Sprintf("%.1f", summary.SQN()),
			fmt.Sprintf("%.2f", summary.Profit()),
			fmt.Sprintf("%.2f", summary.Volume),
		})
		if err != nil {
			log.Error(err)
		}

		total += summary.Profit()
		sqn += summary.SQN()
		wins += len(summary.Win())
		loses += len(summary.Lose())
		volume += summary.Volume

		returns = append(returns, summary.WinPercent()...)
		returns = append(returns, summary.LosePercent()...)
	}

	// averages over an empty run are reported as 0 instead of NaN
	trades := math.Max(float64(wins+loses), 1)
	pairs := math.Max(float64(len(n.orderController.Results)), 1)
	table.Footer([]string{
		"TOTAL",
		strconv.Itoa(wins + loses),
		strconv.Itoa(wins),
		strconv.Itoa(loses),
		fmt.Sprintf("%.1f %%", float64(wins)/trades*100),
		fmt.Sprintf("%.3f", avgPayoff/trades),
		fmt.Sprintf("%.3f", avgProfitFactor/trades),
		fmt.Sprintf("%.1f", sqn/pairs),
		fmt.Sprintf("%.2f", total),
		fmt.Sprintf("%.2f", volume),
	})
	if err := table.Render(); err != nil {
		log.Error(err)
	}

	fmt.Println(buffer.String())
	fmt.Println("------ RETURN -------")
	totalReturn := 0.0
	returnsPercent := make([]float64, 0, len(returns))
	for _, p := range returns {
		returnsPercent = append(returnsPercent, p*100)
		totalReturn += p
	}
	hist := histogram.Hist(15, returnsPercent)
	_ = histogram.Fprint(os.Stdout, hist, histogram.Linear(10))
	fmt.Println()

	fmt.Println("------ CONFIDENCE INTERVAL (95%) -------")
	for pair, summary := range n.orderController.Results {
		fmt.Printf("| %s |\n", pair)
		returns := append(summary.WinPercent(), summary.LosePercent()...)
		returnsInterval := metrics.Bootstrap(returns, metrics.Mean, 10000, 0.95)
		payoffInterval := metrics.Bootstrap(returns, metrics.Payoff, 10000, 0.95)
		profitFactorInterval := metrics.Bootstrap(returns, metrics.ProfitFactor, 10000, 0.95)

		fmt.Printf("RETURN:      %.2f%% (%.2f%% ~ %.2f%%)\n",
			returnsInterval.Mean*100, returnsInterval.Lower*100, returnsInterval.Upper*100)
		fmt.Printf("PAYOFF:      %.2f (%.2f ~ %.2f)\n",
			payoffInterval.Mean, payoffInterval.Lower, payoffInterval.Upper)
		fmt.Printf("PROF.FACTOR: %.2f (%.2f ~ %.2f)\n",
			profitFactorInterval.Mean, profitFactorInterval.Lower, profitFactorInterval.Upper)
	}

	fmt.Println()

	if n.paperWallet != nil {
		n.paperWallet.Summary()
	}

}

func (n XTrader) SaveReturns(outputDir string) error {
	for _, summary := range n.orderController.Results {
		outputFile := fmt.Sprintf("%s/%s.csv", outputDir, summary.Pair)
		if err := summary.SaveReturns(outputFile); err != nil {
			return err
		}
	}
	return nil
}

func (n *XTrader) onCandle(candle model.Candle) {
	n.priorityQueueCandle.Push(candle)
}

// processCandle processes candle, returning the panics of the strategy on it,
// recovered and reported by its controller (see strategy.Controller): the
// candle goes on to the order controller all the same.
func (n *XTrader) processCandle(candle model.Candle) error {
	if n.paperWallet != nil {
		n.paperWallet.OnCandle(candle)
	}

	controller := n.strategiesControllers[candle.Pair]
	err := controller.OnPartialCandle(candle)
	if candle.Complete {
		// settle the orders filled so far before the strategy runs, as in the
		// backtest: a trade opened on this candle after a stop filled would
		// otherwise be added to the closed position, until the next tick
		n.orderController.UpdateOrders()
		err = errors.Join(err, controller.OnCandle(candle))
		n.orderController.OnCandle(candle)
	}
	n.hook(candle)
	return err
}

// hook tells candle to the hook of the candles, see CandleHook: live only,
// after the preload, its panic recovered and logged.
func (n *XTrader) hook(candle model.Candle) {
	if n.candleHook == nil || !n.hooked {
		return
	}
	if recovered := recovery.Call("OnCandle of the candle hook", candle.Time, func() {
		n.candleHook.OnCandle(candle)
	}); recovered != nil {
		log.Error(recovered)
	}
}

// processCandles processes the candles of the buffer as they come, from items
// (see PriorityQueue.PopLock), until ctx is done: the candle in progress then
// processed to its end, no other after.
func (n *XTrader) processCandles(ctx context.Context, items <-chan model.Item) {
	for {
		select {
		case <-ctx.Done():
			return
		case item, ok := <-items:
			if !ok {
				return // ctx done
			}
			// a panic of the strategy, reported, skips its call on the candle:
			// the bot goes on with the next one
			_ = n.processCandle(item.(model.Candle))
		}
	}
}

// backtestCandles processes the candles of the priority queue in
// chronological order, telling the backtest clock and progress, if any (else
// a progress bar), and the candle hook. A panic of the strategy ends it,
// returned: the results would be wrong; so does ctx done, with
// ErrBacktestCanceled.
func (n *XTrader) backtestCandles(ctx context.Context) error {
	log.Info("[SETUP] Starting backtesting")

	total := n.priorityQueueCandle.Len()
	var progressBar *progressbar.ProgressBar
	if n.backtestProgress == nil {
		progressBar = progressbar.Default(int64(total))
	}
	for done := 1; n.priorityQueueCandle.Len() > 0; done++ {
		if ctx.Err() != nil {
			return ErrBacktestCanceled
		}
		item := n.priorityQueueCandle.Pop()

		candle := item.(model.Candle)
		if n.backtestClock != nil {
			at := candle.UpdatedAt
			if at.IsZero() {
				at = candle.Time
			}
			n.backtestClock(at)
		}
		if n.paperWallet != nil {
			n.paperWallet.OnCandle(candle)
		}

		// settle the orders filled by this candle before the strategy runs, so
		// positions and trade results follow the candles, not the wall clock
		n.orderController.UpdateOrders()

		if err := n.strategiesControllers[candle.Pair].OnPartialCandle(candle); err != nil {
			return fmt.Errorf("backtest: %w", err)
		}
		if candle.Complete {
			if err := n.strategiesControllers[candle.Pair].OnCandle(candle); err != nil {
				return fmt.Errorf("backtest: %w", err)
			}
			n.orderController.OnCandle(candle)
		}
		n.hook(candle)

		if n.backtestProgress != nil {
			n.backtestProgress(done, total)
		} else if err := progressBar.Add(1); err != nil {
			log.Warnf("update progressbar fail: %v", err)
		}
	}
	return nil
}

// Before XTrader starts, we need to load the necessary data to fill strategy indicators
// Then, we need to get the time frame and warmup period to fetch the necessary candles
func (n *XTrader) preload(ctx context.Context, pair string) error {
	if n.backtest {
		return nil
	}

	candles, err := n.exchange.CandlesByLimit(ctx, pair, n.strategy.Timeframe(), n.strategy.WarmupPeriod())
	if err != nil {
		return err
	}

	for _, candle := range candles {
		// a panic there would come back at every start: the bot doesn't start,
		// its error told (the controller relaunches it with a backoff)
		if err := n.processCandle(candle); err != nil {
			return fmt.Errorf("preloading %s: %w", pair, err)
		}
	}

	n.dataFeed.Preload(pair, n.strategy.Timeframe(), candles)

	return nil
}

// Run will initialize the strategy controller, order controller, preload data and start the bot.
//
// Live, it runs until ctx is done, then stops trading before it returns: no
// order begins any more (order.ErrBotStopped), the strategy ends the candle in
// progress, its orders refused, and the order controller is closed (see
// order.Controller.Close), once the calls begun to the exchange are recorded in
// the storage, which nothing writes to after Run returns.
func (n *XTrader) Run(ctx context.Context) error {
	for _, pair := range n.settings.Pairs {
		// setup and subscribe strategy to data feed (candles)
		n.strategiesControllers[pair] = strategy.NewStrategyController(pair, n.strategy, n.orderController)
		if n.notifier != nil {
			n.strategiesControllers[pair].SetNotifier(n.notifier) // of the panics of the strategy
		}

		// preload candles for warmup period
		err := n.preload(ctx, pair)
		if err != nil {
			return err
		}

		// link to ninja bot controller
		n.dataFeed.Subscribe(pair, n.strategy.Timeframe(), n.onCandle, false)

		// start strategy controller
		n.strategiesControllers[pair].Start()
	}

	// start order feed and controller
	n.orderFeed.Start()
	n.orderController.Start()
	defer n.orderController.Stop()

	// the candles of the buffer taken as they come from the first one of the
	// feed on, see processCandles
	var items <-chan model.Item
	if !n.backtest {
		items = n.priorityQueueCandle.PopLock(ctx)
	}

	// start data feed and receives new candles
	n.dataFeed.Start(n.backtest)

	// start processing new candles for production or backtesting environment
	if n.backtest {
		n.hooked = true // the candles of a backtest go to the hook, on the candle clock
		return n.backtestCandles(ctx)
	}

	n.hooked = true // the preload is over: the live candles go to the hook
	processed := make(chan struct{})
	go func() {
		defer close(processed)
		n.processCandles(ctx, items)
	}()
	<-ctx.Done()
	log.Info("[SHUTDOWN] stopping the bot: no new order")
	n.orderController.Stop() // first: the candle in progress may go on for a while, eg: asking an LLM
	<-processed
	n.orderController.Close()
	log.Info("[SHUTDOWN] the bot stopped, its orders recorded")
	return nil
}
