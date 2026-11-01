package strategies

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
)

// CMEGapConfig holds the inputs of CMEGap, with its defaults in
// DefaultCMEGapConfig. Periods are counted in candles of Timeframe.
type CMEGapConfig struct {
	Timeframe string

	// The week of the CME, as its Globex sessions keep it for the futures of
	// bitcoin (BTC) and of gold (GC): open from Sunday to Friday, 17:00 to
	// 16:00 in Chicago. Times of day, HH:MM, in the time zone Timezone, an IANA
	// name, its daylight saving time followed: the week closes on Friday at
	// Close and reopens on Sunday at Open. The reference of a week is the close
	// of the last candle ending at or before its Friday close; its gap, the
	// close of the first candle opened at or after the reopen that follows,
	// minus the reference. The candles are those of the pair itself, eg:
	// BTCUSDm, traded through the weekend, its price at the close of the CME
	// for the reference, as the futures left it; XAUUSDm, closed at the
	// weekend, the gap of the market itself.
	Timezone string
	Close    string
	Open     string

	// A gap of MinGapATR at least, in ATR of ATRLen candles on the first
	// candle of the week, is faded: a SELL on a gap up, a BUY on a gap down, at
	// the close of that candle, the take profit at the reference (the gap
	// filled), the stop loss StopATR ATR from the entry, away from it. Only
	// that candle enters: a gap left untraded there, eg: under MinGapATR, its
	// entry refused, isn't traded later in the week.
	MinGapATR float64

	// ExpiryHours closes the position still open that many hours after its
	// entry at the market; in any case it is closed on the last candle ending
	// before the next Friday close (the candle ending at the close could
	// complete when a market closing with the CME, eg: XAUUSDm, is closed
	// already): the position never stays over the next weekend, the gap of the
	// next week.
	ExpiryHours int

	// The stop loss, the sizing and the cost filter, see BracketConfig:
	// TakeProfitATR isn't read, the take profit is the reference, unless
	// TakeProfitValue replaces it, as StopValue the stop loss in ATR.
	BracketConfig
}

// DefaultCMEGapConfig returns the defaults of CMEGap, on the 1h timeframe: the
// hours of the CME Globex for the futures of bitcoin and of gold, a gap of half
// an ATR at least, closed at the latest by the next Friday close.
func DefaultCMEGapConfig() CMEGapConfig {
	return CMEGapConfig{
		Timeframe: "1h",

		Timezone: "America/Chicago",
		Close:    "16:00",
		Open:     "17:00",

		MinGapATR:   0.5,
		ExpiryHours: 120,

		BracketConfig: DefaultBracketConfig(),
	}
}

func (c CMEGapConfig) validate() error {
	if err := c.BracketConfig.validate(); err != nil {
		return err
	}

	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case math.IsNaN(c.MinGapATR) || c.MinGapATR < 0:
		return errors.New("the min gap ATR multiple can't be negative")
	case c.ExpiryHours < 1:
		return errors.New("the expiry must be 1 hour at least")
	}
	return nil
}

// cmeWeek is the week of the CME: from its reopen on Sunday at open to its
// close on Friday at close, times of day in loc.
type cmeWeek struct {
	close, open dayTime
	loc         *time.Location
}

// reopen returns the last reopen of the week at or before t.
func (w cmeWeek) reopen(t time.Time) time.Time {
	sunday := -int(t.In(w.loc).Weekday()) // the days back to the Sunday of t
	at := w.open.on(t, w.loc, sunday)
	if at.After(t) {
		at = w.open.on(t, w.loc, sunday-7)
	}
	return at
}

// closeBefore returns the Friday close before the reopen at.
func (w cmeWeek) closeBefore(reopen time.Time) time.Time {
	return w.close.on(reopen, w.loc, -2)
}

// closeAfter returns the Friday close of the week reopened at reopen.
func (w cmeWeek) closeAfter(reopen time.Time) time.Time {
	return w.close.on(reopen, w.loc, 5)
}

// nextClose returns the first Friday close after t.
func (w cmeWeek) nextClose(t time.Time) time.Time {
	reopen := w.reopen(t)
	if at := w.closeAfter(reopen); at.After(t) {
		return at
	}
	return w.close.on(reopen, w.loc, 12) // t over the weekend, after the close
}

