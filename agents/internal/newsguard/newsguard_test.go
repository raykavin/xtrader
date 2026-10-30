package newsguard

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
)

func init() {
	exchange.RegisterMT5Pair("XAUUSDm", "XAU", "USD")
	exchange.RegisterMT5Pair("EURJPYm", "EUR", "JPY")
}

// fakeNews is a calendar of the tests: the switch of the news for its state and
// its window, its releases or its error for the reads.
type fakeNews struct {
	*llm.NewsSwitch
	events []llm.Event
	err    error
	reads  int
}

func (f *fakeNews) Events(_ context.Context, currencies []string, _, _ time.Time) ([]llm.Event, error) {
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	var events []llm.Event
	for _, event := range f.events {
		for _, currency := range currencies {
			if event.Currency == currency {
				events = append(events, event)
			}
		}
	}
	return events, nil
}

// fakeController records the holds, the closes and the cancels.
type fakeController struct {
	mu       sync.Mutex
	holds    []string
	held     int
	closed   []string
	canceled []int64
	flat     map[string]bool
}

func (c *fakeController) HoldEntries(reason string) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.holds = append(c.holds, reason)
	c.held++
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			c.held--
			c.mu.Unlock()
		})
	}
}

func (c *fakeController) ClosePosition(pair string) (model.Order, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.flat[pair] {
		return model.Order{}, fmt.Errorf("%w: %s", exchange.ErrNothingToReduce, pair)
	}
	c.closed = append(c.closed, pair)
	return model.Order{Pair: pair, Side: model.SideTypeSell, Quantity: 0.01}, nil
}

func (c *fakeController) Cancel(order model.Order) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.canceled = append(c.canceled, order.ID)
	return nil
}

func release(at time.Time, currency string, importance llm.Importance) llm.Event {
	return llm.Event{Time: at, Currency: currency, Name: "payroll", Code: currency + "-payroll",
		Importance: importance, Actual: math.NaN()}
}

// newGuard returns a guard of XAUUSDm and EURJPYm with news on, a window of 15
// minutes before and after the releases of high importance, at now.
func newGuard(t *testing.T, now time.Time) (*Guard, *fakeNews, *fakeController, storage.Storage) {
	t.Helper()
	news := &fakeNews{NewsSwitch: llm.NewNewsSwitch(llm.NewsConfig{Enabled: true, URL: "http://127.0.0.1:1/mcp",
		Token: "token", Before: 15 * time.Minute, After: 15 * time.Minute, Importance: llm.ImportanceHigh})}
	controller := &fakeController{flat: map[string]bool{}}
	orders, err := storage.FromMemory()
	require.NoError(t, err)
	group := int64(7)
	for _, o := range []model.Order{
		{ExchangeID: 1, Pair: "XAUUSDm", Type: model.OrderTypeLimit, Status: model.OrderStatusTypeNew},
		{ExchangeID: 2, Pair: "XAUUSDm", Type: model.OrderTypeStopLoss, Status: model.OrderStatusTypeNew,
			GroupID: &group}, // a leg of the position
		{ExchangeID: 3, Pair: "XAUUSDm", Type: model.OrderTypeLimit, Status: model.OrderStatusTypeFilled},
		{ExchangeID: 4, Pair: "EURJPYm", Type: model.OrderTypeLimit, Status: model.OrderStatusTypeNew},
	} {
		require.NoError(t, orders.CreateOrder(&o))
	}
	g := New(news, controller, orders, []string{"XAUUSDm", "EURJPYm"})
	g.now = func() time.Time { return now }
	return g, news, controller, orders
}

