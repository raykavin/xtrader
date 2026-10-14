package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// The positions an agent discarded are kept once each, the same sent again
// changing nothing (as the outbox sends a record again), by the fill after
// which each one was, of the agent only; a discard missing its pair, its side,
// its quantity or its fill is refused.
func TestSaveDiscard_KeptOnceByTheFillAfter(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	other := llmAgent(t, s, "llm-other")

	at := time.Date(2026, 10, 13, 21, 42, 57, 0, time.FixedZone("BRT", -3*3600))
	gold := model.Discard{Pair: "XAUUSDm", Side: model.SideTypeBuy, Quantity: 3, AfterID: 6001, At: at}
	bitcoin := model.Discard{Pair: "BTCUSDm", Side: model.SideTypeSell, Quantity: 0.5, AfterID: 41, At: at}
	require.NoError(t, s.SaveDiscard(ctx, agent.ID, gold))
	require.NoError(t, s.SaveDiscard(ctx, agent.ID, bitcoin))
	again := gold
	again.Quantity, again.At = 9, at.Add(time.Hour)
	require.NoError(t, s.SaveDiscard(ctx, agent.ID, again), "sent again: no error")

	discards, err := s.Discards(ctx, agent.ID)
	require.NoError(t, err)
	require.Len(t, discards, 2)
	require.Equal(t, bitcoin.Pair, discards[0].Pair, "by the fill after")
	require.Equal(t, gold.Pair, discards[1].Pair)
	require.Equal(t, gold.Quantity, discards[1].Quantity, "kept as first written")
	require.True(t, discards[1].At.Equal(at))
	require.Equal(t, gold.Side, discards[1].Side)
	require.Equal(t, gold.AfterID, discards[1].AfterID)

	none, err := s.Discards(ctx, other.ID)
	require.NoError(t, err)
	require.Empty(t, none, "of the agent only")

	for name, invalid := range map[string]model.Discard{
		"pair":     {Side: model.SideTypeBuy, Quantity: 1, AfterID: 1, At: at},
		"side":     {Pair: "XAUUSDm", Side: "HOLD", Quantity: 1, AfterID: 1, At: at},
		"quantity": {Pair: "XAUUSDm", Side: model.SideTypeBuy, AfterID: 1, At: at},
		"fill":     {Pair: "XAUUSDm", Side: model.SideTypeBuy, Quantity: 1, At: at},
	} {
		require.ErrorIs(t, s.SaveDiscard(ctx, agent.ID, invalid), ErrInvalid, name)
	}
}

// The migration of the discards down drops their table only, the agents kept,
// and up again creates it empty.
func TestPositionDiscards_MigrationDownAndUp(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	require.NoError(t, s.CheckSchema(ctx))
	require.NoError(t, s.SaveDiscard(ctx, agent.ID, model.Discard{Pair: "XAUUSDm", Side: model.SideTypeBuy,
		Quantity: 1, AfterID: 1, At: time.Now()}))

	tables := func() int64 {
		var count int64
		require.NoError(t, s.db.Raw("SELECT count(*) FROM information_schema.tables WHERE table_name = "+
			"'position_discards'").Scan(&count).Error)
		return count
	}
	run := func(name string) {
		data, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "000013_create_position_discards."+name+
			".sql"))
		require.NoError(t, err)
		require.NoError(t, s.db.Exec(string(data)).Error, name)
	}
	run("down")
	require.Zero(t, tables(), "dropped")
	_, err := s.AgentByName(ctx, agent.Name)
	require.NoError(t, err, "the agents kept")
	run("up")
	require.EqualValues(t, 1, tables())
	discards, err := s.Discards(ctx, agent.ID)
	require.NoError(t, err)
	require.Empty(t, discards)
}
