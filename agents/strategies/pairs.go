package strategies

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/xhit/go-str2duration/v2"
)

// PairsConfig holds the inputs of Pairs, with its defaults in
// DefaultPairsConfig. The rule of entry and exit is the one of Gatev,
// Goetzmann and Rouwenhorst ("Pairs Trading: Performance of a Relative-Value
// Arbitrage Rule", The Review of Financial Studies 19(3), 2006): a spread is
// opened once the prices of the legs diverge by two historical standard
// deviations, and closed at the next crossing of the prices, which z back to 0
// stands for here. The spread they measure is the difference of the prices of
// the legs normalized at the start of a formation period of a year, its
// deviation over that period kept for the six months of trading after it; the
// spread here, the log of the ratio of the prices or the residual of a
// regression of their logs (see Hedge), and its Z-Score over a window rolling
// on every candle (see Len), are ours. Periods are counted in candles of
// Timeframe that both legs have: the candles of one leg alone (a gap of its
// feed, a market closed) are left out of every count.
type PairsConfig struct {
	Timeframe string

	// Pairs are the two legs, in the order of -pair: leg A, then leg B. Any
	// other number of pairs is refused. Empty when the pairs aren't known at
	// the creation of the strategy: the legs are then the first two pairs
	// whose candles come, A the first of them in alphabetical order, and a
	// third pair stops the entries on its first candle (the spread held, if
	// any, still closed by the exits).
	Pairs []string

	// Hedge is how the spread is measured on each candle both legs have, and
	// how the legs are weighed, so that the position held is the spread
	// measured: a value V long in A and β·V short in B gains V·(rA − β·rB) on
	// returns rA and rB of the legs, V times the move of s = ln A − β ln B.
	//
	//   - PairsHedgeRatio, the default: β = 1, s = ln A − ln B, the log of the
	//     ratio of the prices, and the two legs of equal value, "one dollar
	//     long and one dollar short" as Gatev et al. open them: each leg by its
	//     PositionSize (-lot), which must then balance the values of the legs,
	//     or each worth half of PositionFraction, see BracketConfig below.
	//   - PairsHedgeOLS: β the slope of the OLS regression of ln A on ln B over
	//     the window, s = ln A − β ln B the residual of the cointegrating
	//     regression of Engle and Granger ("Co-Integration and Error
	//     Correction", Econometrica 55(2), 1987), the spread Vidyamurthy
	//     ("Pairs Trading: Quantitative Methods and Analysis", 2004) trades,
	//     and leg B worth β times leg A: by value, the two legs share
	//     PositionFraction, leg A 1/(1+β) of it and leg B β/(1+β); with
	//     PositionSize, leg A takes its size and leg B β times the value of
	//     leg A, rounded to the nearest multiple of its own PositionSize, the
	//     step of leg B (its volume step, eg: 0.01 lot, for the closest hedge:
	//     the runner checks that the size of a pair is a multiple of its volume
	//     step). An entry is skipped while β isn't positive (the legs would go
	//     the same way, not a spread) or leg B rounds to nothing.
	Hedge string

	// Len is the window of the spread: the mean and the standard deviation
	// (the population one, as ta.stdev) of s over the last Len candles both
	// legs have, its Z-Score z = (s − mean) / deviation, and, with
	// PairsHedgeOLS, β, estimated over the same candles. The candle's own
	// close is in the window, nothing after it. The window rolls, its
	// estimates made again on every candle (ours: Gatev et al. keep the ones
	// of a formation period for the trading period after it).
	Len int

	// The entries, both legs at the market, A first: a long spread (buy A,
	// sell B) on the candle z falls to -EntryZ or under from above it on the
	// candle before; a short spread (sell A, buy B) on the candle z rises to
	// EntryZ or over from under it: the two historical standard deviations of
	// Gatev et al., on the Z-Score of the rolling spread. A divergence beyond
	// StopZ already, when set, isn't entered: the stop would close it.
	EntryZ float64

	// The exits, both legs closed at the market, A first: once z reverts to
	// ExitZ from its side (-ExitZ for a long spread, ExitZ for a short one; 0
	// the mean of the window, the analog here of the next crossing of the
	// prices of Gatev et al., where their spread is back at 0), after
	// MaxHoldBars candles (0 never; the end of the trading period in Gatev et
	// al.), or once |z| reaches StopZ (0 off: Gatev et al. place no stop), the
	// divergence widening instead.
	ExitZ       float64
	MaxHoldBars int
	StopZ       float64

	// The sizing, see BracketConfig and Hedge: with PositionSize (-lot), the
	// size of each leg, leg B's computed from β with PairsHedgeOLS; without
	// it, the two legs share PositionFraction of the free balance (times the
	// Leverage), which must then be under 1: the costs of leg A would leave
	// leg B short of the balance otherwise. Direction restricts the side of
	// leg A: buy, the long spreads only; sell, the short ones. Its stop loss,
	// take profit, ATR and max bars aren't read: nothing but the market
	// orders of the legs goes to the broker, so the fixed stop loss and take
	// profit (StopValue, TakeProfitValue), the max risk at the stop loss
	// (MaxRiskPct) and the ATR filter (MinATRPct) are refused rather than
	// ignored.
	BracketConfig
}

