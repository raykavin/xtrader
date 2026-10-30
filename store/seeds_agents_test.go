package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/internal/pgtest"
)

// The agents of a new database come from sql/seeds/0004_agents.sql: the
// agents the database had, an LLM agent of the prompt support_resistance, and
// an agent without LLM of each strategy of the code, all of them off and
// without secrets, on XAUUSDm and BTCUSDm with a lot of 0.01 on each: inserted
// once, never overwriting an edit nor bringing back an agent deleted or
// renamed.
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
	require.Equal(t, append([]string{"llm-trend", "matrend", "llm-smc-liquidity", "fibonacci", "fimathe",
		"llm-support-resistance"}, codeStrategies...), names(agents), "in the order of the file")
	for i, agent := range agents {
		require.False(t, agent.Enabled, "%s: off until its secrets are set", agent.Name)
		require.Equal(t, "live", agent.Mode)
		require.True(t, agent.Launched)
		require.True(t, agent.Restart)
		require.False(t, agent.TradePanel)
		require.True(t, agent.TelegramControl)
		require.Equal(t, i, agent.Position)
		require.Equal(t, "XAUUSDm;BTCUSDm", agent.Params["pair"], "%s: the default pairs", agent.Name)
		require.Equal(t, "XAUUSDm=0.01;BTCUSDm=0.01", agent.Params["lot"], "%s: 0.01 on each", agent.Name)
		require.NotNil(t, agent.Account)
		require.Equal(t, "mt5", agent.Account.Exchange)
		require.Equal(t, "http://mt5:18812", agent.Account.BridgeURL)
		require.Empty(t, agent.Account.BridgeToken, "%s: no secret in the seeds", agent.Name)
		require.Nil(t, agent.Account.Magic, "the hash of the comment")
		require.False(t, agent.Account.RealAllowed)
		secrets, err := s.SecretsOf(agent)
		require.NoError(t, err)
		require.Equal(t, AgentSecretValues{}, secrets, "%s: no secret in the seeds", agent.Name)
	}
	byName := map[string]Agent{}
	for _, agent := range agents {
		byName[agent.Name] = agent
	}

	// the two LLM agents, each with its prompt, its model and its parameters
	llm := byName["llm-trend"]
	require.Equal(t, "llm", llm.Kind)
	require.Equal(t, "localhost:5580", llm.APIAddress)
	require.True(t, llm.ReanalysisAllowed(), "its reanalysis allowed")
	require.Equal(t, Params{"pair": "XAUUSDm;BTCUSDm", "lot": "XAUUSDm=0.01;BTCUSDm=0.01", "timeframe": "15m",
		"leverage": "400"}, llm.Params)
	require.Nil(t, llm.Account.Comment, "the default of an LLM agent, nb:llm")
	require.Equal(t, "nb:llm", llm.MT5Book().Comment)
	require.NotNil(t, llm.LLM)
	require.Equal(t, "openai", llm.LLM.Provider)
	require.Equal(t, "gpt-6-astra", llm.LLM.Model)
	require.Equal(t, "high", *llm.LLM.ReasoningEffort)
	require.Nil(t, llm.LLM.BaseURL)
	require.Nil(t, llm.LLM.MaxTokens)
	require.Nil(t, llm.LLM.PromptStatus, "no state of a run before")
	require.Empty(t, llm.LLM.APIKey, "no secret in the seeds")
	require.Equal(t, "http://mt5:22347/mcp", *llm.LLM.CalendarURL)
	trend, err := s.PromptByName(ctx, "trend_following")
	require.NoError(t, err)
	require.Equal(t, trend.ID, *llm.LLM.PromptID)
	require.Equal(t, Params{"llm-setup": "free", "llm-candles": "60", "llm-min-stop-atr": "1",
		"llm-max-spread-atr": "0.2", "calendar-importance": "high", "llm-consult-interval": "15m",
		"llm-higher-timeframes": "1h;4h", "llm-higher-trend-filter": "true", "llm-higher-trend-required": "false"},
		llm.LLM.Params)

	smc := byName["llm-smc-liquidity"]
	require.Equal(t, "llm", smc.Kind)
	require.Equal(t, "localhost:5581", smc.APIAddress)
	require.True(t, smc.ReanalysisAllowed())
	require.Equal(t, Params{"pair": "XAUUSDm;BTCUSDm", "lot": "XAUUSDm=0.01;BTCUSDm=0.01", "timeframe": "5m",
		"leverage": "400"}, smc.Params)
	require.Equal(t, "nb:llm-smc", *smc.Account.Comment, "a magic number of its own")
	require.NotEqual(t, llm.MT5Book().Magic, smc.MT5Book().Magic)
	require.Equal(t, "gpt-6-astra", smc.LLM.Model)
	require.Equal(t, "high", *smc.LLM.ReasoningEffort)
	ict, err := s.PromptByName(ctx, "smc_ict")
	require.NoError(t, err)
	require.Equal(t, ict.ID, *smc.LLM.PromptID)
	require.Equal(t, Params{"llm-setup": "free", "llm-candles": "2000", "llm-timeout": "3m",
		"calendar-importance": "high", "llm-consult-interval": "30m", "llm-higher-timeframes": "1h"}, smc.LLM.Params)

	sr := byName["llm-support-resistance"]
	require.Equal(t, "llm", sr.Kind)
	require.Equal(t, "localhost:5582", sr.APIAddress)
	require.True(t, sr.ReanalysisAllowed())
	require.Equal(t, Params{"pair": "XAUUSDm;BTCUSDm", "lot": "XAUUSDm=0.01;BTCUSDm=0.01", "timeframe": "15m",
		"leverage": "400"}, sr.Params)
	require.Equal(t, "nb:llm-support-resistance", *sr.Account.Comment, "apart from llm-trend")
	require.Equal(t, "gpt-6-astra", sr.LLM.Model)
	require.Equal(t, "high", *sr.LLM.ReasoningEffort)
	require.Equal(t, "http://mt5:22347/mcp", *sr.LLM.CalendarURL)
	levels, err := s.PromptByName(ctx, "support_resistance")
	require.NoError(t, err)
	require.Equal(t, levels.ID, *sr.LLM.PromptID)
	require.Equal(t, Params{"llm-setup": "free", "llm-candles": "60", "llm-min-rr": "1.5",
		"calendar-importance": "high", "llm-consult-interval": "15m"}, sr.LLM.Params)

	// the agents of the strategies of the code, without LLM
	for i, name := range codeStrategies {
		agent := byName[name]
		require.Equal(t, Params{"strategy": name, "pair": "XAUUSDm;BTCUSDm", "lot": "XAUUSDm=0.01;BTCUSDm=0.01",
			"leverage": "2000"}, agent.Params, "%s: on the timeframe of its strategy", name)
		require.Equal(t, fmt.Sprintf("localhost:%d", 5600+i), agent.APIAddress)
		require.Nil(t, agent.LLM)
		require.Nil(t, agent.Reanalysis)
		require.Nil(t, agent.Account.Comment)
		require.Equal(t, "nb:"+name, agent.MT5Book().Comment, "the default of its strategy")
	}
	for kind, count := range map[string]int{"trend": 14, "momentum": 7, "breakout": 11, "intraday": 8} {
		n := 0
		for _, name := range codeStrategies {
			if byName[name].Kind == kind {
				n++
			}
		}
		require.Equal(t, count, n, kind)
	}
	matrend := byName["matrend"]
	require.Equal(t, "trend", matrend.Kind)
	require.Equal(t, "localhost:5570", matrend.APIAddress)
	require.Nil(t, matrend.Reanalysis, "no reanalysis without a language model")
	require.Nil(t, matrend.LLM)
	require.Nil(t, matrend.Account.Comment, "the default of its strategy, nb:matrend")
	require.Equal(t, Params{"strategy": "matrend", "pair": "XAUUSDm;BTCUSDm", "lot": "XAUUSDm=0.01;BTCUSDm=0.01",
		"timeframe": "5m", "leverage": "400", "stop-value": "5.4", "take-profit-value": "10.8", "max-distance": "0.5"},
		matrend.Params)

	fib := byName["fibonacci"]
	require.Equal(t, "trend", fib.Kind)
	require.Equal(t, "localhost:5572", fib.APIAddress)
	require.Equal(t, "nb:fibonacci-xau", *fib.Account.Comment, "the magic number of its positions kept")
	require.Nil(t, fib.LLM)
	require.Equal(t, Params{"strategy": "fibonacci", "pair": "XAUUSDm;BTCUSDm", "lot": "XAUUSDm=0.01;BTCUSDm=0.01",
		"timeframe": "15m", "leverage": "2000", "fib-swing": "pivot", "fib-pivot": "5", "fib-min-impulse-atr": "3",
		"fib-entry-low": "0.5", "fib-entry-high": "0.618", "fib-trigger": "close", "fib-stop-level": "0.786",
		"fib-stop-buffer-atr": "0.25", "fib-target": "extreme"}, fib.Params)

	fimathe := byName["fimathe"]
	require.Equal(t, "breakout", fimathe.Kind)
	require.Equal(t, "localhost:5573", fimathe.APIAddress)
	require.Equal(t, "nb:fimathe", *fimathe.Account.Comment)
	require.Nil(t, fimathe.LLM)
	require.Equal(t, Params{"strategy": "fimathe", "pair": "XAUUSDm;BTCUSDm", "lot": "XAUUSDm=0.01;BTCUSDm=0.01",
		"timeframe": "15m", "leverage": "20", "fimathe-analysis-timeframe": "1h", "fimathe-mapping": "week",
		"fimathe-mark": "auto", "fimathe-trail": "surf", "fimathe-trail-step": "0.5", "fimathe-max-risk-pct": "3"},
		fimathe.Params)

	// each one as the dashboard would save it: its settings valid
	for _, agent := range agents {
		_, err := s.UpdateAgent(ctx, agent.ID, AgentUpdate{Params: agent.AllParams()})
		require.NoError(t, err, "%s: its settings as seeded", agent.Name)
	}

	// each trades positions of its own on the account
	for i, a := range agents {
		for _, b := range agents[i+1:] {
			require.False(t, a.MT5Book().Overlaps(b.MT5Book()), "%s and %s", a.Name, b.Name)
		}
	}

	// no user, no Telegram token, no state of a run: the secrets are set in
	// the dashboard, the first user by bootstrap
	var users, tokens, orders int64
	require.NoError(t, s.db.Table("users").Count(&users).Error)
	require.NoError(t, s.db.Table("telegram_settings").Where("token IS NOT NULL OR enabled").Count(&tokens).Error)
	require.NoError(t, s.db.Table("orders").Count(&orders).Error)
	require.Zero(t, users)
	require.Zero(t, tokens, "Telegram off, without a token")
	require.Zero(t, orders)

	// run again on every migration: nothing doubled, an edit kept
	params := llm.AllParams()
	params["lot"] = "XAUUSDm=0.02;BTCUSDm=0.02"
	_, err = s.UpdateAgent(ctx, llm.ID, AgentUpdate{Params: params})
	require.NoError(t, err)
	pgtest.MigrateSeeded(t, dsn, "..")
	agents, err = s.Agents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 46)
	llm, err = s.AgentByName(ctx, "llm-trend")
	require.NoError(t, err)
	require.Equal(t, "XAUUSDm=0.02;BTCUSDm=0.02", llm.Params["lot"], "the edit kept")

	// an agent deleted never comes back
	require.NoError(t, s.DeleteAgent(ctx, matrend.ID))
	pgtest.MigrateSeeded(t, dsn, "..")
	_, err = s.AgentByName(ctx, "matrend")
	require.ErrorIs(t, err, ErrNotFound)

	// nor one renamed under its old name
	renamed, err := s.RenameAgent(ctx, smc.ID, "llm-smc")
	require.NoError(t, err)
	pgtest.MigrateSeeded(t, dsn, "..")
	_, err = s.AgentByName(ctx, "llm-smc-liquidity")
	require.ErrorIs(t, err, ErrNotFound, "not seeded again")
	agents, err = s.Agents(ctx)
	require.NoError(t, err)
	require.Equal(t, append([]string{"llm-trend", renamed.Name, "fibonacci", "fimathe", "llm-support-resistance"},
		codeStrategies...), names(agents))
}

