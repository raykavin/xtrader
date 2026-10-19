package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

// The protection of the positions of an agent: off without its row, set and
// read back with the agent, refused out of the rules as the database checks
// them.
func TestAgentProtection(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := llmAgent(t, s, "llm")
	require.False(t, a.ProtectionOf().Active(), "off without a row")
	require.Equal(t, ui.ProtectionUnitATR, a.ProtectionOf().Unit)

	settings := ui.ProtectionSettings{Unit: ui.ProtectionUnitATR,
		Breakeven: ui.BreakevenSettings{Enabled: true, Trigger: 1, Margin: 0.1},
		Trailing:  ui.TrailingSettings{Supervisor: true, Trigger: 1.5, Distance: 1, Step: 0.2}}
	_, err := s.SetAgentProtection(ctx, a.ID, settings, "admin")
	require.NoError(t, err)
	got, err := s.Agent(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, settings, got.ProtectionOf())
	require.True(t, got.ProtectionOf().Active())

	// changed in place
	settings.Breakeven.Enabled = false
	_, err = s.SetAgentProtection(ctx, a.ID, settings, "admin")
	require.NoError(t, err)
	got, err = s.AgentByName(ctx, "llm")
	require.NoError(t, err)
	require.False(t, got.ProtectionOf().Breakeven.Enabled)

	for name, bad := range map[string]ui.ProtectionSettings{
		"another unit": {Unit: "points"},
		"a margin as large as the trigger": {Unit: ui.ProtectionUnitATR,
			Breakeven: ui.BreakevenSettings{Enabled: true, Trigger: 1, Margin: 1}},
		"a breakeven without a trigger": {Unit: ui.ProtectionUnitATR,
			Breakeven: ui.BreakevenSettings{Supervisor: true}},
		"a step beyond the distance": {Unit: ui.ProtectionUnitATR,
			Trailing: ui.TrailingSettings{Enabled: true, Trigger: 1, Distance: 1, Step: 1.5}},
		"a distance beyond 50 ATR": {Unit: ui.ProtectionUnitATR,
			Trailing: ui.TrailingSettings{Enabled: true, Trigger: 1, Distance: 51, Step: 1}},
		"a negative trigger": {Unit: ui.ProtectionUnitATR, Breakeven: ui.BreakevenSettings{Trigger: -1}},
	} {
		_, err := s.SetAgentProtection(ctx, a.ID, bad, "admin")
		require.ErrorIs(t, err, ErrInvalid, name)
	}
	// off with distances left: taken, as the dashboard keeps them
	_, err = s.SetAgentProtection(ctx, a.ID, ui.ProtectionSettings{Unit: ui.ProtectionUnitATR,
		Breakeven: ui.BreakevenSettings{Trigger: 2, Margin: 3}}, "admin")
	require.NoError(t, err)
}

// The arms of the XTrader AI on the positions: one in force per position and
// feature, its status and its last move recorded, ended once the position
// closed.
func TestProtectionArms(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := llmAgent(t, s, "llm")
	arm := PositionProtection{AgentID: a.ID, Pair: "XAUUSDm", PositionRef: "mt5:111", Ticket: 111, Side: "BUY",
		Feature: ui.ProtectionTrailing, Reason: "momento perdendo força perto da resistência",
		Evidence: json.RawMessage(`["RSI 72 caindo"]`), RequestedBy: "supervisor"}

	armed, err := s.ArmProtection(ctx, arm)
	require.NoError(t, err)
	require.Equal(t, ArmRequested, armed.Status)
	_, err = s.ArmProtection(ctx, arm)
	require.ErrorIs(t, err, ErrConflict, "one in force on the position and the feature")
	breakeven := arm
	breakeven.Feature = ui.ProtectionBreakeven
	_, err = s.ArmProtection(ctx, breakeven)
	require.NoError(t, err, "the other feature")
	other := arm
	other.PositionRef = "mt5:222"
	_, err = s.ArmProtection(ctx, other)
	require.NoError(t, err, "another position")

	bad := arm
	bad.Reason = " "
	_, err = s.ArmProtection(ctx, bad)
	require.ErrorIs(t, err, ErrInvalid)

	require.NoError(t, s.SetArmStatus(ctx, armed.ID, ArmActive, &ProtectionMove{Move: MoveConfirmed,
		Previous: 4280, Requested: 4290, Confirmed: 4290}))
	inForce, err := s.ProtectionArmsInForce(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, inForce, 3)
	require.Equal(t, ArmActive, inForce[0].Status)
	require.Equal(t, 4290.0, inForce[0].Confirmed)
	require.Equal(t, ui.ProtectionArm{ID: armed.ID, Pair: "XAUUSDm", PositionRef: "mt5:111",
		Feature: ui.ProtectionTrailing, Reason: arm.Reason, ArmedAt: armed.CreatedAt.Unix()}, inForce[0].Arm())

	ended, err := s.EndProtectionArms(ctx, a.ID, []string{"mt5:222"})
	require.NoError(t, err)
	require.EqualValues(t, 2, ended, "the two of the position closed")
	inForce, err = s.ProtectionArmsInForce(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, inForce, 1)
	require.Equal(t, "mt5:222", inForce[0].PositionRef)

	require.NoError(t, s.SetArmStatus(ctx, armed.ID, ArmActive, nil))
	recent, err := s.ProtectionArms(ctx, 10)
	require.NoError(t, err)
	require.Len(t, recent, 3)
	for _, a := range recent {
		if a.ID == armed.ID {
			require.Equal(t, ArmEnded, a.Status, "an arm ended stays ended")
			require.NotNil(t, a.EndedAt)
		}
	}
	_, err = s.ArmProtection(ctx, arm)
	require.NoError(t, err, "armed again once the other ended")
}
