package strategy

import (
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/internal/recovery"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
)

// Controller runs a strategy on the candles of a pair. A panic of the
// strategy, eg: an indicator failing on the data of the day, fails the candle
// it was called on only: recovered, logged with its stack, notified (see
// SetNotifier) and returned, for the bot to go on with the next candle live,
// or to stop, eg: in the warmup, where it would come back at every start.
type Controller struct {
	strategy     Strategy
	dataframe    *model.Dataframe
	broker       service.Broker
	started      bool
	warmupPeriod int // Cache warmup period
	panics       *recovery.Reporter
}

func NewStrategyController(pair string, strategy Strategy, broker service.Broker) *Controller {
	warmupPeriod := strategy.WarmupPeriod()
	dataframe := &model.Dataframe{
		Pair:     pair,
		Metadata: make(map[string]model.Series[float64]),
	}

	// Pre-allocate slices with warmup capacity to reduce reallocations
	dataframe.Close = make(model.Series[float64], 0, warmupPeriod+100)
	dataframe.Open = make(model.Series[float64], 0, warmupPeriod+100)
	dataframe.High = make(model.Series[float64], 0, warmupPeriod+100)
	dataframe.Low = make(model.Series[float64], 0, warmupPeriod+100)
	dataframe.Volume = make(model.Series[float64], 0, warmupPeriod+100)
	dataframe.Time = make([]time.Time, 0, warmupPeriod+100)

	return &Controller{
		dataframe:    dataframe,
		strategy:     strategy,
		broker:       broker,
		warmupPeriod: warmupPeriod,
		panics:       recovery.NewReporter(nil),
	}
}

// SetNotifier sets who is notified of the panics of the strategy, see
// Controller.
func (s *Controller) SetNotifier(notifier service.Notifier) {
	if notifier == nil {
		s.panics.SetNotify(nil)
		return
	}
	s.panics.SetNotify(notifier.OnError)
}

func (s *Controller) Start() {
	s.started = true
}

// run calls call of the strategy, named name, on candle, returning its panic
// recovered and reported (see Controller), nil when it returns.
func (s *Controller) run(name string, candle model.Candle, call func()) error {
	recovered := recovery.Call(fmt.Sprintf("%s of the strategy on %s", name, s.dataframe.Pair), candle.Time, call)
	if recovered == nil {
		return nil
	}
	s.panics.Report(recovered, "the strategy skipped the candle")
	return recovered
}

// OnPartialCandle runs a high frequency strategy on candle, a candle in
// progress, returning its panic recovered, see Controller. The strategy is
// handed the candles kept (see trim), candle the last one.
func (s *Controller) OnPartialCandle(candle model.Candle) error {
	if !candle.Complete && len(s.dataframe.Close) >= s.strategy.WarmupPeriod() {
		if str, ok := s.strategy.(HighFrequencyStrategy); ok {
			s.updateDataFrame(candle)
			if err := s.run("Indicators", candle, func() { str.Indicators(s.dataframe) }); err != nil {
				return err
			}
			return s.run("OnPartialCandle", candle, func() { str.OnPartialCandle(s.dataframe, s.broker) })
		}
	}
	return nil
}

func (s *Controller) updateDataFrame(candle model.Candle) {
	if len(s.dataframe.Time) > 0 && candle.Time.Equal(s.dataframe.Time[len(s.dataframe.Time)-1]) {
		last := len(s.dataframe.Time) - 1
		s.dataframe.Close[last] = candle.Close
		s.dataframe.Open[last] = candle.Open
		s.dataframe.High[last] = candle.High
		s.dataframe.Low[last] = candle.Low
		s.dataframe.Volume[last] = candle.Volume
		s.dataframe.Time[last] = candle.Time
		for k, v := range candle.Metadata {
			s.dataframe.Metadata[k][last] = v
		}
	} else {
		s.dataframe.Close = append(s.dataframe.Close, candle.Close)
		s.dataframe.Open = append(s.dataframe.Open, candle.Open)
		s.dataframe.High = append(s.dataframe.High, candle.High)
		s.dataframe.Low = append(s.dataframe.Low, candle.Low)
		s.dataframe.Volume = append(s.dataframe.Volume, candle.Volume)
		s.dataframe.Time = append(s.dataframe.Time, candle.Time)
		s.dataframe.LastUpdate = candle.Time
		for k, v := range candle.Metadata {
			s.dataframe.Metadata[k] = append(s.dataframe.Metadata[k], v)
		}
		s.trim()
	}
}

// warmupsKept are the warmups of candles the dataframe keeps, see trim.
const warmupsKept = 3

// trim drops the oldest candles of the dataframe beyond warmupsKept times the
// warmup and the candle in progress, so the dataframe of a bot running for
// months, and the indicators of a high frequency strategy computed again on
// every tick (see OnPartialCandle), stay that size rather than the whole
// history: OnCandle hands the strategy its warmup only, and the indicators the
// warmup leaves room for (eg: an EMA, an RSI, an ATR) take the same values on
// as many candles as on the whole history, their seed washed out. The series
// are resliced, append copying the candles kept once out of room.
func (s *Controller) trim() {
	df := s.dataframe
	drop := len(df.Time) - (warmupsKept*s.warmupPeriod + 1)
	if drop <= 0 {
		return
	}
	df.Close, df.Open, df.High, df.Low = df.Close[drop:], df.Open[drop:], df.High[drop:], df.Low[drop:]
	df.Volume, df.Time = df.Volume[drop:], df.Time[drop:]
	for key, values := range df.Metadata {
		df.Metadata[key] = values[min(drop, len(values)):] // as long as the candles, unless out of step already
	}
}

// OnCandle runs the strategy on candle, a closed one, its indicators only
// until Start, eg: the candles of the warmup; it returns its panic recovered,
// see Controller.
func (s *Controller) OnCandle(candle model.Candle) error {
	if len(s.dataframe.Time) > 0 && candle.Time.Before(s.dataframe.Time[len(s.dataframe.Time)-1]) {
		log.Errorf("late candle received: %#v", candle)
		return nil
	}

	s.updateDataFrame(candle)

	if len(s.dataframe.Close) >= s.warmupPeriod {
		// Reuse cached sample to avoid repeated allocations
		sample := s.dataframe.Sample(s.warmupPeriod)
		if err := s.run("Indicators", candle, func() { s.strategy.Indicators(&sample) }); err != nil {
			return err
		}
		if s.started {
			return s.run("OnCandle", candle, func() { s.strategy.OnCandle(&sample, s.broker) })
		}
	}
	return nil
}
