package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The sentiment of a decision is kept with it, replaced by a newer state of
// the decision, and read back with it, in every read; a decision without one
// reads none.
func TestSaveDecision_Sentiment(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm-smc")
	bullish := 72.5
	read := decision("20261010T120000.000000-XAUUSDm", "XAUUSDm", 0)
	read.Prompt = nil
	read.Sentiment = &bullish
	require.NoError(t, s.SaveDecision(ctx, agent.ID, read, nil, true))
	none := decision("20261010T121500.000000-XAUUSDm", "XAUUSDm", 15)
	none.Prompt = nil
	require.NoError(t, s.SaveDecision(ctx, agent.ID, none, nil, true))

	got, err := s.Decision(ctx, agent.ID, read.ID)
	require.NoError(t, err)
	require.Equal(t, &bullish, got.Sentiment)
	got, err = s.Decision(ctx, agent.ID, none.ID)
	require.NoError(t, err)
	require.Nil(t, got.Sentiment, "none")

	// the decision kept again: its sentiment as it is now, or kept when only
	// missing
	bearish := 20.0
	read.Sentiment = &bearish
	require.NoError(t, s.SaveDecision(ctx, agent.ID, read, nil, true))
	got, err = s.Decision(ctx, agent.ID, read.ID)
	require.NoError(t, err)
	require.Equal(t, &bearish, got.Sentiment)
	neutral := 50.0
	read.Sentiment = &neutral
	require.NoError(t, s.SaveDecision(ctx, agent.ID, read, nil, false), "kept only when missing")
	got, err = s.Decision(ctx, agent.ID, read.ID)
	require.NoError(t, err)
	require.Equal(t, &bearish, got.Sentiment)

	all, err := s.Decisions(ctx, agent.ID, "XAUUSDm", 10)
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, &bearish, all[1].Sentiment, "read with the list")
	require.Nil(t, all[0].Sentiment)
	perPair, err := s.DecisionsPerPair(ctx, agent.ID, 5)
	require.NoError(t, err)
	require.Equal(t, &bearish, perPair[1].Sentiment, "and with the last ones of each pair, the ones the agent reads")

	// the range of the column: a sentiment out of it refused
	out := 101.0
	wrong := decision("20261010T123000.000000-XAUUSDm", "XAUUSDm", 30)
	wrong.Prompt = nil
	wrong.Sentiment = &out
	require.Error(t, s.SaveDecision(ctx, agent.ID, wrong, nil, true))
	_, err = s.Decision(ctx, agent.ID, wrong.ID)
	require.ErrorIs(t, err, ErrNotFound, "the decision not kept either: one transaction")
}

// The migration of the sentiments down drops their table only, the decisions
// kept, and up again creates it empty.
func TestDecisionSentiments_MigrationDownAndUp(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	read := 61.0
	d := decision("20261010T130000.000000-XAUUSDm", "XAUUSDm", 0)
	d.Prompt = nil
	d.Sentiment = &read
	require.NoError(t, s.SaveDecision(ctx, agent.ID, d, nil, true))

	run := func(name string) {
		data, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "000018_create_decision_sentiments."+name+".sql"))
		require.NoError(t, err)
		require.NoError(t, s.db.Exec(string(data)).Error, name)
	}
	run("down")
	var decisions int64
	require.NoError(t, s.db.Table("decisions").Where("agent_id = ?", agent.ID).Count(&decisions).Error)
	require.EqualValues(t, 1, decisions, "the decisions kept")

	run("up")
	got, err := s.Decision(ctx, agent.ID, d.ID)
	require.NoError(t, err)
	require.Nil(t, got.Sentiment, "the table back, empty")
	require.NoError(t, s.SaveDecision(ctx, agent.ID, d, nil, true))
	got, err = s.Decision(ctx, agent.ID, d.ID)
	require.NoError(t, err)
	require.Equal(t, &read, got.Sentiment)
}
