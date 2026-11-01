package strategies

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
)

// Seasonality trades the intraday seasonality of fixed windows of the day:
// the return of a window on the days before, the same clock time in the time
// zone of its market, tells the direction it tends to take. Heston, Korajczyk
// and Sadka ("Intraday Patterns in the Cross-section of Stock Returns",
// Journal of Finance 65(4), 2010) found that the return of an interval of the
// day continues at the same interval on the following days, for weeks; and
// the London fixes of gold move its price around them (Caminschi and Heaney,
// "Fixing a Leaky Fixing: Short-Term Market Reactions to the London PM Gold
// Price Fixing", Journal of Futures Markets 34(11), 2014). The windows of the
// defaults are the two auctions of the LBMA Gold Price, at 10:30 and 15:00 in
// London, and the open of the New York stock exchanges, at 09:30 in New York.
//
// The bias of a window is measured walk-forward, from the candles of the
// strategy only, before each day it trades: its return on each of the last
// days on which it traded, the day traded never among them, and the Student t
// statistic of their mean, mean / (stdev / √n), the sample deviation (n − 1):
// a |t| of 2 is the usual significance of 5% of a two-sided test. Whether a
// bias survives its costs is for the backtests to tell.

// seasonalityMaxWarmup caps the warmup of Seasonality, the candles it is
// handed, which hold the days of its statistic: Days of 15m candles (96 a day)
// fit up to 206 days, of 5m candles up to 68 days; beyond it, the statistic
// measures the days the candles hold, MinDays of them at least, or the
// configuration is refused, eg: the 20 MinDays of 1m candles.
const seasonalityMaxWarmup = 20000

// SeasonalityConfig holds the inputs of Seasonality, with its defaults in
// DefaultSeasonalityConfig. The windows and the statistic are in candles of
// Timeframe.
type SeasonalityConfig struct {
	Timeframe string

	// Windows are the windows of the day traded, separated by ";", each
	// written HH:MM/minutes@zone: it starts at HH:MM in the time zone zone,
	// an IANA name, eg: Europe/London, following the daylight saving time of
	// its city, and lasts that many minutes, a multiple of Timeframe, under
	// 23 hours (the day of a change of the daylight saving time); its start
	// must fall on the open of a candle of Timeframe all year, eg: 10:30 in
	// London on 15m, not on 1h.
	Windows string

	// The bias of a window: its return, from the open of its first candle to
	// the close of its last, on each of the last Days days on which it traded
	// before the current one (a day it traded holds the candle opening at its
	// start and the one closing at its end, eg: not the weekends of gold);
	// with MinDays of them at least, its t statistic, at least MinT in
	// absolute value, trades the window in the direction of the mean, 0
	// trading every window by the sign of its mean.
	Days    int
	MinDays int
	MinT    float64

	// The exits and the sizing, see BracketConfig. The entry is placed at the
	// market on the close of the candle ending at the start of the window,
	// and closed at the market on the close of the candle ending at its end:
	// its stop loss and take profit are a safety, far from the moves of a
	// window. MaxBars, 0 by default, closes it earlier.
	BracketConfig
}

// DefaultSeasonalityConfig returns the defaults of Seasonality, on the 15m
// timeframe: the London fixes of gold, AM and PM, and the open of New York,
// an hour each, measured on 60 days.
func DefaultSeasonalityConfig() SeasonalityConfig {
	exits := DefaultBracketConfig()
	exits.StopATR, exits.TakeProfitATR = 3, 4.5

	return SeasonalityConfig{
		Timeframe: "15m",

		Windows: "10:30/60@Europe/London;15:00/60@Europe/London;09:30/60@America/New_York",

		Days:    60,
		MinDays: 20,
		MinT:    2,

		BracketConfig: exits,
	}
}

// seasonWindow is a window of Seasonality: its start, a time of day in loc,
// and its length.
type seasonWindow struct {
	name   string // as written in its normal form, eg: 10:30/60@Europe/London
	start  dayTime
	length time.Duration
	loc    *time.Location
}

