package exchange_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/storage"
)

// A protected pending entry canceled before its fill: its legs settle
// CANCELED as they were placed, their side, level and size kept, whatever the
// terminal still knows of them (the controller refuses an order without a
// side): in the run that placed them, after a restart, and after a restart
// while the history of the terminal lags.
func TestMT5Controller_CanceledPendingKeepsItsLegs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		restart bool
		lag     bool
	}{
		{"the same run", false, false},
		{"after a restart", true, false},
		{"after a restart, the history late", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := exchange.NewMT5TestFake(t)
			st, err := storage.FromMemory()
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			m, err := exchange.NewMT5(ctx, fake.Options()...)
			require.NoError(t, err)
			c := order.NewController(ctx, m, st, order.NewOrderFeed())
			placed, err := c.CreateOrderLimitProtected(model.SideTypeBuy, m5cPair, 2, 4250, 4240, 4300)
			require.NoError(t, err)
			require.Len(t, placed, 3)
			entry := placed[0]
			tp, sl := m5cLegs(placed[1:])

			if tc.restart {
				cancel()
				ctx, cancel = context.WithCancel(context.Background())
				t.Cleanup(cancel)
				m, err = exchange.NewMT5(ctx, fake.Options()...)
				require.NoError(t, err)
				c = order.NewController(ctx, m, st, order.NewOrderFeed())
				c.Start() // the orders of the previous run followed again
				t.Cleanup(c.Stop)
				entry = m5cStored(t, st, entry.ID)
			}
			if tc.lag {
				fake.HoldHistory()
			}
			require.NoError(t, c.Cancel(entry))
			if tc.lag {
				fake.ReleaseHistory()
			}

			m5cSettle(t, c, st, entry.ID, model.OrderStatusTypeCanceled)
			for _, want := range []model.Order{tp, sl} {
				stored := m5cSettle(t, c, st, want.ID, model.OrderStatusTypeCanceled)
				require.Equal(t, model.SideTypeSell, stored.Side, "the %s", want.Type)
				require.Equal(t, want.Type, stored.Type)
				require.Equal(t, m5cPair, stored.Pair)
				require.Equal(t, want.Price, stored.Price, "the %s", want.Type)
				require.Equal(t, want.Quantity, stored.Quantity, "the %s", want.Type)
				require.Equal(t, want.GroupID, stored.GroupID)
			}
		})
	}
}