// The hedges of PairsConfig.Hedge.
const (
	PairsHedgeRatio = "ratio" // the log of the ratio of the prices, the legs of equal value
	PairsHedgeOLS   = "ols"   // the residual of the OLS regression of ln A on ln B, leg B worth β times leg A
)

// DefaultPairsConfig returns the defaults of Pairs, on the 1h timeframe.
func DefaultPairsConfig() PairsConfig {
	exits := DefaultBracketConfig()
	exits.PositionFraction = 0.5 // a quarter of the free balance in each leg of equal value

	return PairsConfig{
		Timeframe: "1h",

		Hedge: PairsHedgeRatio,

		Len: 250,

		EntryZ: 2,

		ExitZ:       0,
		MaxHoldBars: 250,
		StopZ:       0,

		BracketConfig: exits,
	}
}

func (c PairsConfig) validate() error {
	exits := c.BracketConfig
	exits.StopATR, exits.TakeProfitATR = 1, 1 // unused: the exits on the Z-Score in their place
	if err := exits.validate(); err != nil {
		return err
	}

	tf, err := str2duration.ParseDuration(c.Timeframe)
	if err != nil || tf <= 0 {
		return fmt.Errorf("invalid timeframe %q", c.Timeframe)
	}
	switch {
	case c.Hedge != PairsHedgeRatio && c.Hedge != PairsHedgeOLS:
		return fmt.Errorf("the hedge must be %s or %s, not %q", PairsHedgeRatio, PairsHedgeOLS, c.Hedge)
	case c.Len < 3:
		return errors.New("the window of the spread must be at least 3 candles")
	case c.EntryZ <= 0:
		return errors.New("the entry Z-Score must be positive")
	case c.EntryZ >= math.Sqrt(float64(c.Len-1)):
		// the deviations of s from its mean summing to 0, the last of n is
		// never more than √(n-1) deviations away
		return fmt.Errorf("the entry Z-Score must be under %.4g, the most a Z-Score of %d candles reaches",
			math.Sqrt(float64(c.Len-1)), c.Len)
	case c.ExitZ <= -c.EntryZ || c.ExitZ >= c.EntryZ:
		return errors.New("the exit Z-Score must be between -EntryZ and EntryZ")
	case c.StopZ < 0 || c.StopZ > 0 && c.StopZ <= c.EntryZ:
		return errors.New("the stop Z-Score must be 0 (off) or above the entry Z-Score")
	case c.MaxHoldBars < 0:
		return errors.New("max bars can't be negative")
	case c.StopValue > 0 || c.TakeProfitValue > 0:
		return errors.New("pairs places no stop loss nor take profit at the broker: its exits are on the Z-Score " +
			"of the spread")
	case c.MaxRiskPct > 0:
		return errors.New("pairs places no stop loss: the max risk of an entry at its stop loss can't apply")
	case c.MinATRPct > 0:
		return errors.New("pairs reads no ATR: the min ATR % can't apply")
	case len(c.PositionSize) == 0 && c.PositionFraction >= 1:
		return errors.New("without a size per leg, the two legs share the position fraction, which must be " +
			"below 1: the costs of the first leg would leave the second one short of the balance")
	}
	return c.checkPairs()
}

// checkPairs checks the legs, when known.
func (c PairsConfig) checkPairs() error {
	if len(c.Pairs) == 0 {
		return nil // learned from the candles, see Pairs
	}
	if len(c.Pairs) != 2 {
		return fmt.Errorf("pairs trades two legs, leg A and leg B, not %d pairs: %s", len(c.Pairs),
			strings.Join(c.Pairs, ";"))
	}
	a, b := c.Pairs[0], c.Pairs[1]
	switch {
	case a == "" || b == "":
		return errors.New("a leg without a pair")
	case a == b:
		return fmt.Errorf("the two legs are the same pair, %s", a)
	}
	return nil
}

