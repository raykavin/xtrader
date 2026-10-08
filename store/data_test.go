package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/ui"
)

// dsn returns the URL of the database of the store.
func (s *Store) dsn(t *testing.T) string {
	t.Helper()
	var name string
	require.NoError(t, s.db.Raw("SELECT current_database()").Scan(&name).Error)
	return testDSN(t, name)
}

func TestTelegram_TokenAndUsers(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	settings, hasToken, err := s.Telegram(ctx)
	require.NoError(t, err)
	require.False(t, settings.Enabled, "off by the seed")
	require.False(t, hasToken)

	on := true
	require.NoError(t, s.SetTelegram(ctx, &on, secret("123:telegram-token")))
	settings, hasToken, err = s.Telegram(ctx)
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.True(t, hasToken)
	require.NotContains(t, string(settings.Token), "telegram-token", "encrypted")
	token, err := s.TelegramToken(ctx)
	require.NoError(t, err)
	require.Equal(t, "123:telegram-token", token)
	require.NoError(t, s.SetTelegram(ctx, nil, nil))
	token, err = s.TelegramToken(ctx)
	require.NoError(t, err)
	require.Equal(t, "123:telegram-token", token, "kept")

	first, err := s.AddTelegramUser(ctx, 111, "Ray")
	require.NoError(t, err)
	_, err = s.AddTelegramUser(ctx, 222, "")
	require.NoError(t, err)
	_, err = s.AddTelegramUser(ctx, 111, "again")
	require.ErrorIs(t, err, ErrConflict)
	_, err = s.AddTelegramUser(ctx, -1, "")
	require.ErrorIs(t, err, ErrInvalid)
	require.NoError(t, s.DeleteTelegramUser(ctx, first.ID))
	users, err := s.TelegramUsers(ctx)
	require.NoError(t, err)
	require.Len(t, users, 1)
	require.EqualValues(t, 222, users[0].TelegramID)
}

func TestSaveOrders_KeepsTheirTimesAndProfits(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	other := llmAgent(t, s, "other")

	stop, group := 4173.0, int64(8174584131579702652)
	created := time.Date(2026, 9, 28, 12, 30, 29, 106770558, time.UTC)
	order := model.Order{ID: 1, ExchangeID: 6144628423280988838, Pair: "XAUUSDm", Side: model.SideTypeSell,
		Type: model.OrderTypeStopLoss, Status: model.OrderStatusTypeNew, Price: 4154.246, Quantity: 1, Stop: &stop,
		GroupID: &group, CreatedAt: created, UpdatedAt: created}
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{order}, true))
	require.NoError(t, s.SaveOrders(ctx, other.ID, []model.Order{order}, true), "the IDs of each agent")

	filled := created.Add(time.Hour)
	order.Status, order.UpdatedAt, order.Profit, order.ProfitValue = model.OrderStatusTypeFilled, filled, 0.0073, 30.39
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{order}, true))

	stale := order
	stale.Status = model.OrderStatusTypeCanceled
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{stale, {ID: 2, Pair: "BTCUSDm",
		Side: model.SideTypeBuy, Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Price: 84000,
		Quantity: 0.03, CreatedAt: created, UpdatedAt: created}}, false))

	orders, err := s.Orders(ctx, agent.ID, 0, 100)
	require.NoError(t, err)
	require.Len(t, orders, 2)
	got := orders[0]
	require.Equal(t, model.OrderStatusTypeFilled, got.Status, "an import never overwrites")
	require.Equal(t, int64(6144628423280988838), got.ExchangeID, "the whole int64")
	require.Equal(t, group, *got.GroupID)
	require.Equal(t, stop, *got.Stop)
	require.Equal(t, 30.39, got.ProfitValue)
	require.Equal(t, 0.0073, got.Profit)
	require.True(t, got.CreatedAt.Equal(created.Truncate(time.Microsecond)), "the time of the agent, to the µs")
	require.True(t, got.UpdatedAt.Equal(filled.Truncate(time.Microsecond)))

	page, err := s.Orders(ctx, agent.ID, 1, 100)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.EqualValues(t, 2, page[0].ID)
	orders, err = s.Orders(ctx, other.ID, 0, 100)
	require.NoError(t, err)
	require.Len(t, orders, 1)
	require.Equal(t, model.OrderStatusTypeNew, orders[0].Status, "the other agent's own")
}

