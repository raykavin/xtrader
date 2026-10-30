// Package newsguard keeps an agent out of the market around the releases of
// the economic calendar, the news of the dashboard (the tab Notícias), for
// every agent, LLM or not: as the window of a release of a currency of a pair
// starts, the entries of the agent are held, the pending entries of the pair
// canceled and its position closed at the market; as no window holds any of
// its pairs any more, the entries are released. A calendar that can't be read
// blocks nothing, as the news off.
package newsguard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
)

// Controller is the order controller of the bot of the agent, see
// order.Controller.
type Controller interface {
	HoldEntries(reason string) (release func())
	ClosePosition(pair string) (model.Order, error)
	Cancel(order model.Order) error
}

// News is the calendar of the news of the agent, see llm.NewsSwitch.
type News interface {
	Enabled() bool
	Window() (before, after time.Duration, importance llm.Importance)
	Events(ctx context.Context, currencies []string, from, to time.Time) ([]llm.Event, error)
	Blocking(events []llm.Event, at time.Time) (llm.Event, bool)
}

// The reads of the guard: how often, how long one may take, and how often a
// calendar that can't be read is warned about.
const (
	every     = 15 * time.Second
	readLimit = 10 * time.Second
	warnEvery = 15 * time.Minute
)

// Guard is the news guard of an agent, see the package.
type Guard struct {
	news       News
	controller Controller
	orders     storage.Storage
	pairs      []string
	now        func() time.Time

	mu      sync.Mutex
	release func()            // of the hold of the entries, nil without one
	handled map[string]string // the release whose window each pair is in, its positions already closed
	warned  time.Time
}

// New returns the guard of the pairs of an agent, reading the calendar of
// news, holding and closing through controller, its pending entries read from
// orders.
func New(news News, controller Controller, orders storage.Storage, pairs []string) *Guard {
	return &Guard{news: news, controller: controller, orders: orders, pairs: pairs, now: time.Now,
		handled: make(map[string]string)}
}