// weekend is the longest time from a Friday close to the next reopen, as the
// wall clock goes, an hour of daylight saving time included.
func (w cmeWeek) weekend() time.Duration {
	return 2*rkDay + time.Duration(w.open) - time.Duration(w.close) + time.Hour
}

// CMEGap fades the weekend gap of the CME: the price at the reopen of the
// week away from the one of the Friday close by MinGapATR at least, a SELL on
// a gap up, a BUY on a gap down, the take profit at the price of the Friday
// close, the gap filled. That gaps tend to fill is a belief of the market, not
// a rule of a source: a hypothesis its backtests measure. See CMEGapConfig.
type CMEGap struct {
	cfg     CMEGapConfig
	week    cmeWeek
	tf      time.Duration
	warmup  int
	bracket *bracket
}

// NewCMEGap validates the configuration and creates the strategy.
func NewCMEGap(cfg CMEGapConfig) (*CMEGap, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("cmegap: %w", err)
	}
	hours, err := newSession(cfg.Close, cfg.Open, cfg.Timezone)
	if err != nil {
		return nil, fmt.Errorf("cmegap: %w", err)
	}
	week := cmeWeek{close: hours.start, open: hours.end, loc: hours.loc}

	// the window reaches back from the first candle of the week, opened less
	// than a candle after the reopen, to the reference, opened less than two
	// candles before the Friday close: the weekend and three candles, the two
	// candles of the ends included, with the ATR seeded
	candles, tf, err := timeframeCandles(cfg.Timeframe, week.weekend())
	if err != nil {
		return nil, fmt.Errorf("cmegap: %w", err)
	}
	if tf > rkDay {
		return nil, fmt.Errorf("cmegap: the timeframe must be a day at most, not %s", cfg.Timeframe)
	}
	candles += 3 + 1

	return &CMEGap{
		cfg:     cfg,
		week:    week,
		tf:      tf,
		warmup:  max(candles, seedWarmup(cfg.ATRLen)) + 2,
		bracket: newBracket("CMEGAP", cfg.Timeframe, cfg.BracketConfig),
	}, nil
}

func (s *CMEGap) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *CMEGap) WarmupPeriod() int {
	return s.warmup
}

func (s *CMEGap) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["cg_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)
	df.Metadata["cg_ref"] = s.references(df)

	return []strategy.ChartIndicator{
		{
			Overlay:   true,
			GroupName: "CME gap",
			Time:      df.Time,
			Metrics: []strategy.IndicatorMetric{
				{Values: df.Metadata["cg_ref"], Name: "CME close", Color: colorLine, Style: strategy.StyleScatter},
			},
		},
	}
}

// references returns the reference of the week of each candle of df, the
// target of its trade, over the week, from its reopen to its Friday close: NaN
// over the weekend and when df doesn't hold it, see reference.
func (s *CMEGap) references(df *model.Dataframe) []float64 {
	out := make([]float64, len(df.Time))
	var (
		week, closing time.Time
		value         = na
	)
	for i, t := range df.Time {
		if reopen := s.week.reopen(t); !reopen.Equal(week) {
			week, closing, value = reopen, s.week.closeAfter(reopen), na
			if ref, ok := s.reference(df, reopen); ok {
				value = ref
			}
		}
		out[i] = na
		if t.Before(closing) {
			out[i] = value
		}
	}
	return out
}

// reference returns the reference of the week reopened at reopen: the close of
// the last candle of df ending at or before the Friday close before it, the
// last price of the week before; false when df doesn't hold it, eg: its window
// starting after that close, or that candle opened before the reopen of the
// week before (a hole in the candles). It reads only candles opened before the
// reopen, the ones of the gap.
func (s *CMEGap) reference(df *model.Dataframe, reopen time.Time) (float64, bool) {
	closing := s.week.closeBefore(reopen)
	j := sort.Search(len(df.Time), func(k int) bool { return df.Time[k].Add(s.tf).After(closing) }) - 1
	if j < 0 || j >= len(df.Close) || df.Time[j].Before(s.week.open.on(reopen, s.week.loc, -7)) {
		return 0, false
	}
	return df.Close[j], true
}

