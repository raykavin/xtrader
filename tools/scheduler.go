package tools

import (
	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/service"
	log "github.com/sirupsen/logrus"
)

// MaxOrderAttempts is how many times a Scheduler sends the order of a
// condition met, once per Update while it fails: past it, the condition is
// dropped, an error logged, never sent again.
const MaxOrderAttempts = 3

// OrderCondition is an order at the market a Scheduler sends once Condition
// is met.
//
// Deprecated: see Scheduler.
type OrderCondition struct {
	Condition func(df *xtrader.Dataframe) bool
	Size      float64
	Side      xtrader.SideType

	attempts int // the orders sent that failed
}

// Scheduler sends an order at the market once a condition is met on a
// candle, the condition dropped once sent, or after MaxOrderAttempts failures.
//
// Deprecated: no agent uses it, only the unregistered strategy of
// agents/strategies.NewTrailing. Kept until its removal is decided.
type Scheduler struct {
	pair            string
	orderConditions []OrderCondition
}

// NewScheduler returns a scheduler of the orders of pair.
//
// Deprecated: see Scheduler.
func NewScheduler(pair string) *Scheduler {
	return &Scheduler{pair: pair}
}

func (s *Scheduler) SellWhen(size float64, condition func(df *xtrader.Dataframe) bool) {
	s.orderConditions = append(
		s.orderConditions,
		OrderCondition{Condition: condition, Size: size, Side: xtrader.SideTypeSell},
	)
}

func (s *Scheduler) BuyWhen(size float64, condition func(df *xtrader.Dataframe) bool) {
	s.orderConditions = append(
		s.orderConditions,
		OrderCondition{Condition: condition, Size: size, Side: xtrader.SideTypeBuy},
	)
}

// Update sends the orders of the conditions met on df. One that fails is sent
// again on the next Update its condition is met, MaxOrderAttempts times at
// most, eg: refused by the broker for good.
func (s *Scheduler) Update(df *xtrader.Dataframe, broker service.Broker) {
	kept := s.orderConditions[:0]
	for _, oc := range s.orderConditions {
		if !oc.Condition(df) {
			kept = append(kept, oc)
			continue
		}
		_, err := broker.CreateOrderMarket(oc.Side, s.pair, oc.Size)
		if err == nil {
			continue
		}
		oc.attempts++
		if oc.attempts >= MaxOrderAttempts {
			log.Errorf("scheduler %s: the order %s %g failed %d times, dropped: %v",
				s.pair, oc.Side, oc.Size, oc.attempts, err)
			continue
		}
		log.Warnf("scheduler %s: the order %s %g failed (%d of %d attempts), sent again on the next candle: %v",
			s.pair, oc.Side, oc.Size, oc.attempts, MaxOrderAttempts, err)
		kept = append(kept, oc)
	}
	clear(s.orderConditions[len(kept):])
	s.orderConditions = kept
}
