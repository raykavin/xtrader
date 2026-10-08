package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
)

// launchedRun runs the agent as the controller launches it, with the settings
// cfg of a fake controller, on the bridge of a fake terminal refusing every
// request: the requests the bridge got, and the error of run. An agent trading
// the book of its settings (see agentapi.Config.CheckMT5Book) connects to it.
func launchedRun(t *testing.T, cfg agentapi.Config) (int64, error) {
	t.Helper()
	var requests atomic.Int64
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, `{"error":"a fake bridge"}`, http.StatusTeapot)
	}))
	t.Cleanup(bridge.Close)
	if cfg.Account != nil {
		cfg.Account.BridgeURL = bridge.URL
	}
	controller := http.NewServeMux()
	controller.HandleFunc("GET "+agentapi.Prefix+"/config", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cfg)
	})
	controller.HandleFunc("POST "+agentapi.Prefix+"/outbox", func(w http.ResponseWriter, r *http.Request) {
		var request agentapi.OutboxRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		var acked uint64
		if n := len(request.Records); n > 0 {
			acked = request.Records[n-1].Seq
		}
		_ = json.NewEncoder(w).Encode(agentapi.OutboxResponse{Acked: acked})
	})
	srv := httptest.NewServer(controller)
	t.Cleanup(srv.Close)
	t.Setenv(agentapi.EnvURL, srv.URL)
	t.Setenv(agentapi.EnvToken, "the-token")
	t.Setenv(agentapi.EnvAgent, cfg.Name)
	t.Chdir(t.TempDir()) // its outbox

	args, commandLine := os.Args, flag.CommandLine
	os.Args, flag.CommandLine = []string{"llmagent"}, flag.NewFlagSet("llmagent", flag.ContinueOnError)
	t.Cleanup(func() { os.Args, flag.CommandLine = args, commandLine })
	err := run()
	return requests.Load(), err
}

// Launched by the controller, an LLM agent trades the book the controller
// keeps apart from the ones of the other agents, its comment and its magic
// number by default as the controller computes them.
func TestRun_TradesTheBookOfItsSettings(t *testing.T) {
	model := func(params map[string]string) *agentapi.LLM {
		return &agentapi.LLM{Provider: "openai", Model: "gpt-5", APIKey: "sk-test", Params: params}
	}
	comment, magic := " nb:gold ", int64(42)
	for _, cfg := range []agentapi.Config{
		{ID: 1, Name: "llm", Kind: "llm", Mode: "live", Account: &agentapi.Account{}, LLM: model(nil)},
		{ID: 2, Name: "gold", Kind: "llm", Mode: "live", Params: map[string]string{"pair": "XAUUSDm;BTCUSDm"},
			Account: &agentapi.Account{Comment: &comment}, LLM: model(map[string]string{"llm-min-stop-atr": "1"})},
		{ID: 3, Name: "paper", Kind: "llm", Mode: "paper", Account: &agentapi.Account{Magic: &magic},
			LLM: model(nil)},
	} {
		t.Run(cfg.Name, func(t *testing.T) {
			requests, err := launchedRun(t, cfg)
			require.ErrorContains(t, err, "mt5: the bridge at", "connected to the bridge of its settings")
			require.NotZero(t, requests)
		})
	}
}

// B2: launched with settings whose book isn't the one it reads from its
// flags, eg: the settings of another kind, of another default comment, the
// agent refuses to trade, never connecting to the bridge: it would trade
// positions the controller doesn't keep apart from the other agents.
func TestRun_RefusesTheBookOfOtherSettings(t *testing.T) {
	cfg := agentapi.Config{ID: 1, Name: "gold", Kind: "trend", Mode: "live", Account: &agentapi.Account{},
		LLM: &agentapi.LLM{Provider: "openai", Model: "gpt-5", APIKey: "sk-test"}}
	requests, err := launchedRun(t, cfg)
	require.ErrorContains(t, err, "the agent gold trades the bridge")
	require.ErrorContains(t, err, `(comment "nb:llm")`)
	require.ErrorContains(t, err, `but its settings give the bridge`)
	require.ErrorContains(t, err, `(comment "nb:trendconfluence")`)
	require.Zero(t, requests, "never connected")
}

// The book of the agent by default is the one the controller and the store
// compute for an LLM agent without settings of its own.
func TestMT5Book_DefaultOfAnLLMAgent(t *testing.T) {
	want := agentapi.NewMT5Book("llm", map[string]string{}, nil)
	require.Equal(t, want.Comment, mt5Comment(""))
	require.Equal(t, want.Magic, mt5Magic(0, mt5Comment("")))
	require.Empty(t, agentapi.DefaultStrategy("llm"), "no strategy: the comment of the kind")
}