// Run checks the calendar at once and every few seconds until ctx is done, the
// entries released then.
func (g *Guard) Run(ctx context.Context) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	defer g.free()
	for {
		g.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Check reads the calendar of the pairs once and holds, cancels and closes as
// the windows of their releases tell, see the package.
func (g *Guard) Check(ctx context.Context) {
	before, after, _ := g.news.Window()
	if !g.news.Enabled() || before == 0 && after == 0 {
		g.free()
		return
	}
	now := g.now()
	blocking := make(map[string]llm.Event)
	for _, pair := range g.pairs {
		currencies := llm.CalendarCurrencies(pair)
		if len(currencies) == 0 {
			continue
		}
		read, cancel := context.WithTimeout(ctx, readLimit)
		events, err := g.news.Events(read, currencies, now.Add(-after), now.Add(before))
		cancel()
		if err != nil {
			g.warn(pair, err)
			continue
		}
		if event, ok := g.news.Blocking(events, now); ok {
			blocking[pair] = event
		}
	}
	if len(blocking) == 0 {
		g.free()
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.release == nil {
		var reasons []string
		for _, pair := range g.pairs {
			if event, ok := blocking[pair]; ok {
				reasons = append(reasons, describe(event))
			}
		}
		g.release = g.controller.HoldEntries("notícia: " + strings.Join(reasons, "; "))
	}
	for pair, event := range blocking {
		key := event.Code + "@" + event.Time.UTC().Format(time.RFC3339)
		if g.handled[pair] == key {
			continue
		}
		g.handled[pair] = key
		log.Warnf("[NEWS] %s: %s: entries held, the pending entries canceled and the position closed",
			pair, describe(event))
		g.cancelEntries(pair)
		g.close(pair)
	}
	for pair := range g.handled {
		if _, ok := blocking[pair]; !ok {
			delete(g.handled, pair)
		}
	}
}

// free releases the entries held, if any.
func (g *Guard) free() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.release != nil {
		g.release()
		g.release = nil
		log.Infof("[NEWS] out of the windows of the news: entries released")
	}
	clear(g.handled)
}

// cancelEntries cancels the pending entries of pair: its orders open, limit or
// stop, out of a group (the legs of its position go with it).
func (g *Guard) cancelEntries(pair string) {
	if g.orders == nil {
		return
	}
	open, err := g.orders.Orders(storage.WithPair(pair),
		storage.WithStatusIn(model.OrderStatusTypeNew, model.OrderStatusTypePartiallyFilled))
	if err != nil {
		log.Errorf("[NEWS] %s: the pending entries not read: %v", pair, err)
		return
	}
	for _, o := range open {
		if !pendingEntry(*o) {
			continue
		}
		err := g.controller.Cancel(*o)
		switch {
		case err == nil:
			log.Warnf("[NEWS] %s: the pending entry %d canceled", pair, o.ID)
		case errors.Is(err, exchange.ErrOrderNotOpen), errors.Is(err, exchange.ErrOrderNotFound):
		default:
			log.Errorf("[NEWS] %s: the pending entry %d not canceled: %v", pair, o.ID, err)
		}
	}
}

// close closes the position of pair at the market, if any.
func (g *Guard) close(pair string) {
	order, err := g.controller.ClosePosition(pair)
	switch {
	case err == nil:
		log.Warnf("[NEWS] %s: the position closed (%s %.4f)", pair, order.Side, order.Quantity)
	case errors.Is(err, exchange.ErrNothingToReduce):
	default:
		log.Errorf("[NEWS] %s: the position not closed: %v", pair, err)
	}
}

// warn tells a calendar that can't be read, every warnEvery at most: nothing
// blocked meanwhile. The news off is never one.
func (g *Guard) warn(pair string, err error) {
	if errors.Is(err, llm.ErrNewsDisabled) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if now := g.now(); now.Sub(g.warned) > warnEvery {
		g.warned = now
		log.Warnf("[NEWS] %s: economic calendar not read, entries not blocked by news: %v", pair, err)
	}
}

// pendingEntry reports whether o is a pending entry: a limit or stop order
// out of a group, the legs of a position being in one.
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

// describe names a release for the logs and the reason of the hold.
func describe(event llm.Event) string {
	return fmt.Sprintf("%s %s (%s) às %s UTC", event.Currency, event.Name, event.Importance,
		event.Time.UTC().Format("02/01 15:04"))
}

// ConfigOf returns the news of the dashboard of n for the calendar, off for
// none.
func ConfigOf(n *agentapi.News) llm.NewsConfig {
	if n == nil {
		return llm.NewsConfig{}
	}
	importance, err := llm.ParseImportance(n.BlockImportance)
	if err != nil {
		importance = llm.ImportanceHigh
	}
	return llm.NewsConfig{Enabled: n.Enabled, URL: n.URL, Token: n.Token,
		Before: time.Duration(n.BlockBefore) * time.Minute, After: time.Duration(n.BlockAfter) * time.Minute,
		Importance: importance}
}

// State tells how the news stand for the logs: never the token.
func State(config llm.NewsConfig) string {
	switch {
	case !config.Enabled:
		return "news off on the dashboard: the calendar is not read, nothing blocked"
	case config.URL == "":
		return "news on without the address of the MCP server: the calendar is not read"
	case config.Token == "":
		return "news on without the token of the MCP server: the calendar is not read"
	case config.Before == 0 && config.After == 0:
		return fmt.Sprintf("news on at %s, without a window: nothing blocked", config.URL)
	}
	return fmt.Sprintf("news on at %s: no entry from %s before a release of %s importance to %s after it, "+
		"the pending entries canceled and the positions closed as it starts", config.URL, config.Before,
		config.Importance, config.After)
}

// Apply applies the news of runtime to news while the agent runs, telling the
// change in the logs.
func Apply(news *llm.NewsSwitch, runtime agentapi.Runtime) {
	if runtime.News == nil {
		return
	}
	config := ConfigOf(runtime.News)
	if _, changed := news.Set(config); changed {
		log.Infof("[NEWS] %s", State(config))
	}
}

// Start starts the news guard of an agent launched by the controller, its
// news of cfg (see ConfigOf), until ctx is done: the switch of its calendar,
// to apply its runtime to (see Apply), shared with an LLM trader.
func Start(ctx context.Context, cfg agentapi.Config, news *llm.NewsSwitch, controller Controller,
	orders storage.Storage, pairs []string) {
	log.Infof("[SETUP] %s", State(ConfigOf(cfg.News)))
	go New(news, controller, orders, pairs).Run(ctx)
}
