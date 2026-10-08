package exchange

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// R17g: a pending order executed in part is PARTIALLY_FILLED while its rest
// may still be executed, then FILLED of what was executed: the whole order, or
// the part executed when its rest is canceled or expires, as the order
// controller settles a fill (it has no partial cancel).

func TestMT5Partial_PendingFilledInParts(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	limit, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 3, 4250)
	require.NoError(t, err)

	f.fillPendingPartly(limit.ExchangeID, 0.01)
	partial := m5fWaitStatus(t, m, limit.ExchangeID, model.OrderStatusTypePartiallyFilled)
	require.Equal(t, 3.0, partial.Quantity, "the quantity asked, until its end")

	f.fillPending(limit.ExchangeID)
	filled := m5fWaitStatus(t, m, limit.ExchangeID, model.OrderStatusTypeFilled)
	require.Equal(t, 3.0, filled.Quantity)
	require.Equal(t, 4250.0, filled.Price)
	require.Equal(t, 3.0, m5fNet(t, m))
}

// Canceled after a part of it was executed, a pending order reports what was
// executed, never CANCELED: its position stays, with the levels of the order,
// and so do its legs.
func TestMT5Partial_CanceledAfterAPartialFill(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour)) // polled by the test
	orders, err := m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 3, 4250, 4240, 4300)
	require.NoError(t, err)
	entry := orders[0]
	tp, sl := m5fLegs(t, orders[1:])

	f.fillPendingPartly(entry.ExchangeID, 0.01)
	require.NoError(t, m.poll(context.Background()))
	require.Equal(t, model.OrderStatusTypePartiallyFilled, m5fOrderOf(t, m, entry.ExchangeID).Status)

	require.NoError(t, m.Cancel(entry))
	_, pending := f.pending(entry.ExchangeID)
	require.False(t, pending, "its rest removed")
	require.Equal(t, model.OrderStatusTypePartiallyFilled, m5fOrderOf(t, m, entry.ExchangeID).Status,
		"until the history tells what was executed")

	require.NoError(t, m.poll(context.Background()))
	filled := m5fOrderOf(t, m, entry.ExchangeID)
	require.Equal(t, model.OrderStatusTypeFilled, filled.Status)
	require.Equal(t, 1.0, filled.Quantity, "what was executed")
	require.Equal(t, 4250.0, filled.Price)
	for _, leg := range []model.Order{tp, sl} {
		require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, leg.ExchangeID).Status,
			"the position stays protected")
	}
	position, ok := f.position(entry.ExchangeID)
	require.True(t, ok)
	require.Equal(t, 0.01, position.volume)
	require.Equal(t, 4240.0, position.sl)
	require.Equal(t, 4300.0, position.tp)
	require.Equal(t, 1.0, m5fNet(t, m))
	require.ErrorIs(t, m.Cancel(entry), ErrOrderNotOpen)

	f.triggerSL(entry.ExchangeID, 0)
	require.NoError(t, m.poll(context.Background()))
	require.NoError(t, m.poll(context.Background()))
	require.Equal(t, model.OrderStatusTypeFilled, m5fOrderOf(t, m, sl.ExchangeID).Status)
	require.Equal(t, model.OrderStatusTypeCanceled, m5fOrderOf(t, m, tp.ExchangeID).Status)
}

// A part executed after the last snapshot of the book is found by Cancel, from
// the account read after the removal: never CANCELED meanwhile, its legs kept.
func TestMT5Partial_ExecutedUnseenThenCanceled(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour))
	orders, err := m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 3, 4250, 4240, 4300)
	require.NoError(t, err)
	entry := orders[0]
	tp, sl := m5fLegs(t, orders[1:])
	f.fillPendingPartly(entry.ExchangeID, 0.02) // the book still has it whole

	require.NoError(t, m.Cancel(entry))
	require.Equal(t, model.OrderStatusTypePartiallyFilled, m5fOrderOf(t, m, entry.ExchangeID).Status)
	for _, leg := range []model.Order{tp, sl} {
		require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, leg.ExchangeID).Status)
	}

	require.NoError(t, m.poll(context.Background()))
	filled := m5fOrderOf(t, m, entry.ExchangeID)
	require.Equal(t, model.OrderStatusTypeFilled, filled.Status)
	require.Equal(t, 2.0, filled.Quantity)
	require.Equal(t, 2.0, m5fNet(t, m))
	for _, leg := range []model.Order{tp, sl} {
		require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, leg.ExchangeID).Status)
	}
}