// The prompts of a new database: the ones of the strategies only a language
// model trades and the ones created in the dashboard, each with its first
// version, the same content, once however many times the seeds run; none of
// the strategies of the code nor of the rules of prompts coded, traded by
// agents without LLM.
func TestSeeds_ThePromptsOfANewDatabase(t *testing.T) {
	dsn := testDatabase(t)
	pgtest.MigrateSeeded(t, dsn, "..")
	s, err := Open(config.Database{DSN: dsn, MaxOpenConns: 2, ConnMaxLifetime: time.Minute, LogLevel: "silent"},
		testKey)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	prompts, err := s.Prompts(ctx)
	require.NoError(t, err)
	require.Len(t, prompts, 4)
	for _, name := range []string{"smc_ict", "support_resistance", "trend_following", "trend_following_v0"} {
		_, err := s.PromptByName(ctx, name)
		require.NoError(t, err, name)
	}
	for _, name := range []string{"emacross", "breakout", "default", "mean_reversion", "scalp_1m", "scalp_1m_btc"} {
		_, err = s.PromptByName(ctx, name)
		require.ErrorIs(t, err, ErrNotFound, "%s: a strategy of the code", name)
	}
	copied, err := s.PromptByName(ctx, "trend_following_v0")
	require.NoError(t, err)
	original, err := s.PromptByName(ctx, "trend_following")
	require.NoError(t, err)
	require.Equal(t, original.Content, copied.Content, "a copy of trend_following")
	require.Equal(t, 1, copied.Version)
	var versions, mismatched int64
	require.NoError(t, s.db.Table("prompt_versions").Count(&versions).Error)
	require.NoError(t, s.db.Raw("SELECT count(*) FROM prompt_versions v JOIN prompts p ON p.id = v.prompt_id "+
		"WHERE v.version <> p.version OR v.content <> p.content OR v.created_by <> 'seed'").Scan(&mismatched).Error)
	require.EqualValues(t, 4, versions, "the first version of each")
	require.Zero(t, mismatched)

	pgtest.MigrateSeeded(t, dsn, "..")
	prompts, err = s.Prompts(ctx)
	require.NoError(t, err)
	require.Len(t, prompts, 4, "once")
}

// codeStrategies are the strategies of the code the seeds create an agent of,
// in the order of the file: the ones of agents/trendagent, momentumagent,
// breakoutagent and intradayagent but matrend, fibonacci and fimathe, seeded
// before them with settings of their own, the rules of the prompts coded last.
var codeStrategies = []string{
	"trendconfluence", "emapullback", "emacross", "ema200", "ribbon", "supertrend", "emasupertrend", "psarema",
	"atrtrailing", "ichimoku", "ichimokuadx", "mtftrend", "mtfema",
	"adxema", "macdtrend", "emamacd", "emarsiadx", "rsitrend", "stochpullback", "papullback",
	"atrbreakout", "donchian", "channel", "adxdonchian", "donchianatr", "bbbreakout", "keltner", "volumebreakout",
	"highlow", "momentumbreakout",
	"orb", "asialondon", "vwap", "sweep", "zscore",
	"meanreversion", "scalp1m", "scalp1mbtc", "trendpullback", "squeezebreakout",
}
