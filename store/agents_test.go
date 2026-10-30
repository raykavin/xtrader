package store

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/model"
)

// secret returns a pointer to value, a Secret to write.
func secret(value string) Secret { return &value }

// ptr returns a pointer to value, a field to set.
func ptr[T any](value T) *T { return &value }

// llmAgent creates an LLM agent named name with its account and model, its
// secrets set, its API at an address of its own.
func llmAgent(t *testing.T, s *Store, name string) Agent {
	t.Helper()
	var count int64
	require.NoError(t, s.db.Model(&Agent{}).Count(&count).Error)
	comment := "nb:" + name
	agent, err := s.CreateAgent(context.Background(), Agent{Name: name, Kind: "llm", Enabled: true, Mode: "live",
		APIAddress: fmt.Sprintf("localhost:%d", 5580+count), Launched: true, Restart: true, TradePanel: true,
		Params:  Params{"pair": "BTCUSDm,XAUUSDm", "timeframe": "30m"},
		Account: &Account{BridgeURL: "http://127.0.0.1:18812", Comment: &comment},
		LLM:     &LLMSettings{Provider: "openai", Model: "gpt-5", Params: Params{"llm-min-stop-atr": "1"}}},
		AgentSecrets{LLMAPIKey: secret("sk-test-key"), BridgeToken: secret("bridge-token")})
	require.NoError(t, err)
	return agent
}

// names returns the names of agents.
func names(agents []Agent) []string {
	list := make([]string, 0, len(agents))
	for _, a := range agents {
		list = append(list, a.Name)
	}
	return list
}

func TestCreateAgent_EncryptsItsSecrets(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	require.Equal(t, "llm", agent.Kind)
	require.Equal(t, "30m", agent.Params["timeframe"])
	require.Equal(t, "gpt-5", agent.LLM.Model)
	require.Equal(t, "1", agent.LLM.Params["llm-min-stop-atr"])
	require.Equal(t, "nb:llm", *agent.Account.Comment)
	require.EqualValues(t, 1, agent.OrderIDNext)

	var raw struct{ APIKey []byte }
	require.NoError(t, s.db.Raw("SELECT api_key FROM agent_llm_settings WHERE agent_id = ?", agent.ID).
		Scan(&raw).Error)
	require.NotEmpty(t, raw.APIKey)
	require.NotContains(t, string(raw.APIKey), "sk-test-key", "encrypted at rest")

	secrets, err := s.AgentSecrets(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, AgentSecretValues{LLMAPIKey: "sk-test-key", BridgeToken: "bridge-token"}, secrets)

	require.NoError(t, s.SetAgentSecrets(ctx, agent.ID, AgentSecrets{LLMAPIKey: secret("sk-new")}))
	secrets, err = s.AgentSecrets(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, AgentSecretValues{LLMAPIKey: "sk-new", BridgeToken: "bridge-token"}, secrets,
		"the bridge token kept")

	// another key: the secrets can't be read, never returned garbled
	other, err := Open(config.Database{DSN: s.dsn(t), MaxOpenConns: 2, LogLevel: "silent"},
		[]byte(strings.Repeat("o", 32)))
	require.NoError(t, err)
	defer func() { _ = other.Close() }()
	_, err = other.AgentSecrets(ctx, agent.ID)
	require.ErrorContains(t, err, "another key")
}

