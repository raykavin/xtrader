package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The XTrader AI arms the protection of a position: refused where the agent
// doesn't allow it, nothing recorded; armed, the trigger kept, as the agent
// answers; applied with the stop loss the broker confirmed; its record kept
// up to date as the agent tells it, ended once the position closed; from a
// conversation too, its result written by the system.
func TestSupervision_ProtectPosition(t *testing.T) {
	s := newSupervised(t)
	ctx := context.Background()
	trend := s.liveAgent(t, "trend", 101, true)
	s.bridge.set(protected(1, 101))
	trend.protections = []ui.PositionProtection{{Pair: "XAUUSDm", PositionRef: "mt5:1", Side: "BUY", Entry: 4160,
		Price: 4170, StopLoss: 4150, ATR: 5, Favorable: 2,
		Breakeven: ui.ProtectionFeature{State: ui.ProtectionActive, Level: 4161},
		Trailing:  ui.ProtectionFeature{State: ui.ProtectionWaiting, Remaining: 0.5}}}
	arm := func(id, feature string) llm.ToolCall {
		return tool(id, "protect_position", map[string]any{"agent": "trend", "pair": "XAUUSDm",
			"position_ref": "mt5:1", "feature": feature, "reason": "preço recuando do melhor nível com o stop longe",
			"category": store.CategoryRisk, "evidence": "avanço 2 ATR, preço 4170, stop 4150, ATR 5"})
	}
	results := func(messages []llm.ChatMessage) []string {
		var out []string
		for _, m := range messages {
			if m.Role == llm.ChatTool {
				out = append(out, m.Content)
			}
		}
		return out
	}
	a, err := s.store.AgentByName(ctx, "trend")
	require.NoError(t, err)

	// not allowed: refused, nothing armed
	var told []string
	s.chat.script(calling(arm("p1", ui.ProtectionTrailing)),
		func(_ string, messages []llm.ChatMessage, _ []llm.ToolSpec) (llm.ChatAnswer, error) {
			told = results(messages)
			return llm.ChatAnswer{ToolCalls: []llm.ToolCall{report()}}, nil
		})
	_, err = s.sup.Analyze(ctx, store.RunManual, "admin")
	require.NoError(t, err)
	require.Len(t, told, 1)
	assert.Contains(t, told[0], "doesn't let the XTrader AI")
	arms, err := s.store.ProtectionArms(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, arms)
	require.Empty(t, trend.armed)

	_, err = s.store.SetAgentProtection(ctx, a.ID, ui.ProtectionSettings{Unit: ui.ProtectionUnitATR,
		Breakeven: ui.BreakevenSettings{Supervisor: true, Trigger: 1, Margin: 0.1},
		Trailing:  ui.TrailingSettings{Supervisor: true, Trigger: 2.5, Distance: 1, Step: 0.2}}, "admin")
	require.NoError(t, err)

	// armed, its trigger not reached
	trend.protect = ui.SupervisionProtectResponse{Status: ui.ProtectArmed, Previous: 4150,
		Detail: "armado: aguardando o gatilho, faltam 0.50 ATR"}
	s.chat.script(calling(arm("p2", ui.ProtectionTrailing)), calling(report()))
	run, err := s.sup.Analyze(ctx, store.RunManual, "admin")
	require.NoError(t, err)
	inForce, err := s.store.ProtectionArmsInForce(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, inForce, 1)
	trailing := inForce[0]
	assert.Equal(t, store.ArmArmed, trailing.Status)
	assert.Equal(t, run.ID, *trailing.RunID)
	assert.Equal(t, int64(1), trailing.Ticket)
	assert.Equal(t, "supervisor", trailing.RequestedBy)
	require.Len(t, trend.armed, 1)
	assert.Equal(t, trailing.ID, trend.armed[0].ArmID, "the agent asked with the record")
	a, err = s.store.AgentByName(ctx, "trend") // read again, its protection set
	require.NoError(t, err)
	protection, err := protectionOf(ctx, s.store, a)
	require.NoError(t, err)
	require.Len(t, protection.ProtectionArms, 1, "in its runtime, for a restart")
	require.NotNil(t, protection.ProtectionSettings)

	// applied at once, the stop loss confirmed by the broker
	trend.protect = ui.SupervisionProtectResponse{Status: ui.ProtectApplied, Previous: 4150, Requested: 4161,
		Confirmed: 4161}
	s.chat.script(calling(arm("p3", ui.ProtectionBreakeven)), calling(report()))
	_, err = s.sup.Analyze(ctx, store.RunManual, "admin")
	require.NoError(t, err)
	inForce, err = s.store.ProtectionArmsInForce(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, inForce, 2)
	breakeven := inForce[1]
	assert.Equal(t, store.ArmActive, breakeven.Status)
	assert.Equal(t, store.MoveConfirmed, breakeven.Move)
	assert.Equal(t, [3]float64{4150, 4161, 4161}, [3]float64{breakeven.Previous, breakeven.Requested,
		breakeven.Confirmed})

	// armed again: refused, one in force
	s.chat.script(calling(arm("p4", ui.ProtectionBreakeven)),
		func(_ string, messages []llm.ChatMessage, _ []llm.ToolSpec) (llm.ChatAnswer, error) {
			told = results(messages)
			return llm.ChatAnswer{ToolCalls: []llm.ToolCall{report()}}, nil
		})
	_, err = s.sup.Analyze(ctx, store.RunManual, "admin")
	require.NoError(t, err)
	assert.Contains(t, told[0], "armed already")

	// the agent applied the trailing stop meanwhile
	trend.mu.Lock()
	trend.protections[0].Trailing = ui.ProtectionFeature{State: ui.ProtectionActive, Armed: true,
		ArmID: trailing.ID, Last: &ui.ProtectionMove{Status: ui.MoveConfirmed, Previous: 4161, Requested: 4165,
			Confirmed: 4165, At: time.Now().Unix()}}
	trend.mu.Unlock()
	s.sup.syncArms(ctx)
	got, err := s.store.ProtectionArmsInForce(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ArmActive, got[0].Status)
	assert.Equal(t, 4165.0, got[0].Confirmed)

	// from a conversation: the result written by the system
	s.bridge.set(protected(1, 101), protected(2, 101))
	trend.mu.Lock()
	trend.protections = append(trend.protections, ui.PositionProtection{Pair: "BTCUSDm", PositionRef: "mt5:2",
		Side: "BUY", Entry: 100, Price: 104, StopLoss: 98, ATR: 1, Favorable: 4})
	trend.protect = ui.SupervisionProtectResponse{Status: ui.ProtectApplied, Previous: 98, Requested: 100.1,
		Confirmed: 100.1}
	trend.mu.Unlock()
	s.chat.script(calling(tool("c1", "protect_position", map[string]any{"agent": "trend", "pair": "BTCUSDm",
		"position_ref": "mt5:2", "feature": "breakeven", "reason": "o usuário pediu para proteger o BTC",
		"category": store.CategoryRisk, "evidence": "avanço 4 ATR"})), saying("Feito."))
	added, err := s.sup.Ask(adminCtx(ctx), userOfTests(t, s), "admin", "proteja o BTC do trend no breakeven")
	require.NoError(t, err)
	var result *store.SupervisorMessage
	for i := range added {
		if added[i].Kind == store.MessageResult {
			result = &added[i]
		}
	}
	require.NotNil(t, result)
	assert.Contains(t, result.Content, "breakeven de trend em BTCUSDm")
	assert.Contains(t, result.Content, "stop 98 → 100.1, confirmado pela corretora")

	// the record of the dashboard
	call, _ := settingsSession(t, s.managed)
	resp, body := call(http.MethodGet, "/api/supervisor/protections", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var listed ui.SupervisorProtectionsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &listed))
	require.Len(t, listed.Protections, 3)
	assert.Equal(t, "trend", listed.Protections[0].Agent)
	assert.NotEmpty(t, listed.Protections[0].Evidence)
	resp, body = call(http.MethodGet, "/api/supervisor/runs", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var runs ui.SupervisorRunsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &runs))
	var protectedIDs []int64
	for _, r := range runs.Runs {
		protectedIDs = append(protectedIDs, r.ProtectionIDs...)
	}
	assert.ElementsMatch(t, []int64{trailing.ID, breakeven.ID}, protectedIDs)

	// the positions closed: their arms end
	s.bridge.set()
	trend.mu.Lock()
	trend.protections = nil
	trend.mu.Unlock()
	s.sup.syncArms(ctx)
	inForce, err = s.store.ProtectionArmsInForce(ctx, a.ID)
	require.NoError(t, err)
	assert.Empty(t, inForce)
}

