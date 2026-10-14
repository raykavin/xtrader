package remote

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
)

// The positions the order controller discarded are the ones of the
// controller and the ones of the outbox not sent yet, each one once; a new
// one is written through the outbox, once.
func TestOrders_TheDiscardsOfTheControllerAndOfTheOutbox(t *testing.T) {
	f := newFakeController(t)
	at := time.Date(2026, 10, 13, 21, 42, 57, 0, time.UTC)
	gold := model.Discard{Pair: "XAUUSDm", Side: model.SideTypeBuy, Quantity: 3, AfterID: 6001, At: at}
	bitcoin := model.Discard{Pair: "BTCUSDm", Side: model.SideTypeSell, Quantity: 1, AfterID: 41, At: at}
	f.set(func(f *fakeController) { f.discards = []model.Discard{gold} })
	outbox := openOutbox(t, f)
	// of a run before, not sent: one the controller has already, one it hasn't
	require.NoError(t, outbox.Add(agentapi.KindDiscard, gold.Key(), gold))
	require.NoError(t, outbox.Add(agentapi.KindDiscard, bitcoin.Key(), bitcoin))

	orders, err := NewOrders(context.Background(), f.client(), outbox)
	require.NoError(t, err)
	var keeper storage.DiscardStorage = orders
	discards, err := keeper.Discards()
	require.NoError(t, err)
	require.Len(t, discards, 2, "each one once")
	require.True(t, discards[0].Same(gold))
	require.Equal(t, gold.Quantity, discards[0].Quantity)
	require.True(t, discards[0].At.Equal(at))
	require.True(t, discards[1].Same(bitcoin))
	discards[0].Pair = "changed" // a copy
	again, _ := keeper.Discards()
	require.Equal(t, "XAUUSDm", again[0].Pair)

	next := model.Discard{Pair: "XAUUSDm", Side: model.SideTypeSell, Quantity: 1, AfterID: 7006, At: at.Add(time.Hour)}
	require.NoError(t, keeper.CreateDiscard(next))
	require.NoError(t, keeper.CreateDiscard(next), "once")
	require.NoError(t, keeper.CreateDiscard(gold), "kept already")
	all, _ := keeper.Discards()
	require.Len(t, all, 3)

	pending, err := outbox.Pending(agentapi.KindDiscard)
	require.NoError(t, err)
	var added []model.Discard
	for _, record := range pending {
		var discard model.Discard
		require.NoError(t, json.Unmarshal(record.Payload, &discard))
		added = append(added, discard)
	}
	require.Len(t, added, 3, "the two of the run before, and the new one once")
	require.Equal(t, "XAUUSDm:7006", pending[2].Key)
	require.True(t, added[2].Same(next))
}

// The positions discarded can't be read, eg: of a controller of a version
// before: no orders either, as the replay of the order controller needs them.
func TestOrders_NoneWithoutTheDiscards(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) { f.discards = nil })
	_, err := NewOrders(context.Background(), f.client(), openOutbox(t, f))
	require.ErrorContains(t, err, "the positions discarded")
}
