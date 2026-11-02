package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPrompts_TheSeedsAndTheirUsers(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	prompts, err := s.Prompts(ctx)
	require.NoError(t, err)
	require.Len(t, prompts, 4, "the ones only a language model trades and the one created in the dashboard")
	require.Empty(t, prompts[0].Content, "the list leaves the content out")

	trend, err := s.PromptByName(ctx, "trend_following")
	require.NoError(t, err)
	require.Contains(t, trend.Content, "# Take profit: none")
	require.Contains(t, trend.Description, "trend following on the price action")
	require.Equal(t, 1, trend.Version)

	agent := llmAgent(t, s, "llm")
	require.NoError(t, s.SetAgentPrompt(ctx, agent.ID, &trend.ID))
	prompts, err = s.Prompts(ctx)
	require.NoError(t, err)
	for _, p := range prompts {
		if p.ID == trend.ID {
			require.Equal(t, []string{"llm"}, p.UsedBy)
		} else {
			require.Empty(t, p.UsedBy)
		}
	}
	require.ErrorIs(t, s.DeletePrompt(ctx, trend.ID), ErrConflict, "used by llm")
}

func TestUpdatePrompt_VersionsTheContent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	prompt, err := s.CreatePrompt(ctx, "breakout_v2", "the breakouts", "# Indicators: atr\n\nSetup: v1", "web:xtrader")
	require.NoError(t, err)
	require.Equal(t, 1, prompt.Version)

	description := "the breakouts of the range"
	prompt, err = s.UpdatePrompt(ctx, prompt.ID, PromptEdit{Description: &description}, "web:xtrader")
	require.NoError(t, err)
	require.Equal(t, 1, prompt.Version, "the description alone isn't a version")
	require.Equal(t, description, prompt.Description)

	content := "# Indicators: atr, volume\n\nSetup: v2"
	prompt, err = s.UpdatePrompt(ctx, prompt.ID, PromptEdit{Content: &content, Version: 1}, "telegram:42")
	require.NoError(t, err)
	require.Equal(t, 2, prompt.Version)
	require.Equal(t, content, prompt.Content)

	first, err := s.PromptVersion(ctx, prompt.ID, 1)
	require.NoError(t, err)
	require.Equal(t, "# Indicators: atr\n\nSetup: v1", first.Content, "the first version kept")
	second, err := s.PromptVersion(ctx, prompt.ID, 2)
	require.NoError(t, err)
	require.Equal(t, "telegram:42", second.CreatedBy)

	stale := "# Indicators: atr\n\nSetup: v3"
	_, err = s.UpdatePrompt(ctx, prompt.ID, PromptEdit{Content: &stale, Version: 1}, "web:other")
	require.ErrorIs(t, err, ErrConflict, "edited on version 1, now 2")

	same := content
	prompt, err = s.UpdatePrompt(ctx, prompt.ID, PromptEdit{Content: &same}, "web:xtrader")
	require.NoError(t, err)
	require.Equal(t, 2, prompt.Version, "the same content isn't a version")

	empty := "   "
	_, err = s.UpdatePrompt(ctx, prompt.ID, PromptEdit{Content: &empty}, "web:xtrader")
	require.ErrorIs(t, err, ErrInvalid)
	_, err = s.UpdatePrompt(ctx, 999999, PromptEdit{Content: &content}, "web:xtrader")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestCreatePrompt_RefusesWhatTheAgentsCantRead(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, tc := range []struct{ name, prompt, content, want string }{
		{"taken", "trend_following", "Setup: x", "o prompt trend_following já existe"},
		{"uppercase", "Breakout", "Setup: x", "nome de prompt"},
		{"empty", "blank", " \n ", "está vazio"},
		{"not utf-8", "binary", "Setup: \xff", "não é texto UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreatePrompt(ctx, tc.prompt, "", tc.content, "web:xtrader")
			require.ErrorContains(t, err, tc.want)
		})
	}

	prompt, err := s.CreatePrompt(ctx, "scratch", "", "Setup: x", "web:xtrader")
	require.NoError(t, err)
	require.NoError(t, s.DeletePrompt(ctx, prompt.ID))
	_, err = s.Prompt(ctx, prompt.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.CreatePrompt(ctx, "scratch", "", "Setup: y", "web:xtrader")
	require.NoError(t, err, "the name of a deleted prompt is free again")
}

