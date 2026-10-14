// Command intradayagent runs the intraday strategies of the sessions, each
// entry placed with its stop loss and take profit at the broker (mt5 or the
// paper wallet), in ATR from the entry close, on their own timeframe,
// 5m, unless -timeframe says otherwise: orb (the opening range breakout),
// asialondon (the Asia range broken in London), vwap (the reclaim and the
// reject of the VWAP of the session, -setup reject trading the continuation of
// its trend), sweep (the sweeps of the liquidity of the swings) and zscore (the
// reversion of the Z-Score of the close); and the rules of prompts of the LLM
// agent, their stop loss and take profit set by each, moved by the R multiple
// of the position: meanreversion (the rejections of the Bollinger Bands of a
// range back to its mean, 15m), scalp1m (the micro pullbacks of the micro
// trend, 1m) and scalp1mbtc (the strong bursts of momentum, for a spread wide
// next to the moves, 1m). See runner.Run for the shared flags:
//
//	go run ./agents/intradayagent -strategy orb -pair XAUUSDm -csv xau-5m.csv -lot 0.01
//	go run ./agents/intradayagent -strategy asialondon -mode paper -pair XAUUSDm \
//		-stop-atr 1 -take-profit-atr 2 -position-fraction 0.1
//	go run ./agents/intradayagent -strategy vwap -setup reject -mode paper \
//		-pair BTCUSDm -timeframe 15m -lot 0.01
//
// Launched by the controller, the live mode takes the same flags from the
// settings of the agent in its database.
package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/raykavin/xtrader/agents/internal/runner"
	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/strategy"
)

func main() {
	runner.Run(agent())
}

// agent returns the agent and its strategies, the first one its default, the
// one of agentapi.DefaultStrategy for the kind intraday.
func agent() runner.Agent {
	f := newFlags()
	return runner.Agent{Name: "intradayagent", Strategies: []runner.Strategy{
		{Name: "orb", About: "opening range breakout", New: f.orb, Inputs: inputs("timezone", "session", "open-range",
			"buffer-atr", "max-trades", "max-range-atr", "trend-ema", "vwap")},
		{Name: "asialondon", About: "the Asia range broken in London", New: f.asiaLondon, Inputs: inputs("timezone",
			"asia", "london", "buffer-atr", "max-trades", "max-range-atr", "trend-ema", "vwap")},
		{Name: "vwap", About: "reclaim and reject of the VWAP of the session", New: f.vwapReclaim,
			Inputs: inputs("timezone", "anchor", "setup")},
		{Name: "sweep", About: "sweep of the liquidity of the swings", New: f.sweep,
			Inputs: inputs("timezone", "lookback", "day-levels")},
		{Name: "zscore", About: "reversion of the Z-Score of the close", New: f.zscore,
			Inputs: inputs("entry-z", "exit-z", "max-adx")},
		{Name: "meanreversion", About: "the rejections of the Bollinger Bands of a range back to its mean",
			New: f.meanReversion, Inputs: []string{"min-atr-pct"}},
		{Name: "scalp1m", About: "scalping the micro pullbacks of the micro trend on 1 minute candles",
			New: f.scalp1m, Inputs: []string{"min-atr-pct"}},
		{Name: "scalp1mbtc", About: "scalping the strong bursts of momentum on 1 minute candles, for a wide spread",
			New: f.scalp1mBTC, Inputs: []string{"min-atr-pct"}},
	}}
}

// inputs returns the flags of a strategy: its own and the ones of its exits.
func inputs(own ...string) []string {
	return append(own, runner.BracketInputs...)
}

// flags holds the flags of the strategies of the agent.
type flags struct {
	timezone, session, asia, london *string
	openRange                       *time.Duration
	bufferATR, maxRangeATR          *float64
	maxTrades, trendEMA             *int
	vwap, dayLevels                 *bool
	anchor, setup                   *string
	lookback                        *int
	entryZ, exitZ, maxADX           *float64
}

// newFlags defines the flags of the strategies of the agent, before
// runner.Run parses them.
func newFlags() flags {
	return flags{
		timezone:    flag.String("timezone", "", "orb, asialondon, vwap, sweep: IANA time zone of the session times, eg: America/New_York, Europe/London, UTC"),
		session:     flag.String("session", "", "orb: session traded, HH:MM-HH:MM in -timezone (default 09:30-12:00 America/New_York)"),
		openRange:   flag.Duration("open-range", 0, "orb: duration of the opening range from the start of the session (default 15m)"),
		asia:        flag.String("asia", "", "asialondon: Asia session of the range, HH:MM-HH:MM in -timezone (default 00:00-07:00 Europe/London)"),
		london:      flag.String("london", "", "asialondon: London session of the breakouts, HH:MM-HH:MM in -timezone (default 07:00-10:00)"),
		bufferATR:   flag.Float64("buffer-atr", 0, "orb, asialondon: how far beyond the range the close must end, in ATR"),
		maxTrades:   flag.Int("max-trades", 0, "orb, asialondon: positions opened per session at most (default 1)"),
		maxRangeATR: flag.Float64("max-range-atr", 0, "orb, asialondon: skip the sessions whose range is wider than that many ATR (0: off)"),
		trendEMA:    flag.Int("trend-ema", 0, "orb, asialondon: period of an EMA the close of a breakout must be on the side of (0: off)"),
		vwap:        flag.Bool("vwap", false, "orb, asialondon: the close of a breakout must be on the side of the VWAP of the session (default: on for orb)"),
		anchor:      flag.String("anchor", "", "vwap: time of day the VWAP starts over, HH:MM in -timezone (default 00:00 UTC)"),
		setup:       flag.String("setup", "", "vwap: reclaim, reject (the continuation of its trend) or both (default both)"),
		lookback:    flag.Int("lookback", 0, "sweep: candles of the swings whose liquidity is swept (default 50)"),
		dayLevels:   flag.Bool("day-levels", false, "sweep: also sweep the high and the low of the previous day"),
		entryZ:      flag.Float64("entry-z", 0, "zscore: Z-Score of the entries, -entry-z for a buy (default 2)"),
		exitZ:       flag.Float64("exit-z", 0, "zscore: Z-Score of the exits, 0 at the mean"),
		maxADX:      flag.Float64("max-adx", 0, "zscore: trade only while the ADX is under it (default 25)"),
	}
}