// pairsMinDeviation is the deviation of the residuals under which the spread
// is taken for flat: a Z-Score over a deviation of rounding errors is noise.
const pairsMinDeviation = 1e-9

// pairsWarmup returns the candles of each leg the strategy is handed: the
// window of the spread, the candle before it for the cross of the entries, and
// a tenth more for the candles one leg has and the other doesn't, left out of
// the window.
func pairsWarmup(length int) int {
	return length + 1 + (length+9)/10
}

// Pairs trades the spread of two cointegrated legs back to its mean: it buys
// leg A and sells leg B when the Z-Score of the spread falls to -EntryZ (A
// cheap against B), sells A and buys B when it rises to EntryZ, and closes both
// legs once the Z-Score reverts, after MaxHoldBars candles, or at StopZ. See
// PairsConfig.
//
// One strategy serves the two legs, called with the candles of each in turn:
// it keeps the closes of each leg as its last candles were handed, and decides
// on the candle of a time once both legs have it as their last one, whatever
// the order they come in (in a backtest, the alphabetical one; live, the order
// of their polls). A time one leg doesn't have is skipped, and a candle of the
// other leg already past it is never decided on: the orders go at the prices
// of now. Entries and exits place leg A then leg B; a leg B that fails closes
// leg A at once, and on every candle a leg held alone (closed by a stop, a
// limit or a hand meanwhile, or a close that failed) is closed, so the
// position is the spread or nothing.
//
// The protection of the agent (breakeven and trailing) must stay off, and no
// stop loss or take profit be put on a leg by hand: they act on each leg on
// its own, breaking the hedge, the other leg then closed as held alone. The
// spread held is memory only: after a restart, the legs found held on
// opposite sides are followed as the spread again, its max bars counted from
// the opening of leg A the broker tells.
type Pairs struct {
	cfg    PairsConfig
	period time.Duration
	warmup int
	sizing rkSizing
	ols    bool // β the OLS slope of ln A on ln B, else 1, see PairsConfig.Hedge

	// guarded by mu, OnCandle writing them and Indicators, called by the
	// chart, reading them: the closes of each leg, by pair, replaced and
	// never changed in place, and the legs, A then B, known or learned
	mu    sync.RWMutex
	legs  map[string]pairsLeg
	names [2]string
	seen  []string // the pairs whose candles came, when the legs aren't known

	// read and written by OnCandle only, which the bot calls in turn
	refused string    // why no entry is taken any more, eg: a third pair
	decided time.Time // the last time decided on
	trade   *pairsTrade
	logged  map[string]bool // the warnings told once
}

// pairsLeg holds the closes of the last candles of a leg, by time.
type pairsLeg struct {
	times  []time.Time
	closes []float64
}

// last returns the time of the last candle of the leg, zero for none.
func (l pairsLeg) last() time.Time {
	if len(l.times) == 0 {
		return time.Time{}
	}
	return l.times[len(l.times)-1]
}

// pairsTrade is the spread held: the side of leg A (BUY a long spread), the
// candle of its entry, whose close decided it, and whether its exit was
// decided, retried on each candle while it fails.
type pairsTrade struct {
	side    model.SideType
	entry   time.Time
	exiting bool
}

// NewPairs validates the configuration and creates the strategy.
func NewPairs(cfg PairsConfig) (*Pairs, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("pairs: %w", err)
	}

	period, _ := str2duration.ParseDuration(cfg.Timeframe) // validated
	s := &Pairs{
		cfg:    cfg,
		period: period,
		warmup: pairsWarmup(cfg.Len),
		ols:    cfg.Hedge == PairsHedgeOLS,
		sizing: rkSizing{sizes: cfg.PositionSize, fraction: cfg.PositionFraction, leverage: cfg.Leverage,
			minValue: cfg.MinPositionValue},
		legs:   make(map[string]pairsLeg),
		logged: make(map[string]bool),
	}
	if len(cfg.Pairs) == 2 {
		s.names = [2]string{cfg.Pairs[0], cfg.Pairs[1]}
	}
	return s, nil
}

func (s *Pairs) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *Pairs) WarmupPeriod() int {
	return s.warmup
}