// decision returns a decision of the tests of id on pair, at the minute m.
func decision(id, pair string, m int, orders ...int64) ui.Decision {
	at := time.Date(2026, 9, 28, 12, m, 0, 0, time.UTC).Unix()
	entry := 4154.25
	return ui.Decision{ID: id, Pair: pair, Timeframe: "30m", Candle: at - 1800, DecidedAt: at, Trigger: "candle",
		Model: "openai gpt-5", Prompt: &ui.DecisionPrompt{ID: 3, Name: "trend_following", Version: 2},
		LatencyMS: 27974, Price: 4154.246, Action: "ENTER_SHORT", Confidence: 0.67, Reason: "the trend",
		Entry: &entry, Levels: []ui.Level{{Kind: "resistance", Price: 4173}},
		Notes: []ui.DecisionNote{{Kind: "reasoning", Text: "why"}}, Outcome: "applied", OrderIDs: orders}
}

func TestSaveDecision_TheOrderKeepsTheDecisionThatPlacedIt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	prompt, err := s.PromptByName(ctx, "trend_following")
	require.NoError(t, err)

	first := decision("20260928T120000.000000-XAUUSDm", "XAUUSDm", 0, 1, 2)
	first.Prompt.ID = prompt.ID
	require.NoError(t, s.SaveDecision(ctx, agent.ID, first,
		[]Attachment{{MediaType: "image/png", Data: []byte("png")}}, true))
	require.NoError(t, s.SaveDecision(ctx, agent.ID, first, nil, true), "a retry changes nothing")
	moved := decision("20260928T123000.000000-XAUUSDm", "XAUUSDm", 30, 2)
	moved.Prompt = nil
	moved.Action = "MODIFY_SL"
	require.NoError(t, s.SaveDecision(ctx, agent.ID, moved, nil, true))
	btc := decision("20260928T121500.000000-BTCUSDm", "BTCUSDm", 15)
	btc.Prompt = nil
	require.NoError(t, s.SaveDecision(ctx, agent.ID, btc, nil, true))

	links, err := s.DecisionLinks(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, map[int64]string{1: first.ID, 2: first.ID}, links, "placed by the first")

	got, err := s.Decision(ctx, agent.ID, moved.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{2}, got.OrderIDs, "moved by it")
	got, err = s.Decision(ctx, agent.ID, first.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, got.OrderIDs)
	require.Equal(t, first.Candle, got.Candle)
	require.Equal(t, first.DecidedAt, got.DecidedAt)
	require.Equal(t, &ui.DecisionPrompt{ID: prompt.ID, Name: "trend_following", Version: 2}, got.Prompt)
	require.Equal(t, first.Levels, got.Levels)
	require.Equal(t, first.Notes, got.Notes)
	require.Equal(t, 4154.25, *got.Entry)
	require.Nil(t, got.StopLoss)
	require.Equal(t, []ui.DecisionAttachment{{Index: 0, MediaType: "image/png", Size: 3}}, got.Attachments)

	mediaType, data, err := s.DecisionAttachment(ctx, agent.ID, first.ID, 0)
	require.NoError(t, err)
	require.Equal(t, "image/png", mediaType)
	require.Equal(t, []byte("png"), data)
	_, _, err = s.DecisionAttachment(ctx, agent.ID, first.ID, 1)
	require.ErrorIs(t, err, ErrNotFound)

	all, err := s.Decisions(ctx, agent.ID, "", 10)
	require.NoError(t, err)
	require.Equal(t, []string{moved.ID, btc.ID, first.ID}, ids(all), "newest first")
	gold, err := s.Decisions(ctx, agent.ID, "XAUUSDm", 1)
	require.NoError(t, err)
	require.Equal(t, []string{moved.ID}, ids(gold))
	perPair, err := s.DecisionsPerPair(ctx, agent.ID, 1)
	require.NoError(t, err)
	require.Equal(t, []string{moved.ID, btc.ID}, ids(perPair))

	_, err = s.Decision(ctx, agent.ID, "nothing")
	require.ErrorIs(t, err, ErrNotFound)
	none, err := s.Decisions(ctx, llmAgent(t, s, "other").ID, "", 10)
	require.NoError(t, err)
	require.Empty(t, none, "the other agent has none")
}

