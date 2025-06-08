package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrompts_TheSeedsAndTheirUsers(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	prompts, err := s.Prompts(ctx)
	require.NoError(t, err)
	require.Len(t, prompts, 43)
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
		{"taken", "default", "Setup: x", "already exists"},
		{"uppercase", "Breakout", "Setup: x", "prompt name"},
		{"empty", "blank", " \n ", "empty"},
		{"not utf-8", "binary", "Setup: \xff", "not UTF-8"},
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