// parseSeasonWindows parses the windows of SeasonalityConfig.Windows.
func parseSeasonWindows(spec string) ([]seasonWindow, error) {
	var windows []seasonWindow
	for _, value := range strings.Split(spec, ";") {
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		w, err := parseSeasonWindow(value)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(windows, func(other seasonWindow) bool { return other.name == w.name }) {
			return nil, fmt.Errorf("the window %s given twice", w.name)
		}
		windows = append(windows, w)
	}
	if len(windows) == 0 {
		return nil, errors.New("no window to trade")
	}
	return windows, nil
}

// parseSeasonWindow parses a window written HH:MM/minutes@zone.
func parseSeasonWindow(value string) (seasonWindow, error) {
	invalid := fmt.Errorf("invalid window %q, expected HH:MM/minutes@zone, eg: 10:30/60@Europe/London", value)
	clock, zone, ok := strings.Cut(value, "@")
	if !ok {
		return seasonWindow{}, invalid
	}
	start, minutes, ok := strings.Cut(clock, "/")
	if !ok {
		return seasonWindow{}, invalid
	}
	length, err := strconv.Atoi(strings.TrimSpace(minutes))
	if err != nil || length < 1 {
		return seasonWindow{}, invalid
	}
	start = strings.TrimSpace(start)
	day, err := newSession(start, start, strings.TrimSpace(zone))
	if err != nil {
		return seasonWindow{}, fmt.Errorf("window %q: %w", value, err)
	}
	since := time.Duration(day.start)
	clock = fmt.Sprintf("%02d:%02d", since/time.Hour, since%time.Hour/time.Minute)
	return seasonWindow{
		name:   fmt.Sprintf("%s/%d@%s", clock, length, day.loc),
		start:  day.start,
		length: time.Duration(length) * time.Minute,
		loc:    day.loc,
	}, nil
}

// misaligned returns a start of the window that doesn't fall on the open of a
// candle of tf, false when each does: the 15th of each month of a year, both
// daylight saving times of its zone among them. The candles open at the
// multiples of their timeframe from the Unix epoch, as the MT5 ones do.
func (w seasonWindow) misaligned(tf time.Duration) (time.Time, bool) {
	for month := time.January; month <= time.December; month++ {
		start := w.start.on(time.Date(2024, month, 15, 12, 0, 0, 0, w.loc), w.loc, 0)
		if start.UnixNano()%int64(tf) != 0 {
			return start, true
		}
	}
	return time.Time{}, false
}

// seasonDay is a window on a day it traded: when it ended, and its return.
type seasonDay struct {
	end time.Time
	ret float64
}

// traded returns the days on which the window traded in df, candles of tf,
// from the oldest: the candle opening at its start and the one closing at its
// end both in df, with its return from the open of the first to the close of
// the last. A window still open at the last candle of df isn't among them.
func (w seasonWindow) traded(df *model.Dataframe, tf time.Duration) []seasonDay {
	if len(df.Time) == 0 {
		return nil
	}
	first, last := df.Time[0], df.Time[len(df.Time)-1]
	var days []seasonDay
	for day := 0; ; day++ {
		start := w.start.on(first, w.loc, day)
		if start.After(last) {
			break
		}
		end := start.Add(w.length)
		i, opened := seasonCandleAt(df.Time, start)
		j, closed := seasonCandleAt(df.Time, end.Add(-tf))
		if !opened || !closed || i >= len(df.Open) || j >= len(df.Close) || !(df.Open[i] > 0) {
			continue
		}
		days = append(days, seasonDay{end: end, ret: df.Close[j]/df.Open[i] - 1})
	}
	return days
}

// seasonCandleAt returns the index of the candle of times opened at t, false
// without one.
func seasonCandleAt(times []time.Time, t time.Time) (int, bool) {
	return slices.BinarySearchFunc(times, t, time.Time.Compare)
}

// seasonBias is the bias of a window on a day: the days measured, the mean of
// their returns, and its t statistic.
type seasonBias struct {
	days    int
	mean, t float64
}

