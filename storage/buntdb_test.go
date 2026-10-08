package storage

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/buntdb"

	"github.com/raykavin/xtrader/model"
)

func TestFromFile(t *testing.T) {
	file, err := os.CreateTemp(os.TempDir(), "*.db")
	require.NoError(t, err)
	defer func() {
		_ = os.RemoveAll(file.Name())
	}()
	db, err := FromFile(file.Name())
	require.NoError(t, err)
	require.NotNil(t, db)
}

func TestNewBunt(t *testing.T) {
	repo, err := FromMemory()
	require.NoError(t, err)

	storageUseCase(repo, t)
}

func TestNewBuntErrors(t *testing.T) {
	t.Run("fails when the database file cannot be opened", func(t *testing.T) {
		// A directory is not a valid buntdb file.
		_, err := FromFile(t.TempDir())

		require.Error(t, err)
	})
}

func TestBuntOrdersSkipsCorruptedEntries(t *testing.T) {
	repo, err := FromMemory()
	require.NoError(t, err)

	require.NoError(t, repo.CreateOrder(&model.Order{
		ExchangeID: 1, Pair: "BTCUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeLimit,
		Status: model.OrderStatusTypeNew, Price: 100, Quantity: 1,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	// Write a value that is not an order, as a corrupted database would hold.
	bunt := repo.(*Bunt)
	require.NoError(t, bunt.db.Update(func(tx *buntdb.Tx) error {
		_, _, err := tx.Set("999", `{"updated_at": "not an order"}`, nil)
		return err
	}))

	orders, err := repo.Orders()

	require.NoError(t, err)
	require.Len(t, orders, 1, "the unreadable entry is skipped")
}

// Reopened, the file goes on with the ids of its orders: a restart doesn't
// overwrite the orders of the previous runs.
func TestBunt_IDsAcrossReopens(t *testing.T) {
	file := filepath.Join(t.TempDir(), "orders.db")
	db, err := FromFile(file)
	require.NoError(t, err)
	for range 3 {
		require.NoError(t, db.CreateOrder(&model.Order{Pair: "BTCUSDT", Status: model.OrderStatusTypeFilled}))
	}
	require.NoError(t, db.(*Bunt).db.Close())

	db, err = FromFile(file)
	require.NoError(t, err)
	order := &model.Order{Pair: "ETHUSDT"}
	require.NoError(t, db.CreateOrder(order))
	require.Equal(t, int64(4), order.ID)
	orders, err := db.Orders()
	require.NoError(t, err)
	require.Len(t, orders, 4, "none overwritten")
}

// B5: an order of numbers that aren't finite, eg: the profit of a fill without
// a price, which JSON can't write, is stored with them at 0, rather than
// refused for good while it was executed; the order of the caller untouched.
func TestBunt_NumbersNotFiniteStoredAsZero(t *testing.T) {
	st, err := FromMemory()
	require.NoError(t, err)
	stop := math.Inf(1)
	order := &model.Order{ExchangeID: 7, Pair: "BTCUSDT", Side: model.SideTypeSell, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Price: 100, Quantity: 1, Profit: math.NaN(), Stop: &stop,
		UpdatedAt: time.Now()}
	require.NoError(t, st.CreateOrder(order))
	require.True(t, math.IsNaN(order.Profit), "the order of the caller untouched")
	require.True(t, math.IsInf(*order.Stop, 1))

	order.ProfitValue = math.Inf(-1)
	require.NoError(t, st.UpdateOrder(order))
	orders, err := st.Orders()
	require.NoError(t, err)
	require.Len(t, orders, 1)
	require.Equal(t, order.ID, orders[0].ID)
	require.Zero(t, orders[0].Profit)
	require.Zero(t, orders[0].ProfitValue)
	require.NotNil(t, orders[0].Stop)
	require.Zero(t, *orders[0].Stop)
	require.Equal(t, 100.0, orders[0].Price)
}

// B19: the end of an order (filled, canceled, rejected, expired) is never
// undone by an open state written after it, eg: a move the exchange answered
// as the order filled; the open states and the ends replace one another as
// written.
func TestBunt_AnEndIsNeverUndone(t *testing.T) {
	st, err := FromMemory()
	require.NoError(t, err)
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	order := &model.Order{ExchangeID: 7, Pair: "BTCUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeLimit,
		Status: model.OrderStatusTypeNew, Price: 2000, Quantity: 1, CreatedAt: at, UpdatedAt: at}
	require.NoError(t, st.CreateOrder(order))
	state := func() model.Order {
		t.Helper()
		orders, err := st.Orders()
		require.NoError(t, err)
		require.Len(t, orders, 1)
		return *orders[0]
	}
	write := func(status model.OrderStatusType, price float64) {
		t.Helper()
		update := *order
		update.Status, update.Price = status, price
		require.NoError(t, st.UpdateOrder(&update))
	}

	write(model.OrderStatusTypePartiallyFilled, 2005)
	require.Equal(t, model.OrderStatusTypePartiallyFilled, state().Status)
	write(model.OrderStatusTypeNew, 2010)
	require.Equal(t, 2010.0, state().Price, "an open state replaces another")

	write(model.OrderStatusTypeFilled, 2010)
	for _, status := range []model.OrderStatusType{model.OrderStatusTypeNew, model.OrderStatusTypePartiallyFilled,
		model.OrderStatusTypePendingCancel} {
		write(status, 2015)
		require.Equal(t, model.OrderStatusTypeFilled, state().Status, "never %s after filled", status)
		require.Equal(t, 2010.0, state().Price)
	}
	update := state()
	update.ProfitValue = 15
	require.NoError(t, st.UpdateOrder(&update))
	require.Equal(t, 15.0, state().ProfitValue, "an end written again, eg: with its profit")
}