// Indicators draws the Z-Score of the spread on the candles of df that the
// other leg has too, as the strategy reads it on each of them: from the
// candles up to it only. The other leg holds the last candles handed to the
// strategy, so the Z-Score covers those, and none on candles of another
// timeframe than the strategy's (eg: the chart on another one).
func (s *Pairs) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	z := make(model.Series[float64], len(df.Close))
	for i := range z {
		z[i] = na
	}

	if other, isA, ok := s.otherLeg(df.Pair); ok && len(df.Time) == len(df.Close) && s.spaced(df.Time) {
		this := pairsLeg{times: df.Time, closes: df.Close}
		a, b := this, other
		if !isA {
			a, b = other, this
		}
		joined := pairsJoin(a, b)
		index := joined.ia
		if !isA {
			index = joined.ib
		}
		n := s.cfg.Len
		for k := n - 1; k < len(joined.times); k++ {
			if value, _, ok := pairsZ(joined.lnA[k-n+1:k+1], joined.lnB[k-n+1:k+1], s.ols); ok {
				z[index[k]] = value
			}
		}
	}
	df.Metadata["pr_z"] = z

	return []strategy.ChartIndicator{
		{
			Overlay:   false,
			GroupName: "Pairs Z-Score",
			Time:      df.Time,
			Metrics: []strategy.IndicatorMetric{
				{
					Values: z,
					Name:   "Z",
					Color:  "#8e44ad",
					Style:  strategy.StyleLine,
				},
			},
		},
	}
}

// otherLeg returns the closes of the leg other than pair, and whether pair is
// leg A: false when pair isn't a leg, or the other one isn't known yet.
func (s *Pairs) otherLeg(pair string) (other pairsLeg, isA, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.names[1] == "" {
		return pairsLeg{}, false, false
	}
	switch pair {
	case s.names[0]:
		other, ok = s.legs[s.names[1]]
		return other, true, ok
	case s.names[1]:
		other, ok = s.legs[s.names[0]]
		return other, false, ok
	}
	return pairsLeg{}, false, false
}

// spaced reports whether times are candles of the timeframe of the strategy:
// the shortest step between the last of them, gaps aside.
func (s *Pairs) spaced(times []time.Time) bool {
	step := time.Duration(0)
	for i := max(len(times)-20, 1); i < len(times); i++ {
		if d := times[i].Sub(times[i-1]); d > 0 && (step == 0 || d < step) {
			step = d
		}
	}
	return step == s.period
}

func (s *Pairs) OnCandle(df *model.Dataframe, broker service.Broker) {
	n := len(df.Close)
	if n == 0 || len(df.Time) != n {
		return
	}
	names, a, b, ok := s.record(df)
	if !ok {
		return
	}
	pair := names[0] + ";" + names[1]
	if len(a.closes) == 0 || len(b.closes) == 0 {
		missing := names[0]
		if len(a.closes) > 0 {
			missing = names[1]
		}
		s.once("leg", "[PAIRS] %s %s | waiting for the candles of %s, the other leg", pair, s.cfg.Timeframe,
			missing)
		return
	}

	// the positions of the legs, valued at their last closes, on every
	// candle of either leg
	priceA, priceB := a.closes[len(a.closes)-1], b.closes[len(b.closes)-1]
	sideA, errA := s.side(broker, names[0], priceA)
	sideB, errB := s.side(broker, names[1], priceB)
	if err := errors.Join(errA, errB); err != nil {
		log.Error(err)
		return
	}
	at := df.Time[n-1]
	if !s.reconcile(broker, names, sideA, sideB, priceA, priceB, at) {
		return
	}

	// the decision of a time, once both legs have its candle as their last
	// one: the first of the two to come waits for the other
	if !a.last().Equal(at) || !b.last().Equal(at) || !at.After(s.decided) {
		return
	}
	s.decided = at
	joined := pairsJoin(a, b)
	z, prev, beta, ready := s.spread(joined)
	if s.trade != nil {
		s.exit(broker, names, priceA, priceB, joined.times, z, ready)
		return
	}
	if !ready {
		s.once("ready", "[PAIRS] %s %s | waiting for %d candles of both legs, %d so far", pair, s.cfg.Timeframe,
			s.cfg.Len+1, len(joined.times))
		return
	}
	s.enter(broker, names, priceA, priceB, z, prev, beta, at)
}

// record keeps the closes of the candles of df, if its pair is a leg, and
// returns the legs, A then B, with the closes of each, false when the pair
// isn't a leg or the other leg isn't known yet.
func (s *Pairs) record(df *model.Dataframe) (names [2]string, a, b pairsLeg, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isLeg(df.Pair) {
		return names, a, b, false
	}
	s.legs[df.Pair] = pairsLeg{times: slices.Clone(df.Time), closes: slices.Clone(df.Close)}
	if s.names[1] == "" {
		s.once("leg", "[PAIRS] %s %s | waiting for the candles of the other leg", df.Pair, s.cfg.Timeframe)
		return names, a, b, false
	}
	return s.names, s.legs[s.names[0]], s.legs[s.names[1]], true
}