// Seasonality trades each window of the day in the direction of its bias, a
// BUY when its returns of the days before were significantly positive, a
// SELL when they were significantly negative, from its start to its end. See
// SeasonalityConfig.
type Seasonality struct {
	cfg     SeasonalityConfig
	windows []seasonWindow
	tf      time.Duration
	warmup  int
	bracket *bracket
	held    map[string]seasonHeld // the window of the position followed, by pair
}

// seasonHeld is the window of a position of Seasonality: the trade of the
// bracket it is for, and the end of the window, when it is closed.
type seasonHeld struct {
	trade *scalpTrade
	end   time.Time
}

// seasonalityRules are the rules of Seasonality laid out from its
// configuration: its windows, the duration of a candle and the warmup.
type seasonalityRules struct {
	windows []seasonWindow
	tf      time.Duration
	warmup  int
}

// rules validates the configuration and lays out its windows.
func (c SeasonalityConfig) rules() (seasonalityRules, error) {
	if err := c.BracketConfig.validate(); err != nil {
		return seasonalityRules{}, err
	}
	switch {
	case c.Timeframe == "":
		return seasonalityRules{}, errors.New("timeframe is required")
	case c.MinDays < 2:
		return seasonalityRules{}, errors.New("the min days of a window must be 2 at least: a deviation needs two " +
			"returns")
	case c.Days < c.MinDays:
		return seasonalityRules{}, errors.New("the days of a window measured must be at least its min days")
	case math.IsNaN(c.MinT) || math.IsInf(c.MinT, 0) || c.MinT < 0:
		return seasonalityRules{}, errors.New("the min t statistic can't be negative")
	}

	windows, err := parseSeasonWindows(c.Windows)
	if err != nil {
		return seasonalityRules{}, err
	}

	// the candles handed to the strategy reach back to the start of each window
	// days days before, an hour more for a change of the daylight saving time
	// in between, as if the market traded every hour of every day (a day with
	// fewer candles, or none, leaves room for more days), plus the seed of the
	// ATR; capped at seasonalityMaxWarmup, they hold fewer days than Days,
	// MinDays at least
	warmup := func(days int) (int, time.Duration, error) {
		candles, tf, err := timeframeCandles(c.Timeframe,
			time.Duration(min(days, seasonalityMaxWarmup))*rkDay+time.Hour)
		return candles + seedWarmup(c.ATRLen) + 1, tf, err
	}
	candles, tf, err := warmup(c.Days)
	if err != nil {
		return seasonalityRules{}, err
	}
	if needed, _, _ := warmup(c.MinDays); needed > seasonalityMaxWarmup {
		return seasonalityRules{}, fmt.Errorf("the %d min days of a window on %s need %d candles, more than the %d "+
			"the strategy keeps: raise the timeframe or lower the min days", c.MinDays, c.Timeframe, needed,
			seasonalityMaxWarmup)
	}
	for _, w := range windows {
		if w.length%tf != 0 || w.length > 23*time.Hour {
			return seasonalityRules{}, fmt.Errorf("the window %s must last a multiple of the timeframe %s, under 23 "+
				"hours", w.name, c.Timeframe)
		}
		if at, ok := w.misaligned(tf); ok {
			return seasonalityRules{}, fmt.Errorf("the window %s starts at %s UTC on %s, not on the open of a candle "+
				"of %s", w.name, at.UTC().Format("15:04"), at.Format("2006-01-02"), c.Timeframe)
		}
	}

	return seasonalityRules{
		windows: windows,
		tf:      tf,
		warmup:  min(candles, seasonalityMaxWarmup),
	}, nil
}

// NewSeasonality validates the configuration and creates the strategy.
func NewSeasonality(cfg SeasonalityConfig) (*Seasonality, error) {
	rules, err := cfg.rules()
	if err != nil {
		return nil, fmt.Errorf("seasonality: %w", err)
	}
	return &Seasonality{
		cfg:     cfg,
		windows: rules.windows,
		tf:      rules.tf,
		warmup:  rules.warmup,
		bracket: newBracket("SEASONALITY", cfg.Timeframe, cfg.BracketConfig),
		held:    make(map[string]seasonHeld),
	}, nil
}

func (s *Seasonality) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *Seasonality) WarmupPeriod() int {
	return s.warmup
}