// signal returns the entry of the last candle, if any, with its stop loss and
// take profit: only on the first candle opened at or after the reopen of the
// week, the gap from the reference MinGapATR ATR at least.
func (s *CMEGap) signal(df *model.Dataframe) (model.SideType, float64, float64) {
	atrs := df.Metadata["cg_atr"]
	i := len(df.Close) - 1
	if i < 1 || len(df.Time) != len(df.Close) || len(atrs) != len(df.Close) {
		return "", 0, 0
	}
	opened := df.Time[i]
	reopen := s.week.reopen(opened)
	if !df.Time[i-1].Before(reopen) {
		return "", 0, 0 // not the first candle of the week
	}

	closes := opened.Add(s.tf)
	if !closes.Add(s.tf).Before(s.week.nextClose(closes)) {
		return "", 0, 0 // the week over already, eg: a hole in the candles, its position closed at once
	}
	reference, ok := s.reference(df, reopen)
	if !ok {
		log.Infof("[CMEGAP] %s %s | the reopen of the week, the reference of the Friday close out of the "+
			"candles: no trade", df.Pair, s.cfg.Timeframe)
		return "", 0, 0
	}

	closePrice, atr := df.Close[i], atrs[i]
	gap := closePrice - reference
	if isNa(atr) || atr <= 0 || gap == 0 || math.Abs(gap) < s.cfg.MinGapATR*atr {
		log.Infof("[CMEGAP] %s %s | gap %f from the reference %f, under %.2f ATR (%f): no trade", df.Pair,
			s.cfg.Timeframe, gap, reference, s.cfg.MinGapATR, atr)
		return "", 0, 0
	}
	if s.bracket.belowCost(closePrice, atr) {
		return "", 0, 0
	}

	side := model.SideTypeBuy // a gap down, up to the reference
	if gap > 0 {
		side = model.SideTypeSell
	}
	log.Infof("[CMEGAP] %s %s | gap %f (%.2f ATR) from the reference %f | %s", df.Pair, s.cfg.Timeframe, gap,
		math.Abs(gap)/atr, reference, side)
	return side, closePrice - rkSign(side)*s.cfg.StopATR*atr, reference
}

// exit closes the position ExpiryHours after its entry, or on the last candle
// ending before the next Friday close, whichever comes first. A position found
// without its opening, eg: after a restart on a broker that doesn't tell it,
// counts from the close of the first candle the strategy follows it on.
func (s *CMEGap) exit(df *model.Dataframe, _ model.SideType) string {
	trade := s.bracket.trades[df.Pair]
	if trade == nil || len(df.Time) == 0 {
		return ""
	}
	last := df.Time[len(df.Time)-1]
	closes := last.Add(s.tf)
	if trade.opened.IsZero() {
		trade.opened = closes
	}
	entered := s.entered(trade.opened, last)
	switch {
	case !closes.Before(entered.Add(time.Duration(s.cfg.ExpiryHours) * time.Hour)):
		return "EXPIRY"
	case !closes.Add(s.tf).Before(s.week.nextClose(entered)):
		return "CME CLOSE"
	}
	return ""
}

// entered returns the close of the candle whose close opened a position at
// opened, as the strategy counts it, on the grid of the candles of the last
// one, opened at last: the broker may stamp the fill a moment after that
// close, eg: MT5, or later, eg: a position opened by hand, counted from the
// open of the candle it was opened in then, as rkFound.held counts the
// candles held.
func (s *CMEGap) entered(opened, last time.Time) time.Time {
	candles := max((last.Sub(opened)+s.tf-1)/s.tf, 0)
	return last.Add(-candles * s.tf)
}

func (s *CMEGap) OnCandle(df *model.Dataframe, broker service.Broker) {
	entry, stop, profit := s.signal(df)
	if !s.bracket.manage(df, broker, s.exit) || entry == "" {
		return
	}
	if s.bracket.openAt(df, broker, entry, stop, profit) {
		// the opening of its expiry: the close of the candle whose close
		// opened it
		s.bracket.trades[df.Pair].opened = df.Time[len(df.Time)-1].Add(s.tf)
	}
}