func TestCreateAgent_RefusesTheInvalidOnes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	llmAgent(t, s, "llm")
	// trend and llm return a valid agent of their kind, changed by edit
	trend := func(edit func(a *Agent)) Agent {
		a := Agent{Name: "x", Kind: "trend", Mode: "live", APIAddress: "localhost:1", Launched: true,
			Params: Params{"pair": "XAUUSDm"}, Account: &Account{BridgeURL: "http://127.0.0.1:18812"}}
		edit(&a)
		return a
	}
	llm := func(edit func(a *Agent)) Agent {
		a := trend(func(a *Agent) {
			a.Kind, a.LLM = "llm", &LLMSettings{Provider: "openai", Model: "gpt-5"}
		})
		edit(&a)
		return a
	}
	for _, tc := range []struct {
		name    string
		agent   Agent
		secrets AgentSecrets
		is      error
		want    string
	}{
		{"taken", trend(func(a *Agent) { a.Name = "llm" }), AgentSecrets{}, ErrConflict, "already exists: agent llm"},
		{"bad name", trend(func(a *Agent) { a.Name = "a b" }), AgentSecrets{}, ErrInvalid, "agent name"},
		{"bad kind", trend(func(a *Agent) { a.Kind = "grid" }), AgentSecrets{}, ErrInvalid, `kind "grid"`},
		{"bad mode", trend(func(a *Agent) { a.Mode = "backtest" }), AgentSecrets{}, ErrInvalid, "live or paper"},
		{"no address", trend(func(a *Agent) { a.APIAddress = " " }), AgentSecrets{}, ErrInvalid,
			"the address of its API is required"},
		{"address without a port", trend(func(a *Agent) { a.APIAddress = "localhost" }), AgentSecrets{}, ErrInvalid,
			"host:port"},
		{"port 0", trend(func(a *Agent) { a.APIAddress = "localhost:0" }), AgentSecrets{}, ErrInvalid, "host:port"},
		{"port too high", trend(func(a *Agent) { a.APIAddress = "localhost:65536" }), AgentSecrets{}, ErrInvalid,
			"host:port"},
		{"named port", trend(func(a *Agent) { a.APIAddress = "localhost:http" }), AgentSecrets{}, ErrInvalid,
			"host:port"},
		{"no host", trend(func(a *Agent) { a.APIAddress = ":5580" }), AgentSecrets{}, ErrInvalid, "host:port"},
		{"URL", trend(func(a *Agent) { a.APIAddress = "http://localhost:5580" }), AgentSecrets{}, ErrInvalid,
			"host:port"},
		{"address taken", trend(func(a *Agent) { a.APIAddress = "LOCALHOST:5580" }), AgentSecrets{}, ErrConflict,
			"the one of the agent llm"},
		{"address of the loopback taken", trend(func(a *Agent) { a.APIAddress = "127.0.0.1:5580" }), AgentSecrets{},
			ErrConflict, "the one of the agent llm"},
		{"address of every interface", trend(func(a *Agent) { a.APIAddress = "0.0.0.0:5580" }),
			AgentSecrets{}, ErrInvalid, "the host on the loopback"},
		{"address of every IPv6 interface", trend(func(a *Agent) { a.APIAddress = "[::]:5581" }),
			AgentSecrets{}, ErrInvalid, "the host on the loopback"},
		{"address of another machine", trend(func(a *Agent) { a.APIAddress = "10.0.0.5:5581" }),
			AgentSecrets{}, ErrInvalid, "the host on the loopback"},
		{"address of the metadata of a cloud", trend(func(a *Agent) { a.APIAddress = "169.254.169.254:80" }),
			AgentSecrets{}, ErrInvalid, "the host on the loopback"},
		{"name of another machine", trend(func(a *Agent) { a.APIAddress = "bot.lan:5581" }),
			AgentSecrets{}, ErrInvalid, "the host on the loopback"},
		{"model of another kind", trend(func(a *Agent) { a.LLM = &LLMSettings{Provider: "openai", Model: "gpt-5"} }),
			AgentSecrets{}, ErrInvalid, "language model for an agent of kind trend"},
		{"llm without model", llm(func(a *Agent) { a.LLM = nil }), AgentSecrets{}, ErrInvalid,
			"needs its language model"},
		{"empty model", llm(func(a *Agent) { a.LLM.Model = " " }), AgentSecrets{}, ErrInvalid, "needs a model"},
		{"max tokens beyond the column", llm(func(a *Agent) { a.LLM.MaxTokens = ptr(math.MaxInt32 + 1) }),
			AgentSecrets{}, ErrInvalid, "max tokens"},
		{"model of two lines", llm(func(a *Agent) { a.LLM.Model = "gpt\n5" }), AgentSecrets{}, ErrInvalid,
			"a line of text"},
		{"bad provider", llm(func(a *Agent) { a.LLM.Provider = "google" }), AgentSecrets{}, ErrInvalid,
			`provider "google"`},
		{"bad reasoning effort", llm(func(a *Agent) { a.LLM.ReasoningEffort = ptr("extreme") }), AgentSecrets{},
			ErrInvalid, `reasoning effort "extreme"`},
		{"negative max tokens", llm(func(a *Agent) { a.LLM.MaxTokens = ptr(-1) }), AgentSecrets{}, ErrInvalid,
			"max tokens -1"},
		{"bad base URL", llm(func(a *Agent) { a.LLM.BaseURL = ptr("api.openai.com/v1") }), AgentSecrets{},
			ErrInvalid, "the base URL of its language model"},
		{"no bridge URL", trend(func(a *Agent) { a.Account.BridgeURL = "" }), AgentSecrets{}, ErrInvalid,
			"the URL of its bridge is required"},
		{"bad bridge URL", trend(func(a *Agent) { a.Account.BridgeURL = "127.0.0.1:18812" }), AgentSecrets{},
			ErrInvalid, "the URL of its bridge"},
		{"bridge URL with credentials", trend(func(a *Agent) {
			a.Account.BridgeURL = "http://user:hunter2@127.0.0.1:18812"
		}), AgentSecrets{}, ErrInvalid, "without credentials"},
		{"negative magic", trend(func(a *Agent) { a.Account.Magic = ptr(int64(-7)) }), AgentSecrets{}, ErrInvalid,
			"magic number -7"},
		{"long comment", trend(func(a *Agent) { a.Account.Comment = ptr(strings.Repeat("x", 32)) }), AgentSecrets{},
			ErrInvalid, "1 to 31 printable ASCII characters"},
		{"comment of the exits", trend(func(a *Agent) { a.Account.Comment = ptr("[sl 4173]") }), AgentSecrets{},
			ErrInvalid, "not starting with ["},
		{"comment not ASCII", trend(func(a *Agent) { a.Account.Comment = ptr("nb:ação") }), AgentSecrets{},
			ErrInvalid, "printable ASCII"},
		{"param name", trend(func(a *Agent) { a.Params = Params{"Pair": "XAUUSDm"} }), AgentSecrets{}, ErrInvalid,
			`param "Pair"`},
		{"param name of _", trend(func(a *Agent) { a.Params = Params{"max_bars": "5"} }), AgentSecrets{},
			ErrInvalid, `param "max_bars"`},
		{"param name of -", trend(func(a *Agent) { a.Params = Params{"-pair": "XAUUSDm"} }), AgentSecrets{},
			ErrInvalid, `param "-pair"`},
		{"param value of two lines", trend(func(a *Agent) { a.Params = Params{"pair": "XAUUSDm\n-mode=paper"} }),
			AgentSecrets{}, ErrInvalid, "param pair, a line of text"},
		{"param value too long", trend(func(a *Agent) { a.Params = Params{"pair": strings.Repeat("x", 1025)} }),
			AgentSecrets{}, ErrInvalid, "1024 bytes at most"},
		{"param of a language model", trend(func(a *Agent) { a.Params = Params{"llm-min-stop-atr": "1"} }),
			AgentSecrets{}, ErrInvalid, "for an LLM agent only"},
		{"param of a model missing", llm(func(a *Agent) {
			a.LLM, a.Launched, a.Params = nil, false, Params{"calendar-importance": "high"}
		}), AgentSecrets{}, ErrInvalid, "which the agent lacks"},
		{"bridge token without an account", trend(func(a *Agent) { a.Account = nil }),
			AgentSecrets{BridgeToken: secret("bridge")}, ErrInvalid, "without an account"},
		{"key without a language model", trend(func(*Agent) {}), AgentSecrets{LLMAPIKey: secret("sk")}, ErrInvalid,
			"a secret of a language model"},
		{"prompt missing", llm(func(a *Agent) { a.LLM.PromptID = ptr(int64(999999)) }), AgentSecrets{}, ErrInvalid,
			"prompt 999999 doesn't exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateAgent(ctx, tc.agent, tc.secrets)
			require.ErrorIs(t, err, tc.is)
			require.ErrorContains(t, err, tc.want)
			require.NotContains(t, err.Error(), "hunter2", "never a credential")
		})
	}
	for name := range columnFlags {
		_, err := s.CreateAgent(ctx, llm(func(a *Agent) { a.Params = Params{name: "x"} }), AgentSecrets{})
		require.ErrorIs(t, err, ErrInvalid, name)
		require.ErrorContains(t, err, "param "+name+" is a setting of its own", "set by its field")
	}
	agents, err := s.Agents(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"llm"}, names(agents), "none created")
}