// Expired, or canceled from the terminal, after a part of it was executed:
// FILLED of the part executed.
func TestMT5Partial_EndedOutsideAfterAPartialFill(t *testing.T) {
	for name, end := range map[string]func(f *m5fFake, ticket int64){
		"expired":          (*m5fFake).expirePending,
		"canceled by hand": (*m5fFake).cancelPendingByHand,
	} {
		t.Run(name, func(t *testing.T) {
			f := newM5F(t)
			m := newM5FAdapter(t, f)
			limit, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 3, 4250)
			require.NoError(t, err)
			f.fillPendingPartly(limit.ExchangeID, 0.01)
			m5fWaitStatus(t, m, limit.ExchangeID, model.OrderStatusTypePartiallyFilled)

			end(f, limit.ExchangeID)
			filled := m5fWaitStatus(t, m, limit.ExchangeID, model.OrderStatusTypeFilled)
			require.Equal(t, 1.0, filled.Quantity)
			require.Equal(t, 4250.0, filled.Price)
			require.Equal(t, 1.0, m5fNet(t, m))
		})
	}
}

// Executed in part and ended between two snapshots: the position smaller than
// the order tells it, the history its fill.
func TestMT5Partial_ExecutedAndExpiredUnseen(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour))
	limit, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 3, 4250)
	require.NoError(t, err)
	f.fillPendingPartly(limit.ExchangeID, 0.01)
	f.expirePending(limit.ExchangeID)

	require.NoError(t, m.poll(context.Background()))
	require.NoError(t, m.poll(context.Background()))
	filled := m5fOrderOf(t, m, limit.ExchangeID)
	require.Equal(t, model.OrderStatusTypeFilled, filled.Status)
	require.Equal(t, 1.0, filled.Quantity)
}

// m5fLegStatus returns the statuses of the legs of the order, the take profit
// first.
func m5fLegStatus(t *testing.T, m *MT5, tp, sl model.Order) []model.OrderStatusType {
	t.Helper()
	return []model.OrderStatusType{m5fOrderOf(t, m, tp.ExchangeID).Status, m5fOrderOf(t, m, sl.ExchangeID).Status}
}

// R17g: read while Cancel reads the account after the removal of a pending
// order executed in part since the last snapshot, the order is PENDING_CANCEL,
// never CANCELED (a final state the order controller would settle, the part
// executed lost), and its legs stay NEW.
func TestMT5Partial_ReadWhileCancelChecksTheAccount(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour)) // polled by the test
	orders, err := m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 3, 4250, 4240, 4300)
	require.NoError(t, err)
	entry := orders[0]
	tp, sl := m5fLegs(t, orders[1:])
	f.fillPendingPartly(entry.ExchangeID, 0.02) // the book still has it whole

	reached, release := f.holdNext("GET", "/positions", "symbol")
	canceled := make(chan error, 1)
	go func() { canceled <- m.Cancel(entry) }()
	<-reached
	require.Equal(t, model.OrderStatusTypePendingCancel, m5fOrderOf(t, m, entry.ExchangeID).Status)
	require.Equal(t, []model.OrderStatusType{model.OrderStatusTypeNew, model.OrderStatusTypeNew},
		m5fLegStatus(t, m, tp, sl))
	release()
	require.NoError(t, <-canceled)
	require.Equal(t, model.OrderStatusTypePartiallyFilled, m5fOrderOf(t, m, entry.ExchangeID).Status)

	require.NoError(t, m.poll(context.Background()))
	filled := m5fOrderOf(t, m, entry.ExchangeID)
	require.Equal(t, model.OrderStatusTypeFilled, filled.Status)
	require.Equal(t, 2.0, filled.Quantity, "what was executed")
	require.Equal(t, []model.OrderStatusType{model.OrderStatusTypeNew, model.OrderStatusTypeNew},
		m5fLegStatus(t, m, tp, sl), "its position protected")
	require.Equal(t, 2.0, m5fNet(t, m))
}

