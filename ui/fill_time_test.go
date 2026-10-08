package ui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// R17j: a fill published again with another time, eg: a later copy of it,
// keeps the time of the first: the one the accounting periods of the
// dashboard and of /profit count the trade closed by.
func TestChart_AFillPublishedAgainKeepsItsTime(t *testing.T) {
	c := newTestChart(t)
	base := time.Date(2026, 10, 6, 23, 59, 0, 0, time.UTC)
	c.OnCandle(candleAt("XAUUSDm", base, 2000))
	leg := model.Order{ID: 3, Pair: "XAUUSDm", Side: model.SideTypeSell, Type: model.OrderTypeStopLoss,
		Status: model.OrderStatusTypeNew, Price: 1990, Quantity: 1, CreatedAt: base, UpdatedAt: base}
	c.OnOrder(leg)
	filled := leg
	filled.Status, filled.UpdatedAt, filled.Profit = model.OrderStatusTypeFilled, base.Add(30*time.Second), -0.005
	c.OnOrder(filled)
	require.Equal(t, filled.UpdatedAt.Unix(), c.snapshot("XAUUSDm").Orders[0].UpdatedAt, "a fill takes its time")

	again := filled
	again.UpdatedAt, again.Price = base.Add(24*time.Hour), 1989.5 // after the start of a period
	c.OnOrder(again)
	order := c.snapshot("XAUUSDm").Orders[0]
	require.Equal(t, filled.UpdatedAt.Unix(), order.UpdatedAt, "the time of the fill kept")
	require.Equal(t, 1989.5, order.Price, "the rest of the copy taken")
}
