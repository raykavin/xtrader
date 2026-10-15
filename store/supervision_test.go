package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The seeds give the supervisor its settings, off: an analysis every 15
// minutes, an answer in 2 minutes at most, alone it blocks the entries and
// stops an agent once flat, 3 actions an analysis, and its criteria, without a
// key.
func TestSupervisor_Seeded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	settings, found, err := s.Supervisor(ctx)
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, settings.Enabled)
	require.Equal(t, 900, settings.IntervalSeconds)
	require.Equal(t, 120, settings.TimeoutSeconds)
	require.Equal(t, Autonomy{BlockEntries: true, StopWhenFlat: true, MaxActionsPerRun: 3}, settings.Autonomy)
	require.Contains(t, settings.Instructions, "amostra")
	require.Nil(t, settings.Provider)
	require.Empty(t, settings.APIKey)
	key, err := s.SupervisorKey(ctx)
	require.NoError(t, err)
	require.Empty(t, key)
}

// The settings are checked against their bounds; the supervisor is enabled only
// with its model and its key, which is encrypted and written only.
func TestSetSupervisor(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for name, update := range map[string]SupervisorUpdate{
		"an interval too short":       {IntervalSeconds: ptr(60)},
		"an interval too long":        {IntervalSeconds: ptr(90000)},
		"a timeout too short":         {TimeoutSeconds: ptr(5)},
		"an unknown provider":         {Provider: ptr("google")},
		"an unknown reasoning effort": {ReasoningEffort: ptr("max")},
		"negative max tokens":         {MaxTokens: ptr(-1)},
		"too many actions alone":      {Autonomy: &Autonomy{MaxActionsPerRun: 11}},
		"enabled without a model":     {Enabled: ptr(true)},
	} {
		_, err := s.SetSupervisor(ctx, update)
		require.ErrorIs(t, err, ErrInvalid, name)
	}
	settings, _, err := s.Supervisor(ctx)
	require.NoError(t, err)
	require.Equal(t, 900, settings.IntervalSeconds, "nothing written by the refused ones")

	settings, err = s.SetSupervisor(ctx, SupervisorUpdate{Enabled: ptr(true), IntervalSeconds: ptr(600),
		Provider: ptr("openai"), Model: ptr(" gpt-test "), APIKey: secret("sk-supervisor"),
		ReasoningEffort: ptr("low"), MaxTokens: ptr(2000), Instructions: ptr("  watch the gold  ")})
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, "gpt-test", *settings.Model)
	require.Equal(t, "watch the gold", settings.Instructions)
	require.NotContains(t, string(settings.APIKey), "sk-supervisor", "encrypted")
	data, err := json.Marshal(settings)
	require.NoError(t, err)
	require.NotContains(t, string(data), "api_key", "never told")
	key, err := s.SupervisorKey(ctx)
	require.NoError(t, err)
	require.Equal(t, "sk-supervisor", key)

	_, err = s.SetSupervisor(ctx, SupervisorUpdate{APIKey: secret("")})
	require.ErrorIs(t, err, ErrInvalid, "the key removed from a supervisor enabled")
	_, err = s.SetSupervisor(ctx, SupervisorUpdate{Enabled: ptr(false), APIKey: secret("")})
	require.NoError(t, err)

	// the table refuses what the store would
	err = s.db.Exec("UPDATE supervisor_settings SET interval_seconds = 10").Error
	require.Error(t, err, "the CHECK of the interval")
}

// The analyses: running, then ended with their summary, findings, snapshot and
// usage; the ones left running by a controller stopped fail; the list leaves
// the snapshots out.
func TestSupervisorRuns(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	first, err := s.StartSupervisorRun(ctx, RunSchedule, RunRunning, "", "gpt-test")
	require.NoError(t, err)
	first.Status, first.Summary = RunOK, "all well"
	first.Findings = json.RawMessage(`[{"agent":"orb","category":"risk"}]`)
	first.Snapshot = json.RawMessage(`{"agents":[]}`)
	first.Usage = json.RawMessage(`{"input":10,"output":5}`)
	require.NoError(t, s.FinishSupervisorRun(ctx, first))

	skipped, err := s.StartSupervisorRun(ctx, RunManual, RunSkipped, "admin", "")
	require.NoError(t, err)
	require.NotNil(t, skipped.FinishedAt)
	orphan, err := s.StartSupervisorRun(ctx, RunSchedule, RunRunning, "", "gpt-test")
	require.NoError(t, err)
	failed, err := s.FailRunningSupervisorRuns(ctx, "the controller stopped")
	require.NoError(t, err)
	require.EqualValues(t, 1, failed)

	runs, err := s.SupervisorRuns(ctx, 10)
	require.NoError(t, err)
	require.Len(t, runs, 3)
	require.Equal(t, orphan.ID, runs[0].ID, "the newest first")
	require.Equal(t, RunFailed, runs[0].Status)
	require.Equal(t, "the controller stopped", runs[0].Error)
	require.Nil(t, runs[2].Snapshot, "left out of the list")

	got, err := s.SupervisorRun(ctx, first.ID)
	require.NoError(t, err)
	require.JSONEq(t, `{"agents":[]}`, string(got.Snapshot))
	require.JSONEq(t, `[{"agent":"orb","category":"risk"}]`, string(got.Findings))
	require.Equal(t, "all well", got.Summary)

	previous, found, err := s.PreviousSupervisorRun(ctx, orphan.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, first.ID, previous.ID, "the last one ended ok")
	_, found, err = s.PreviousSupervisorRun(ctx, first.ID)
	require.NoError(t, err)
	require.False(t, found)
}