// isLeg reports whether pair is a leg: one of the configuration, or of the
// first two pairs whose candles came, learning them when they aren't known,
// see PairsConfig.Pairs. Must be called while holding s.mu.
func (s *Pairs) isLeg(pair string) bool {
	if len(s.cfg.Pairs) > 0 {
		if pair == s.names[0] || pair == s.names[1] {
			return true
		}
		s.once("other:"+pair, "[PAIRS] %s isn't a leg of the strategy (%s;%s): its candles are left out", pair,
			s.names[0], s.names[1])
		return false
	}

	if slices.Contains(s.seen, pair) {
		return pair == s.names[0] || pair == s.names[1] || s.names[1] == ""
	}
	s.seen = append(s.seen, pair)
	switch len(s.seen) {
	case 1:
		return true
	case 2:
		legs := slices.Clone(s.seen)
		slices.Sort(legs)
		s.names = [2]string{legs[0], legs[1]}
		log.Infof("[PAIRS] the legs: %s (A) and %s (B)", s.names[0], s.names[1])
		return true
	}
	if s.refused == "" {
		s.refused = fmt.Sprintf("pairs trades two legs, the agent trades more pairs (%s)", strings.Join(s.seen, ";"))
		log.Errorf("[PAIRS] %s: no entry any more", s.refused)
	}
	return false
}

// once logs a warning once per key.
func (s *Pairs) once(key, format string, args ...any) {
	if s.logged[key] {
		return
	}
	s.logged[key] = true
	log.Warnf(format, args...)
}

// side returns the side of the position held on pair, valued at price: none
// for dust.
func (s *Pairs) side(broker service.Broker, pair string, price float64) (model.SideType, error) {
	asset, _, err := broker.Position(pair)
	if err != nil {
		return "", err
	}
	return rkHeld(rkWorth(broker, pair, asset, price), s.cfg.MinPositionValue), nil
}

// reconcile follows the positions of the legs held on sideA and sideB, and
// reports whether they are a position of the strategy: both flat, or A held
// on one side and B on the other, the spread followed then (adopted when it
// isn't, eg: opened before a restart). Otherwise, it closes the legs held: a
// leg held alone, or both legs held on the same side, isn't a spread. A
// spread whose exit failed is closed again.
func (s *Pairs) reconcile(broker service.Broker, names [2]string, sideA, sideB model.SideType, priceA,
	priceB float64, at time.Time) bool {

	pair := names[0] + ";" + names[1]
	switch {
	case sideA == "" && sideB == "":
		if s.trade != nil {
			log.Infof("[PAIRS] %s | the spread was closed by someone else", pair)
			s.trade = nil
		}
		return true
	case sideA != "" && sideB != "" && sideA != sideB:
		if s.trade == nil || s.trade.side != sideA {
			s.trade = s.adopt(broker, names, sideA, at)
		}
		if !s.trade.exiting {
			return true
		}
		log.Infof("[PAIRS] %s %s | RETRY", pair, s.cfg.Timeframe)
		if s.closeSpread(broker, names, sideA, priceA, priceB) {
			s.trade = nil
		}
		return false
	case sideA == sideB:
		log.Errorf("[PAIRS] %s | both legs held %s, not a spread: both closed", pair, sideA)
	default:
		lone := names[0]
		if sideA == "" {
			lone = names[1]
		}
		log.Warnf("[PAIRS] %s | %s held alone, the other leg gone: closed", pair, lone)
	}

	s.trade = nil
	if sideA != "" {
		s.closeLeg(broker, names[0], sideA, priceA)
	}
	if sideB != "" {
		s.closeLeg(broker, names[1], sideB, priceB)
	}
	return false
}

