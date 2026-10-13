// Command trendagent runs the trend following strategies: trendconfluence (RK
// Trend Confluence), matrend (MA Trend: the break of the SMA 200 with the EMA
// 9/21 on its side), emapullback (the pullbacks to the fast EMA in a trend of
// two EMAs), fibonacci (the pullbacks of the impulses to their Fibonacci
// retracements), and the signal strategies emacross, ema200, ribbon,
// supertrend, emasupertrend, psarema, atrtrailing, ichimoku, ichimokuadx,
// mtftrend and mtfema, see strategies.NewSignal. See runner.Run for the shared
// flags:
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
//	# the pullbacks to 50%-61.8% of the impulses of 3 ATR between swings of 5
//	# candles, on 15m, the stop loss beyond 78.6% and the take profit at the
//	# extreme of the impulse; then a limit at 61.8% with the target at the
//	# extension 1.618, the impulses on the side of the EMA 200 only
//	go run ./agents/trendagent -strategy fibonacci -mode paper -pair XAUUSDm -lot 0.01
//	go run ./agents/trendagent -strategy fibonacci -mode paper -pair XAUUSDm -lot 0.01 \
//		-fib-trigger limit -fib-target extension -fib-target-level 1.618 -fib-trend-ema 200
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
	"slices"

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
	fib := strategies.DefaultFibonacciConfig()
	f := flags{
		maxDistance:  flag.Float64("max-distance", matrend.MaxTrendDistancePct, "matrend: how far the close may be from the SMA 200 at an entry, in % of it"),
		stopBuffer:   flag.Float64("stop-buffer-atr", matrend.StopBufferATR, "matrend: initial stop beyond the SMA 200, in ATR"),
		brokerStopOn: flag.Bool("broker-stop", false, "matrend: also place the stop at the broker and move it there (mt5 or the paper wallet)"),
		brokerStop:   flag.Float64("broker-stop-atr", 0, "trendconfluence: stop placed at the broker with the entry, from the entry close in ATR: it closes the position when touched, even with the bot off (0: off; mt5 or the paper wallet)"),
		brokerTrail:  flag.Float64("broker-trailing-atr", 0, "trendconfluence: stop placed at the broker trailing the best price since the entry, in ATR, moved on every candle close (0: off; mt5 or the paper wallet)"),
		fastEMA:      flag.Int("fast-ema", 0, "emapullback: period of the fast EMA (default 20)"),
		slowEMA:      flag.Int("slow-ema", 0, "emapullback: period of the slow EMA (default 50)"),
		fib: fibFlags{
			swing:       flag.String("fib-swing", fib.Swing, "fibonacci: how the impulse is found: pivot, from a swing to the opposite one after it (see -fib-pivot), or range, from the lowest low to the highest high of -fib-lookback candles"),
			pivot:       flag.Int("fib-pivot", fib.PivotLen, "fibonacci: candles on each side of a swing, which is known that many candles later"),
			lookback:    flag.Int("fib-lookback", fib.Lookback, "fibonacci: candles the impulse is looked for in"),
			minImpulse:  flag.Float64("fib-min-impulse-atr", fib.MinImpulseATR, "fibonacci: shortest impulse traded, in ATR at its extreme"),
			trendEMA:    flag.Int("fib-trend-ema", fib.TrendEMA, "fibonacci: trade only the pullbacks on the side of the EMA of that period, a BUY with the close above it (0: every impulse)"),
			entryLow:    flag.Float64("fib-entry-low", fib.EntryLow, "fibonacci: retracement where the entry zone starts, from the extreme of the impulse (0) to its origin (1)"),
			entryHigh:   flag.Float64("fib-entry-high", fib.EntryHigh, "fibonacci: retracement where the entry zone ends"),
			trigger:     flag.String("fib-trigger", fib.Trigger, "fibonacci: close, at the market on the close of a candle whose wick reached -fib-entry-low, closing in the direction of the impulse no further than -fib-entry-high, or limit, a limit order at -fib-limit-level"),
			limit:       flag.Float64("fib-limit-level", fib.LimitLevel, "fibonacci: retracement of the limit entry, within the entry zone (-fib-trigger limit)"),
			pendingBars: flag.Int("fib-pending-bars", fib.PendingBars, "fibonacci: cancel the limit entry not filled after that many candles (0: once the impulse ends only)"),
			stopLevel:   flag.Float64("fib-stop-level", fib.StopLevel, "fibonacci: retracement of the stop loss, beyond the entry zone, 1 at the origin of the impulse"),
			stopBuffer:  flag.Float64("fib-stop-buffer-atr", fib.StopBufferATR, "fibonacci: stop loss beyond -fib-stop-level, in ATR"),
			target:      flag.String("fib-target", fib.Target, "fibonacci: take profit at the extreme of the impulse (extreme), at -fib-target-level times the impulse from its origin (extension), at -fib-reward-risk times the risk (rr), or none, with -trailing-atr"),
			targetLevel: flag.Float64("fib-target-level", fib.TargetLevel, "fibonacci: extension of the take profit, above 1, eg: 1.272 or 1.618 (-fib-target extension)"),
			rewardRisk:  flag.Float64("fib-reward-risk", fib.RewardRisk, "fibonacci: take profit at that many times the distance to the stop loss (-fib-target rr)"),
		},
	}
	return runner.Agent{Name: "trendagent", Strategies: append([]runner.Strategy{
		{Name: "trendconfluence", About: "RK Trend Confluence", New: f.trendConfluence,
			Inputs: []string{"stop-atr", "trailing-atr", "broker-stop-atr", "broker-trailing-atr"}},
		{Name: "matrend", About: "the break of the SMA 200 with the EMA 9/21 on its side", New: f.maTrend,
			Inputs: []string{"trailing-atr", "max-distance", "stop-buffer-atr", "broker-stop"}},
		{Name: "emapullback", About: "the pullbacks to the fast EMA in a trend of two EMAs", New: f.emaPullback,
			Inputs: append([]string{"fast-ema", "slow-ema"}, runner.BracketInputs...)},
		{Name: "fibonacci", About: "the pullbacks of the impulses to their Fibonacci retracements", New: f.fibonacci,
			Inputs: append(slices.Clone(fibInputs), "trailing-atr", "max-bars", "min-atr-pct")},
	}, runner.Signals("emacross", "ema200", "ribbon", "supertrend", "emasupertrend", "psarema", "atrtrailing",
		"ichimoku", "ichimokuadx", "mtftrend", "mtfema")...)}
}

