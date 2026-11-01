// Command momentumagent runs the momentum strategies, each entry placed with
// its stop loss and take profit at the broker (mt5 or the paper wallet), the
// opposite signal closing it: adxema, macdtrend, emamacd, emarsiadx, rsitrend,
// stochpullback and papullback, see strategies.NewSignal; and rsi2 (the RSI 2
// of Connors and Alvarez: the RSI 2 under 10 with the close above the SMA 200
// a buy, over 90 under it a sell, closed on the close beyond the SMA 5, on the
// daily candles of the book, its stop loss and take profit a safety), see
// strategies.NewRSI2. See runner.Run for the shared flags:
//
//	go run ./agents/momentumagent -strategy macdtrend -pair BTCUSDm -csv btc-15m.csv \
//		-timeframe 15m -lot 0.01
//	go run ./agents/momentumagent -strategy stochpullback -mode paper -pair BTCUSDm \
//		-timeframe 5m -lot 0.01 -mt5-comment nb:stochpullback-5m
//	# the RSI 2 on 1d, the levels of the book; then a deeper pullback, without
//	# a take profit: the exit at the SMA 5 alone
//	go run ./agents/momentumagent -strategy rsi2 -mode paper -pair XAUUSDm -lot 0.01
//	go run ./agents/momentumagent -strategy rsi2 -mode paper -pair XAUUSDm -lot 0.01 \
//		-rsi2-oversold 5 -rsi2-overbought 95 -take-profit-atr 0
//
// Launched by the controller, the live mode takes the same flags from the
// settings of the agent in its database.
package main

import (
	"flag"
	"slices"

	"github.com/raykavin/xtrader/agents/internal/runner"
	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/strategy"
)

func main() {
	runner.Run(agent())
}

// agent returns the agent and its strategies, the first one its default, the
// one of agentapi.DefaultStrategy for the kind momentum.
func agent() runner.Agent {
	r2 := strategies.DefaultRSI2Config()
	f := rsi2Flags{
		oversold:   flag.Float64("rsi2-oversold", r2.Oversold, "rsi2: the RSI 2 under it with the close above the SMA 200, a buy"),
		overbought: flag.Float64("rsi2-overbought", r2.Overbought, "rsi2: the RSI 2 over it with the close under the SMA 200, a sell"),
	}
	return runner.Agent{Name: "momentumagent", Strategies: append(runner.TrailingSignals("adxema", "macdtrend",
		"emamacd", "emarsiadx", "rsitrend", "stochpullback", "papullback"),
		runner.Strategy{Name: "rsi2", About: "the RSI 2 under 10 above the SMA 200 (over 90 under it), closed on " +
			"the close beyond the SMA 5, as Connors", New: f.rsi2,
			Inputs: append(append(slices.Clone(runner.BracketInputs), "trailing-atr"), rsi2Inputs...)})}
}

// rsi2Flags holds the flags of rsi2, named in rsi2Inputs: its levels, the
// ones of DefaultRSI2Config unless set.
type rsi2Flags struct {
	oversold, overbought *float64
}

// rsi2Inputs are the flags of rsi2 of its own.
var rsi2Inputs = []string{"rsi2-oversold", "rsi2-overbought"}

func (f rsi2Flags) rsi2(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultRSI2Config()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	if in.Set["trailing-atr"] {
		cfg.TrailingATR = in.TrailingATR
	}
	cfg.Oversold, cfg.Overbought = *f.oversold, *f.overbought
	trader, err := strategies.NewRSI2(cfg)
	return trader, err
}
