package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

func TestImportOrders_TheMissingOnesOrNoneOnAConflict(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	at := time.Date(2026, 9, 28, 12, 30, 29, 106770558, time.UTC)
	entry := model.Order{ID: 1, ExchangeID: 9, Pair: "XAUUSDm", Side: model.SideTypeSell, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeNew, Price: 4154.2, Quantity: 1, CreatedAt: at, UpdatedAt: at}
	filled := entry
	filled.Status, filled.UpdatedAt = model.OrderStatusTypeFilled, at.Add(time.Minute)
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{filled}, true), "changed by the agent since")

	stop := model.Order{ID: 2, ExchangeID: 10, Pair: "XAUUSDm", Side: model.SideTypeBuy,
		Type: model.OrderTypeStopLoss, Status: model.OrderStatusTypeNew, Price: 4173, Quantity: 1, CreatedAt: at,
		UpdatedAt: at}
	result, err := s.ImportOrders(ctx, agent.ID, []model.Order{entry, stop}, LeasedIDs{})
	require.NoError(t, err)
	require.Equal(t, OrdersImport{Written: 1, Kept: 1, KeptLast: 1}, result)
	orders, err := s.Orders(ctx, agent.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, orders, 2)
	require.Equal(t, model.OrderStatusTypeFilled, orders[0].Status, "the order kept left as it is")

	// another order at the ID of one of the agent: nothing written
	other := entry
	other.ExchangeID, other.Pair = 11, "EURUSDm"
	third := stop
	third.ID = 3
	result, err = s.ImportOrders(ctx, agent.ID, []model.Order{other, third}, LeasedIDs{})
	require.NoError(t, err)
	require.Equal(t, OrdersImport{Conflicts: []int64{1}}, result)
	orders, err = s.Orders(ctx, agent.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, orders, 2)
	require.Equal(t, "XAUUSDm", orders[0].Pair)

	// B18: a missing order at an ID leased to the agent since an import (past
	// it and the orders it kept), its order maybe on its way: nothing written;
	// past the IDs leased, written
	fourth := stop
	fourth.ID, fourth.ExchangeID = 4, 12
	result, err = s.ImportOrders(ctx, agent.ID, []model.Order{entry, stop, third, fourth}, LeasedIDs{After: 1, Next: 4})
	require.NoError(t, err)
	require.Equal(t, OrdersImport{Kept: 2, KeptLast: 2, Leased: []int64{3}}, result, "past the orders kept")
	orders, err = s.Orders(ctx, agent.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, orders, 2)
	result, err = s.ImportOrders(ctx, agent.ID, []model.Order{entry, stop, fourth}, LeasedIDs{After: 1, Next: 4})
	require.NoError(t, err)
	require.Equal(t, OrdersImport{Written: 1, Kept: 2, KeptLast: 2}, result)
}

func TestImportDecision_OnceWithItsLinksAndAttachments(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	d := decision("20260928T123000.000000-XAUUSDm", "XAUUSDm", 30, 1, 2)
	d.Prompt = nil
	png := []Attachment{{MediaType: "image/png", Data: []byte("png")}}
	result, err := s.ImportDecision(ctx, agent.ID, d, png)
	require.NoError(t, err)
	require.Equal(t, DecisionImport{Written: true, Links: 2, Attachments: 1}, result)

	// kept already: left as it is, without the links of the import
	d.OrderIDs = []int64{3}
	result, err = s.ImportDecision(ctx, agent.ID, d, png)
	require.NoError(t, err)
	require.Equal(t, DecisionImport{}, result)
	links, err := s.DecisionLinks(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, map[int64]string{1: d.ID, 2: d.ID}, links)

	written, err := s.ImportJournal(ctx, agent.ID, []JournalEntry{{ID: d.ID, At: time.Unix(d.DecidedAt, 0),
		Pair: "XAUUSDm", Input: []byte(`{"symbol":"XAUUSDm"}`)}})
	require.NoError(t, err)
	require.EqualValues(t, 1, written)
	written, err = s.ImportJournal(ctx, agent.ID, []JournalEntry{{ID: d.ID, At: time.Unix(d.DecidedAt, 0),
		Pair: "XAUUSDm", Input: []byte(`{"symbol":"XAUUSDm"}`)}})
	require.NoError(t, err)
	require.Zero(t, written, "kept already")
}