// A proposal is executed once: of two confirmations at once, one claims it,
// the other gets ErrDecided; an expired one is refused, ErrExpired.
func TestSupervisorActions_ClaimedOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user, err := s.CreateUser(ctx, "admin", "password123")
	require.NoError(t, err)

	expires := time.Now().Add(10 * time.Minute)
	proposal, err := s.CreateSupervisorAction(ctx, SupervisorAction{Source: SourceSupervisor, UserID: &user.ID,
		Action: ActionClosePositions, Scope: json.RawMessage(`{"tickets":[1,2]}`), Reason: "close all",
		Status: ActionProposed, RequestedBy: "admin", ExpiresAt: &expires})
	require.NoError(t, err)
	require.JSONEq(t, `[]`, string(proposal.Evidence))
	require.JSONEq(t, `{}`, string(proposal.Options))

	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = s.ClaimSupervisorAction(ctx, proposal.ID, "admin")
		}()
	}
	wg.Wait()
	claimed := 0
	for _, err := range results {
		if err == nil {
			claimed++
			continue
		}
		require.ErrorIs(t, err, ErrDecided)
	}
	require.Equal(t, 1, claimed, "executed once")
	got, err := s.SupervisorAction(ctx, proposal.ID)
	require.NoError(t, err)
	require.Equal(t, ActionExecuting, got.Status)
	require.Equal(t, "admin", got.ConfirmedBy)

	require.NoError(t, s.FinishSupervisorAction(ctx, proposal.ID, ActionPartial,
		json.RawMessage(`{"closed":[1],"unknown":[2]}`)))
	_, err = s.RejectSupervisorAction(ctx, proposal.ID, "admin")
	require.ErrorIs(t, err, ErrDecided)

	past := time.Now().Add(-time.Second)
	old, err := s.CreateSupervisorAction(ctx, SupervisorAction{Source: SourceUser, Action: ActionBlockEntries,
		Reason: "too much gold", Status: ActionProposed, ExpiresAt: &past})
	require.NoError(t, err)
	_, err = s.ClaimSupervisorAction(ctx, old.ID, "admin")
	require.ErrorIs(t, err, ErrExpired)
	got, err = s.SupervisorAction(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, ActionExpired, got.Status)

	// one left executing by a controller stopped: unknown, never executed again
	stuck, err := s.CreateSupervisorAction(ctx, SupervisorAction{Source: SourceUser, Action: ActionCancelPending,
		Reason: "clean up", Status: ActionExecuting})
	require.NoError(t, err)
	count, err := s.UnknownExecutingSupervisorActions(ctx, "the controller stopped")
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	got, err = s.SupervisorAction(ctx, stuck.ID)
	require.NoError(t, err)
	require.Equal(t, ActionUnknown, got.Status)

	actions, err := s.SupervisorActions(ctx, 10)
	require.NoError(t, err)
	require.Len(t, actions, 3)
	require.Equal(t, stuck.ID, actions[0].ID)
}

// The conversation of each user is apart from the others', read the last ones
// first, in their order.
func TestSupervisorMessages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	alice, err := s.CreateUser(ctx, "alice", "password123")
	require.NoError(t, err)
	bob, err := s.CreateUser(ctx, "bob", "password123")
	require.NoError(t, err)

	var ids []int64
	for i, kind := range []string{MessageQuestion, MessageAnswer, MessageProposal, MessageResult} {
		role := RoleAssistant
		if i == 0 {
			role = RoleUser
		}
		m, err := s.AddSupervisorMessage(ctx, SupervisorMessage{UserID: alice.ID, Role: role, Kind: kind,
			Content: kind})
		require.NoError(t, err)
		ids = append(ids, m.ID)
	}
	_, err = s.AddSupervisorMessage(ctx, SupervisorMessage{UserID: bob.ID, Role: RoleUser, Kind: MessageQuestion,
		Content: "bob's"})
	require.NoError(t, err)

	last, err := s.SupervisorMessages(ctx, alice.ID, 0, 2)
	require.NoError(t, err)
	require.Len(t, last, 2)
	assert.Equal(t, []string{MessageProposal, MessageResult}, []string{last[0].Kind, last[1].Kind})
	earlier, err := s.SupervisorMessages(ctx, alice.ID, ids[2], 10)
	require.NoError(t, err)
	assert.Equal(t, []int64{ids[0], ids[1]}, []int64{earlier[0].ID, earlier[1].ID})
	all, err := s.SupervisorMessages(ctx, bob.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "bob's", all[0].Content)

	err = s.db.Exec("INSERT INTO supervisor_messages (user_id, role, kind) VALUES (?, 'user', 'shout')",
		alice.ID).Error
	require.Error(t, err, "the CHECK of the kinds")
}

