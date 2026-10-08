package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// A state of an order written after a later one, eg: the copy of a move of a
// limit order kept by the bot while the fill was written, never replaces it:
// a final status never goes back to an open one, nor a state to an older one;
// the same state written again, as the outbox sends it again, is no error.
func TestSaveOrders_AnOlderStateNeverReplacesALaterOne(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")

	created := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	entry := model.Order{ID: 1, ExchangeID: 9001, Pair: "XAUUSDm", Side: model.SideTypeBuy,
		Type: model.OrderTypeLimit, Status: model.OrderStatusTypeNew, Price: 2000, Quantity: 1, CreatedAt: created,
		UpdatedAt: created}
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{entry}, true))
	filled := entry
	filled.Status, filled.UpdatedAt = model.OrderStatusTypeFilled, created.Add(time.Minute) // the deal of the broker
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{filled}, true))
	moved := entry
	moved.Price, moved.UpdatedAt = 1999, created.Add(2*time.Minute) // the clock of the agent, later
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{moved}, true), "kept as it is, no error")
	require.Equal(t, filled, storedOrder(t, s, agent.ID, 1), "never open again")
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{filled}, true), "sent again")
	require.Equal(t, filled, storedOrder(t, s, agent.ID, 1))

	leg := model.Order{ID: 2, ExchangeID: 9002, Pair: "XAUUSDm", Side: model.SideTypeSell,
		Type: model.OrderTypeStopLoss, Status: model.OrderStatusTypeNew, Price: 1990, Quantity: 1,
		CreatedAt: created, UpdatedAt: created.Add(3 * time.Minute)}
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{leg}, true))
	older := leg
	older.Price, older.UpdatedAt = 1980, created.Add(time.Minute)
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{older}, true))
	require.Equal(t, leg, storedOrder(t, s, agent.ID, 2), "never an older state")
	stopped := leg
	stopped.Status, stopped.UpdatedAt = model.OrderStatusTypeFilled, created.Add(2*time.Minute) // before the move
	stopped.Profit, stopped.ProfitValue = -0.005, -10
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{stopped}, true))

	require.Equal(t, stopped, storedOrder(t, s, agent.ID, 2), "the fill replaces a later move")
}

// storedOrder returns the order of ID id of the agent of agentID, as stored.
func storedOrder(t *testing.T, s *Store, agentID, id int64) model.Order {
	t.Helper()
	orders, err := s.Orders(context.Background(), agentID, id-1, 1)
	require.NoError(t, err)
	require.Len(t, orders, 1)
	require.Equal(t, id, orders[0].ID)
	order := orders[0]
	order.CreatedAt, order.UpdatedAt = order.CreatedAt.UTC(), order.UpdatedAt.UTC()
	return order
}

// The store keeps an order as model.Order.Supersedes tells, whatever the
// states written one after the other.
func TestSaveOrders_AsSupersedesTells(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")

	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	var states []model.Order
	for _, status := range []model.OrderStatusType{model.OrderStatusTypeNew, model.OrderStatusTypePartiallyFilled,
		model.OrderStatusTypePendingCancel, model.OrderStatusTypeFilled, model.OrderStatusTypeCanceled,
		model.OrderStatusTypeRejected, model.OrderStatusTypeExpired} {
		for _, shift := range []time.Duration{-time.Second, 0, time.Second} {
			states = append(states, model.Order{Pair: "XAUUSDm", Side: model.SideTypeBuy,
				Type: model.OrderTypeLimit, Status: status, Quantity: 1, CreatedAt: at, UpdatedAt: at.Add(shift)})
		}
	}
	id := int64(0)
	for _, prev := range states {
		for _, next := range states {
			id++
			prev.ID, next.ID = id, id
			prev.Price, next.Price = 1, 2
			require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{prev}, true))
			require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{next}, true))
		}
	}
	orders, err := s.Orders(ctx, agent.ID, 0, len(states)*len(states))
	require.NoError(t, err)
	require.Len(t, orders, len(states)*len(states))
	id = 0
	for _, prev := range states {
		for _, next := range states {
			got := orders[id]
			id++
			want := prev
			if next.Supersedes(prev) {
				want = next
				if prev.Status == model.OrderStatusTypeFilled && next.Status == model.OrderStatusTypeFilled {
					want.UpdatedAt = prev.UpdatedAt // the time of the fill kept, see fillTime
				}
			}
			what := fmt.Sprintf("%s at %s, then %s at %s", prev.Status, prev.UpdatedAt.Format(time.TimeOnly),
				next.Status, next.UpdatedAt.Format(time.TimeOnly))
			require.Equal(t, want.Status, got.Status, what)
			require.True(t, want.UpdatedAt.Equal(got.UpdatedAt), what)
		}
	}
}