// flags holds the flags of the strategies of the agent.
type flags struct {
	maxDistance, stopBuffer *float64
	brokerStopOn            *bool
	brokerStop, brokerTrail *float64
	fastEMA, slowEMA        *int
	fib                     fibFlags
}

// fibFlags holds the flags of fibonacci, named in fibInputs.
type fibFlags struct {
	swing, trigger, target          *string
	pivot, lookback, trendEMA       *int
	pendingBars                     *int
	minImpulse, entryLow, entryHigh *float64
	limit, stopLevel, stopBuffer    *float64
	targetLevel, rewardRisk         *float64
}

// fibInputs are the flags of fibonacci of its own.
var fibInputs = []string{"fib-swing", "fib-pivot", "fib-lookback", "fib-min-impulse-atr", "fib-trend-ema",
	"fib-entry-low", "fib-entry-high", "fib-trigger", "fib-limit-level", "fib-pending-bars", "fib-stop-level",
	"fib-stop-buffer-atr", "fib-target", "fib-target-level", "fib-reward-risk"}

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

func (f flags) fibonacci(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultFibonacciConfig()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	if in.Set["trailing-atr"] {
		cfg.TrailingATR = in.TrailingATR
	}
	cfg.Swing, cfg.PivotLen, cfg.Lookback = *f.fib.swing, *f.fib.pivot, *f.fib.lookback
	cfg.MinImpulseATR, cfg.TrendEMA = *f.fib.minImpulse, *f.fib.trendEMA
	cfg.EntryLow, cfg.EntryHigh = *f.fib.entryLow, *f.fib.entryHigh
	cfg.Trigger, cfg.LimitLevel, cfg.PendingBars = *f.fib.trigger, *f.fib.limit, *f.fib.pendingBars
	cfg.StopLevel, cfg.StopBufferATR = *f.fib.stopLevel, *f.fib.stopBuffer
	cfg.Target, cfg.TargetLevel, cfg.RewardRisk = *f.fib.target, *f.fib.targetLevel, *f.fib.rewardRisk
	trader, err := strategies.NewFibonacci(cfg)
	if err != nil {
		return nil, err
	}
	return trader, nil
}
