package exchange_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/storage"
)

// R11, end to end with the adapter: a position of the bot closed by hand in
// the terminal while the bot was off leaves no fill in the storage; at the
// restart the controller doesn't hold it any more, without sending any order,
// and the next trade of the pair is settled on its own rather than averaged
// with it. Protected, it is kept until its legs, canceled by the close by
// hand, end.
func TestMT5Controller_RestartDropsAPositionClosedByHand(t *testing.T) {
	for _, protected := range []bool{false, true} {
		name := "unprotected"
		if protected {
			name = "protected"
		}
		t.Run(name, func(t *testing.T) {
			fake := exchange.NewMT5TestFake(t)
			st, err := storage.FromMemory()
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			first, err := exchange.NewMT5(ctx, fake.Options()...)
			require.NoError(t, err)
			c := order.NewController(ctx, first, st, order.NewOrderFeed())
			var entry model.Order
			var legs []model.Order
			if protected {
				orders, err := c.CreateOrderMarketProtected(model.SideTypeBuy, m5cPair, 2, 4280, 4300)
				require.NoError(t, err)
				entry, legs = orders[0], orders[1:]
			} else {
				entry, err = c.CreateOrderMarket(model.SideTypeBuy, m5cPair, 2)
				require.NoError(t, err)
			}
			cancel()
			fake.CloseByHand(entry.ExchangeID, 0.02)

			ctx, cancel = context.WithCancel(context.Background())
			t.Cleanup(cancel)
			second, err := exchange.NewMT5(ctx, fake.Options()...)
			require.NoError(t, err)
			fake.ClearRequests()
			c = order.NewController(ctx, second, st, order.NewOrderFeed())
			c.Start()
			t.Cleanup(c.Stop)
			for _, leg := range legs {
				m5cSettle(t, c, st, leg.ID, model.OrderStatusTypeCanceled)
			}
			require.Zero(t, fake.Requests("POST", "/order"), "no order sent")
			require.Empty(t, c.Results[m5cPair].WinLong)
			require.Empty(t, c.Results[m5cPair].LoseLong)

			fake.SetPrice(m5cPair, 4300.000, 4300.260)
			buy, err := c.CreateOrderMarket(model.SideTypeBuy, m5cPair, 1)
			require.NoError(t, err)
			fake.SetPrice(m5cPair, 4310.000, 4310.260)
			sell, err := c.CreateOrderMarket(model.SideTypeSell, m5cPair, 1)
			require.NoError(t, err)
			require.InDelta(t, sell.Price-buy.Price, sell.ProfitValue, 1e-9, "not averaged with the position closed")
			require.Len(t, c.Results[m5cPair].WinLong, 1)
			asset, _, err := c.Position(m5cPair)
			require.NoError(t, err)
			require.Zero(t, asset)
			require.Eventually(t, func() bool { return fake.Positions() == 0 }, 5*time.Second, 10*time.Millisecond)
		})
	}
}