func ids(decisions []ui.Decision) []string {
	list := make([]string, 0, len(decisions))
	for _, d := range decisions {
		list = append(list, d.ID)
	}
	return list
}

func TestJournal_AppendsOnceAndRemembersPerPair(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")

	entry := func(id, pair string, m int, action string, failed bool) JournalEntry {
		candle := time.Date(2026, 9, 28, 12, m, 0, 0, time.UTC)
		input, _ := json.Marshal(map[string]interface{}{"symbol": pair, "time": candle, "price": 4154.2})
		e := JournalEntry{ID: id, At: candle.Add(time.Minute), Pair: pair, Trigger: "candle", Model: "openai gpt-5",
			Prompt: &ui.DecisionPrompt{Name: "trend_following"}, LatencyMS: 900, Input: input}
		if failed {
			e.Error = "timeout"
		} else {
			e.Decision = json.RawMessage(`{"action":"` + action + `","confidence":0.7,"reason":" the trend "}`)
		}
		return e
	}
	entries := []JournalEntry{entry("a", "XAUUSDm", 0, "WAIT", false), entry("b", "XAUUSDm", 30, "ENTER_LONG", false),
		entry("c", "XAUUSDm", 45, "", true), entry("d", "BTCUSDm", 10, "HOLD", false)}
	require.NoError(t, s.AppendJournal(ctx, agent.ID, entries))
	require.NoError(t, s.AppendJournal(ctx, agent.ID, entries[:1]), "a retry changes nothing")

	remembered, err := s.JournalMemory(ctx, agent.ID, 1)
	require.NoError(t, err)
	require.Len(t, remembered, 2, "the last of each pair, the error left out")
	require.Equal(t, "BTCUSDm", remembered[0].Pair, "oldest first")
	require.Equal(t, "ENTER_LONG", remembered[1].Action)
	require.Equal(t, "the trend", remembered[1].Reason)
	require.Equal(t, 0.7, remembered[1].Confidence)
	require.True(t, remembered[1].Candle.Equal(time.Date(2026, 9, 28, 12, 30, 0, 0, time.UTC)))
	require.True(t, remembered[1].At.Equal(time.Date(2026, 9, 28, 12, 31, 0, 0, time.UTC)), "when it was taken")

	var count int64
	require.NoError(t, s.db.Table("journal_entries").Count(&count).Error)
	require.EqualValues(t, 4, count)
	require.ErrorIs(t, s.AppendJournal(ctx, agent.ID, []JournalEntry{{ID: "e", Pair: "X"}}), ErrInvalid)
}

func TestLegacyImports_KeepWhatWasImported(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	imported, err := s.LegacyImported(ctx, "data/nb-llm.db")
	require.NoError(t, err)
	require.False(t, imported)
	require.NoError(t, s.MarkLegacyImported(ctx, "data/nb-llm.db", Params{"orders": "17"}))
	imported, err = s.LegacyImported(ctx, "data/nb-llm.db")
	require.NoError(t, err)
	require.True(t, imported)
}