func TestCreateAgent_SplitsItsParamsAndGoesLast(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.Equal(t, 0, llmAgent(t, s, "llm").Position)

	params := Params{"pair": "XAUUSDm", "llm-candles": "60", "calendar-importance": "high", "news-block-before": "30m"}
	llmParams := Params{"llm-min-stop-atr": "1"}
	agent, err := s.CreateAgent(ctx, Agent{Name: " gold ", Kind: "llm", Mode: "paper", APIAddress: " localhost:5590 ",
		Launched: true, Params: params,
		Account: &Account{BridgeURL: " http://127.0.0.1:18812 ", Magic: ptr(int64(0)), Comment: ptr(" ")},
		LLM: &LLMSettings{Provider: "anthropic", Model: " claude-sonnet-4-5 ", BaseURL: ptr(""),
			ReasoningEffort: ptr(" "), MaxTokens: ptr(0), Params: llmParams}},
		AgentSecrets{})
	require.NoError(t, err)
	require.Equal(t, "gold", agent.Name)
	require.Equal(t, "localhost:5590", agent.APIAddress)
	require.Equal(t, 1, agent.Position, "after the others")
	require.Equal(t, Params{"pair": "XAUUSDm"}, agent.Params)
	require.Equal(t, Params{"llm-candles": "60", "calendar-importance": "high", "news-block-before": "30m",
		"llm-min-stop-atr": "1"}, agent.LLM.Params, "the ones of the language model kept with it")
	require.Equal(t, Params{"pair": "XAUUSDm", "llm-candles": "60", "calendar-importance": "high",
		"news-block-before": "30m", "llm-min-stop-atr": "1"}, agent.AllParams())
	require.Equal(t, Params{"pair": "XAUUSDm", "llm-candles": "60", "calendar-importance": "high",
		"news-block-before": "30m"}, params, "the params given left as they were")
	require.Equal(t, Params{"llm-min-stop-atr": "1"}, llmParams)

	require.Equal(t, "mt5", agent.Account.Exchange)
	require.Equal(t, "http://127.0.0.1:18812", agent.Account.BridgeURL)
	require.Nil(t, agent.Account.Magic, "0: a hash of the comment")
	require.Nil(t, agent.Account.Comment, `"": the one of the kind`)
	require.Equal(t, "claude-sonnet-4-5", agent.LLM.Model)
	require.Nil(t, agent.LLM.BaseURL)
	require.Nil(t, agent.LLM.ReasoningEffort)
	require.Nil(t, agent.LLM.MaxTokens)

	agents, err := s.Agents(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"llm", "gold"}, names(agents))
}

func TestUpdateAgent_SwitchesAndPrompt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	trend, err := s.CreateAgent(ctx, Agent{Name: "matrend", Kind: "trend", Mode: "live", APIAddress: "localhost:5570",
		Launched: true, Params: Params{"strategy": "matrend"}}, AgentSecrets{})
	require.NoError(t, err)

	off, on := false, true
	updated, err := s.UpdateAgent(ctx, agent.ID, AgentUpdate{Enabled: &off, TelegramControl: &on})
	require.NoError(t, err)
	require.False(t, updated.Enabled)
	require.True(t, updated.TelegramControl)
	require.True(t, updated.TradePanel, "left as it was")
	_, err = s.UpdateAgent(ctx, 999999, AgentUpdate{Enabled: &on})
	require.ErrorIs(t, err, ErrNotFound)

	prompt, err := s.PromptByName(ctx, "smc_ict")
	require.NoError(t, err)
	require.NoError(t, s.SetAgentPrompt(ctx, agent.ID, &prompt.ID))
	updated, err = s.Agent(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, prompt.ID, *updated.LLM.PromptID)
	require.NoError(t, s.SetAgentPrompt(ctx, agent.ID, nil))
	updated, err = s.Agent(ctx, agent.ID)
	require.NoError(t, err)
	require.Nil(t, updated.LLM.PromptID, "the default one")

	missing := int64(999999)
	require.ErrorIs(t, s.SetAgentPrompt(ctx, agent.ID, &missing), ErrNotFound)
	require.ErrorIs(t, s.SetAgentPrompt(ctx, trend.ID, &prompt.ID), ErrInvalid, "not an LLM agent")

	require.NoError(t, s.SetPromptStatus(ctx, agent.ID, PromptStatus{PromptID: prompt.ID, Name: "smc_ict",
		Version: 1, Error: "needs 1441 candles"}))
	updated, err = s.Agent(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, "needs 1441 candles", updated.LLM.PromptStatus.Error)

	agents, err := s.Agents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 2)
	require.Nil(t, agents[1].LLM)
}

