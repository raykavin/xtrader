package exchange

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// R17e: the trade requests closing or modifying a position take its ticket,
// which a service operation of the server may change (eg: a swap charged by
// reopening it), while the book and the history keep its identifier.

func TestMT5Ticket_CloseAfterAServiceOperation(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour)) // polled by the test
	entry, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 3)
	require.NoError(t, err)
	ticket := f.reticket(entry.ExchangeID)
	require.NotEqual(t, entry.ExchangeID, ticket)
	require.NoError(t, m.poll(context.Background()))

	f.clearRequests()
	closed, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 3)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeFilled, closed.Status)
	requests := f.tradeRequests(mt5ActionDeal)
	require.Len(t, requests, 1)
	require.Equal(t, float64(ticket), requests[0]["position"], "the ticket of the position, not its identifier")
	require.Empty(t, f.positionsOf(m5fPair, m5fMagic), "closed, no short opened")
	require.Zero(t, m5fNet(t, m))
	deals := f.dealsOf(entry.ExchangeID)
	require.Len(t, deals, 2, "its entry and its exit, by its identifier")
	require.Equal(t, mt5EntryOut, deals[1].entry)
}

func TestMT5Ticket_LevelsAfterAServiceOperation(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour))
	orders, err := m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 1, 4280, 4300)
	require.NoError(t, err)
	entry := orders[0]
	tp, sl := m5fLegs(t, orders[1:])
	ticket := f.reticket(entry.ExchangeID)
	require.NoError(t, m.poll(context.Background()))
	for _, leg := range []model.Order{tp, sl} {
		require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, leg.ExchangeID).Status,
			"the legs of the position, by its identifier")
	}

	f.clearRequests()
	moved, err := m.UpdateProtection(sl, 4282)
	require.NoError(t, err)
	require.Equal(t, 4282.0, *moved.Stop)
	requests := f.tradeRequests(mt5ActionSLTP)
	require.Len(t, requests, 1)
	require.Equal(t, float64(ticket), requests[0]["position"])
	p, ok := f.position(entry.ExchangeID)
	require.True(t, ok)
	require.Equal(t, 4282.0, p.sl)
	require.Equal(t, 4300.0, p.tp)
}

func TestMT5Ticket_CloseByAfterAServiceOperation(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour))
	long, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 3)
	require.NoError(t, err)
	short := f.openPosition(m5fPair, mt5TypeSell, 0.01, m5fMagic, "nb:test") // eg: a pending order filled
	longTicket, shortTicket := f.reticket(long.ExchangeID), f.reticket(short)
	require.NoError(t, m.poll(context.Background()))

	require.NoError(t, m.settlePair(context.Background(), m5fPair))
	closeBy := f.tradeRequests(mt5ActionCloseBy)
	require.Len(t, closeBy, 1)
	require.Equal(t, float64(shortTicket), closeBy[0]["position"])
	require.Equal(t, float64(longTicket), closeBy[0]["position_by"])
	positions := f.positionsOf(m5fPair, m5fMagic)
	require.Len(t, positions, 1, "netted")
	require.Equal(t, long.ExchangeID, positions[0].ticket)
	require.Equal(t, 0.02, positions[0].volume)
}

// The close of a position whose answer is lost is found by its deals, whose
// position_id is the identifier of the position, not the ticket the request
// took.
func TestMT5Ticket_LostAnswerOfACloseFound(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour))
	_, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 3)
	require.NoError(t, err)
	positions := f.positionsOf(m5fPair, m5fMagic)
	require.Len(t, positions, 1)
	f.reticket(positions[0].ticket)
	require.NoError(t, m.poll(context.Background()))
	f.loseNext(1)

	closed, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 3)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeFilled, closed.Status)
	require.Equal(t, 3.0, closed.Quantity)
	require.Len(t, f.tradeRequests(mt5ActionDeal), 2, "the entry and the close, never sent again")
	require.Empty(t, f.positionsOf(m5fPair, m5fMagic), "closed, no short opened")
	require.Zero(t, m5fNet(t, m))
}