// seasonKey is the key of the metadata of name of the window of index window,
// eg: sb_t0.
func seasonKey(name string, window int) string {
	return fmt.Sprintf("sb_%s%d", name, window)
}

// seasonColors are the colors of the windows on the chart, in turn.
var seasonColors = []string{colorLine, colorSlow, colorOther}

func (s *Seasonality) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["sb_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)

	metrics := make([]strategy.IndicatorMetric, 0, len(s.windows)+2)
	for k, w := range s.windows {
		t, days, mean := s.biases(df, w)
		df.Metadata[seasonKey("t", k)], df.Metadata[seasonKey("days", k)] = t, days
		df.Metadata[seasonKey("mean", k)] = mean
		metrics = append(metrics, strategy.IndicatorMetric{Values: t, Name: "t " + w.name,
			Color: seasonColors[k%len(seasonColors)], Style: strategy.StyleScatter})
	}
	if s.cfg.MinT > 0 {
		upper, lower := make([]float64, len(df.Time)), make([]float64, len(df.Time))
		for i := range upper {
			upper[i], lower[i] = s.cfg.MinT, -s.cfg.MinT
		}
		metrics = append(metrics, line(upper, fmt.Sprintf("t %g", s.cfg.MinT), colorUp),
			line(lower, fmt.Sprintf("t %g", -s.cfg.MinT), colorDown))
	}
	// the statistic is drawn where it is known, from the first candle
	return []strategy.ChartIndicator{pane(df, "Seasonality", 1, metrics...)}
}

// biases returns the bias of the window w on each candle of df closing at its
// start, NaN elsewhere, see bias: its t statistic, NaN too when it isn't
// known, the days measured and the mean of their returns. Each is measured on
// the days of the window ended by the close of its candle, the candles after
// it never read.
func (s *Seasonality) biases(df *model.Dataframe, w seasonWindow) (t, days, mean []float64) {
	n := len(df.Time)
	t, days, mean = missing(n), missing(n), missing(n)
	if n == 0 {
		return t, days, mean
	}
	traded := w.traded(df, s.tf)
	first, last := df.Time[0], df.Time[n-1]
	for day := 0; ; day++ {
		start := w.start.on(first, w.loc, day)
		if start.After(last.Add(s.tf)) {
			break
		}
		i, ok := seasonCandleAt(df.Time, start.Add(-s.tf))
		if !ok {
			continue
		}
		b, known := s.bias(traded, start)
		days[i], mean[i] = float64(b.days), b.mean
		if known {
			t[i] = b.t
		}
	}
	return t, days, mean
}

// bias returns the bias of the window starting at start from days, the days
// it traded: the last Days of the ones ended by then, never the one starting
// at start; false with fewer than MinDays of them, or their returns all alike.
func (s *Seasonality) bias(days []seasonDay, start time.Time) (seasonBias, bool) {
	ended := sort.Search(len(days), func(i int) bool { return days[i].end.After(start) })
	sample := days[max(0, ended-s.cfg.Days):ended]
	n := len(sample)
	if n == 0 {
		return seasonBias{}, false
	}

	mean := 0.0
	for _, day := range sample {
		mean += day.ret
	}
	mean /= float64(n)
	if n < s.cfg.MinDays {
		return seasonBias{days: n, mean: mean}, false
	}
	variance := 0.0
	for _, day := range sample {
		variance += (day.ret - mean) * (day.ret - mean)
	}
	variance /= float64(n - 1)
	if !(variance > 0) {
		return seasonBias{days: n, mean: mean}, false
	}
	return seasonBias{days: n, mean: mean, t: mean / math.Sqrt(variance/float64(n))}, true
}

// seasonEntry is a window to trade from the close of the last candle: its
// index, the side of its bias, and the bias.
type seasonEntry struct {
	window int
	side   model.SideType
	bias   seasonBias
}