func TestUpdateAgent_EditsEveryField(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	require.NoError(t, s.SetPromptStatus(ctx, agent.ID, PromptStatus{Name: "smc_ict", Version: 1, Applied: true}))

	updated, err := s.UpdateAgent(ctx, agent.ID, AgentUpdate{Mode: ptr("paper"), APIAddress: ptr(" localhost:5599 "),
		Launched: ptr(false), Restart: ptr(false),
		Params: Params{"pair": "XAUUSDm", "lot": "0.02", "llm-candles": "80", "calendar-importance": "medium"},
		Account: &AccountUpdate{BridgeURL: ptr("https://bridge.local:18813"), Magic: ptr(int64(4242)),
			Comment: ptr("nb:gold"), RealAllowed: ptr(true)},
		LLM: &LLMUpdate{Provider: ptr("anthropic"), Model: ptr("claude-opus-4-1"),
			BaseURL: ptr("https://api.anthropic.com"), ReasoningEffort: ptr("high"), MaxTokens: ptr(4096)}})
	require.NoError(t, err)
	require.Equal(t, "llm", updated.Name, "never changed")
	require.Equal(t, "llm", updated.Kind, "never changed")
	require.Equal(t, "paper", updated.Mode)
	require.Equal(t, "localhost:5599", updated.APIAddress)
	require.False(t, updated.Launched)
	require.False(t, updated.Restart)
	require.True(t, updated.Enabled, "left as it was")
	require.True(t, updated.TradePanel, "left as it was")
	require.Equal(t, Params{"pair": "XAUUSDm", "lot": "0.02"}, updated.Params, "replaced")
	require.Equal(t, Params{"llm-candles": "80", "calendar-importance": "medium"}, updated.LLM.Params,
		"the ones of the language model with it, llm-min-stop-atr removed")
	require.Equal(t, "https://bridge.local:18813", updated.Account.BridgeURL)
	require.EqualValues(t, 4242, *updated.Account.Magic)
	require.Equal(t, "nb:gold", *updated.Account.Comment)
	require.True(t, updated.Account.RealAllowed)
	require.Equal(t, "anthropic", updated.LLM.Provider)
	require.Equal(t, "claude-opus-4-1", updated.LLM.Model)
	require.Equal(t, "https://api.anthropic.com", *updated.LLM.BaseURL)
	require.Equal(t, "high", *updated.LLM.ReasoningEffort)
	require.Equal(t, 4096, *updated.LLM.MaxTokens)
	require.True(t, updated.LLM.PromptStatus.Applied, "the status of its prompt kept")

	// the optional fields removed, the others left as they are
	updated, err = s.UpdateAgent(ctx, agent.ID, AgentUpdate{Params: Params{},
		Account: &AccountUpdate{Magic: ptr(int64(0)), Comment: ptr("")},
		LLM:     &LLMUpdate{BaseURL: ptr(""), ReasoningEffort: ptr(""), MaxTokens: ptr(0)}})
	require.NoError(t, err)
	require.Empty(t, updated.AllParams(), "{} removes them all")
	require.Nil(t, updated.Account.Magic)
	require.Nil(t, updated.Account.Comment)
	require.Equal(t, "https://bridge.local:18813", updated.Account.BridgeURL)
	require.True(t, updated.Account.RealAllowed)
	require.Nil(t, updated.LLM.BaseURL)
	require.Nil(t, updated.LLM.ReasoningEffort)
	require.Nil(t, updated.LLM.MaxTokens)
	require.Equal(t, "claude-opus-4-1", updated.LLM.Model)
	require.Equal(t, "paper", updated.Mode)

	secrets, err := s.AgentSecrets(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, AgentSecretValues{LLMAPIKey: "sk-test-key", BridgeToken: "bridge-token"}, secrets,
		"the secrets kept")
	_, err = s.UpdateAgent(ctx, agent.ID, AgentUpdate{APIAddress: ptr("LOCALHOST:5599")})
	require.NoError(t, err, "its own address")
}

func TestUpdateAgent_CreatesTheAccountAndTheModelMissing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	trend, err := s.CreateAgent(ctx, Agent{Name: "matrend", Kind: "trend", Mode: "live", APIAddress: "localhost:5570",
		Launched: true}, AgentSecrets{})
	require.NoError(t, err)
	require.Nil(t, trend.Account)
	_, err = s.UpdateAgent(ctx, trend.ID, AgentUpdate{Account: &AccountUpdate{Magic: ptr(int64(7))}})
	require.ErrorContains(t, err, "the URL of its bridge is required", "a new account")
	updated, err := s.UpdateAgent(ctx, trend.ID, AgentUpdate{Account: &AccountUpdate{
		BridgeURL: ptr("http://127.0.0.1:18812"), Magic: ptr(int64(7))}})
	require.NoError(t, err)
	require.Equal(t, "mt5", updated.Account.Exchange)
	require.EqualValues(t, 7, *updated.Account.Magic)
	require.NoError(t, s.SetAgentSecrets(ctx, trend.ID, AgentSecrets{BridgeToken: secret("bridge")}),
		"an account for its token now")
	_, err = s.UpdateAgent(ctx, trend.ID, AgentUpdate{LLM: &LLMUpdate{Provider: ptr("openai"), Model: ptr("gpt-5")}})
	require.ErrorContains(t, err, "a language model for an agent of kind trend")

	llm, err := s.CreateAgent(ctx, Agent{Name: "llm", Kind: "llm", Mode: "live", APIAddress: "localhost:5580"},
		AgentSecrets{})
	require.NoError(t, err, "not launched: without its language model")
	_, err = s.UpdateAgent(ctx, llm.ID, AgentUpdate{Launched: ptr(true)})
	require.ErrorContains(t, err, "needs its language model")
	_, err = s.UpdateAgent(ctx, llm.ID, AgentUpdate{Params: Params{"llm-candles": "60"}})
	require.ErrorContains(t, err, "which the agent lacks")
	_, err = s.UpdateAgent(ctx, llm.ID, AgentUpdate{LLM: &LLMUpdate{Model: ptr("gpt-5")}})
	require.ErrorContains(t, err, `provider ""`, "a new language model")
	updated, err = s.UpdateAgent(ctx, llm.ID, AgentUpdate{Launched: ptr(true),
		Params: Params{"pair": "XAUUSDm", "llm-candles": "60"},
		LLM:    &LLMUpdate{Provider: ptr("openai"), Model: ptr("gpt-5")}})
	require.NoError(t, err)
	require.True(t, updated.Launched)
	require.Equal(t, "gpt-5", updated.LLM.Model)
	require.Equal(t, Params{"pair": "XAUUSDm"}, updated.Params)
	require.Equal(t, Params{"llm-candles": "60"}, updated.LLM.Params)
	require.NoError(t, s.SetAgentSecrets(ctx, llm.ID, AgentSecrets{LLMAPIKey: secret("sk")}),
		"a language model for its key now")
}

