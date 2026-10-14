// Command breakoutagent runs the breakout strategies, each entry placed with
// its stop loss and take profit at the broker (mt5 or the paper wallet):
// atrbreakout (the breakouts of the recent range on an expansion of
// the volatility), fimathe (the first entry of the trend of the reference
// channel of each week of the Fimathe technique, its levels drawn on the
// chart), squeezebreakout (the release of a compression with the volume: the
// rules of the prompt breakout of the LLM agent, its stop loss moved by the R
// multiple of the position), and the signal strategies donchian, channel, adxdonchian,
// donchianatr, bbbreakout, keltner, volumebreakout, highlow and
// momentumbreakout, see strategies.NewSignal. See runner.Run for the shared
// flags:
//
//	go run ./agents/breakoutagent -strategy atrbreakout -pair XAUUSDm -csv xau-5m.csv -lot 0.01
//	# Fimathe: the analysis on 1h, the entries on 15m, the channel mapped each
//	# week; then mapped each day, with a take profit two levels away and the
//	# stop loss locked at the entry (0x0) after the first 50% of the subcycle
//	go run ./agents/breakoutagent -strategy fimathe -mode paper -pair XAUUSDm -lot 0.01
//	go run ./agents/breakoutagent -strategy fimathe -mode paper -pair XAUUSDm -lot 0.01 \
//		-fimathe-mapping day -fimathe-target-levels 2 -fimathe-trail lock
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
	"errors"
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
// one of agentapi.DefaultStrategy for the kind breakout.
func agent() runner.Agent {
	fm := strategies.DefaultFimatheConfig()
	f := flags{
		bufferATR:   flag.Float64("buffer-atr", 0, "atrbreakout: how far beyond the range the close must end, in ATR"),
		expansion:   flag.Float64("expansion", 0, "atrbreakout: ATR multiple of its average for an expansion (default 1.2)"),
		breakoutLen: flag.Int("breakout-len", 0, "atrbreakout: candles of the channel broken (default 20)"),
		fm: fimatheFlags{
			analysis:   flag.String("fimathe-analysis-timeframe", fm.AnalysisTimeframe, "fimathe: timeframe of the analysis (the trend, the reference channel), longer than -timeframe, the one of the entries, and a multiple of it"),
			mapping:    flag.String("fimathe-mapping", fm.Mapping, "fimathe: when the reference channel is mapped: week (day trade, the start of each week, on Sunday) or day (scalping, each day)"),
			timezone:   flag.String("fimathe-timezone", fm.Timezone, "fimathe: time zone of the midnight starting the weeks and the days, an IANA name"),
			pivot:      flag.Int("fimathe-pivot", fm.PivotLen, "fimathe: bars on each side of a top or a bottom of the analysis timeframe"),
			bars:       flag.Int("fimathe-analysis-bars", fm.AnalysisBars, "fimathe: bars of the analysis timeframe the tops and the bottoms are looked for in"),
			mark:       flag.String("fimathe-mark", fm.Mark, "fimathe: the reference channel on the wicks (wick) or the bodies (body) of its bars, or auto: the wicks, the bodies for a channel wider than -fimathe-max-channel-atr"),
			minChannel: flag.Float64("fimathe-min-channel-atr", fm.MinChannelATR, "fimathe: narrowest reference channel traded, in ATR of the analysis timeframe"),
			maxChannel: flag.Float64("fimathe-max-channel-atr", fm.MaxChannelATR, "fimathe: widest reference channel traded, in ATR of the analysis timeframe"),
			stopBuffer: flag.Float64("fimathe-stop-buffer-atr", fm.StopBufferATR, "fimathe: stop loss beyond the neutral zone, in ATR of -timeframe"),
			target:     flag.Float64("fimathe-target-levels", fm.TargetLevels, "fimathe: take profit at that many heights of the channel from the entry line (0: none; the documented one is 2)"),
			trail:      flag.String("fimathe-trail", fm.Trail, "fimathe: the stop loss moved by the subcycle: surf (to 0x0 at the first step, then one step behind each step passed), lock (to 0x0 at the first step only) or off"),
			trailStep:  flag.Float64("fimathe-trail-step", fm.TrailStep, "fimathe: step of the subcycle moving the stop loss, a share of its height, eg: 0.5 or 1"),
			subcycle:   flag.Int("fimathe-subcycle-pivot", fm.SubcyclePivot, "fimathe: candles on each side of the top and the bottom of the subcycle, on -timeframe"),
			maxRisk:    flag.Float64("fimathe-max-risk-pct", fm.MaxRiskPct, "fimathe: skip an entry losing more than that % of the free balance at its stop loss (0: off): the -max-risk-pct of fimathe, 3 by default, kept for the agents set with it"),
		},
	}
	return runner.Agent{Name: "breakoutagent", Strategies: append([]runner.Strategy{
		{Name: "atrbreakout", About: "the breakouts of the recent range on an expansion of the volatility",
			New: f.atrBreakout, Inputs: append([]string{"buffer-atr", "expansion", "breakout-len"}, runner.BracketInputs...)},
		{Name: "fimathe", About: "the first entry of the trend of the reference channel of the Fimathe technique",
			New: f.fimathe, Inputs: append(slices.Clone(fimatheInputs), "max-bars")},
		{Name: "squeezebreakout", About: "the release of a compression with the volume", New: f.squeezeBreakout,
			Inputs: []string{"min-atr-pct"}},
	}, runner.Signals("donchian", "channel", "adxdonchian", "donchianatr", "bbbreakout", "keltner",
		"volumebreakout", "highlow", "momentumbreakout")...)}
}

