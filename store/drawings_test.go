package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

// The drawings of a decision are kept with it, in their order, replaced by a
// newer state of the decision, and read back with it, in every read; a
// decision without any reads none.
func TestSaveDecision_Drawings(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm-smc")
	to := int64(1790000900)
	low, high, level := 83500.0, 83800.0, 84600.0
	drawn := decision("20261008T120000.000000-BTCUSDm", "BTCUSDm", 0, 1, 2)
	drawn.Prompt = nil
	drawn.Action = "BUY_LIMIT"
	drawn.Drawings = []ui.Drawing{
		{ID: "fvg-1", Kind: ui.DrawingZone, Label: "FVG", Bias: ui.BiasBullish, Timeframe: "4h", From: 1789990000,
			To: &to, Low: &low, High: &high},
		{ID: "bsl", Kind: ui.DrawingLevel, Label: "BSL", Bias: ui.BiasBullish, Timeframe: "30m", From: 1789995000,
			Price: &level},
	}
	require.NoError(t, s.SaveDecision(ctx, agent.ID, drawn, nil, true))
	plain := decision("20261008T123000.000000-BTCUSDm", "BTCUSDm", 30)
	plain.Prompt = nil
	require.NoError(t, s.SaveDecision(ctx, agent.ID, plain, nil, true))

	got, err := s.Decision(ctx, agent.ID, drawn.ID)
	require.NoError(t, err)
	require.Equal(t, drawn.Drawings, got.Drawings)
	got, err = s.Decision(ctx, agent.ID, plain.ID)
	require.NoError(t, err)
	require.Empty(t, got.Drawings, "none")

	// the decision kept again with its orders: its drawings as they are now
	drawn.Drawings = drawn.Drawings[:1]
	require.NoError(t, s.SaveDecision(ctx, agent.ID, drawn, nil, true))
	got, err = s.Decision(ctx, agent.ID, drawn.ID)
	require.NoError(t, err)
	require.Len(t, got.Drawings, 1)
	drawn.Drawings = nil
	require.NoError(t, s.SaveDecision(ctx, agent.ID, drawn, nil, false), "kept only when missing")
	got, err = s.Decision(ctx, agent.ID, drawn.ID)
	require.NoError(t, err)
	require.Len(t, got.Drawings, 1)

	all, err := s.Decisions(ctx, agent.ID, "BTCUSDm", 10)
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Len(t, all[1].Drawings, 1, "read with the list")
	perPair, err := s.DecisionsPerPair(ctx, agent.ID, 5)
	require.NoError(t, err)
	require.Len(t, perPair[1].Drawings, 1, "and with the last ones of each pair, the ones the agent reads")
}