// raceTransactions runs first in a transaction, then second while it is open:
// second either ends at once, or waits for a lock of the transaction, which
// then commits. It returns their errors.
func raceTransactions(t *testing.T, s *Store, first, second func(ctx context.Context) error) (error, error) {
	t.Helper()
	ctx := context.Background()
	held, release, committed := make(chan error, 1), make(chan struct{}), make(chan error, 1)
	go func() {
		committed <- s.Transaction(ctx, func(ctx context.Context) error {
			err := first(ctx)
			held <- err
			<-release
			return err
		})
	}()
	firstErr := <-held
	done := make(chan error, 1)
	go func() { done <- second(ctx) }()

	var secondErr error
	ended := false
	for deadline := time.Now().Add(5 * time.Second); !ended; {
		select {
		case secondErr = <-done:
			ended = true
			continue
		case <-time.After(5 * time.Millisecond):
		}
		var waiting int64
		require.NoError(t, s.db.Raw("SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() "+
			"AND wait_event_type = 'Lock'").Scan(&waiting).Error)
		if waiting > 0 {
			break // waits for the transaction
		}
		require.True(t, time.Now().Before(deadline), "neither ended nor waiting")
	}
	close(release)
	require.Equal(t, firstErr, <-committed)
	if !ended {
		secondErr = <-done
	}
	return firstErr, secondErr
}

// A prompt given to an agent while it is deleted: whichever comes first, the
// other waits for it and fails, never an agent left with a prompt deleted
// (the controller would answer 404 to its /config and /prompt).
func TestDeletePrompt_RacesTheAgentsGivenIt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	scratch := func(name string) Prompt {
		prompt, err := s.CreatePrompt(ctx, name, "", "Setup: x", "web:xtrader")
		require.NoError(t, err)
		return prompt
	}
	usable := func(agentID int64) {
		t.Helper()
		got, err := s.Agent(ctx, agentID)
		require.NoError(t, err)
		if got.LLM.PromptID != nil {
			_, err := s.Prompt(ctx, *got.LLM.PromptID)
			require.NoError(t, err, "the prompt of the agent")
		}
	}

	t.Run("given first", func(t *testing.T) {
		prompt := scratch("given_first")
		given, deleted := raceTransactions(t, s,
			func(ctx context.Context) error { return s.SetAgentPrompt(ctx, agent.ID, &prompt.ID) },
			func(ctx context.Context) error { return s.DeletePrompt(ctx, prompt.ID) })
		require.NoError(t, given)
		require.ErrorIs(t, deleted, ErrConflict)
		usable(agent.ID)
		require.NoError(t, s.SetAgentPrompt(ctx, agent.ID, nil))
	})
	t.Run("deleted first", func(t *testing.T) {
		prompt := scratch("deleted_first")
		deleted, given := raceTransactions(t, s,
			func(ctx context.Context) error { return s.DeletePrompt(ctx, prompt.ID) },
			func(ctx context.Context) error { return s.SetAgentPrompt(ctx, agent.ID, &prompt.ID) })
		require.NoError(t, deleted)
		require.ErrorIs(t, given, ErrNotFound)
		usable(agent.ID)
	})
	t.Run("a new agent given it first", func(t *testing.T) {
		prompt := scratch("created_first")
		var created Agent
		given, deleted := raceTransactions(t, s,
			func(ctx context.Context) error {
				var err error
				created, err = s.CreateAgent(ctx, newLLMAgent("created", 5701, prompt.ID), AgentSecrets{})
				return err
			},
			func(ctx context.Context) error { return s.DeletePrompt(ctx, prompt.ID) })
		require.NoError(t, given)
		require.ErrorIs(t, deleted, ErrConflict)
		usable(created.ID)
	})
	t.Run("deleted before a new agent", func(t *testing.T) {
		prompt := scratch("deleted_before")
		deleted, given := raceTransactions(t, s,
			func(ctx context.Context) error { return s.DeletePrompt(ctx, prompt.ID) },
			func(ctx context.Context) error {
				_, err := s.CreateAgent(ctx, newLLMAgent("refused", 5702, prompt.ID), AgentSecrets{})
				return err
			})
		require.NoError(t, deleted)
		require.ErrorIs(t, given, ErrInvalid)
		_, err := s.AgentByName(ctx, "refused")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

// newLLMAgent returns an LLM agent of the tests named name, disabled, of the
// prompt of promptID, its API at port.
func newLLMAgent(name string, port int, promptID int64) Agent {
	return Agent{Name: name, Kind: "llm", Mode: "paper", APIAddress: fmt.Sprintf("localhost:%d", port),
		Params: Params{"pair": "XAUUSDm", "timeframe": "30m"},
		LLM:    &LLMSettings{Provider: "openai", Model: "gpt-5", PromptID: &promptID, Params: Params{}}}
}