func TestLockOrderIDs_TheOrdersAndTheIDsLeased(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	ids, err := s.LockOrderIDs(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, OrderIDs{Next: 1}, ids)
	require.False(t, ids.Used())

	_, _, err = s.LeaseOrderIDs(ctx, agent.ID, 2)
	require.NoError(t, err)
	ids, err = s.LockOrderIDs(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, OrderIDs{Next: 3}, ids)
	require.True(t, ids.Used(), "leased, the orders on their way")

	at := time.Date(2026, 9, 28, 12, 30, 0, 0, time.UTC)
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{{ID: 2, ExchangeID: 9, Pair: "XAUUSDm",
		Side: model.SideTypeSell, Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Price: 4154.2,
		Quantity: 1, CreatedAt: at, UpdatedAt: at}}, true))
	ids, err = s.LockOrderIDs(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, OrderIDs{Orders: 1, Max: 2, Next: 3}, ids)

	_, err = s.LockOrderIDs(ctx, 999999)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestImportChecks_WhatTheDashboardDeletedOrConfigured(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	deleted, err := s.AgentDeleted(ctx, "llm")
	require.NoError(t, err)
	require.False(t, deleted)
	_, err = s.UpdateAgent(ctx, agent.ID, AgentUpdate{Enabled: ptr(false)})
	require.NoError(t, err)
	require.NoError(t, s.DeleteAgent(ctx, agent.ID))
	deleted, err = s.AgentDeleted(ctx, "llm")
	require.NoError(t, err)
	require.True(t, deleted)
	llmAgent(t, s, "llm")
	deleted, err = s.AgentDeleted(ctx, "llm")
	require.NoError(t, err)
	require.True(t, deleted, "one of its name deleted, another one created since")

	_, err = s.CreateUser(ctx, "admin", "a strong password")
	require.NoError(t, err)
	user, err := s.CreateUser(ctx, "XTrader", "a strong password")
	require.NoError(t, err)
	deleted, err = s.UserDeleted(ctx, "xtrader")
	require.NoError(t, err)
	require.False(t, deleted)
	require.NoError(t, s.DeleteUser(ctx, user.ID))
	deleted, err = s.UserDeleted(ctx, " xtrader ")
	require.NoError(t, err)
	require.True(t, deleted, "in any case")

	configured, err := s.TelegramConfigured(ctx)
	require.NoError(t, err)
	require.False(t, configured, "the seed: off, without a token nor users")
	telegram, err := s.AddTelegramUser(ctx, 111, "")
	require.NoError(t, err)
	require.NoError(t, s.DeleteTelegramUser(ctx, telegram.ID))
	configured, err = s.TelegramConfigured(ctx)
	require.NoError(t, err)
	require.True(t, configured, "a user revoked")

	imported, err := s.LegacyImportOf(ctx, "agent:llm")
	require.ErrorIs(t, err, ErrNotFound)
	require.Empty(t, imported.Source)
	require.NoError(t, s.MarkLegacyImported(ctx, "agent:llm", Params{"agent_id": "1"}))
	imported, err = s.LegacyImportOf(ctx, "agent:llm")
	require.NoError(t, err)
	require.Equal(t, Params{"agent_id": "1"}, imported.Details)
	require.False(t, imported.ImportedAt.IsZero())
}

// B13: the data files imported by a version before the imports kept what
// they took from the env (no agent ID in their records) tell such an import,
// which wrote Telegram too; the ones of this version, or the keys of the
// agents, the users and Telegram, never do.
func TestImportedByAnOlderVersion_TheRecordsOfItsFiles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	older, err := s.ImportedByAnOlderVersion(ctx)
	require.NoError(t, err)
	require.False(t, older, "never imported")
	for source, details := range map[string]Params{"agent:llm": {"agent_id": "1"}, "user:xtrader": {"user_id": "1"},
		"env:TELEGRAM":        {"users": "0", "enabled": "false"},
		"/srv/data/nb-llm.db": {"agent": "llm", "agent_id": "1", "order_offset": "0", "orders": "3"}} {
		require.NoError(t, s.MarkLegacyImported(ctx, source, details))
	}
	older, err = s.ImportedByAnOlderVersion(ctx)
	require.NoError(t, err)
	require.False(t, older, "this version")

	require.NoError(t, s.MarkLegacyImported(ctx, "/srv/data/nb-llm.decisions.jsonl",
		Params{"agent": "llm", "entries": "2"}))
	older, err = s.ImportedByAnOlderVersion(ctx)
	require.NoError(t, err)
	require.True(t, older)
}