// adopt returns the trade of a spread held, leg A on sideA, that the strategy
// doesn't follow, eg: opened before a restart: its entry the candle whose close
// opened leg A, when the broker tells the opening (service.PositionLegs), a
// candle early at most where it stamps the fills with the open of their
// candle (the paper wallet); else at, the candle it is found on, the max bars
// counted from there.
func (s *Pairs) adopt(broker service.Broker, names [2]string, sideA model.SideType, at time.Time) *pairsTrade {
	trade := &pairsTrade{side: sideA, entry: at}
	since := "unknown"
	if finder, ok := broker.(service.PositionLegs); ok {
		if _, opened, err := finder.HeldLegs(names[0], sideA); err != nil {
			log.Warnf("[PAIRS] %s | the opening of the position found: %v", names[0], err)
		} else if !opened.IsZero() {
			trade.entry = opened.Truncate(s.period).Add(-s.period)
			since = opened.UTC().Format(time.RFC3339)
		}
	}
	log.Infof("[PAIRS] %s;%s | the %s spread found followed, opened %s", names[0], names[1], pairsSpread(sideA),
		since)
	return trade
}

// spread returns the Z-Score of the spread on the last candle of joined and on
// the one before, and the hedge ratio β of the last (1 for PairsHedgeRatio);
// ready is false without the candles of both windows, or with a flat spread.
func (s *Pairs) spread(joined pairsJoined) (z, prev, beta float64, ready bool) {
	n, length := len(joined.times), s.cfg.Len
	if n < length+1 {
		return na, na, na, false
	}
	z, beta, ok := pairsZ(joined.lnA[n-length:], joined.lnB[n-length:], s.ols)
	prev, _, okPrev := pairsZ(joined.lnA[n-length-1:n-1], joined.lnB[n-length-1:n-1], s.ols)
	return z, prev, beta, ok && okPrev
}

// exit closes the spread held once its Z-Score reverts to ExitZ, reaches StopZ
// or after MaxHoldBars candles, see PairsConfig: the Z-Score not ready, the max
// bars only. times are the candles both legs have, the last one now.
func (s *Pairs) exit(broker service.Broker, names [2]string, priceA, priceB float64, times []time.Time, z float64,
	ready bool) {

	trade := s.trade
	bars := s.barsSince(times, trade.entry)
	reason := ""
	switch {
	case ready && rkSign(trade.side)*z >= -s.cfg.ExitZ:
		reason = "MEAN"
	case ready && s.cfg.StopZ > 0 && math.Abs(z) >= s.cfg.StopZ:
		reason = "STOP"
	case s.cfg.MaxHoldBars > 0 && bars >= s.cfg.MaxHoldBars:
		reason = "TIME"
	default:
		return
	}

	log.Infof("[PAIRS] %s;%s %s | %s | the %s spread closed | z %.3f | %d candles | %s %f | %s %f", names[0],
		names[1], s.cfg.Timeframe, reason, pairsSpread(trade.side), z, bars, names[0], priceA, names[1], priceB)
	if s.closeSpread(broker, names, trade.side, priceA, priceB) {
		s.trade = nil
		return
	}
	trade.exiting = true // retried on the next candles, see reconcile
}

// barsSince returns the candles both legs closed after the candle entry, up to
// the last of times included: counted on times, and by the time for the ones
// before them.
func (s *Pairs) barsSince(times []time.Time, entry time.Time) int {
	first := sort.Search(len(times), func(i int) bool { return times[i].After(entry) })
	bars := len(times) - first
	if first == 0 && len(times) > 0 {
		if gap := times[0].Sub(entry); gap > s.period {
			bars += int(gap/s.period) - 1
		}
	}
	return bars
}

