// Command trendagent runs the trend following strategies: trendconfluence (RK
// Trend Confluence), matrend (MA Trend: the break of the SMA 200 with the EMA
// 9/21 on its side), emapullback (the pullbacks to the fast EMA in a trend of
// two EMAs), and the signal strategies emacross, ema200, ribbon, supertrend,
// emasupertrend, psarema, atrtrailing, ichimoku, ichimokuadx, mtftrend and
// mtfema, see strategies.NewSignal. See runner.Run for the shared flags:
//
//	# RK Trend Confluence, a stop on the close 3 ATR from the entry, backed up
//	# by a stop placed at the broker 4 ATR away
//	go run ./agents/trendagent -mode paper -pair XAUUSDm -lot 0.01 \
//		-stop-atr 3 -broker-stop-atr 4
//	# the same with trailing stops: 3 ATR behind the best price on the close,
//	# and 4 ATR at the broker, moved there on every candle close
//	go run ./agents/trendagent -mode paper -pair XAUUSDm -lot 0.01 \
//		-trailing-atr 3 -broker-trailing-atr 4
//	# MA Trend: the close breaking the SMA 200 with the EMA 9/21 already on its
//	# side, at most 2% from it, the stop 1 ATR beyond the SMA 200 then 3 ATR
//	# behind the best price, also placed at the broker
//	go run ./agents/trendagent -strategy matrend -mode paper -pair XAUUSDm -lot 0.01 \
//		-max-distance 2 -stop-buffer-atr 1 -trailing-atr 3 -broker-stop
//	# the pullbacks to the EMA 20 in the trend of the EMA 20/50, on 15m
//	go run ./agents/trendagent -strategy emapullback -mode paper -pair BTCUSDm \
//		-timeframe 15m -lot 0.01
//	# the Supertrend flips on 1h, next to the bot of 5m of the same strategy
//	go run ./agents/trendagent -strategy supertrend -mode paper -pair BTCUSDm \
//		-timeframe 1h -lot 0.01 -mt5-comment nb:supertrend-1h
//
// Launched by the controller, the live mode takes the same flags from the
// settings of the agent in its database.
package main

import (
	"flag"
	"fmt"

	"github.com/raykavin/xtrader/agents/internal/runner"
	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/strategy"
)

func main() {
	runner.Run(agent())
}

// agent returns the agent and its strategies, the first one its default, the
// one of agentapi.DefaultStrategy for the kind trend.
func agent() runner.Agent {
	matrend := strategies.DefaultMATrendConfig()
	f := flags{
		maxDistance:  flag.Float64("max-distance", matrend.MaxTrendDistancePct, "matrend: how far the close may be from the SMA 200 at an entry, in % of it"),
		stopBuffer:   flag.Float64("stop-buffer-atr", matrend.StopBufferATR, "matrend: initial stop beyond the SMA 200, in ATR"),
		brokerStopOn: flag.Bool("broker-stop", false, "matrend: also place the stop at the broker and move it there (mt5 or the paper wallet)"),
		brokerStop:   flag.Float64("broker-stop-atr", 0, "trendconfluence: stop placed at the broker with the entry, from the entry close in ATR: it closes the position when touched, even with the bot off (0: off; mt5 or the paper wallet)"),
		brokerTrail:  flag.Float64("broker-trailing-atr", 0, "trendconfluence: stop placed at the broker trailing the best price since the entry, in ATR, moved on every candle close (0: off; mt5 or the paper wallet)"),
		fastEMA:      flag.Int("fast-ema", 0, "emapullback: period of the fast EMA (default 20)"),
		slowEMA:      flag.Int("slow-ema", 0, "emapullback: period of the slow EMA (default 50)"),
	}
	return runner.Agent{Name: "trendagent", Strategies: append([]runner.Strategy{
		{Name: "trendconfluence", About: "RK Trend Confluence", New: f.trendConfluence,
			Inputs: []string{"stop-atr", "trailing-atr", "broker-stop-atr", "broker-trailing-atr"}},
		{Name: "matrend", About: "the break of the SMA 200 with the EMA 9/21 on its side", New: f.maTrend,
			Inputs: []string{"trailing-atr", "max-distance", "stop-buffer-atr", "broker-stop"}},
		{Name: "emapullback", About: "the pullbacks to the fast EMA in a trend of two EMAs", New: f.emaPullback,
			Inputs: append([]string{"fast-ema", "slow-ema"}, runner.BracketInputs...)},
	}, runner.Signals("emacross", "ema200", "ribbon", "supertrend", "emasupertrend", "psarema", "atrtrailing",
		"ichimoku", "ichimokuadx", "mtftrend", "mtfema")...)}
}

// flags holds the flags of the strategies of the agent.
type flags struct {
	maxDistance, stopBuffer *float64
	brokerStopOn            *bool
	brokerStop, brokerTrail *float64
	fastEMA, slowEMA        *int
}

func (f flags) trendConfluence(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultRKTrendConfluenceConfig()
	cfg.Timeframe = in.Timeframe
	cfg.Direction = in.Direction
	cfg.StopATR = in.StopATR
	cfg.TrailingATR = in.TrailingATR
	cfg.BrokerStopATR = *f.brokerStop
	cfg.BrokerTrailingATR = *f.brokerTrail
	cfg.StopValue = in.StopValue
	cfg.TakeProfitValue = in.TakeProfitValue
	cfg.MinPositionValue = in.MinValue
	cfg.PositionFraction = in.Fraction
	cfg.Leverage = in.Leverage
	cfg.PositionSize = in.Lots
	return strategies.NewRKTrendConfluence(cfg)
}

func (f flags) maTrend(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultMATrendConfig()
	cfg.Timeframe = in.Timeframe
	cfg.Direction = in.Direction
	cfg.MaxTrendDistancePct = *f.maxDistance
	cfg.StopBufferATR = *f.stopBuffer
	if in.Set["trailing-atr"] {
		cfg.TrailingATR = in.TrailingATR
	}
	cfg.BrokerStop = *f.brokerStopOn
	cfg.StopValue = in.StopValue
	cfg.TakeProfitValue = in.TakeProfitValue
	cfg.MinPositionValue = in.MinValue
	cfg.PositionFraction = in.Fraction
	cfg.Leverage = in.Leverage
	cfg.PositionSize = in.Lots
	return strategies.NewMATrend(cfg)
}

func (f flags) emaPullback(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultEMAPullbackConfig()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	if in.Set["fast-ema"] {
		cfg.FastLen = *f.fastEMA
	}
	if in.Set["slow-ema"] {
		cfg.SlowLen = *f.slowEMA
	}
	trader, err := strategies.NewEMAPullback(cfg)
	if err != nil {
		return nil, fmt.Errorf("emapullback: %w", err)
	}
	return trader, nil
}
