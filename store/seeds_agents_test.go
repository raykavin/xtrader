package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/internal/pgtest"
)

// The agents of a new database come from sql/seeds/0003_agents.sql, all of
// them off and without secrets: inserted once, never overwriting an edit nor
// bringing back an agent deleted.
func TestSeeds_TheAgentsOfANewDatabase(t *testing.T) {
	dsn := testDatabase(t)
	pgtest.MigrateSeeded(t, dsn, "..")
	s, err := Open(config.Database{DSN: dsn, MaxOpenConns: 2, ConnMaxLifetime: time.Minute, LogLevel: "silent"},
		testKey)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	agents, err := s.Agents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 6)

	llm, err := s.AgentByName(ctx, "llm")
	require.NoError(t, err)
	require.Equal(t, "llm", llm.Kind)
	require.False(t, llm.Enabled, "off until its secrets are set")
	require.Equal(t, "live", llm.Mode)
	require.Equal(t, "localhost:5580", llm.APIAddress)
	require.True(t, llm.Launched)
	require.True(t, llm.Restart)
	require.True(t, llm.TradePanel)
	require.True(t, llm.TelegramControl)
	require.True(t, llm.ReanalysisAllowed(), "its reanalysis allowed")
	require.Equal(t, Params{"pair": "BTCUSDm;XAUUSDm", "timeframe": "30m", "lot": "BTCUSDm=0.01;XAUUSDm=0.02",
		"leverage": "400"}, llm.Params)
	require.NotNil(t, llm.Account)
	require.Equal(t, "mt5", llm.Account.Exchange)
	require.Equal(t, "http://mt5:18812", llm.Account.BridgeURL)
	require.Empty(t, llm.Account.BridgeToken, "no secret in the seeds")
	require.Nil(t, llm.Account.Magic, "the hash of the comment of the kind")
	require.Nil(t, llm.Account.Comment)
	require.False(t, llm.Account.RealAllowed)
	require.NotNil(t, llm.LLM)
	require.Equal(t, "openai", llm.LLM.Provider)
	require.Equal(t, "gpt-5", llm.LLM.Model)
	require.Empty(t, llm.LLM.APIKey, "no secret in the seeds")
	require.Equal(t, "low", *llm.LLM.ReasoningEffort)
	prompt, err := s.PromptByName(ctx, "trend_following")
	require.NoError(t, err)
	require.Equal(t, prompt.ID, *llm.LLM.PromptID)
	require.Equal(t, "http://mt5:22347/mcp", *llm.LLM.CalendarURL)
	require.Empty(t, llm.LLM.CalendarToken, "no secret in the seeds")
	require.Equal(t, Params{"llm-setup": "free", "llm-candles": "60", "llm-max-spread-atr": "0.2",
		"llm-min-stop-atr": "1", "llm-higher-trend-required": "true", "llm-higher-timeframes": "4h",
		"llm-correlated": "BTCUSDm=DXYm+XAUUSDm;XAUUSDm=DXYm+USTECm", "calendar-importance": "high"},
		llm.LLM.Params)

	matrend, err := s.AgentByName(ctx, "matrend-btc")
	require.NoError(t, err)
	require.Equal(t, "trend", matrend.Kind)
	require.False(t, matrend.Enabled)
	require.Equal(t, "live", matrend.Mode)
	require.Equal(t, "localhost:5570", matrend.APIAddress)
	require.False(t, matrend.TradePanel)
	require.True(t, matrend.TelegramControl)
	require.Nil(t, matrend.Reanalysis, "no reanalysis without a language model")
	require.Equal(t, Params{"strategy": "matrend", "pair": "BTCUSDm", "timeframe": "5m", "lot": "0.01",
		"leverage": "20", "stop-value": "1", "take-profit-value": "2", "max-distance": "0.5"}, matrend.Params)
	require.Equal(t, "http://mt5:18812", matrend.Account.BridgeURL)
	require.Empty(t, matrend.Account.BridgeToken)
	require.Nil(t, matrend.LLM)
	require.Less(t, llm.Position, matrend.Position, "in the order of the file")

	// the agent of the prompt smc_ict, a magic number of its own on the account
	// of llm, its risk left to the operator
	smc, err := s.AgentByName(ctx, "llm-smc")
	require.NoError(t, err)
	require.Equal(t, "llm", smc.Kind)
	require.False(t, smc.Enabled, "off until its secrets and its risk are set")
	require.Equal(t, "localhost:5581", smc.APIAddress)
	require.True(t, smc.TradePanel)
	require.True(t, smc.ReanalysisAllowed())
	require.Equal(t, llm.Params, smc.Params, "the pairs, the lots and the timeframe of llm")
	require.Equal(t, "http://mt5:18812", smc.Account.BridgeURL)
	require.Equal(t, "nb:llm-smc", *smc.Account.Comment)
	require.Empty(t, smc.Account.BridgeToken)
	require.Empty(t, smc.LLM.APIKey)
	smcPrompt, err := s.PromptByName(ctx, "smc_ict")
	require.NoError(t, err)
	require.Equal(t, smcPrompt.ID, *smc.LLM.PromptID)
	require.Equal(t, Params{"llm-setup": "free", "llm-candles": "60", "llm-higher-timeframes": "4h",
		"llm-correlated": "BTCUSDm=DXYm+XAUUSDm;XAUUSDm=DXYm+USTECm", "calendar-importance": "high"},
		smc.LLM.Params)
	require.Less(t, matrend.Position, smc.Position)

	// the strategy fibonacci, without LLM, an agent per pair, each with a magic
	// number of its own
	for i, want := range []struct{ name, pair, address string }{
		{"fibonacci-btc", "BTCUSDm", "localhost:5571"},
		{"fibonacci-xau", "XAUUSDm", "localhost:5572"},
	} {
		fib, err := s.AgentByName(ctx, want.name)
		require.NoError(t, err)
		require.Equal(t, "trend", fib.Kind)
		require.False(t, fib.Enabled, "off until turned on in the dashboard")
		require.Equal(t, "live", fib.Mode)
		require.Equal(t, want.address, fib.APIAddress)
		require.False(t, fib.TradePanel)
		require.True(t, fib.TelegramControl)
		require.Equal(t, Params{"strategy": "fibonacci", "pair": want.pair, "timeframe": "15m", "lot": "0.01",
			"leverage": "20", "fib-swing": "pivot", "fib-pivot": "5", "fib-min-impulse-atr": "3",
			"fib-entry-low": "0.5", "fib-entry-high": "0.618", "fib-trigger": "close", "fib-stop-level": "0.786",
			"fib-stop-buffer-atr": "0.25", "fib-target": "extreme"}, fib.Params)
		require.Equal(t, "http://mt5:18812", fib.Account.BridgeURL)
		require.Equal(t, "nb:"+want.name, *fib.Account.Comment)
		require.Empty(t, fib.Account.BridgeToken)
		require.Nil(t, fib.LLM)
		require.Less(t, smc.Position, fib.Position)
		require.Equal(t, smc.Position+1+i, fib.Position, "in the order of the file")
	}

	// the strategy fimathe, without LLM, on both pairs, a magic number of its own
	fimathe, err := s.AgentByName(ctx, "fimathe")
	require.NoError(t, err)
	require.Equal(t, "breakout", fimathe.Kind)
	require.False(t, fimathe.Enabled, "off until turned on in the dashboard")
	require.Equal(t, "live", fimathe.Mode)
	require.Equal(t, "localhost:5573", fimathe.APIAddress)
	require.False(t, fimathe.TradePanel)
	require.True(t, fimathe.TelegramControl)
	require.Nil(t, fimathe.Reanalysis)
	require.Equal(t, Params{"strategy": "fimathe", "pair": "XAUUSDm;BTCUSDm", "timeframe": "15m", "lot": "0.01",
		"leverage": "20", "fimathe-analysis-timeframe": "1h", "fimathe-mapping": "week", "fimathe-mark": "auto",
		"fimathe-trail": "surf", "fimathe-trail-step": "0.5", "fimathe-max-risk-pct": "3"}, fimathe.Params)
	require.Equal(t, "http://mt5:18812", fimathe.Account.BridgeURL)
	require.Equal(t, "nb:fimathe", *fimathe.Account.Comment)
	require.Empty(t, fimathe.Account.BridgeToken)
	require.Nil(t, fimathe.LLM)
	require.Equal(t, smc.Position+3, fimathe.Position, "in the order of the file")

	// run again on every migration: nothing doubled, an edit kept
	params := llm.AllParams()
	params["lot"] = "BTCUSDm=0.05;XAUUSDm=0.05"
	_, err = s.UpdateAgent(ctx, llm.ID, AgentUpdate{Params: params})
	require.NoError(t, err)
	pgtest.MigrateSeeded(t, dsn, "..")
	agents, err = s.Agents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 6)
	llm, err = s.AgentByName(ctx, "llm")
	require.NoError(t, err)
	require.Equal(t, "BTCUSDm=0.05;XAUUSDm=0.05", llm.Params["lot"], "the edit kept")

	// an agent deleted never comes back
	require.NoError(t, s.DeleteAgent(ctx, matrend.ID))
	pgtest.MigrateSeeded(t, dsn, "..")
	_, err = s.AgentByName(ctx, "matrend-btc")
	require.ErrorIs(t, err, ErrNotFound)
	agents, err = s.Agents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 5)

	// nor one renamed under its old name
	renamed, err := s.RenameAgent(ctx, smc.ID, "llm-liquidez-smc")
	require.NoError(t, err)
	pgtest.MigrateSeeded(t, dsn, "..")
	_, err = s.AgentByName(ctx, "llm-smc")
	require.ErrorIs(t, err, ErrNotFound, "not seeded again")
	agents, err = s.Agents(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"llm", renamed.Name, "fibonacci-btc", "fibonacci-xau", "fimathe"}, names(agents))
}
