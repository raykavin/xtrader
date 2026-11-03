package backtest

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/internal/protect"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

// limitZone is the zone of the days of the daily limits, as the controller
// and the LLM count them.
const limitZone = "America/Belem"

// guard runs on every candle of a backtest, once the strategy ran (see
// xtrader.CandleHook): the protection of the agent; the entries held before
// the trading period (the warm-up); the daily limits, the positions closed
// and the entries blocked until the next day once one is reached; the trading
// hours, the entries blocked outside their window and, at its end, the
// positions closed and the pending entries canceled as checked; and the stop
// out, everything closed and blocked for good once the equity reaches 0.
type guard struct {
	cfg        Config
	controller *order.Controller
	storage    storage.Storage
	wallet     *wallet
	feed       *exchange.BacktestFeed
	protection *protect.Manager // nil without one
	zone       *time.Location

	mu         sync.Mutex
	hold       func() // releases the hold of the warm-up, nil once released
	day        string
	dayStart   float64
	blockedDay string // the day the daily limit blocked, empty for none
	revision   int64
	stoppedOut bool
	blocked    bool // the entries blocked, by the daily limit, the hours or the stop out
	// the trading hours: their windows (see agentapi.HoursWindows), outside
	// them now, the window open (-1 for none) and seen open since the start,
	// and the window whose end to act on at the close of the candle
	hoursWindows []ui.TradingWindow
	hoursClosed  bool
	hoursAt      int
	hoursOpened  bool
	hoursEnd     *ui.TradingWindow
	own          map[int64]string // the reasons of the orders of the guard, by order ID
	warnings     []string
	lastEquity   float64
	equity       []Point
	lastAt       time.Time
	completed    int
}

func newGuard(cfg Config, controller *order.Controller, store storage.Storage, w *wallet,
	feed *exchange.BacktestFeed, protection *protect.Manager) (*guard, error) {
	zone, err := time.LoadLocation(limitZone)
	if err != nil {
		return nil, fmt.Errorf("backtest: the zone of the daily limits: %w", err)
	}
	g := &guard{cfg: cfg, controller: controller, storage: store, wallet: w, feed: feed, protection: protection,
		zone: zone, own: make(map[int64]string), hoursAt: -1}
	if h := cfg.Hours; h != nil {
		g.hoursWindows = agentapi.HoursWindows(h.Windows, h.Start, h.End, h.Flatten, h.CancelPending)
	}
	g.hold = controller.HoldEntries("the warm-up of the backtest, before its period")
	return g, nil
}

// tick takes the simulated now before a candle is processed: the hold of the
// warm-up released once the period starts, so that the strategy may enter on
// the first candle of it; a new day of the daily limits started.
func (g *guard) tick(now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hold != nil && !now.Before(from(g.cfg)) {
		g.hold()
		g.hold = nil
	}
	// a new day of the daily limits: its result from the equity then, the
	// block of the day before released
	if day := now.In(g.zone).Format(time.DateOnly); day != g.day {
		g.day, g.dayStart = day, g.equityNow()
		if g.blockedDay != "" && g.blockedDay != day {
			g.blockedDay = ""
		}
	}
	// the trading hours: their window at the close of the candle; a window
	// left after it was open in the trading period, its end acted on once the
	// candle closed, as it checks (see OnCandle)
	if g.cfg.Hours != nil {
		open, _, _ := agentapi.TradingWindows(g.hoursWindows, now, g.zone)
		if g.hoursAt >= 0 && open != g.hoursAt {
			if was := g.hoursWindows[g.hoursAt]; g.hoursOpened && (was.Flatten || was.CancelPending) {
				g.hoursEnd = &was
			}
			g.hoursOpened = false
		}
		g.hoursAt, g.hoursClosed = open, open < 0
		if open >= 0 {
			g.hoursOpened = g.hold == nil
		}
	}
	g.setEntries()
}

// setEntries blocks the entries while the daily limit, the trading hours or
// the stop out block them, allows them else, a change at a time. Holding g.mu.
func (g *guard) setEntries() {
	blocked := g.stoppedOut || g.blockedDay != "" || g.hoursClosed
	if blocked == g.blocked {
		return
	}
	g.blocked = blocked
	g.revision++
	if !blocked {
		g.controller.AllowEntries(g.revision)
		return
	}
	reason := "outside the trading hours of the backtest"
	switch {
	case g.stoppedOut:
		reason = "the equity of the backtest reached 0: stopped out"
	case g.blockedDay != "":
		reason = "a daily limit of the backtest reached"
	}
	g.controller.BlockEntries(g.revision, reason)
}

// equityNow returns the equity of the account the backtest simulates: the
// paper wallet's less the credit of the leverage.
func (g *guard) equityNow() float64 {
	return g.wallet.Equity() - g.wallet.credit()
}