func TestUpdateAgent_RefusesTheInvalidOnes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	other := llmAgent(t, s, "other")
	for _, tc := range []struct {
		name   string
		update AgentUpdate
		is     error
		want   string
	}{
		{"mode", AgentUpdate{Mode: ptr("backtest")}, ErrInvalid, "live or paper"},
		{"address", AgentUpdate{APIAddress: ptr("localhost")}, ErrInvalid, "host:port"},
		{"address taken", AgentUpdate{APIAddress: ptr(other.APIAddress)}, ErrConflict, "the one of the agent other"},
		{"param of a field", AgentUpdate{Params: Params{"mt5-magic": "7"}}, ErrInvalid, "a setting of its own"},
		{"param value", AgentUpdate{Params: Params{"pair": "a\r\nb"}}, ErrInvalid, "a line of text"},
		{"param value with its name", AgentUpdate{Params: Params{"lot": "lot = BTCUSDm=0.03,XAUUSDm=0.01"}},
			ErrInvalid, "its value repeats its name"},
		{"bridge URL removed", AgentUpdate{Account: &AccountUpdate{BridgeURL: ptr("")}}, ErrInvalid,
			"the URL of its bridge is required"},
		{"magic", AgentUpdate{Account: &AccountUpdate{Magic: ptr(int64(-1))}}, ErrInvalid, "magic number -1"},
		{"comment", AgentUpdate{Account: &AccountUpdate{Comment: ptr(strings.Repeat("c", 32))}}, ErrInvalid,
			"comment"},
		{"provider", AgentUpdate{LLM: &LLMUpdate{Provider: ptr("mistral")}}, ErrInvalid, `provider "mistral"`},
		{"model removed", AgentUpdate{LLM: &LLMUpdate{Model: ptr("")}}, ErrInvalid, "needs a model"},
		{"reasoning effort", AgentUpdate{LLM: &LLMUpdate{ReasoningEffort: ptr("max")}}, ErrInvalid,
			`reasoning effort "max"`},
		{"max tokens", AgentUpdate{LLM: &LLMUpdate{MaxTokens: ptr(-10)}}, ErrInvalid, "max tokens -10"},
		{"base URL", AgentUpdate{LLM: &LLMUpdate{BaseURL: ptr("file:///etc/passwd")}}, ErrInvalid, "base URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.UpdateAgent(ctx, agent.ID, tc.update)
			require.ErrorIs(t, err, tc.is)
			require.ErrorContains(t, err, tc.want)
		})
	}
	unchanged, err := s.Agent(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, agent, unchanged, "nothing written")

	_, err = s.UpdateAgent(ctx, 999999, AgentUpdate{Mode: ptr("paper")})
	require.ErrorIs(t, err, ErrNotFound)

	// the switches always apply, whatever the rest, eg: a param set in SQL
	require.NoError(t, s.db.Exec(`UPDATE agents SET params = '{"mode": "paper"}' WHERE id = ?`, agent.ID).Error)
	updated, err := s.UpdateAgent(ctx, agent.ID, AgentUpdate{Enabled: ptr(false)})
	require.NoError(t, err)
	require.False(t, updated.Enabled)
	_, err = s.UpdateAgent(ctx, agent.ID, AgentUpdate{Restart: ptr(false)})
	require.ErrorContains(t, err, "param mode is a setting of its own")
}