// signal returns the window starting at the close of the last candle whose
// bias trades it, false for none: of several, the one of the largest |t|, the
// first on a tie.
func (s *Seasonality) signal(df *model.Dataframe) (seasonEntry, bool) {
	i := len(df.Time) - 1
	if i < 0 {
		return seasonEntry{}, false
	}
	at := df.Time[i].Add(s.tf)

	var entry seasonEntry
	found := false
	for k, w := range s.windows {
		if !w.start.last(at, w.loc).Equal(at) {
			continue
		}
		m, ok := meta(df, seasonKey("t", k), seasonKey("days", k), seasonKey("mean", k))
		if !ok {
			continue
		}
		t, days := m[seasonKey("t", k)][i], m[seasonKey("days", k)][i]
		if isNa(t) || t == 0 || math.Abs(t) < s.cfg.MinT {
			if isNa(days) {
				days = 0
			}
			log.Debugf("[%s] %s %s | %s | t %.2f over %.0f days: not traded", s.bracket.name, df.Pair,
				s.cfg.Timeframe, w.name, t, days)
			continue
		}
		if found && math.Abs(t) <= math.Abs(entry.bias.t) {
			continue
		}
		side := model.SideTypeBuy
		if t < 0 {
			side = model.SideTypeSell
		}
		entry = seasonEntry{window: k, side: side,
			bias: seasonBias{days: int(days), mean: m[seasonKey("mean", k)][i], t: t}}
		found = true
	}
	return entry, found
}

// exit closes the position at the end of its window.
func (s *Seasonality) exit(df *model.Dataframe, held model.SideType) string {
	at := df.Time[len(df.Time)-1].Add(s.tf)
	if end := s.end(df, held); at.Before(end) {
		return ""
	}
	return "WINDOW END"
}

// end returns the end of the window of the position held on side on the pair
// of df: the one it was opened in; for a position found held, eg: opened
// before a restart, the window started last at or before its opening, or at
// or before the open of the last candle when the broker doesn't tell it (of
// windows starting together, the shortest), the position closed at once when
// that window is over, eg: a manual one opened out of the windows.
func (s *Seasonality) end(df *model.Dataframe, side model.SideType) time.Time {
	trade := s.bracket.trades[df.Pair]
	if held, ok := s.held[df.Pair]; ok && held.trade == trade {
		return held.end
	}

	at := df.Time[len(df.Time)-1]
	if trade != nil && !trade.opened.IsZero() {
		at = trade.opened
	}
	var start, end time.Time
	for _, w := range s.windows {
		begun := w.start.last(at, w.loc)
		if start.IsZero() || begun.After(start) || begun.Equal(start) && begun.Add(w.length).Before(end) {
			start, end = begun, begun.Add(w.length)
		}
	}
	s.held[df.Pair] = seasonHeld{trade: trade, end: end}
	log.Infof("[%s] %s %s | the %s position found held until the end of its window at %s UTC", s.bracket.name,
		df.Pair, s.cfg.Timeframe, side, end.UTC().Format(time.RFC3339))
	return end
}

func (s *Seasonality) OnCandle(df *model.Dataframe, broker service.Broker) {
	entry, ok := s.signal(df)
	flat := s.bracket.manage(df, broker, s.exit)
	if flat {
		delete(s.held, df.Pair) // its window over, or closed by its stop loss or take profit
	}
	if !ok {
		return
	}

	w := s.windows[entry.window]
	start := df.Time[len(df.Time)-1].Add(s.tf)
	end := start.Add(w.length)
	if !flat {
		log.Infof("[%s] %s %s | %s | t %.2f | not traded: a position is held", s.bracket.name, df.Pair,
			s.cfg.Timeframe, w.name, entry.bias.t)
		return
	}
	log.Infof("[%s] %s %s | %s | %d days | mean %+.4f%% | t %.2f | %s until %s UTC", s.bracket.name, df.Pair,
		s.cfg.Timeframe, w.name, entry.bias.days, 100*entry.bias.mean, entry.bias.t, entry.side,
		end.UTC().Format(time.RFC3339))

	atr, ok := meta(df, "sb_atr")
	if !ok {
		return
	}
	if s.bracket.open(df, broker, entry.side, atr["sb_atr"].Last(0)) {
		s.held[df.Pair] = seasonHeld{trade: s.bracket.trades[df.Pair], end: end}
	}
}
