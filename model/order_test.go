package model

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOrder_String(t *testing.T) {
	order := Order{
		ID:         1,
		ExchangeID: 2,
		Pair:       "BNBUSDT",
		Side:       SideTypeSell,
		Type:       OrderTypeLimit,
		Status:     OrderStatusTypeFilled,
		Price:      10,
		Quantity:   1,
		CreatedAt:  time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	require.Equal(t, "[FILLED] SELL BNBUSDT | ID: 1, Type: LIMIT, 1.000000 x $10.000000 (~$10)", order.String())
}

func TestOrder_Finite(t *testing.T) {
	stop := math.NaN()
	order := Order{Price: math.Inf(1), Quantity: 2, Fee: math.NaN(), RefPrice: 3, Profit: math.Inf(-1),
		ProfitValue: 4, Stop: &stop}
	finite, fields := order.Finite()
	require.Equal(t, []string{"price", "fee", "profit", "stop"}, fields)
	require.Equal(t, Order{Quantity: 2, RefPrice: 3, ProfitValue: 4, Stop: finite.Stop}, finite)
	require.Zero(t, *finite.Stop)
	require.True(t, math.IsNaN(*order.Stop), "the stop of the order untouched")
	require.True(t, math.IsInf(order.Price, 1))

	same, fields := finite.Finite()
	require.Empty(t, fields)
	require.Equal(t, finite, same)
}

func TestOrder_SupersedesNeverGoesBack(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	state := func(status OrderStatusType, updated time.Time) Order {
		return Order{ID: 1, Status: status, UpdatedAt: updated}
	}
	for _, c := range []struct {
		name      string
		prev, o   Order
		supersede bool
	}{
		{"a later move", state(OrderStatusTypeNew, at), state(OrderStatusTypeNew, at.Add(time.Second)), true},
		{"the same state again", state(OrderStatusTypeFilled, at), state(OrderStatusTypeFilled, at), true},
		{"an older open state", state(OrderStatusTypeNew, at), state(OrderStatusTypePendingCancel,
			at.Add(-time.Second)), false},
		{"open after the fill", state(OrderStatusTypeFilled, at), state(OrderStatusTypeNew, at.Add(time.Hour)),
			false},
		{"pending cancel after the cancel", state(OrderStatusTypeCanceled, at),
			state(OrderStatusTypePendingCancel, at.Add(time.Hour)), false},
		{"the fill at the time of the broker, before the move", state(OrderStatusTypeNew, at),
			state(OrderStatusTypeFilled, at.Add(-time.Second)), true},
		{"an older final state", state(OrderStatusTypeFilled, at), state(OrderStatusTypeCanceled,
			at.Add(-time.Second)), false},
		{"a later final state", state(OrderStatusTypeCanceled, at), state(OrderStatusTypeExpired,
			at.Add(time.Second)), true},
		{"older by less than the microsecond kept", state(OrderStatusTypeNew, at.Add(999)), state(OrderStatusTypeNew,
			at.Add(1)), true},
	} {
		require.Equal(t, c.supersede, c.o.Supersedes(c.prev), c.name)
	}
	for _, status := range []OrderStatusType{OrderStatusTypeFilled, OrderStatusTypeCanceled, OrderStatusTypeRejected,
		OrderStatusTypeExpired} {
		require.True(t, status.Final(), status)
	}
	for _, status := range []OrderStatusType{OrderStatusTypeNew, OrderStatusTypePartiallyFilled,
		OrderStatusTypePendingCancel, ""} {
		require.False(t, status.Final(), status)
	}
}