func TestDeleteAgent_SoftlyTheDisabledOnes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	prompt, err := s.PromptByName(ctx, "trend_following")
	require.NoError(t, err)
	require.NoError(t, s.SetAgentPrompt(ctx, agent.ID, &prompt.ID))
	at := time.Date(2026, 9, 28, 12, 30, 0, 0, time.UTC)
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{{ID: 1, ExchangeID: 9, Pair: "XAUUSDm",
		Side: model.SideTypeSell, Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Price: 4154.2,
		Quantity: 1, CreatedAt: at, UpdatedAt: at}}, true))
	require.NoError(t, s.SaveDecision(ctx, agent.ID, decision("20260928T123000.000000-XAUUSDm", "XAUUSDm", 30, 1),
		nil, true))
	require.NoError(t, s.AppendJournal(ctx, agent.ID, []JournalEntry{{ID: "20260928T123000.000000-XAUUSDm", At: at,
		Pair: "XAUUSDm", Model: "openai gpt-5", Input: []byte(`{"symbol":"XAUUSDm"}`)}}))

	err = s.DeleteAgent(ctx, agent.ID)
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorContains(t, err, "enabled: disable it first")
	_, err = s.UpdateAgent(ctx, agent.ID, AgentUpdate{Enabled: ptr(false)})
	require.NoError(t, err)
	require.NoError(t, s.DeleteAgent(ctx, agent.ID))

	_, err = s.AgentByName(ctx, "llm")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.Agent(ctx, agent.ID)
	require.ErrorIs(t, err, ErrNotFound)
	agents, err := s.Agents(ctx)
	require.NoError(t, err)
	require.Empty(t, agents)
	require.ErrorIs(t, s.DeleteAgent(ctx, agent.ID), ErrNotFound, "deleted already")
	require.ErrorIs(t, s.DeleteAgent(ctx, 999999), ErrNotFound)
	_, _, err = s.LeaseOrderIDs(ctx, agent.ID, 1)
	require.ErrorIs(t, err, ErrNotFound)
	prompts, err := s.Prompts(ctx)
	require.NoError(t, err)
	for _, p := range prompts {
		require.Empty(t, p.UsedBy, "the prompt of the agent deleted free")
	}

	// kept in its table with its data, its secrets removed
	var kept struct {
		DeletedAt                  *time.Time
		Orders, Decisions, Entries int64
		BridgeToken, APIKey        []byte
	}
	require.NoError(t, s.db.Raw(`SELECT a.deleted_at,
		(SELECT count(*) FROM orders WHERE agent_id = a.id) AS orders,
		(SELECT count(*) FROM decisions WHERE agent_id = a.id) AS decisions,
		(SELECT count(*) FROM journal_entries WHERE agent_id = a.id) AS entries,
		c.bridge_token, l.api_key
		FROM agents a JOIN agent_accounts c ON c.agent_id = a.id JOIN agent_llm_settings l ON l.agent_id = a.id
		WHERE a.id = ?`, agent.ID).Scan(&kept).Error)
	require.NotNil(t, kept.DeletedAt)
	require.Equal(t, [3]int64{1, 1, 1}, [3]int64{kept.Orders, kept.Decisions, kept.Entries})
	require.Empty(t, kept.BridgeToken)
	require.Empty(t, kept.APIKey)

	// its name and its address free for a new agent, of its own data
	again := llmAgent(t, s, "llm")
	require.NotEqual(t, agent.ID, again.ID)
	require.Equal(t, agent.APIAddress, again.APIAddress)
	orders, err := s.Orders(ctx, again.ID, 0, 10)
	require.NoError(t, err)
	require.Empty(t, orders, "never the orders of the one deleted")
	decisions, err := s.Decisions(ctx, again.ID, "", 10)
	require.NoError(t, err)
	require.Empty(t, decisions)
}

func TestLeaseOrderIDs_NeverTheSameTwice(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	first, last, err := s.LeaseOrderIDs(ctx, agent.ID, 100)
	require.NoError(t, err)
	require.Equal(t, [2]int64{1, 100}, [2]int64{first, last})
	first, last, err = s.LeaseOrderIDs(ctx, agent.ID, 10)
	require.NoError(t, err)
	require.Equal(t, [2]int64{101, 110}, [2]int64{first, last})

	require.NoError(t, s.ReserveOrderIDs(ctx, agent.ID, 500), "an import up to the order 500")
	first, _, err = s.LeaseOrderIDs(ctx, agent.ID, 1)
	require.NoError(t, err)
	require.EqualValues(t, 501, first)
	require.NoError(t, s.ReserveOrderIDs(ctx, agent.ID, 20), "never back")
	first, _, err = s.LeaseOrderIDs(ctx, agent.ID, 1)
	require.NoError(t, err)
	require.EqualValues(t, 502, first)

	_, _, err = s.LeaseOrderIDs(ctx, 999999, 1)
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = s.LeaseOrderIDs(ctx, agent.ID, 0)
	require.ErrorIs(t, err, ErrInvalid)
}

// The API of an agent answers whoever reaches it: its address is on the
// loopback, where the controller reaches it, never another host nor every
// interface.
func TestValidAddress_OnTheLoopbackOnly(t *testing.T) {
	for address, valid := range map[string]bool{
		"localhost:5580": true, "LocalHost:5580": true, "127.0.0.1:5580": true, "127.1.2.3:1": true,
		"[::1]:65535": true, "localhost": false, "localhost:0": false, "localhost:65536": false,
		"0.0.0.0:5580": false, "[::]:5580": false, "10.0.0.5:5580": false, "192.168.0.10:5580": false,
		"169.254.169.254:80": false, "bot.lan:5580": false, "localhost.evil.example:5580": false,
		"[fe80::1%lo]:5580": false, "[::1%lo]:5580": false, "127.0.0.1.nip.io:5580": false, ":5580": false,
	} {
		assert.Equal(t, valid, validAddress(address), address)
	}
}

func TestUpdateAgent_APIOnTheLoopbackOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	for _, address := range []string{"0.0.0.0:5590", "10.0.0.5:5590", "bot.lan:5590"} {
		_, err := s.UpdateAgent(ctx, agent.ID, AgentUpdate{APIAddress: &address})
		require.ErrorIs(t, err, ErrInvalid, address)
		require.ErrorContains(t, err, "the host on the loopback", address)
	}
	for _, address := range []string{"127.0.0.2:5590", "[::1]:5591", "localhost:5592"} {
		updated, err := s.UpdateAgent(ctx, agent.ID, AgentUpdate{APIAddress: &address})
		require.NoError(t, err, address)
		require.Equal(t, address, updated.APIAddress)
	}
}