// enter opens the spread on the cross of the Z-Score, z now and prev on the
// candle before, beta the hedge ratio now, see PairsConfig: leg A then leg B
// at the market, leg A closed at once when leg B isn't held after its order.
func (s *Pairs) enter(broker service.Broker, names [2]string, priceA, priceB, z, prev, beta float64,
	at time.Time) {

	var sideA model.SideType
	switch {
	case z <= -s.cfg.EntryZ && prev > -s.cfg.EntryZ:
		sideA = model.SideTypeBuy
	case z >= s.cfg.EntryZ && prev < s.cfg.EntryZ:
		sideA = model.SideTypeSell
	default:
		return
	}
	pair := names[0] + ";" + names[1]
	switch {
	case s.refused != "":
		log.Infof("[PAIRS] %s | the %s spread at z %.3f skipped: %s", pair, pairsSpread(sideA), z, s.refused)
		return
	case !rkOpens(s.cfg.Direction, sideA):
		return
	case s.cfg.StopZ > 0 && math.Abs(z) >= s.cfg.StopZ:
		log.Infof("[PAIRS] %s | the %s spread at z %.3f skipped: beyond the stop Z-Score of %g already", pair,
			pairsSpread(sideA), z, s.cfg.StopZ)
		return
	case !(beta > 0):
		log.Infof("[PAIRS] %s | the %s spread at z %.3f skipped: a hedge ratio of %.4f would hold both legs on "+
			"the same side, not a spread", pair, pairsSpread(sideA), z, beta)
		return
	}

	sizeA, valueA, sizeB, valueB, ok := s.sizes(broker, names, priceA, priceB, beta)
	if !ok {
		return
	}
	sideB := oppositeSide(sideA)
	log.Infof("[PAIRS] %s %s | the %s spread | z %.3f | beta %.4f | %s %s %g at %f | %s %s %g at %f", pair,
		s.cfg.Timeframe, pairsSpread(sideA), z, beta, sideA, names[0], sizeA, priceA, sideB, names[1], sizeB, priceB)

	// an order reported as failed may have been filled even so, eg: a request
	// that timed out, so the position held is read
	errA := s.sizing.open(broker, sideA, names[0], sizeA, valueA)
	if held, err := s.side(broker, names[0], priceA); err != nil || held != sideA {
		log.Errorf("[PAIRS] %s | leg A not opened: %v", names[0], errors.Join(errA, err))
		return
	}
	errB := s.sizing.open(broker, sideB, names[1], sizeB, valueB)
	if held, err := s.side(broker, names[1], priceB); err != nil || held != sideB {
		log.Errorf("[PAIRS] %s | leg B not opened: %v: leg A closed at once", names[1], errors.Join(errB, err))
		s.closeLeg(broker, names[0], sideA, priceA)
		return
	}
	if err := errors.Join(errA, errB); err != nil {
		log.Warnf("[PAIRS] %s | both legs held, an order told an error: %v", pair, err)
	}
	s.trade = &pairsTrade{side: sideA, entry: at}
}

// sizes returns the size of each leg and its value, in the currency of the
// cash, see rkSizing.position and PairsConfig.Hedge, beta the hedge ratio (1
// for PairsHedgeRatio), positive: with the sizes of PositionSize, leg B's the
// multiple of its own nearest to beta times the value of leg A with
// PairsHedgeOLS; else PositionFraction of the free balance shared, leg A
// 1/(1+beta) of it and leg B beta/(1+beta), half each for PairsHedgeRatio. ok
// is false when either leg has none, or both together are worth more than the
// free balance times the leverage.
func (s *Pairs) sizes(broker service.Broker, names [2]string, priceA, priceB, beta float64) (sizeA, valueA,
	sizeB, valueB float64, ok bool) {

	cash, err := rkCashOf(broker, names[0])
	if err != nil {
		log.Error(err)
		return 0, 0, 0, 0, false
	}
	pointB, err := rkPointValue(broker, names[1])
	if err != nil {
		log.Error(err)
		return 0, 0, 0, 0, false
	}
	cashA, cashB := cash, rkCash{free: cash.free, point: pointB}
	if len(s.cfg.PositionSize) == 0 {
		// the legs share the fraction, leg B worth beta times leg A
		cashA.free, cashB.free = cash.free/(1+beta), cash.free*beta/(1+beta)
	}

	sizeA, valueA, okA := s.sizing.position(names[0], cashA, priceA)
	var okB bool
	if s.ols && len(s.cfg.PositionSize) > 0 && okA {
		sizeB, valueB, okB = s.hedge(names[1], cashB, priceB, beta*valueA)
	} else {
		sizeB, valueB, okB = s.sizing.position(names[1], cashB, priceB)
	}
	switch {
	case !okA || !okB:
		return 0, 0, 0, 0, false
	case valueA+valueB > cash.free*s.cfg.Leverage:
		log.Errorf("[PAIRS] %s;%s | the legs are worth %f together, more than the %f available times the "+
			"leverage of %g: skipped", names[0], names[1], valueA+valueB, cash.free, s.cfg.Leverage)
		return 0, 0, 0, 0, false
	}
	return sizeA, valueA, sizeB, valueB, true
}

// hedge returns the size of leg B, pair, worth about value at price, and its
// value, in the currency of the cash, see PairsConfig.Hedge: the multiple of
// its PositionSize, the step of the leg, nearest to value; ok false without
// its PositionSize, or when that rounds to nothing, or to less than the min
// position value, taken for dust.
func (s *Pairs) hedge(pair string, cash rkCash, price, value float64) (size, worth float64, ok bool) {
	step, ok := s.cfg.PositionSize[pair]
	if !ok {
		log.Errorf("%s: no position size set for the pair, the step of its leg sized by the hedge ratio", pair)
		return 0, 0, false
	}
	steps := math.Round(value / (price * cash.point * step))
	size = steps * step
	worth = size * price * cash.point
	switch {
	case steps < 1:
		log.Infof("%s: β times the value of leg A, %f, rounds to no position in steps of %g: skipped", pair, value,
			step)
	case worth < s.cfg.MinPositionValue:
		log.Errorf("%s: a position of %g is worth %f, under the min position value of %g: it would be taken "+
			"for dust", pair, size, worth, s.cfg.MinPositionValue)
	default:
		return size, worth, true
	}
	return 0, 0, false
}