// B19a: the warning of a batch whose orders are partly kept as stored tells
// the ones kept, with their states, never the ones written.
func TestSaveOrders_TheWarningTellsTheOrdersKeptAlone(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")

	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	filled := model.Order{ID: 1, ExchangeID: 9001, Pair: "XAUUSDm", Side: model.SideTypeBuy,
		Type: model.OrderTypeLimit, Status: model.OrderStatusTypeFilled, Price: 2000, Quantity: 1, CreatedAt: at,
		UpdatedAt: at}
	stop := model.Order{ID: 2, ExchangeID: 9002, Pair: "XAUUSDm", Side: model.SideTypeSell,
		Type: model.OrderTypeStopLoss, Status: model.OrderStatusTypeNew, Price: 1990, Quantity: 1, CreatedAt: at,
		UpdatedAt: at}
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{filled, stop}, true))

	late := filled
	late.Status, late.UpdatedAt = model.OrderStatusTypeNew, at.Add(time.Minute) // a copy kept by the bot
	moved := stop
	moved.Price, moved.UpdatedAt = 1995, at.Add(time.Minute)
	hook := logtest.NewGlobal()
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{late, moved}, true))
	var warnings []string
	for _, entry := range hook.AllEntries() {
		if entry.Level == logrus.WarnLevel {
			warnings = append(warnings, entry.Message)
		}
	}
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "the order 1, NEW at 2026-10-06T12:01:00Z, kept FILLED at 2026-10-06T12:00:00Z")
	require.NotContains(t, warnings[0], "2 (", "the order written left out")
	require.NotContains(t, warnings[0], "order 2")
	require.Equal(t, filled, storedOrder(t, s, agent.ID, 1))
	require.Equal(t, moved, storedOrder(t, s, agent.ID, 2))
}

// R17j: a fill written again with a later time, eg: a later copy of it, keeps
// the time of the first, the one the accounting periods count the trade
// closed by; the rest of the copy is written.
func TestSaveOrders_AFillWrittenAgainKeepsItsTime(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")

	created := time.Date(2026, 10, 6, 23, 0, 0, 0, time.UTC)
	exit := model.Order{ID: 1, ExchangeID: 9001, Pair: "XAUUSDm", Side: model.SideTypeSell,
		Type: model.OrderTypeStopLoss, Status: model.OrderStatusTypeNew, Price: 1990, Quantity: 1, CreatedAt: created,
		UpdatedAt: created}
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{exit}, true))
	filled := exit
	filled.Status, filled.UpdatedAt = model.OrderStatusTypeFilled, created.Add(59*time.Minute) // 23:59
	filled.Profit, filled.ProfitValue = -0.005, -10
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{filled}, true))

	again := filled
	again.UpdatedAt, again.Price = created.Add(2*time.Hour), 1989.5 // the next day
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{again}, true))
	want := again
	want.UpdatedAt = filled.UpdatedAt
	require.Equal(t, want, storedOrder(t, s, agent.ID, 1), "the time of the fill kept, the rest written")

	// an order ended otherwise takes the time of its fill, the first
	canceled := exit
	canceled.ID, canceled.Status, canceled.UpdatedAt = 2, model.OrderStatusTypeCanceled, created.Add(time.Minute)
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{canceled}, true))
	late := filled
	late.ID = 2
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{late}, true))
	require.Equal(t, late, storedOrder(t, s, agent.ID, 2))
}