// R17g: the account unread after the removal of a pending order executed in
// part since the last snapshot: PENDING_CANCEL and its legs kept, until the
// next snapshot shows its position and the history what was executed.
func TestMT5Partial_AccountUnreadAfterTheRemoval(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour))
	orders, err := m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 3, 4250, 4240, 4300)
	require.NoError(t, err)
	entry := orders[0]
	tp, sl := m5fLegs(t, orders[1:])
	f.fillPendingPartly(entry.ExchangeID, 0.01)

	f.setFailing("GET", "/positions", 503, `{"error":"positions_get: terminal busy"}`)
	require.NoError(t, m.Cancel(entry), "removed")
	_, pending := f.pending(entry.ExchangeID)
	require.False(t, pending)
	require.Equal(t, model.OrderStatusTypePendingCancel, m5fOrderOf(t, m, entry.ExchangeID).Status)
	require.Equal(t, []model.OrderStatusType{model.OrderStatusTypeNew, model.OrderStatusTypeNew},
		m5fLegStatus(t, m, tp, sl), "kept: its position may be open")
	f.clearFailing("GET", "/positions")

	require.NoError(t, m.poll(context.Background()))
	filled := m5fOrderOf(t, m, entry.ExchangeID)
	require.Equal(t, model.OrderStatusTypeFilled, filled.Status)
	require.Equal(t, 1.0, filled.Quantity)
	require.Equal(t, []model.OrderStatusType{model.OrderStatusTypeNew, model.OrderStatusTypeNew},
		m5fLegStatus(t, m, tp, sl))
	require.Equal(t, 1.0, m5fNet(t, m))

	f.triggerSL(entry.ExchangeID, 0)
	require.NoError(t, m.poll(context.Background()))
	require.NoError(t, m.poll(context.Background()))
	require.Equal(t, []model.OrderStatusType{model.OrderStatusTypeCanceled, model.OrderStatusTypeFilled},
		m5fLegStatus(t, m, tp, sl))
}

// R17g: the account unread after the removal of a pending order of which
// nothing was executed: PENDING_CANCEL and its legs kept until the history
// tells it, then CANCELED with its legs.
func TestMT5Partial_AccountUnreadNothingExecuted(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour))
	orders, err := m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 3, 4250, 4240, 4300)
	require.NoError(t, err)
	entry := orders[0]
	tp, sl := m5fLegs(t, orders[1:])

	f.setFailing("GET", "/positions", 503, `{"error":"positions_get: terminal busy"}`)
	require.NoError(t, m.Cancel(entry))
	require.Equal(t, model.OrderStatusTypePendingCancel, m5fOrderOf(t, m, entry.ExchangeID).Status)
	require.Equal(t, []model.OrderStatusType{model.OrderStatusTypeNew, model.OrderStatusTypeNew},
		m5fLegStatus(t, m, tp, sl))
	f.clearFailing("GET", "/positions")

	require.NoError(t, m.poll(context.Background()))
	require.Equal(t, model.OrderStatusTypeCanceled, m5fOrderOf(t, m, entry.ExchangeID).Status)
	require.Equal(t, []model.OrderStatusType{model.OrderStatusTypeCanceled, model.OrderStatusTypeCanceled},
		m5fLegStatus(t, m, tp, sl))
	require.Zero(t, m5fNet(t, m))
}