// Two agents listen at the same port under the names of the machine, not
// under the ones of two machines.
func TestSameListener(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"localhost:5580", "localhost:5580", true},
		{"localhost:5580", "LOCALHOST:5580", true},
		{"localhost:5580", "127.0.0.1:5580", true},
		{"127.0.0.2:5580", "[::1]:5580", true},
		{"0.0.0.0:5580", "localhost:5580", true},
		{"localhost:5580", "localhost:5581", false},
		{"10.0.0.5:5580", "localhost:5580", false},
		{"10.0.0.5:5580", "10.0.0.6:5580", false},
		{"bot.lan:5580", "BOT.lan:5580", true},
	} {
		assert.Equal(t, c.same, sameListener(c.a, c.b), "%s and %s", c.a, c.b)
	}
}

// tradingAgent returns an agent trading on MT5, enabled and launched in live
// mode, of kind on the bridge, its API at an address of its own, changed by
// edit.
func tradingAgent(name, kind string, port int, edit func(a *Agent)) Agent {
	a := Agent{Name: name, Kind: kind, Enabled: true, Mode: "live", APIAddress: fmt.Sprintf("localhost:%d", port),
		Launched: true, Params: Params{"pair": "XAUUSDm"}, Account: &Account{BridgeURL: "http://127.0.0.1:18812"}}
	if kind == "llm" {
		a.LLM = &LLMSettings{Provider: "openai", Model: "gpt-5"}
	}
	if edit != nil {
		edit(&a)
	}
	return a
}

// Two agents trading the same pair with the same magic number on the bridge
// of an account close, move and net the positions of each other: the second
// is refused, whatever gives it the magic number, its default of the comment
// of its kind or one set; the ones of another bridge, of other pairs, of a
// magic number or a comment of their own, and the ones not trading, aren't.
func TestCreateAgent_RefusesTheSamePositions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.CreateAgent(ctx, tradingAgent("llm", "llm", 5600, nil), AgentSecrets{})
	require.NoError(t, err)
	_, err = s.CreateAgent(ctx, tradingAgent("trend", "trend", 5601, nil), AgentSecrets{})
	require.NoError(t, err, "nb:trendconfluence, another magic number")

	port := 5610
	for _, tc := range []struct {
		name     string
		kind     string
		edit     func(a *Agent)
		conflict string // the agent of the same positions, "" for none
	}{
		{"the same defaults", "llm", nil, "llm"},
		{"the default comment set", "llm", func(a *Agent) { a.Account.Comment = ptr("nb:llm") }, "llm"},
		{"the magic number of the default comment", "llm", func(a *Agent) { a.Account.Magic = ptr(int64(496707372)) },
			"llm"},
		{"the bridge under another name", "llm", func(a *Agent) { a.Account.BridgeURL = "http://LOCALHOST:18812/" },
			"llm"},
		{"a pair in common", "llm", func(a *Agent) { a.Params["pair"] = "BTCUSDm; xauusdm" }, "llm"},
		{"no account, the default bridge", "llm", func(a *Agent) { a.Account = nil }, "llm"},
		{"the default strategy set", "trend", func(a *Agent) { a.Params["strategy"] = "trendconfluence" }, "trend"},
		{"another bridge", "llm", func(a *Agent) { a.Account.BridgeURL = "http://127.0.0.1:18813" }, ""},
		{"another pair", "llm", func(a *Agent) { a.Params["pair"] = "BTCUSDm" }, ""},
		{"a comment of its own", "llm", func(a *Agent) { a.Account.Comment = ptr("nb:llm-2") }, ""},
		{"a magic number of its own", "llm", func(a *Agent) { a.Account.Magic = ptr(int64(42)) }, ""},
		{"the magic number of another kind", "trend", func(a *Agent) { a.Account.Magic = ptr(int64(42)) },
			"a-magic-number-of-its-own"},
		{"another strategy", "trend", func(a *Agent) { a.Params["strategy"] = "matrend" }, ""},
		{"disabled", "llm", func(a *Agent) { a.Enabled = false }, ""},
		{"paper", "llm", func(a *Agent) { a.Mode = "paper" }, ""},
		{"running apart", "llm", func(a *Agent) { a.Launched = false }, "llm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port++
			name := strings.NewReplacer(" ", "-", ",", "").Replace(tc.name)
			created, err := s.CreateAgent(ctx, tradingAgent(name, tc.kind, port, tc.edit), AgentSecrets{})
			if tc.conflict == "" {
				require.NoError(t, err)
				require.Equal(t, name, created.Name)
				return
			}
			require.ErrorIs(t, err, ErrConflict)
			require.ErrorContains(t, err, "agent "+name+": the agent "+tc.conflict+", enabled in live mode, trades")
			_, err = s.AgentByName(ctx, name)
			require.ErrorIs(t, err, ErrNotFound, "nothing written")
		})
	}
}

// B2: an agent running apart (Launched false) trades all the same, the
// controller not seeing it run: an agent trading its positions is refused,
// launched or apart, and so is enabling one of them.
func TestCreateAgent_RefusesThePositionsOfAnAgentRunningApart(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	apart, err := s.CreateAgent(ctx, tradingAgent("apart", "llm", 5600, func(a *Agent) { a.Launched = false }),
		AgentSecrets{})
	require.NoError(t, err)

	_, err = s.CreateAgent(ctx, tradingAgent("launched", "llm", 5601, nil), AgentSecrets{})
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorContains(t, err, "the agent apart, enabled in live mode, trades XAUUSDm")
	_, err = s.CreateAgent(ctx, tradingAgent("apart-too", "llm", 5602, func(a *Agent) { a.Launched = false }),
		AgentSecrets{})
	require.ErrorIs(t, err, ErrConflict, "two of them apart")

	launched, err := s.CreateAgent(ctx, tradingAgent("launched", "llm", 5601, func(a *Agent) { a.Enabled = false }),
		AgentSecrets{})
	require.NoError(t, err, "disabled")
	_, err = s.UpdateAgent(ctx, launched.ID, AgentUpdate{Enabled: ptr(true)})
	require.ErrorIs(t, err, ErrConflict)
	_, err = s.UpdateAgent(ctx, apart.ID, AgentUpdate{Launched: ptr(true)})
	require.NoError(t, err, "launching it changes nothing it trades")
	_, err = s.UpdateAgent(ctx, apart.ID, AgentUpdate{Enabled: ptr(false)})
	require.NoError(t, err)
	_, err = s.UpdateAgent(ctx, launched.ID, AgentUpdate{Enabled: ptr(true)})
	require.NoError(t, err, "the other one disabled")
}