// The protection of the positions of an agent is set from its settings,
// validated, and reaches its runtime without a restart.
func TestSettingsAPI_Protection(t *testing.T) {
	c := newManaged(t)
	c.agent(t, "matrend", "trend", false)
	call, _ := settingsSession(t, c)
	ctx := context.Background()

	resp, body := call(http.MethodGet, "/api/settings/agents", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var list ui.AgentSettingsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	require.Len(t, list.Agents, 1)
	assert.False(t, list.Agents[0].Protection.Active(), "off by default")
	assert.Equal(t, ui.ProtectionUnitATR, list.Agents[0].Protection.Unit)
	var got ui.AgentSettings

	settings := ui.ProtectionSettings{Unit: ui.ProtectionUnitATR,
		Breakeven: ui.BreakevenSettings{Enabled: true, Trigger: 1, Margin: 0.1},
		Trailing:  ui.TrailingSettings{Supervisor: true, Trigger: 2, Distance: 1, Step: 0.25}}
	resp, body = call(http.MethodPatch, "/api/settings/agents/matrend", ui.AgentSettingsRequest{Protection: &settings})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	assert.Equal(t, settings, got.Protection)
	assert.False(t, got.RestartPending, "applied live")

	a, err := c.store.AgentByName(ctx, "matrend")
	require.NoError(t, err)
	doc, ok := runtimeOf(ctx, c.store, a)
	require.True(t, ok)
	require.NotNil(t, doc.ProtectionSettings)
	assert.Equal(t, settings, *doc.ProtectionSettings)

	bad := settings
	bad.Breakeven.Margin = 1
	resp, body = call(http.MethodPatch, "/api/settings/agents/matrend", ui.AgentSettingsRequest{Protection: &bad})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
}