// R17g: Cancel reads the account once after the removal of a pending order of
// which the book knows no part executed, CANCELED at once with its legs when
// it shows none; never when the book knows a part executed.
func TestMT5Partial_CancelReadsTheAccountOnce(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour))
	orders, err := m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 3, 4250, 4240, 4300)
	require.NoError(t, err)
	entry := orders[0]
	tp, sl := m5fLegs(t, orders[1:])

	f.clearRequests()
	require.NoError(t, m.Cancel(entry))
	require.Equal(t, 1, f.requestCount("GET", "/positions"))
	require.Equal(t, model.OrderStatusTypeCanceled, m5fOrderOf(t, m, entry.ExchangeID).Status)
	require.Equal(t, []model.OrderStatusType{model.OrderStatusTypeCanceled, model.OrderStatusTypeCanceled},
		m5fLegStatus(t, m, tp, sl))

	limit, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 3, 4250)
	require.NoError(t, err)
	f.fillPendingPartly(limit.ExchangeID, 0.01)
	require.NoError(t, m.poll(context.Background()))
	require.Equal(t, model.OrderStatusTypePartiallyFilled, m5fOrderOf(t, m, limit.ExchangeID).Status)
	f.clearRequests()
	require.NoError(t, m.Cancel(limit))
	require.Zero(t, f.requestCount("GET", "/positions"), "executed in part, as the book knows")
	require.Equal(t, model.OrderStatusTypePartiallyFilled, m5fOrderOf(t, m, limit.ExchangeID).Status)
}

// R17g: the position of a pending order executed in part listed only after
// the read of Cancel (a terminal listing it late), which canceled the order:
// the next snapshot finds it (applySnapshot), and the order reports what was
// executed, not its whole quantity.
func TestMT5Partial_PositionListedAfterTheCancel(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour))
	limit, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 3, 4250)
	require.NoError(t, err)
	f.fillPendingPartly(limit.ExchangeID, 0.01)

	f.hidePosition(limit.ExchangeID, true)
	require.NoError(t, m.Cancel(limit))
	f.hidePosition(limit.ExchangeID, false)
	require.NoError(t, m.poll(context.Background()))
	require.NoError(t, m.poll(context.Background()))
	filled := m5fOrderOf(t, m, limit.ExchangeID)
	require.Equal(t, model.OrderStatusTypeFilled, filled.Status)
	require.Equal(t, 1.0, filled.Quantity, "what was executed")
	require.Equal(t, 1.0, m5fNet(t, m))
}

// R17g: a pending order executed in part and canceled whose final state never
// shows in the history: after mt5ResolveWait, FILLED of the volume of its
// position.
func TestMT5Partial_HistoryNeverTellsTheFill(t *testing.T) {
	f := newM5F(t)
	const wait = 50 * time.Millisecond
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour), withMT5HistoryWaits(wait, time.Minute))
	f.holdHistory() // the order and its deals out of the history
	limit, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 3, 4250)
	require.NoError(t, err)
	f.fillPendingPartly(limit.ExchangeID, 0.01)
	require.NoError(t, m.poll(context.Background()))
	require.NoError(t, m.Cancel(limit))
	require.NoError(t, m.poll(context.Background()))
	require.Equal(t, model.OrderStatusTypePartiallyFilled, m5fOrderOf(t, m, limit.ExchangeID).Status,
		"awaited from the history")

	time.Sleep(2 * wait)
	require.NoError(t, m.poll(context.Background()))
	filled := m5fOrderOf(t, m, limit.ExchangeID)
	require.Equal(t, model.OrderStatusTypeFilled, filled.Status)
	require.Equal(t, 1.0, filled.Quantity, "the volume of its position")
}

// R17g: a pending order whose volume left is under its volume asked is
// PARTIALLY_FILLED, whatever state the server lists it in.
func TestMT5Partial_VolumeTellsAPartialFill(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour))
	limit, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 3, 4250)
	require.NoError(t, err)
	f.fillPendingPartly(limit.ExchangeID, 0.01)
	f.setPendingState(limit.ExchangeID, mt5StatePlaced)
	require.NoError(t, m.poll(context.Background()))
	partial := m5fOrderOf(t, m, limit.ExchangeID)
	require.Equal(t, model.OrderStatusTypePartiallyFilled, partial.Status)
	require.Equal(t, 3.0, partial.Quantity)
}
