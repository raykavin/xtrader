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
// next to the moves, 1m); and newsbreakout (the breakout of the range after
// the scheduled economic releases, the rules of orb on it, the releases given
// by the runner, 5m), seasonality (the intraday bias of fixed windows of the
// day, measured walk-forward, 15m), volumeprofile (the 80% rule of the value
// area of the volume profile of the previous session, 30m), cmegap (the
// weekend gap of the CME faded back to its Friday close, 1h) and pairs (the
// spread of the two pairs of the agent, leg A and leg B in the order of
// -pair, traded back to its mean on its Z-Score, 1h: the log of the ratio of
// their prices, its legs of equal value, or with -pairs-hedge ols the residual
// of the regression of their logs, leg B worth β times leg A; no stop loss nor
// take profit at the broker). See runner.Run for the shared flags:
//
//	go run ./agents/intradayagent -strategy orb -pair XAUUSDm -csv xau-5m.csv -lot 0.01
//	go run ./agents/intradayagent -strategy asialondon -mode paper -pair XAUUSDm \
//		-stop-atr 1 -take-profit-atr 2 -position-fraction 0.1
//	go run ./agents/intradayagent -strategy vwap -setup reject -mode paper \
//		-pair BTCUSDm -timeframe 15m -lot 0.01
//	# newsbreakout, the breakouts of the 15 minutes after the releases of
//	# high importance: the releases come only to the agent launched by the
//	# controller (live or paper, those of the news tab) or run by a backtest
//	# of the controller (those of its period); run on its own as below, it
//	# gets none and never enters, telling that it waits for them
//	go run ./agents/intradayagent -strategy newsbreakout -mode paper -pair XAUUSDm -lot 0.01 \
//		-newsbreakout-importance high -newsbreakout-wait 15m -newsbreakout-expiry 1h
//	# the London fixes of gold and the open of New York, traded where their
//	# bias of the last 60 days has a t statistic of 2 at least
//	go run ./agents/intradayagent -strategy seasonality -mode paper -pair XAUUSDm -lot 0.01 \
//		-seasonality-days 60 -seasonality-min-t 2
//	# the value area of the CME trading day, from 17:00 of New York
//	go run ./agents/intradayagent -strategy volumeprofile -mode paper -pair XAUUSDm -lot 0.01 \
//		-volumeprofile-anchor 17:00 -volumeprofile-timezone America/New_York
//	go run ./agents/intradayagent -strategy cmegap -mode paper -pair BTCUSDm -lot 0.01 \
//		-cmegap-min-gap-atr 0.5
//	# gold against silver, the lots balancing the notionals of the two legs
//	go run ./agents/intradayagent -strategy pairs -mode paper -pair 'XAUUSDm;XAGUSDm' \
//		-lot 'XAUUSDm=0.01;XAGUSDm=0.02' -pairs-entry-z 2 -pairs-exit-z 0
//	# the residual of the regression of their logs: silver sized by β, in
//	# steps of 0.01 lot, its lot here
//	go run ./agents/intradayagent -strategy pairs -mode paper -pair 'XAUUSDm;XAGUSDm' \
//		-lot 'XAUUSDm=0.10;XAGUSDm=0.01' -pairs-hedge ols
//
// Launched by the controller, the live mode takes the same flags from the
// settings of the agent in its database.
package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/raykavin/xtrader/agents/internal/flaglist"
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
			New: f.meanReversion, Inputs: append(append([]string{"min-atr-pct"}, meanReversionInputs...),
				runner.ATRExitInputs...)},
		{Name: "scalp1m", About: "scalping the micro pullbacks of the micro trend on 1 minute candles",
			New: f.scalp1m, Inputs: append([]string{"min-atr-pct"}, runner.ATRExitInputs...)},
		{Name: "scalp1mbtc", About: "scalping the strong bursts of momentum on 1 minute candles, for a wide spread",
			New: f.scalp1mBTC, Inputs: append([]string{"min-atr-pct"}, runner.ATRExitInputs...)},
		{Name: "newsbreakout", About: "the breakout of the range after the economic releases", New: f.newsBreakout,
			Inputs: inputs(newsBreakoutInputs...)},
		{Name: "seasonality", About: "the intraday bias of fixed windows of the day, measured walk-forward",
			New: f.seasonality, Inputs: inputs(seasonalityInputs...)},
		{Name: "volumeprofile", About: "the 80% rule of the value area of the previous session",
			New: f.volumeProfile, Inputs: append([]string{"stop-atr", "take-profit-atr", "max-bars", "min-atr-pct"},
				volumeProfileInputs...)},
		{Name: "cmegap", About: "the weekend gap of the CME faded back to its Friday close", New: f.cmeGap,
			Inputs: append([]string{"stop-atr", "take-profit-atr", "max-bars", "min-atr-pct"}, cmeGapInputs...)},
		{Name: "pairs", About: "the spread of two cointegrated legs traded back to its mean", New: f.pairs,
			Inputs: pairsInputs},
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
	mr                              meanReversionFlags
	nb                              newsBreakoutFlags
	ss                              seasonalityFlags
	vp                              volumeProfileFlags
	cg                              cmeGapFlags
	pr                              pairsFlags
}

