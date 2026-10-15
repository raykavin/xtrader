package exchange

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/StudioSol/set"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/tools/log"
)

var (
	ErrInvalidQuantity   = errors.New("invalid quantity")
	ErrInsufficientFunds = errors.New("insufficient funds or locked")
	ErrInvalidAsset      = errors.New("invalid asset")
	ErrOrderNotFound     = errors.New("order not found")
	// ErrOrderNotOpen is returned when an order can't be canceled because it
	// has already been filled or canceled.
	ErrOrderNotOpen = errors.New("order is not open")
	// ErrNotSupported is returned by the operations an exchange doesn't offer.
	ErrNotSupported = errors.New("operation not supported by the exchange")
	// ErrInvalidProtection is returned for a stop loss or take profit on the
	// wrong side of the position they protect.
	ErrInvalidProtection = errors.New("invalid stop loss or take profit")
	// ErrNothingToReduce is returned by a reduce-only order when nothing is
	// held against its side, see service.ReduceOnlyBroker: flat, eg: closed by
	// its stop loss meanwhile, or held on the side of the order.
	ErrNothingToReduce = errors.New("no position to reduce")
)

// checkProtection validates the stop loss and the take profit of a position
// opened on side at entry: at least one of them, the stop loss on the losing
// side and the take profit on the winning one. entry 0 is unknown, eg: the
// price of a market order, and only the levels are compared.
func checkProtection(side model.SideType, entry, stop, profit float64) error {
	sign := 1.0
	if side == model.SideTypeSell {
		sign = -1
	}

	var err error
	switch {
	case stop < 0 || profit < 0:
		err = fmt.Errorf("negative levels: stop loss %v, take profit %v", stop, profit)
	case stop == 0 && profit == 0:
		err = errors.New("neither a stop loss nor a take profit")
	case stop > 0 && profit > 0 && sign*stop >= sign*profit:
		err = fmt.Errorf("the stop loss %v of a %s position is beyond its take profit %v", stop, side, profit)
	case entry > 0 && stop > 0 && sign*stop >= sign*entry:
		err = fmt.Errorf("the stop loss %v of a %s position is beyond its entry %v", stop, side, entry)
	case entry > 0 && profit > 0 && sign*profit <= sign*entry:
		err = fmt.Errorf("the take profit %v of a %s position is behind its entry %v", profit, side, entry)
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProtection, err)
	}
	return nil
}

type DataFeed struct {
	Data chan model.Candle
	Err  chan error
}

type DataFeedSubscription struct {
	exchange                service.Exchange
	Feeds                   *set.LinkedHashSetString
	DataFeeds               map[string]*DataFeed
	SubscriptionsByDataFeed map[string][]Subscription
}

type Subscription struct {
	onCandleClose bool
	consumer      DataFeedConsumer
}

type OrderError struct {
	Err      error
	Pair     string
	Quantity float64
}

func (o *OrderError) Error() string {
	return fmt.Sprintf("order error: %v", o.Err)
}

// Unwrap exposes the underlying cause, so callers can match it with errors.Is.
func (o *OrderError) Unwrap() error {
	return o.Err
}

type DataFeedConsumer func(model.Candle)

func NewDataFeed(exchange service.Exchange) *DataFeedSubscription {
	return &DataFeedSubscription{
		exchange:                exchange,
		Feeds:                   set.NewLinkedHashSetString(),
		DataFeeds:               make(map[string]*DataFeed),
		SubscriptionsByDataFeed: make(map[string][]Subscription),
	}
}

func (d *DataFeedSubscription) feedKey(pair, timeframe string) string {
	return fmt.Sprintf("%s--%s", pair, timeframe)
}

func (d *DataFeedSubscription) pairTimeframeFromKey(key string) (pair, timeframe string) {
	parts := strings.Split(key, "--")
	return parts[0], parts[1]
}

func (d *DataFeedSubscription) Subscribe(pair, timeframe string, consumer DataFeedConsumer, onCandleClose bool) {
	key := d.feedKey(pair, timeframe)
	d.Feeds.Add(key)
	d.SubscriptionsByDataFeed[key] = append(d.SubscriptionsByDataFeed[key], Subscription{
		onCandleClose: onCandleClose,
		consumer:      consumer,
	})
}

func (d *DataFeedSubscription) Preload(pair, timeframe string, candles []model.Candle) {
	log.Infof("[SETUP] preloading %d candles for %s-%s", len(candles), pair, timeframe)
	key := d.feedKey(pair, timeframe)
	for _, candle := range candles {
		if !candle.Complete {
			continue
		}

		for _, subscription := range d.SubscriptionsByDataFeed[key] {
			subscription.consumer(candle)
		}
	}
}

func (d *DataFeedSubscription) Connect() {
	log.Infof("Connecting to the exchange.")
	for feed := range d.Feeds.Iter() {
		pair, timeframe := d.pairTimeframeFromKey(feed)
		ccandle, cerr := d.exchange.CandlesSubscription(context.Background(), pair, timeframe)
		d.DataFeeds[feed] = &DataFeed{
			Data: ccandle,
			Err:  cerr,
		}
	}
}

