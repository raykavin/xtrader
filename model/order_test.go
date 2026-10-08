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