// The window of a release of the currency of a pair: the entries held, the
// pending entries of the pair canceled (never the legs of its position, nor
// the orders of the other pair) and its position closed, once a window; out
// of it, the entries released.
func TestGuard_TheWindowOfARelease(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 20, 0, 0, time.UTC)
	g, news, controller, orders := newGuard(t, now)
	ctx := context.Background()
	news.events = []llm.Event{release(now.Add(10*time.Minute), "USD", llm.ImportanceHigh)}

	g.Check(ctx)
	require.Equal(t, 1, controller.held)
	require.Len(t, controller.holds, 1)
	require.Contains(t, controller.holds[0], "notícia: USD payroll (high)")
	require.Equal(t, []string{"XAUUSDm"}, controller.closed, "the pair of the currency")
	all, err := orders.Orders()
	require.NoError(t, err)
	byExchange := map[int64]int64{}
	for _, o := range all {
		byExchange[o.ExchangeID] = o.ID
	}
	require.Equal(t, []int64{byExchange[1]}, controller.canceled, "its pending entry only")

	g.Check(ctx)
	require.Len(t, controller.holds, 1, "held once")
	require.Equal(t, []string{"XAUUSDm"}, controller.closed, "closed once a window")
	require.Len(t, controller.canceled, 1)

	g.now = func() time.Time { return now.Add(30 * time.Minute) } // past the window
	g.Check(ctx)
	require.Zero(t, controller.held, "released")

	// another release: another window, closed again
	g.now = func() time.Time { return now.Add(2 * time.Hour) }
	news.events = append(news.events, release(now.Add(2*time.Hour+5*time.Minute), "USD", llm.ImportanceHigh))
	news.events[1].Code = "USD-cpi"
	controller.flat["XAUUSDm"] = true // nothing to close: silent
	g.Check(ctx)
	require.Equal(t, 1, controller.held)
	require.Len(t, controller.holds, 2)
}

// Nothing held: the news off, the window at 0, a release of lower importance
// or of another currency, a calendar that can't be read.
func TestGuard_NothingHeld(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 20, 0, 0, time.UTC)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		edit func(*fakeNews)
	}{
		{"the news off", func(n *fakeNews) { n.Set(llm.NewsConfig{}) }},
		{"no window", func(n *fakeNews) {
			n.Set(llm.NewsConfig{Enabled: true, URL: "http://x/mcp", Token: "t", Importance: llm.ImportanceHigh})
		}},
		{"a release of lower importance", func(n *fakeNews) {
			n.events = []llm.Event{release(now, "USD", llm.ImportanceMedium)}
		}},
		{"a release of another currency", func(n *fakeNews) {
			n.events = []llm.Event{release(now, "GBP", llm.ImportanceHigh)}
		}},
		{"a calendar that can't be read", func(n *fakeNews) {
			n.events = []llm.Event{release(now, "USD", llm.ImportanceHigh)}
			n.err = errors.New("terminal closed")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g, news, controller, _ := newGuard(t, now)
			c.edit(news)
			g.Check(ctx)
			require.Zero(t, controller.held)
			require.Empty(t, controller.closed)
			require.Empty(t, controller.canceled)
		})
	}
}

// The news of the dashboard for the calendar: off for none, the minutes and the
// importance taken, applied while the agent runs.
func TestConfigOfAndApply(t *testing.T) {
	require.Equal(t, llm.NewsConfig{}, ConfigOf(nil))
	config := ConfigOf(&agentapi.News{Enabled: true, URL: "http://mt5:22347/mcp", Token: "secret-token",
		BlockBefore: 15, BlockAfter: 30, BlockImportance: "medium"})
	require.Equal(t, llm.NewsConfig{Enabled: true, URL: "http://mt5:22347/mcp", Token: "secret-token",
		Before: 15 * time.Minute, After: 30 * time.Minute, Importance: llm.ImportanceMedium}, config)
	require.NotContains(t, State(config), "secret-token", "never the token")

	news := llm.NewNewsSwitch(llm.NewsConfig{})
	Apply(news, agentapi.Runtime{}) // none: as it was
	require.False(t, news.Enabled())
	Apply(news, agentapi.Runtime{News: &agentapi.News{Enabled: true, BlockBefore: 5}})
	require.True(t, news.Enabled())
	before, _, _ := news.Window()
	require.Equal(t, 5*time.Minute, before)
}
