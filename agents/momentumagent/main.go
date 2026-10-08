// Command momentumagent runs the momentum strategies, each entry placed with
// its stop loss and take profit at the broker (mt5 or the paper wallet), the
// opposite signal closing it: adxema, macdtrend, emamacd, emarsiadx, rsitrend,
// stochpullback and papullback, see strategies.NewSignal. See runner.Run for
// the shared flags:
//
//	go run ./agents/momentumagent -strategy macdtrend -pair BTCUSDm -csv btc-15m.csv \
//		-timeframe 15m -lot 0.01
//	go run ./agents/momentumagent -strategy stochpullback -mode paper -pair BTCUSDm \
//		-timeframe 5m -lot 0.01 -mt5-comment nb:stochpullback-5m
//
// Launched by the controller, the live mode takes the same flags from the
// settings of the agent in its database.
package main

import (
	"github.com/raykavin/xtrader/agents/internal/runner"
)

func main() {
	runner.Run(agent())
}

// agent returns the agent and its strategies, the first one its default, the
// one of agentapi.DefaultStrategy for the kind momentum.
func agent() runner.Agent {
	return runner.Agent{Name: "momentumagent", Strategies: runner.TrailingSignals("adxema", "macdtrend", "emamacd",
		"emarsiadx", "rsitrend", "stochpullback", "papullback")}
}