// flags holds the flags of the strategies of the agent.
type flags struct {
	bufferATR, expansion *float64
	breakoutLen          *int
	fm                   fimatheFlags
}

// fimatheFlags holds the flags of fimathe, named in fimatheInputs.
type fimatheFlags struct {
	analysis, mapping, timezone, mark, trail   *string
	pivot, bars, subcycle                      *int
	minChannel, maxChannel, stopBuffer, target *float64
	trailStep, maxRisk                         *float64
}

// fimatheInputs are the flags of fimathe of its own.
var fimatheInputs = []string{"fimathe-analysis-timeframe", "fimathe-mapping", "fimathe-timezone", "fimathe-pivot",
	"fimathe-analysis-bars", "fimathe-mark", "fimathe-min-channel-atr", "fimathe-max-channel-atr",
	"fimathe-stop-buffer-atr", "fimathe-target-levels", "fimathe-trail", "fimathe-trail-step",
	"fimathe-subcycle-pivot", "fimathe-max-risk-pct"}

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

func (f flags) fimathe(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultFimatheConfig()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	cfg.AnalysisTimeframe, cfg.Mapping, cfg.Timezone = *f.fm.analysis, *f.fm.mapping, *f.fm.timezone
	cfg.PivotLen, cfg.AnalysisBars, cfg.Mark = *f.fm.pivot, *f.fm.bars, *f.fm.mark
	cfg.MinChannelATR, cfg.MaxChannelATR = *f.fm.minChannel, *f.fm.maxChannel
	cfg.StopBufferATR, cfg.TargetLevels = *f.fm.stopBuffer, *f.fm.target
	cfg.Trail, cfg.TrailStep, cfg.SubcyclePivot = *f.fm.trail, *f.fm.trailStep, *f.fm.subcycle
	if in.Set["fimathe-max-risk-pct"] {
		if in.Set["max-risk-pct"] {
			return nil, errors.New("-max-risk-pct and -fimathe-max-risk-pct are the same limit: set one of them")
		}
		cfg.MaxRiskPct = *f.fm.maxRisk
	}
	trader, err := strategies.NewFimathe(cfg)
	if err != nil {
		return nil, err
	}
	return trader, nil
}

func (f flags) squeezeBreakout(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultSqueezeBreakoutConfig()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	trader, err := strategies.NewSqueezeBreakout(cfg)
	return trader, err
}