// sessionFlag splits a session flag, HH:MM-HH:MM.
func sessionFlag(name, value string) (start, end string, err error) {
	start, end, ok := strings.Cut(value, "-")
	if !ok {
		return "", "", fmt.Errorf("-%s: expected HH:MM-HH:MM, not %q", name, value)
	}
	return start, end, nil
}

// zone sets tz from -timezone when set.
func (f flags) zone(in runner.Inputs, tz *string) {
	if in.Set["timezone"] {
		*tz = *f.timezone
	}
}

func (f flags) orb(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultORBConfig()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	f.zone(in, &cfg.Timezone)
	if in.Set["session"] {
		var err error
		if cfg.SessionStart, cfg.SessionEnd, err = sessionFlag("session", *f.session); err != nil {
			return nil, err
		}
	}
	if in.Set["open-range"] {
		cfg.OpenRange = *f.openRange
	}
	if in.Set["buffer-atr"] {
		cfg.BufferATR = *f.bufferATR
	}
	if in.Set["max-trades"] {
		cfg.MaxTrades = *f.maxTrades
	}
	if in.Set["vwap"] {
		cfg.VWAP = *f.vwap
	}
	cfg.MaxRangeATR, cfg.TrendLen = *f.maxRangeATR, *f.trendEMA
	trader, err := strategies.NewORB(cfg)
	return trader, err
}

func (f flags) vwapReclaim(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultVWAPReclaimConfig()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	f.zone(in, &cfg.Timezone)
	if in.Set["anchor"] {
		cfg.Anchor = *f.anchor
	}
	switch *f.setup {
	case "", "both":
	case "reclaim":
		cfg.Reject = false
	case "reject":
		cfg.Reclaim = false
	default:
		return nil, fmt.Errorf("-setup: reclaim, reject or both, not %q", *f.setup)
	}
	trader, err := strategies.NewVWAPReclaim(cfg)
	return trader, err
}

func (f flags) sweep(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultLiquiditySweepConfig()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	f.zone(in, &cfg.Timezone)
	if in.Set["lookback"] {
		cfg.Lookback = *f.lookback
	}
	cfg.DayLevels = *f.dayLevels
	trader, err := strategies.NewLiquiditySweep(cfg)
	return trader, err
}

func (f flags) zscore(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultZScoreReversionConfig()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	if in.Set["entry-z"] {
		cfg.EntryZ = *f.entryZ
	}
	cfg.ExitZ = *f.exitZ
	if in.Set["max-adx"] {
		cfg.MaxADX = *f.maxADX
		if cfg.MaxADX == 0 {
			cfg.ADXLen = 0 // -max-adx 0 turns the regime filter off
		}
	}
	trader, err := strategies.NewZScoreReversion(cfg)
	return trader, err
}

func (f flags) asiaLondon(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultAsiaLondonConfig()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	f.zone(in, &cfg.Timezone)
	var err error
	if in.Set["asia"] {
		if cfg.AsiaStart, cfg.AsiaEnd, err = sessionFlag("asia", *f.asia); err != nil {
			return nil, err
		}
	}
	if in.Set["london"] {
		if cfg.LondonStart, cfg.LondonEnd, err = sessionFlag("london", *f.london); err != nil {
			return nil, err
		}
	}
	if in.Set["buffer-atr"] {
		cfg.BufferATR = *f.bufferATR
	}
	if in.Set["max-trades"] {
		cfg.MaxTrades = *f.maxTrades
	}
	cfg.MaxRangeATR, cfg.TrendLen, cfg.VWAP = *f.maxRangeATR, *f.trendEMA, *f.vwap
	trader, err := strategies.NewAsiaLondon(cfg)
	return trader, err
}

func (f flags) meanReversion(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultMeanReversionConfig()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	trader, err := strategies.NewMeanReversion(cfg)
	return trader, err
}

func (f flags) scalp1m(in runner.Inputs) (strategy.Strategy, error) {
	return microScalp(in, strategies.DefaultScalp1mConfig())
}

func (f flags) scalp1mBTC(in runner.Inputs) (strategy.Strategy, error) {
	return microScalp(in, strategies.DefaultScalp1mBTCConfig())
}

// microScalp creates the scalp of cfg with the inputs.
func microScalp(in runner.Inputs, cfg strategies.MicroScalpConfig) (strategy.Strategy, error) {
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	trader, err := strategies.NewMicroScalp(cfg)
	return trader, err
}
