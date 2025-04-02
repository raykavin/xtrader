package storage

import (
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
