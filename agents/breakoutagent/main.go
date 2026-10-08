// Command breakoutagent runs the breakout strategies, each entry placed with
// its stop loss and take profit at the broker (mt5 or the paper wallet):
// atrbreakout (the breakouts of the recent range on an expansion of
// the volatility), and the signal strategies donchian, channel, adxdonchian,
// donchianatr, bbbreakout, keltner, volumebreakout, highlow and
// momentumbreakout, see strategies.NewSignal. See runner.Run for the shared
// flags:
//
//	go run ./agents/breakoutagent -strategy atrbreakout -pair XAUUSDm -csv xau-5m.csv -lot 0.01
//	go run ./agents/breakoutagent -strategy donchian -mode paper -pair BTCUSDm \
//		-timeframe 4h -lot 0.01 -mt5-comment nb:donchian-4h
//	# the Turtles: the stop loss 2 ATR away, trailing 3 ATR behind the best price
//	go run ./agents/breakoutagent -strategy donchianatr -mode paper -pair BTCUSDm \
//		-timeframe 1h -lot 0.01 -trailing-atr 3
//
// Launched by the controller, the live mode takes the same flags from the
// settings of the agent in its database.
package main

import (
	"flag"

	"github.com/raykavin/xtrader/agents/internal/runner"
	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/strategy"
)

func main() {
	runner.Run(agent())
}

// agent returns the agent and its strategies, the first one its default, the
// one of agentapi.DefaultStrategy for the kind breakout.
func agent() runner.Agent {
	f := flags{
		bufferATR:   flag.Float64("buffer-atr", 0, "atrbreakout: how far beyond the range the close must end, in ATR"),
		expansion:   flag.Float64("expansion", 0, "atrbreakout: ATR multiple of its average for an expansion (default 1.2)"),
		breakoutLen: flag.Int("breakout-len", 0, "atrbreakout: candles of the channel broken (default 20)"),
	}
	return runner.Agent{Name: "breakoutagent", Strategies: append([]runner.Strategy{
		{Name: "atrbreakout", About: "the breakouts of the recent range on an expansion of the volatility",
			New: f.atrBreakout, Inputs: append([]string{"buffer-atr", "expansion", "breakout-len"}, runner.BracketInputs...)},
	}, runner.Signals("donchian", "channel", "adxdonchian", "donchianatr", "bbbreakout", "keltner",
		"volumebreakout", "highlow", "momentumbreakout")...)}
}

// flags holds the flags of the strategies of the agent.
type flags struct {
	bufferATR, expansion *float64
	breakoutLen          *int
}

func (f flags) atrBreakout(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultATRBreakoutConfig()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	if in.Set["buffer-atr"] {
		cfg.BufferATR = *f.bufferATR
	}
	if in.Set["expansion"] {
		cfg.Expansion = *f.expansion
	}
	if in.Set["breakout-len"] {
		cfg.BreakoutLen = *f.breakoutLen
	}
	trader, err := strategies.NewATRBreakout(cfg)
	return trader, err
}