// OnCandle guards the backtest after candle, see guard.
func (g *guard) OnCandle(candle model.Candle) {
	if g.protection != nil {
		g.protection.OnCandle(candle)
	}
	now := g.feed.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	equity := g.equityNow()
	g.lastEquity = equity
	if candle.Complete && !candle.Time.Before(from(g.cfg)) {
		at := candle.Time
		if len(g.equity) > 0 && g.equity[len(g.equity)-1].Time == at.Unix() {
			g.equity[len(g.equity)-1].Value = equity
		} else {
			g.equity = append(g.equity, Point{Time: at.Unix(), Value: equity})
		}
	}
	if g.hold != nil || g.stoppedOut {
		return
	}

	if equity <= 0 {
		g.stoppedOut = true
		g.setEntries()
		g.flatten(ReasonStopOut)
		g.warnings = append(g.warnings, fmt.Sprintf("o patrimônio chegou a zero em %s: posições encerradas e "+
			"entradas bloqueadas até o fim", now.UTC().Format(time.RFC3339)))
		return
	}

	if g.hoursEnd != nil {
		g.endHours(*g.hoursEnd)
		g.hoursEnd = nil
	}

	if g.cfg.DailyLoss <= 0 && g.cfg.DailyGain <= 0 {
		return
	}
	if g.blockedDay != "" {
		return
	}
	result := equity - g.dayStart
	if (g.cfg.DailyLoss > 0 && result <= -g.cfg.DailyLoss) || (g.cfg.DailyGain > 0 && result >= g.cfg.DailyGain) {
		g.blockedDay = g.day
		g.setEntries()
		g.flatten(ReasonDailyLimit)
	}
}

// pendingEntry reports whether the open order o would open a position: a
// limit or stop order out of an OCO group, the legs of the positions being in
// one (as the supervisor of the controller tells them apart, see ui).
func pendingEntry(o model.Order) bool {
	if o.GroupID != nil {
		return false
	}
	switch o.Type {
	case model.OrderTypeLimit, model.OrderTypeStopLoss, model.OrderTypeStopLossLimit:
		return true
	}
	return false
}

// endHours acts on the end of the window h of the trading hours, as it
// checks: the pending entries canceled (CancelPending); the positions closed at
// the market (Flatten), the legs protecting them canceled first. The entries
// left pending without CancelPending, the positions open without Flatten.
// Holding g.mu.
func (g *guard) endHours(h ui.TradingWindow) {
	pending, err := g.storage.Orders(storage.WithStatus(model.OrderStatusTypeNew))
	if err != nil {
		log.Warnf("[BACKTEST] the pending orders: %v", err)
	}
	for _, o := range pending {
		entry := pendingEntry(*o)
		if (entry && !h.CancelPending) || (!entry && !h.Flatten) {
			continue
		}
		if err := g.controller.Cancel(*o); err != nil {
			log.Debugf("[BACKTEST] cancel %d: %v", o.ID, err)
		}
	}
	if !h.Flatten {
		return
	}
	for _, pair := range g.wallet.pairs {
		closing, err := g.controller.ClosePosition(pair)
		switch {
		case errors.Is(err, exchange.ErrNothingToReduce):
		case err != nil:
			g.warnings = append(g.warnings, fmt.Sprintf("%s: a posição não pôde ser encerrada (%s): %v", pair,
				ReasonHours, err))
		default:
			g.own[closing.ID] = ReasonHours
		}
	}
}

// flatten cancels the pending orders of the pairs and closes their positions
// at the market, the orders told with reason. Holding g.mu.
func (g *guard) flatten(reason string) {
	pending, err := g.storage.Orders(storage.WithStatus(model.OrderStatusTypeNew))
	if err != nil {
		log.Warnf("[BACKTEST] the pending orders: %v", err)
	}
	for _, o := range pending {
		if err := g.controller.Cancel(*o); err != nil {
			log.Debugf("[BACKTEST] cancel %d: %v", o.ID, err)
		}
	}
	for _, pair := range g.wallet.pairs {
		closing, err := g.controller.ClosePosition(pair)
		switch {
		case errors.Is(err, exchange.ErrNothingToReduce):
		case err != nil:
			g.warnings = append(g.warnings, fmt.Sprintf("%s: a posição não pôde ser encerrada (%s): %v", pair,
				reason, err))
		default:
			g.own[closing.ID] = reason
		}
	}
}

// openPositions values the positions open after the last candle at the
// market, see OpenPosition: their exit at the last close less the slippage,
// a short at the ask (plus the spread of the symbol) plus it.
func (g *guard) openPositions(positions map[string]*open, spread, slippage func(pair string) float64,
	fee float64) []OpenPosition {
	var list []OpenPosition
	for pair, p := range positions {
		last, ok := g.wallet.LastClose(pair)
		if !ok || p.quantity <= 0 {
			continue
		}
		mark := last - slippage(pair)
		sign := 1.0
		if p.side == string(model.SideTypeSell) {
			sign = -1
			mark = last + spread(pair) + slippage(pair)
		}
		average := p.entryValue / p.entryUnits
		profit := p.gross + sign*(mark-average)*p.quantity - p.fees - mark*p.quantity*fee
		list = append(list, OpenPosition{Pair: pair, Side: p.side, Quantity: p.quantity, OpenedAt: p.opened.Unix(),
			Entry: average, Mark: mark, Profit: profit})
	}
	return list
}
