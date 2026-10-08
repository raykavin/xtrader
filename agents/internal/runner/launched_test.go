package runner

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

// launchedRun runs agent as the controller launches it, with the settings cfg
// of a fake controller, on the bridge of a fake terminal refusing every
// request: the requests the bridge got, and the error of run. An agent trading
// the book of its settings (see agentapi.Config.CheckMT5Book) connects to it.
func launchedRun(t *testing.T, agent Agent, cfg agentapi.Config) (int64, error) {
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
	srv := httptest.NewServer(controller)
	t.Cleanup(srv.Close)
	t.Setenv(agentapi.EnvURL, srv.URL)
	t.Setenv(agentapi.EnvToken, "the-token")
	t.Setenv(agentapi.EnvAgent, cfg.Name)
	t.Chdir(t.TempDir()) // its outbox

	args, commandLine := os.Args, flag.CommandLine
	os.Args, flag.CommandLine = []string{agent.Name}, flag.NewFlagSet(agent.Name, flag.ContinueOnError)
	t.Cleanup(func() { os.Args, flag.CommandLine = args, commandLine })
	err := run(agent)
	return requests.Load(), err
}

// Launched by the controller, an agent trades the book the controller keeps
// apart from the ones of the other agents, its strategy, its comment and its
// magic number by default as the controller computes them, and refuses to
// trade another one, eg: a binary whose default strategy isn't the one of its
// kind for the controller.
func TestRun_TradesTheBookOfItsSettings(t *testing.T) {
	momentum := Agent{Name: "momentumagent", Strategies: Signals("adxema", "macdtrend")}
	trend := Agent{Name: "trendagent", Strategies: Signals("emacross", "ema200")}
	for _, tc := range []struct {
		name    string
		agent   Agent
		cfg     agentapi.Config
		refused bool
	}{
		{"its defaults", momentum, agentapi.Config{ID: 1, Name: "adx", Kind: "momentum", Mode: "live",
			Account: &agentapi.Account{}}, false},
		{"its strategy, pairs and magic number", trend, agentapi.Config{ID: 2, Name: "cross", Kind: "trend",
			Mode: "live", Params: map[string]string{"strategy": "emacross", "pair": "XAUUSDm, BTCUSDm"},
			Account: &agentapi.Account{Magic: ptrTo(int64(42))}}, false},
		{"its comment, in paper mode", momentum, agentapi.Config{ID: 3, Name: "macd", Kind: "momentum",
			Mode: "paper", Params: map[string]string{"strategy": "macdtrend"},
			Account: &agentapi.Account{Comment: ptrTo(" nb:macd ")}}, false},
		{"another default strategy", trend, agentapi.Config{ID: 4, Name: "trend", Kind: "trend", Mode: "live",
			Account: &agentapi.Account{}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests, err := launchedRun(t, tc.agent, tc.cfg)
			if tc.refused {
				require.ErrorContains(t, err, "the agent "+tc.cfg.Name+" trades the bridge")
				require.ErrorContains(t, err, "(comment \"nb:emacross\")")
				require.ErrorContains(t, err, "but its settings give the bridge")
				require.ErrorContains(t, err, "(comment \"nb:trendconfluence\")")
				require.Zero(t, requests, "never connected")
				return
			}
			require.ErrorContains(t, err, "mt5: the bridge at", "connected to the bridge of its settings")
			require.NotZero(t, requests)
		})
	}
}

func ptrTo[T any](value T) *T { return &value }

// B7: launched with a lot of a decimal comma for two pairs, "1,5", the agent
// refuses to start, never reading it as 1 lot of the first pair and 5 of the
// second, nor connecting to the bridge.
func TestRun_LotWithADecimalCommaRefused(t *testing.T) {
	trend := Agent{Name: "trendagent", Strategies: Signals("emacross", "ema200")}
	cfg := agentapi.Config{ID: 2, Name: "cross", Kind: "trend", Mode: "live",
		Params:  map[string]string{"strategy": "emacross", "pair": "XAUUSDm;BTCUSDm", "lot": "1,5"},
		Account: &agentapi.Account{Magic: ptrTo(int64(42))}}
	requests, err := launchedRun(t, trend, cfg)
	require.ErrorContains(t, err, `-lot: "1,5" is ambiguous, the comma may be a decimal one`)
	require.Zero(t, requests, "never connected")
}
