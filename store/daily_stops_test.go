package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

// The stop losses of the daily loss limits: one in force by position of an
// agent on an asset of an account a day, its level and budget updated with
// its last move kept, started again on the other side or another day; its
// moves recorded, the ones sent counted; ended but the ones kept.
func TestDailyStops(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := llmAgent(t, s, "llm")
	risk := 12.5
	target := DailyStopTarget{Day: "2026-11-16", AgentID: a.ID, Account: "10203040", Pair: "XAUUSDm", Side: "BUY",
		Tickets: []int64{11, 12}, Currency: "USD", Level: 4150.5,
		Budget: DailyStopBudget{Limit: 100, Realized: -30, Open: -10, Headroom: 60, Share: 40, Risk: &risk}}

	stop, err := s.SetDailyStop(ctx, target)
	require.NoError(t, err)
	require.Equal(t, []int64{11, 12}, stop.TicketsOf())
	require.InDelta(t, 40, stop.BudgetOf().Share, 1e-9)

	require.NoError(t, s.SetDailyStopMove(ctx, stop.ID, DailyStopMove{Via: StopViaAgent, Move: ui.DailyStopConfirmed,
		Previous: 4140, Requested: 4150.5, Confirmed: 4150.5, Sent: true}))
	target.Level, target.Budget.Share = 4152, 38
	again, err := s.SetDailyStop(ctx, target)
	require.NoError(t, err)
	require.Equal(t, stop.ID, again.ID, "the same position: updated")
	inForce, err := s.DailyStopsInForce(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, inForce, 1)
	got := inForce[0]
	require.InDelta(t, 4152, got.Level, 1e-9)
	require.Equal(t, ui.DailyStopConfirmed, got.Move, "its last move kept")
	require.InDelta(t, 4150.5, got.Confirmed, 1e-9)
	require.Equal(t, 1, got.Attempts)
	info := got.Info("llm")
	require.Equal(t, "llm", info.Agent)
	require.InDelta(t, 38, info.Share, 1e-9)
	require.Equal(t, "limite de perda diária de todos os agentes", got.Request().Reason)

	// not sent: not counted
	require.NoError(t, s.SetDailyStopMove(ctx, got.ID, DailyStopMove{Via: StopViaBridge, Move: ui.DailyStopSkipped,
		Detail: "dentro do nível de congelamento"}))
	inForce, err = s.DailyStopsInForce(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, 1, inForce[0].Attempts)
	require.Equal(t, ui.DailyStopSkipped, inForce[0].Move)

	// the other side: another position
	target.Side = "SELL"
	other, err := s.SetDailyStop(ctx, target)
	require.NoError(t, err)
	require.NotEqual(t, stop.ID, other.ID)
	require.Empty(t, other.Move)

	// another asset, then the ones not kept ended
	target.Pair, target.Side = "BTCUSDm", "BUY"
	btc, err := s.SetDailyStop(ctx, target)
	require.NoError(t, err)
	inForce, err = s.DailyStopsInForce(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, inForce, 2, "the gold one on the other side and the bitcoin one")
	ended, err := s.EndDailyStops(ctx, []int64{btc.ID})
	require.NoError(t, err)
	require.EqualValues(t, 1, ended)
	inForce, err = s.DailyStopsInForce(ctx, 0)
	require.NoError(t, err)
	require.Len(t, inForce, 1)
	require.Equal(t, btc.ID, inForce[0].ID)
	ended, err = s.EndDailyStops(ctx, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, ended)

	// refused without what it needs
	_, err = s.SetDailyStop(ctx, DailyStopTarget{Day: "2026-11-16", AgentID: a.ID, Account: "1", Pair: "XAUUSDm",
		Side: "LONG", Level: 1})
	require.ErrorIs(t, err, ErrInvalid)
}