// Enabling an agent, or changing its mode, its params or its account, is
// refused when it would trade the positions of another; disabling it, or
// changing what it trades for its own, isn't, nor are the other switches of
// two agents sharing them already, eg: set in SQL.
func TestUpdateAgent_RefusesTheSamePositions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	trading, err := s.CreateAgent(ctx, tradingAgent("trading", "llm", 5600, nil), AgentSecrets{})
	require.NoError(t, err)
	other, err := s.CreateAgent(ctx, tradingAgent("other", "llm", 5601, func(a *Agent) { a.Enabled = false }),
		AgentSecrets{})
	require.NoError(t, err, "disabled, it may share them")

	refused := func(update AgentUpdate) {
		t.Helper()
		_, err := s.UpdateAgent(ctx, other.ID, update)
		require.ErrorIs(t, err, ErrConflict)
		require.ErrorContains(t, err, "the agent trading, enabled in live mode, trades XAUUSDm")
	}
	refused(AgentUpdate{Enabled: ptr(true)})
	unchanged, err := s.Agent(ctx, other.ID)
	require.NoError(t, err)
	require.False(t, unchanged.Enabled, "nothing written")

	updated, err := s.UpdateAgent(ctx, other.ID, AgentUpdate{Enabled: ptr(true), Mode: ptr("paper")})
	require.NoError(t, err, "a paper agent reads the market data only")
	require.True(t, updated.Enabled)
	refused(AgentUpdate{Mode: ptr("live")})
	_, err = s.UpdateAgent(ctx, other.ID, AgentUpdate{Mode: ptr("live"),
		Account: &AccountUpdate{Comment: ptr("nb:other")}})
	require.NoError(t, err, "a comment of its own")
	refused(AgentUpdate{Account: &AccountUpdate{Comment: ptr("")}})
	refused(AgentUpdate{Account: &AccountUpdate{Magic: ptr(trading.MT5Book().Magic)}})
	refused(AgentUpdate{Account: &AccountUpdate{BridgeURL: ptr("http://localhost:18812"), Comment: ptr("")},
		Params: Params{"pair": "XAUUSDm"}})
	_, err = s.UpdateAgent(ctx, trading.ID, AgentUpdate{Params: Params{"pair": "BTCUSDm"}})
	require.NoError(t, err)
	_, err = s.UpdateAgent(ctx, other.ID, AgentUpdate{Account: &AccountUpdate{Comment: ptr("")}})
	require.NoError(t, err, "the agent trading trades another pair now")
	_, err = s.UpdateAgent(ctx, trading.ID, AgentUpdate{Params: Params{"pair": "BTCUSDm;XAUUSDm"}})
	require.ErrorIs(t, err, ErrConflict, "both ways")
	require.ErrorContains(t, err, "the agent other, enabled in live mode, trades XAUUSDm")

	// two agents sharing them already, set in SQL: the switches apply, and
	// disabling one of them
	require.NoError(t, s.db.Exec(`UPDATE agents SET params = '{"pair": "XAUUSDm"}' WHERE id = ?`, trading.ID).Error)
	_, err = s.UpdateAgent(ctx, trading.ID, AgentUpdate{TradePanel: ptr(true), APIAddress: ptr("localhost:5602")})
	require.NoError(t, err, "what it trades unchanged")
	refused(AgentUpdate{Params: Params{"pair": "XAUUSDm", "lot": "0.01"}})
	_, err = s.UpdateAgent(ctx, other.ID, AgentUpdate{Enabled: ptr(false)})
	require.NoError(t, err)
	_, err = s.UpdateAgent(ctx, trading.ID, AgentUpdate{Params: Params{"pair": "XAUUSDm", "lot": "0.01"}})
	require.NoError(t, err, "the other one disabled")
}

// Two agents enabled at once, each alone free to trade the positions both
// would: the checks apply one at a time, so one of them only is enabled.
func TestUpdateAgent_EnablesOneOfTwoAgentsOfTheSamePositionsAtOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var ids []int64
	for i, name := range []string{"first", "second"} {
		a, err := s.CreateAgent(ctx, tradingAgent(name, "llm", 5600+i, func(a *Agent) { a.Enabled = false }),
			AgentSecrets{})
		require.NoError(t, err)
		ids = append(ids, a.ID)
	}
	for round := 0; round < 5; round++ {
		errs := make(chan error, len(ids))
		start := make(chan struct{})
		for _, id := range ids {
			go func() {
				<-start
				_, err := s.UpdateAgent(ctx, id, AgentUpdate{Enabled: ptr(true)})
				errs <- err
			}()
		}
		close(start)
		var conflicts int
		for range ids {
			if err := <-errs; err != nil {
				require.ErrorIs(t, err, ErrConflict)
				conflicts++
			}
		}
		require.Equal(t, 1, conflicts, "round %d", round)
		agents, err := s.Agents(ctx)
		require.NoError(t, err)
		var enabled int
		for _, a := range agents {
			if a.Enabled {
				enabled++
			}
		}
		require.Equal(t, 1, enabled, "round %d", round)
		for _, id := range ids {
			_, err := s.UpdateAgent(ctx, id, AgentUpdate{Enabled: ptr(false)})
			require.NoError(t, err)
		}
	}
}