// The entries of an agent get a new revision with every change; its stop by the
// supervisor disables it only while it waits for that stop; a user enabling it
// again clears the stop and the block the stop made, not a block of its own.
func TestAgentSupervision(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := llmAgent(t, s, "llm")
	require.Equal(t, StopNone, a.SupervisionOf().StopState, "never supervised")
	_, err := s.UpdateAgent(ctx, a.ID, AgentUpdate{Enabled: ptr(true)})
	require.NoError(t, err)

	blocked, err := s.SetAgentEntries(ctx, a.ID, true, "too much gold", "supervisor", nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, blocked.EntriesRevision)
	allowed, err := s.SetAgentEntries(ctx, a.ID, false, "ignored", "admin", nil)
	require.NoError(t, err)
	require.EqualValues(t, 2, allowed.EntriesRevision)
	require.Empty(t, allowed.EntriesReason)

	stop, err := s.CreateSupervisorAction(ctx, SupervisorAction{Source: SourceSupervisor,
		Action: ActionStopAgent, Mode: ModeWhenFlat, Reason: "crash loop", Category: CategoryOperational,
		Status: ActionExecuting})
	require.NoError(t, err)
	_, err = s.SetAgentEntries(ctx, a.ID, true, "stopping", "supervisor", &stop.ID)
	require.NoError(t, err)
	_, err = s.MarkAgentStop(ctx, a.ID, StopPendingFlat, "crash loop", CategoryOperational, "supervisor", &stop.ID)
	require.NoError(t, err)

	got, err := s.Agent(ctx, a.ID)
	require.NoError(t, err)
	supervision := got.SupervisionOf()
	require.Equal(t, StopPendingFlat, supervision.StopState)
	require.True(t, supervision.EntriesBlocked)
	require.EqualValues(t, 3, supervision.EntriesRevision)

	stopped, err := s.StopAgentFlat(ctx, a.ID, stop.ID+1000)
	require.NoError(t, err)
	require.False(t, stopped, "another action")
	stopped, err = s.StopAgentFlat(ctx, a.ID, stop.ID)
	require.NoError(t, err)
	require.True(t, stopped)
	got, err = s.Agent(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.Enabled)
	require.Equal(t, StopStopped, got.SupervisionOf().StopState)

	// enabled again by a user: the stop and its block cleared
	cleared, err := s.ClearAgentStop(ctx, a.ID, "admin")
	require.NoError(t, err)
	require.True(t, cleared)
	got, err = s.Agent(ctx, a.ID)
	require.NoError(t, err)
	supervision = got.SupervisionOf()
	require.Equal(t, StopNone, supervision.StopState)
	require.False(t, supervision.EntriesBlocked)
	require.EqualValues(t, 4, supervision.EntriesRevision)

	// a block of its own stays
	_, err = s.SetAgentEntries(ctx, a.ID, true, "by hand", "admin", nil)
	require.NoError(t, err)
	_, err = s.MarkAgentStop(ctx, a.ID, StopStopped, "risk", CategoryRisk, "supervisor", &stop.ID)
	require.NoError(t, err)
	cleared, err = s.ClearAgentStop(ctx, a.ID, "admin")
	require.NoError(t, err)
	require.False(t, cleared)
	got, err = s.Agent(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, got.SupervisionOf().EntriesBlocked)

	// disabled by a user meanwhile: never counted as the supervisor's stop
	_, err = s.MarkAgentStop(ctx, a.ID, StopPendingFlat, "risk", CategoryRisk, "supervisor", &stop.ID)
	require.NoError(t, err)
	stopped, err = s.StopAgentFlat(ctx, a.ID, stop.ID)
	require.NoError(t, err)
	require.False(t, stopped, "already disabled")
	got, err = s.Agent(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, StopNone, got.SupervisionOf().StopState)
	require.False(t, errors.Is(err, ErrNotFound))
}