// closeSpread closes both legs of the spread, leg A on sideA, at the market, A
// first, and reports whether both are flat now.
func (s *Pairs) closeSpread(broker service.Broker, names [2]string, sideA model.SideType, priceA,
	priceB float64) bool {

	flatA := s.closeLeg(broker, names[0], sideA, priceA)
	flatB := s.closeLeg(broker, names[1], oppositeSide(sideA), priceB)
	return flatA && flatB
}

// closeLeg closes the position held on side of pair at the market, valued at
// price, see rkCloseProtected, and reports whether the pair is flat now.
func (s *Pairs) closeLeg(broker service.Broker, pair string, side model.SideType, price float64) bool {
	df := &model.Dataframe{Pair: pair, Close: model.Series[float64]{price}}
	flat, _, _ := rkCloseProtected(df, broker, "PAIRS", side, s.cfg.MinPositionValue, nil, rkLevels{})
	return flat
}

// pairsSpread names the spread of leg A held on side.
func pairsSpread(side model.SideType) string {
	if side == model.SideTypeSell {
		return "short"
	}
	return "long"
}

// pairsJoined holds the candles two legs both have, in time: their times, the
// index of each in the closes of leg A (ia) and of leg B (ib), and the log of
// the closes of each leg.
type pairsJoined struct {
	times    []time.Time
	ia, ib   []int
	lnA, lnB []float64
}

// pairsJoin joins the closes of legs a and b by the time of their candles,
// both in time, leaving out the candles of one leg alone, and the ones without
// a positive close, which has no log.
func pairsJoin(a, b pairsLeg) pairsJoined {
	var joined pairsJoined
	for i, j := 0, 0; i < len(a.times) && j < len(b.times); {
		switch ta, tb := a.times[i], b.times[j]; {
		case ta.Before(tb):
			i++
		case tb.Before(ta):
			j++
		default:
			if a.closes[i] > 0 && b.closes[j] > 0 {
				joined.times = append(joined.times, ta)
				joined.ia, joined.ib = append(joined.ia, i), append(joined.ib, j)
				joined.lnA = append(joined.lnA, math.Log(a.closes[i]))
				joined.lnB = append(joined.lnB, math.Log(b.closes[j]))
			}
			i++
			j++
		}
	}
	return joined
}

// pairsZ returns the Z-Score of the last spread of the window, lnA and lnB the
// log closes of the legs on the same candles, and the hedge ratio β, see
// PairsConfig.Hedge: 1, the log of the ratio of the prices, or, ols, the slope
// of the OLS regression of lnA on lnB over the window; the spread s = lnA −
// β·lnB, and z = (s − mean(s)) / stdev(s) over the window, the population
// deviation. ok is false with fewer than 3 candles, lnB flat for the
// regression, or a flat spread (see pairsMinDeviation).
func pairsZ(lnA, lnB []float64, ols bool) (z, beta float64, ok bool) {
	n := len(lnA)
	if n < 3 || len(lnB) != n {
		return na, na, false
	}
	var meanA, meanB float64
	for i := range n {
		meanA += lnA[i]
		meanB += lnB[i]
	}
	meanA /= float64(n)
	meanB /= float64(n)

	beta = 1
	if ols {
		var sxx, sxy float64
		for i := range n {
			dx := lnB[i] - meanB
			sxx += dx * dx
			sxy += dx * (lnA[i] - meanA)
		}
		if !(sxx > 0) {
			return na, na, false
		}
		beta = sxy / sxx
	}

	// mean(s) = meanA − β·meanB, s − mean(s) the residuals of the regression
	// with ols
	var squares float64
	for i := range n {
		r := lnA[i] - meanA - beta*(lnB[i]-meanB)
		squares += r * r
	}
	deviation := math.Sqrt(squares / float64(n))
	if !(deviation > pairsMinDeviation) {
		return na, beta, false
	}
	return (lnA[n-1] - meanA - beta*(lnB[n-1]-meanB)) / deviation, beta, true
}