// Start connects the feeds and hands their candles to their consumers, until
// the feeds close. With loadSync, eg: a backtest, the consumers take each
// candle in turn and Start returns once the feeds closed; else each consumer
// takes the candles from a queue of its own (see candleQueue), so that a slow
// one, eg: the chart of a bot serving a snapshot, holds itself only, never the
// feed nor the other consumers, eg: the bot.
func (d *DataFeedSubscription) Start(loadSync bool) {
	d.Connect()
	wg := new(sync.WaitGroup)
	for key, feed := range d.DataFeeds {
		wg.Add(1)
		subscriptions := d.SubscriptionsByDataFeed[key]
		consumers := make([]DataFeedConsumer, len(subscriptions))
		var queues []*candleQueue
		for i, subscription := range subscriptions {
			consumers[i] = subscription.consumer
			if !loadSync {
				pair, timeframe := d.pairTimeframeFromKey(key)
				queue := newCandleQueue(pair+" "+timeframe, subscription.consumer)
				queues = append(queues, queue)
				consumers[i] = queue.push
			}
		}
		go func(feed *DataFeed) {
			for {
				select {
				case candle, ok := <-feed.Data:
					if !ok {
						for _, queue := range queues {
							queue.close()
						}
						wg.Done()
						return
					}
					for i, subscription := range subscriptions {
						if subscription.onCandleClose && !candle.Complete {
							continue
						}
						consumers[i](candle)
					}
				case err := <-feed.Err:
					if err != nil {
						log.Error("dataFeedSubscription/start: ", err)
					}
				}
			}
		}(feed)
	}

	log.Infof("Data feed connected.")
	if loadSync {
		wg.Wait()
	}
}

// candleQueue hands the candles of a feed to one of its consumers, every one in
// their order, from a goroutine of its own, see Start. It has no bound: a
// candle dropped or merged would change the ticks the strategy sees; a
// consumer behind by candleQueueWarn candles or more is warned about instead,
// once every candleQueueWarnEvery at most.
type candleQueue struct {
	feed     string
	mtx      sync.Mutex
	candles  []model.Candle
	closed   bool
	warned   time.Time // the last warning of a consumer behind
	ready    chan struct{}
	consumer DataFeedConsumer
}

const (
	// candleQueueWarn is how many candles queued for a consumer tell it is
	// behind, eg: stuck: about a quarter of an hour of the candles in progress
	// of a pair polled every second, more than a backfill after a short gap.
	candleQueueWarn = 1000
	// candleQueueWarnEvery bounds how often a consumer behind is warned about.
	candleQueueWarnEvery = time.Minute
)

func newCandleQueue(feed string, consumer DataFeedConsumer) *candleQueue {
	q := &candleQueue{feed: feed, ready: make(chan struct{}, 1), consumer: consumer}
	go q.run()
	return q
}

// push queues candle for the consumer, without waiting for it.
func (q *candleQueue) push(candle model.Candle) {
	q.mtx.Lock()
	q.candles = append(q.candles, candle)
	queued, warn := len(q.candles), false
	if queued >= candleQueueWarn && time.Since(q.warned) >= candleQueueWarnEvery {
		q.warned, warn = time.Now(), true
	}
	q.mtx.Unlock()
	if warn {
		log.Warnf("data feed %s: a consumer is %d candles behind, eg: stuck: they are kept for it, in their "+
			"order, its memory growing", q.feed, queued)
	}
	q.signal()
}

// close ends the goroutine of the queue once the candles queued are taken.
func (q *candleQueue) close() {
	q.mtx.Lock()
	q.closed = true
	q.mtx.Unlock()
	q.signal()
}

func (q *candleQueue) signal() {
	select {
	case q.ready <- struct{}{}:
	default: // signaled already
	}
}

func (q *candleQueue) run() {
	for range q.ready {
		for {
			q.mtx.Lock()
			if len(q.candles) == 0 {
				q.candles = nil
				closed := q.closed
				q.mtx.Unlock()
				if closed {
					return
				}
				break
			}
			candle := q.candles[0]
			q.candles = q.candles[1:]
			q.mtx.Unlock()
			q.consumer(candle)
		}
	}
}

// sleep waits for d, or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// mid is the middle of a bid and an ask, or the one provided.
func mid(bid, ask float64) float64 {
	switch {
	case bid == 0:
		return ask
	case ask == 0:
		return bid
	}
	return (bid + ask) / 2
}

// samePrices reports whether two candles are the same prices of the same time.
func samePrices(a, b model.Candle) bool {
	return a.Time.Equal(b.Time) && a.Open == b.Open && a.High == b.High && a.Low == b.Low &&
		a.Close == b.Close && a.Volume == b.Volume
}