// meanReversionFlags holds the flags of meanreversion, named in
// meanReversionInputs: its filters, the ones of DefaultMeanReversionConfig
// unless set.
type meanReversionFlags struct {
	oversold, overbought, maxADX, flatATR, wick *float64
}

// meanReversionInputs are the flags of meanreversion of its own.
var meanReversionInputs = []string{"meanreversion-oversold", "meanreversion-overbought", "meanreversion-max-adx",
	"meanreversion-flat-atr", "meanreversion-wick-pct"}

// newsBreakoutFlags holds the flags of newsbreakout, named in
// newsBreakoutInputs, the ones of DefaultNewsBreakoutConfig unless set.
type newsBreakoutFlags struct {
	importance, codes   *string
	wait, expiry        *time.Duration
	bufferATR, maxRange *float64
}

// newsBreakoutInputs are the flags of newsbreakout of its own.
var newsBreakoutInputs = []string{"newsbreakout-importance", "newsbreakout-codes", "newsbreakout-wait",
	"newsbreakout-expiry", "newsbreakout-buffer-atr", "newsbreakout-max-range-atr"}

// seasonalityFlags holds the flags of seasonality, named in
// seasonalityInputs, the ones of DefaultSeasonalityConfig unless set.
type seasonalityFlags struct {
	windows       *string
	days, minDays *int
	minT          *float64
}

// seasonalityInputs are the flags of seasonality of its own.
var seasonalityInputs = []string{"seasonality-windows", "seasonality-days", "seasonality-min-days",
	"seasonality-min-t"}

// volumeProfileFlags holds the flags of volumeprofile, named in
// volumeProfileInputs, the ones of DefaultVolumeProfileConfig unless set.
type volumeProfileFlags struct {
	timezone, anchor *string
	bins, confirm    *int
	valueArea        *float64
}

// volumeProfileInputs are the flags of volumeprofile of its own: it takes
// -stop-atr (the buffer beyond the extreme of the session), -max-bars and
// -min-atr-pct too, not -take-profit-atr (the target is the other edge of the
// value area).
var volumeProfileInputs = []string{"volumeprofile-timezone", "volumeprofile-anchor", "volumeprofile-bins",
	"volumeprofile-value-area", "volumeprofile-confirm"}

// cmeGapFlags holds the flags of cmegap, named in cmeGapInputs, the ones of
// DefaultCMEGapConfig unless set.
type cmeGapFlags struct {
	timezone, close, open *string
	minGapATR             *float64
	expiry                *int
}

// cmeGapInputs are the flags of cmegap of its own: it takes -stop-atr,
// -max-bars and -min-atr-pct too, not -take-profit-atr (the target is the
// reference, the gap filled).
var cmeGapInputs = []string{"cmegap-timezone", "cmegap-close", "cmegap-open", "cmegap-min-gap-atr",
	"cmegap-expiry"}

// pairsFlags holds the flags of pairs, named in pairsInputs, the ones of
// DefaultPairsConfig unless set.
type pairsFlags struct {
	hedge             *string
	length, maxBars   *int
	entry, exit, stop *float64
}

// pairsInputs are the flags of pairs, its only ones: it reads no ATR and
// places no stop loss nor take profit at the broker, its exits on the Z-Score
// of the spread, so none of runner.BracketInputs.
var pairsInputs = []string{"pairs-hedge", "pairs-len", "pairs-entry-z", "pairs-exit-z", "pairs-stop-z",
	"pairs-max-bars"}

// importanceName returns the name of the importance of a release, as
// strategies.ParseReleaseImportance reads it.
func importanceName(importance strategies.ReleaseImportance) string {
	switch importance {
	case strategies.ReleaseLow:
		return "low"
	case strategies.ReleaseMedium:
		return "medium"
	}
	return "high"
}

// newFlags defines the flags of the strategies of the agent, before
// runner.Run parses them.
func newFlags() flags {
	mr := strategies.DefaultMeanReversionConfig()
	nb := strategies.DefaultNewsBreakoutConfig()
	ss := strategies.DefaultSeasonalityConfig()
	vp := strategies.DefaultVolumeProfileConfig()
	cg := strategies.DefaultCMEGapConfig()
	pr := strategies.DefaultPairsConfig()
	return flags{
		nb: newsBreakoutFlags{
			importance: flag.String("newsbreakout-importance", importanceName(nb.Importance), "newsbreakout: the releases of that importance at least: low, medium or high"),
			codes:      flag.String("newsbreakout-codes", strings.Join(nb.Codes, ","), "newsbreakout: the MQL5 event codes traded (event_code of the calendar), separated by commas or semicolons; empty: every event"),
			wait:       flag.Duration("newsbreakout-wait", nb.Wait, "newsbreakout: the range is the high and the low of the candles of that long after a release (keep it at least the news window after a release)"),
			expiry:     flag.Duration("newsbreakout-expiry", nb.Expiry, "newsbreakout: the breakouts traded that long after the wait, or until the next release; a position still open then is closed"),
			bufferATR:  flag.Float64("newsbreakout-buffer-atr", nb.BufferATR, "newsbreakout: how far beyond the range the close must end, in ATR"),
			maxRange:   flag.Float64("newsbreakout-max-range-atr", nb.MaxRangeATR, "newsbreakout: skip the releases whose range is wider than that many ATR (0: off)"),
		},
		ss: seasonalityFlags{
			windows: flag.String("seasonality-windows", ss.Windows, "seasonality: the windows of the day traded, HH:MM/minutes@IANA zone, separated by ;"),
			days:    flag.Int("seasonality-days", ss.Days, "seasonality: the last days on which a window traded measuring its bias"),
			minDays: flag.Int("seasonality-min-days", ss.MinDays, "seasonality: the days of a window needed at least to trade it"),
			minT:    flag.Float64("seasonality-min-t", ss.MinT, "seasonality: the t statistic of the mean return of a window needed at least, in absolute value (0: every window by the sign of its mean)"),
		},
		vp: volumeProfileFlags{
			timezone:  flag.String("volumeprofile-timezone", vp.Timezone, "volumeprofile: IANA time zone of the session anchor, eg: UTC, America/New_York"),
			anchor:    flag.String("volumeprofile-anchor", vp.Anchor, "volumeprofile: time of day the session (and its volume profile) starts, HH:MM in -volumeprofile-timezone"),
			bins:      flag.Int("volumeprofile-bins", vp.Bins, "volumeprofile: price bins of the volume profile of the previous session, 2 at least"),
			valueArea: flag.Float64("volumeprofile-value-area", vp.ValueArea, "volumeprofile: share of the volume of the session in its value area, above 0 and at most 1"),
			confirm:   flag.Int("volumeprofile-confirm", vp.Confirm, "volumeprofile: consecutive closes back inside the value area after an open outside it"),
		},
		cg: cmeGapFlags{
			timezone:  flag.String("cmegap-timezone", cg.Timezone, "cmegap: IANA time zone of -cmegap-close and -cmegap-open, the CME's"),
			close:     flag.String("cmegap-close", cg.Close, "cmegap: Friday close of the CME, HH:MM in -cmegap-timezone: the reference is the close of the last candle ending at or before it"),
			open:      flag.String("cmegap-open", cg.Open, "cmegap: Sunday reopen of the CME, HH:MM in -cmegap-timezone: the gap is the close of the first candle opened at or after it minus the reference"),
			minGapATR: flag.Float64("cmegap-min-gap-atr", cg.MinGapATR, "cmegap: fade a gap of that many ATR at least (0: any gap)"),
			expiry:    flag.Int("cmegap-expiry", cg.ExpiryHours, "cmegap: close the position that many hours after its entry, at the latest on the last candle before the next Friday close"),
		},
		pr: pairsFlags{
			hedge:   flag.String("pairs-hedge", pr.Hedge, "pairs: the spread and the weights of its legs: ratio (ln A − ln B, the log of the ratio of the prices, the legs of equal value: by -lot, which must balance them, or by value) or ols (ln A − β ln B, β the OLS slope of ln A on ln B over -pairs-len, leg B worth β times leg A: by value, or with -lot leg A its lot and leg B rounded to multiples of its own lot)"),
			length:  flag.Int("pairs-len", pr.Len, "pairs: candles both legs have in the window of the spread and its Z-Score (and of its hedge ratio β with -pairs-hedge ols)"),
			entry:   flag.Float64("pairs-entry-z", pr.EntryZ, "pairs: Z-Score of the entries, -pairs-entry-z buying A and selling B, pairs-entry-z the other way"),
			exit:    flag.Float64("pairs-exit-z", pr.ExitZ, "pairs: Z-Score the spread reverts to, closing both legs (0: its mean)"),
			stop:    flag.Float64("pairs-stop-z", pr.StopZ, "pairs: |Z-Score| closing both legs as the divergence widens (0: off)"),
			maxBars: flag.Int("pairs-max-bars", pr.MaxHoldBars, "pairs: candles both legs have after which the spread held is closed (0: never)"),
		},
		mr: meanReversionFlags{
			oversold:   flag.Float64("meanreversion-oversold", mr.Oversold, "meanreversion: the RSI under it on the rejection of the lower band, a buy"),
			overbought: flag.Float64("meanreversion-overbought", mr.Overbought, "meanreversion: the RSI over it on the rejection of the upper band, a sell"),
			maxADX:     flag.Float64("meanreversion-max-adx", mr.MaxADX, "meanreversion: the ADX of a range under it, and not rising"),
			flatATR:    flag.Float64("meanreversion-flat-atr", mr.FlatATR, "meanreversion: the fast and the slow EMAs of a range closer than that ATR"),
			wick:       flag.Float64("meanreversion-wick-pct", mr.WickPct, "meanreversion: the wick of the rejection, a share of its candle at least"),
		},
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
	cfg.Oversold, cfg.Overbought, cfg.MaxADX = *f.mr.oversold, *f.mr.overbought, *f.mr.maxADX
	cfg.FlatATR, cfg.WickPct = *f.mr.flatATR, *f.mr.wick
	trader, err := strategies.NewMeanReversion(cfg)
	return trader, err
}

func (f flags) scalp1m(in runner.Inputs) (strategy.Strategy, error) {
	return microScalp(in, strategies.DefaultScalp1mConfig())
}

func (f flags) scalp1mBTC(in runner.Inputs) (strategy.Strategy, error) {
	return microScalp(in, strategies.DefaultScalp1mBTCConfig())
}

func (f flags) newsBreakout(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultNewsBreakoutConfig()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	importance, err := strategies.ParseReleaseImportance(*f.nb.importance)
	if err != nil {
		return nil, fmt.Errorf("-newsbreakout-importance: %w", err)
	}
	cfg.Importance, cfg.Codes = importance, strategies.ParseReleaseCodes(*f.nb.codes)
	cfg.Wait, cfg.Expiry = *f.nb.wait, *f.nb.expiry
	cfg.BufferATR, cfg.MaxRangeATR = *f.nb.bufferATR, *f.nb.maxRange
	trader, err := strategies.NewNewsBreakout(cfg)
	return trader, err
}

func (f flags) seasonality(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultSeasonalityConfig()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	cfg.Windows, cfg.Days, cfg.MinDays, cfg.MinT = *f.ss.windows, *f.ss.days, *f.ss.minDays, *f.ss.minT
	trader, err := strategies.NewSeasonality(cfg)
	return trader, err
}

func (f flags) volumeProfile(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultVolumeProfileConfig()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	cfg.ATRStop = false // its -stop-atr: the room beyond the session, not from the entry
	cfg.Timezone, cfg.Anchor = *f.vp.timezone, *f.vp.anchor
	cfg.Bins, cfg.ValueArea, cfg.Confirm = *f.vp.bins, *f.vp.valueArea, *f.vp.confirm
	trader, err := strategies.NewVolumeProfile(cfg)
	return trader, err
}

func (f flags) cmeGap(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultCMEGapConfig()
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	cfg.ATRStop = false // its -stop-atr: from the entry already, as it places it
	cfg.Timezone, cfg.Close, cfg.Open = *f.cg.timezone, *f.cg.close, *f.cg.open
	cfg.MinGapATR, cfg.ExpiryHours = *f.cg.minGapATR, *f.cg.expiry
	trader, err := strategies.NewCMEGap(cfg)
	return trader, err
}

// pairs creates the strategy pairs, its legs the pairs of -pair in their
// order, leg A first (unset, as in the tests, learned from the candles), its
// position fraction the one of DefaultPairsConfig unless -position-fraction is
// set: the default of Run, the whole balance, would leave the second leg short
// of it without -lot.
func (f flags) pairs(in runner.Inputs) (strategy.Strategy, error) {
	cfg := strategies.DefaultPairsConfig()
	fraction := cfg.PositionFraction
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	if !in.Set["position-fraction"] {
		cfg.PositionFraction = fraction
	}
	if pair := flag.Lookup("pair"); pair != nil {
		cfg.Pairs = flaglist.List(pair.Value.String())
	}
	cfg.Hedge, cfg.Len, cfg.EntryZ, cfg.ExitZ = *f.pr.hedge, *f.pr.length, *f.pr.entry, *f.pr.exit
	cfg.StopZ, cfg.MaxHoldBars = *f.pr.stop, *f.pr.maxBars
	trader, err := strategies.NewPairs(cfg)
	return trader, err
}

// microScalp creates the scalp of cfg with the inputs.
func microScalp(in runner.Inputs, cfg strategies.MicroScalpConfig) (strategy.Strategy, error) {
	in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
	trader, err := strategies.NewMicroScalp(cfg)
	return trader, err
}
