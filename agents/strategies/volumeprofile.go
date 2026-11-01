package strategies

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
)

// VolumeProfileConfig holds the inputs of VolumeProfile, with its defaults in
// DefaultVolumeProfileConfig: the "80% rule" of the value area of the Market
// Profile of J. Peter Steidlmayer, as James Dalton tells it in "Mind Over
// Markets", read on the volume of the candles. Periods are counted in candles
// of Timeframe.
type VolumeProfileConfig struct {
	Timeframe string

	// The session is a day from Anchor, HH:MM, in the time zone Timezone, an
	// IANA name, eg: UTC, as the VWAP of VWAPReclaim, the candles placed in it
	// by their open time. The previous session of one is the last session
	// before it holding candles, the previous trading day of the Market
	// Profile: a day without candles, eg: a weekend or a holiday, is skipped.
	Timezone string
	Anchor   string

	// The volume profile of the previous session: the volume of each of its
	// candles (the real volume of MT5, else its tick volume) spread evenly
	// over the range of the candle, from its low to its high, into Bins price
	// bins of the same height from the low of the session to its high, a
	// candle without range all in its bin; a session without volume, eg: from
	// a feed without it, weighs its candles the same, the time spent at each
	// price as the TPOs of the Market Profile count it. The point of control,
	// the POC, is the bin with the most volume (on a tie, the one closest to
	// the middle of the range, the lower one when as close), and the value
	// area the bins around it holding ValueArea of the volume of the session,
	// 0.70 for the 70% of Steidlmayer, about a standard deviation, built as the
	// CBOT's Market Profile method builds it (Dalton, "Mind Over Markets"):
	// from the POC, the volume of the two bins above the value area so far is
	// compared with the one of the two bins below it, and the larger pair is
	// added (a single bin on a side with one left, the other side alone once a
	// side has none), until the bins added hold that share. The method doesn't
	// settle a tie of the two pairs: both are added then, so the value area
	// leans to neither side. The value area high and low are the top of its
	// highest bin and the bottom of its lowest one.
	Bins      int
	ValueArea float64

	// The rule: a session opening outside the value area of the previous one,
	// its first candle opening above the value area high or below its low,
	// then closing back inside it on Confirm consecutive candles, 2 on the
	// 30m timeframe for the two 30 minute periods of Dalton, the opening
	// candle included when it closes inside, is likely to fill it, crossing
	// it to its other edge. The close of the last of them enters toward that
	// edge: a SELL after an open above, a BUY after an open below; only on
	// that close (a run of closes inside going on doesn't enter later), not
	// on the candle whose close ends the positions of the session (see
	// BracketConfig below) nor after it, and not once the session traded at
	// that edge, the value area already filled. One position per session and
	// pair at most: after a restart, the positions of the session opened
	// before it count too, when the broker tells them, as in ORBConfig.
	Confirm int

	// The exits and the sizing, see BracketConfig: the take profit at the
	// other edge of the value area; the stop loss StopATR ATR, of ATRLen
	// candles, beyond the extreme of the session outside the value area (its
	// high after an open above, its low after an open below), or StopATR ATR
	// from the entry close, the farther, the first one always so; and the end
	// of the session the position was opened in closes it at the market: the
	// close of the last candle ending before the end, the next one closing at
	// it, when the market is closed (17:00 in New York, the daily break of the
	// CME and of gold, with the default session), as CMEGap closes on the last
	// candle before the Friday close; or a later candle when that one is
	// missing, eg: the market closed earlier, the first candle of a later
	// session then. The rule fills the value area within the session, the
	// next one having a value area of its own. StopValue and TakeProfitValue
	// replace the stop loss and the take profit, MaxBars closes the position
	// earlier, and TakeProfitATR isn't used.
	BracketConfig
}

// DefaultVolumeProfileConfig returns the defaults of VolumeProfile, on the 30m
// timeframe, the period of the Market Profile: the value area of 70% of the
// volume of the previous trading day, in 50 bins, entered after two closes
// back inside it, the stop loss an ATR beyond the extreme of the session. The
// trading day is the one of the CME, from 17:00 to 17:00 in New York (the
// Globex day of the gold and bitcoin futures, the Market Profile taking the
// day of the exchange): a midnight anchor would cut it in two, and leave the
// session of a Friday open over the weekend, gold closing before midnight UTC.
func DefaultVolumeProfileConfig() VolumeProfileConfig {
	return VolumeProfileConfig{
		Timeframe: "30m",

		Timezone: "America/New_York",
		Anchor:   "17:00",

		Bins:      50,
		ValueArea: 0.70,

		Confirm: 2,

		BracketConfig: DefaultBracketConfig(),
	}
}

func (c VolumeProfileConfig) validate() error {
	exits := c.BracketConfig
	exits.TakeProfitATR = 1 // unused: the take profit at the other edge of the value area
	if err := exits.validate(); err != nil {
		return err
	}

	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case c.Bins < 2:
		return errors.New("the profile needs 2 bins at least")
	case !(c.ValueArea > 0 && c.ValueArea <= 1):
		return errors.New("the value area must be a share of the volume, above 0 and at most 1")
	case c.Confirm < 1:
		return errors.New("confirmation candles must be at least 1")
	}
	return nil
}

// VolumeProfile trades the 80% rule of the value area of the previous session:
// a SELL when a session opening above it closes back inside it, toward its low,
// a BUY when one opening below it closes back inside it, toward its high. See
// VolumeProfileConfig.
type VolumeProfile struct {
	cfg     VolumeProfileConfig
	day     session // from the anchor to the next one
	tf      time.Duration
	warmup  int
	bracket *bracket
	traded  map[string]breakoutSession // the positions opened in the current session, by pair
	entries map[string]vpEntry         // the position of the strategy followed, by pair
}

// vpEntry is a position VolumeProfile opened, followed by its bracket as
// trade, and the start of its session.
type vpEntry struct {
	trade *scalpTrade
	start time.Time
}

// NewVolumeProfile validates the configuration and creates the strategy.
func NewVolumeProfile(cfg VolumeProfileConfig) (*VolumeProfile, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("volumeprofile: %w", err)
	}
	day, err := newSession(cfg.Anchor, cfg.Anchor, cfg.Timezone)
	if err != nil {
		return nil, fmt.Errorf("volumeprofile: %w", err)
	}

	// the window holds the session of the last candle and the previous one,
	// 25 hours long each at most (a change of the daylight saving time), and
	// a candle before them, which tells that the previous one starts in the
	// window; the days without candles hold none, and the seed of the ATR is
	// washed out
	candles, tf, err := timeframeCandles(cfg.Timeframe, 2*(rkDay+time.Hour))
	if err != nil {
		return nil, fmt.Errorf("volumeprofile: %w", err)
	}
	if time.Duration(cfg.Confirm+1)*tf >= rkDay-time.Hour {
		return nil, fmt.Errorf("volumeprofile: a session must hold the %d confirmation candles of %s and one "+
			"more", cfg.Confirm, cfg.Timeframe)
	}

	return &VolumeProfile{
		cfg:     cfg,
		day:     day,
		tf:      tf,
		warmup:  max(candles+1, seedWarmup(cfg.ATRLen)),
		bracket: newBracket("VOLUMEPROFILE", cfg.Timeframe, cfg.BracketConfig),
		traded:  make(map[string]breakoutSession),
		entries: make(map[string]vpEntry),
	}, nil
}

func (s *VolumeProfile) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *VolumeProfile) WarmupPeriod() int {
	return s.warmup
}

func (s *VolumeProfile) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	high, poc, low := s.levels(df)
	df.Metadata["vp_vah"], df.Metadata["vp_poc"], df.Metadata["vp_val"] = high, poc, low
	df.Metadata["vp_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)

	return []strategy.ChartIndicator{
		{
			Overlay:   true,
			GroupName: "Volume Profile",
			Time:      df.Time,
			Metrics: []strategy.IndicatorMetric{
				{Values: high, Name: "Value area high", Color: colorUp, Style: strategy.StyleScatter},
				{Values: poc, Name: "POC", Color: colorLine, Style: strategy.StyleScatter},
				{Values: low, Name: "Value area low", Color: colorDown, Style: strategy.StyleScatter},
			},
		},
	}
}

// vpProfile is the volume profile of a session, see VolumeProfileConfig: the
// high and the low of its value area, and its point of control, the middle of
// its bin.
type vpProfile struct {
	high, poc, low float64
}

// levels returns the value area high, the POC and the value area low of the
// previous session of each candle of df, from the candles of that session
// only, all of them before the candle: NaN when the window doesn't reach back
// to its start (it may miss candles of it), or it has no range.
func (s *VolumeProfile) levels(df *model.Dataframe) (high, poc, low []float64) {
	n := len(df.Time)
	high, poc, low = missing(n), missing(n), missing(n)
	if len(df.High) != n || len(df.Low) != n {
		return high, poc, low
	}

	var (
		start   time.Time // of the session of the candles from first
		first   int
		profile vpProfile // of the session before it
		known   bool
	)
	for i, t := range df.Time {
		if at := s.day.start.last(t, s.day.loc); i == 0 || !at.Equal(start) {
			// a session starts: the one before ended, the profile of the
			// candles from first on, when the window holds its start
			profile, known = vpProfile{}, false
			if i > 0 && !df.Time[0].After(start) {
				profile, known = volumeProfile(df, first, i, s.cfg.Bins, s.cfg.ValueArea)
			}
			start, first = at, i
		}
		if known {
			high[i], poc[i], low[i] = profile.high, profile.poc, profile.low
		}
	}
	return high, poc, low
}

// volumeProfile returns the volume profile of the candles of df from index
// from to index to, excluded, in bins bins, its value area holding share of
// their volume, see VolumeProfileConfig: false when they have no range.
func volumeProfile(df *model.Dataframe, from, to, bins int, share float64) (vpProfile, bool) {
	low, high, volume := math.Inf(1), math.Inf(-1), 0.0
	for i := from; i < to; i++ {
		low, high = math.Min(low, df.Low[i]), math.Max(high, df.High[i])
		if i < len(df.Volume) && df.Volume[i] > 0 {
			volume += df.Volume[i]
		}
	}
	if !(high > low) || math.IsInf(high-low, 0) {
		return vpProfile{}, false
	}

	height := (high - low) / float64(bins)
	bin := func(price float64) int { return min(bins-1, max(0, int((price-low)/height))) }
	edge := func(k int) float64 { // the bottom of bin k, the high of the range for bins
		if k >= bins {
			return high
		}
		return low + float64(k)*height
	}

	profile := make([]float64, bins)
	for i := from; i < to; i++ {
		weight := 1.0 // each candle the same without volume
		if volume > 0 {
			weight = 0
			if i < len(df.Volume) && df.Volume[i] > 0 {
				weight = df.Volume[i]
			}
		}
		if weight == 0 {
			continue
		}
		bottom, top := df.Low[i], df.High[i]
		if !(top > bottom) {
			profile[bin(bottom)] += weight
			continue
		}
		for k := bin(bottom); k <= bin(top); k++ {
			if overlap := math.Min(top, edge(k+1)) - math.Max(bottom, edge(k)); overlap > 0 {
				profile[k] += weight * overlap / (top - bottom)
			}
		}
	}

	// the point of control, the one closest to the middle of the range on a
	// tie, the lower one when as close
	poc, middle, total := 0, float64(bins-1)/2, profile[0]
	for k := 1; k < bins; k++ {
		total += profile[k]
		if profile[k] > profile[poc] ||
			profile[k] == profile[poc] && math.Abs(float64(k)-middle) < math.Abs(float64(poc)-middle) {
			poc = k
		}
	}

	// the value area, from the POC, as the CBOT builds it: the two bins above
	// it against the two below it, the larger pair added (one bin on a side
	// with one left, none on a side with none), both pairs on a tie, until it
	// holds its share of the volume
	bottomBin, topBin, inside := poc, poc, profile[poc]
	for inside < share*total && (bottomBin > 0 || topBin < bins-1) {
		belowBins, aboveBins := min(2, bottomBin), min(2, bins-1-topBin)
		below, above := 0.0, 0.0
		for k := 1; k <= belowBins; k++ {
			below += profile[bottomBin-k]
		}
		for k := 1; k <= aboveBins; k++ {
			above += profile[topBin+k]
		}
		switch {
		case aboveBins == 0 || belowBins > 0 && below > above:
			bottomBin, inside = bottomBin-belowBins, inside+below
		case belowBins == 0 || above > below:
			topBin, inside = topBin+aboveBins, inside+above
		default:
			bottomBin, topBin, inside = bottomBin-belowBins, topBin+aboveBins, inside+below+above
		}
	}
	return vpProfile{high: edge(topBin + 1), poc: (edge(poc) + edge(poc+1)) / 2, low: edge(bottomBin)}, true
}

// signal returns the entry of the last candle, if any, with its stop loss and
// take profit, and the start of the session of the candle.
func (s *VolumeProfile) signal(df *model.Dataframe) (entry model.SideType, stop, profit float64, start time.Time) {
	m, ok := meta(df, "vp_vah", "vp_val", "vp_atr")
	n := len(df.Close)
	if !ok || n == 0 || len(df.Time) != n || len(df.Open) != n || len(df.High) != n || len(df.Low) != n {
		return "", 0, 0, start
	}
	i := n - 1
	start, end := s.day.bounds(df.Time[i])
	high, low, atr, closePrice := m["vp_vah"][i], m["vp_val"][i], m["vp_atr"][i], df.Close[i]
	if isNa(high) || isNa(low) || !(high > low) || s.bracket.belowCost(closePrice, atr) ||
		df.Time[0].After(start) || s.ending(df.Time[i], end) {
		// no value area, the ATR too small, the open of the session out of the
		// window, or the candle closing the positions of the session or after
		// it
		return "", 0, 0, start
	}

	// the closes inside the value area since the last one out of it, in the
	// session: Confirm, the run of them just completed
	first := i
	for first > 0 && !df.Time[first-1].Before(start) {
		first--
	}
	run := 0
	for k := i; k >= first && run <= s.cfg.Confirm && df.Close[k] >= low && df.Close[k] <= high; k-- {
		run++
	}
	if run != s.cfg.Confirm {
		return "", 0, 0, start
	}

	// the side of the open, and the extremes of the session since then
	sessionHigh, sessionLow := df.High[first], df.Low[first]
	for k := first + 1; k <= i; k++ {
		sessionHigh, sessionLow = math.Max(sessionHigh, df.High[k]), math.Min(sessionLow, df.Low[k])
	}
	buffer := s.cfg.StopATR * atr
	switch open := df.Open[first]; {
	case open > high && sessionLow > low:
		entry, profit, stop = model.SideTypeSell, low, math.Max(sessionHigh, closePrice)+buffer
	case open < low && sessionHigh < high:
		entry, profit, stop = model.SideTypeBuy, high, math.Min(sessionLow, closePrice)-buffer
	default:
		return "", 0, 0, start // opened inside, or the value area filled already
	}
	if !enoughReward(entry, closePrice, stop, profit, 0) {
		return "", 0, 0, start
	}

	log.Debugf("[VOLUMEPROFILE] %s %s | %s | value area %f-%f | open %f | close %f", df.Pair, s.cfg.Timeframe,
		entry, low, high, df.Open[first], closePrice)
	return entry, stop, profit, start
}

// exit closes the position at the end of the session it was opened in, on the
// last candle ending before it, see ending.
func (s *VolumeProfile) exit(df *model.Dataframe, _ model.SideType) string {
	i := len(df.Time) - 1
	if _, end := s.day.bounds(s.opened(df)); s.ending(df.Time[i], end) {
		return "SESSION END"
	}
	return ""
}

// ending reports whether the candle opened at t closes the positions of a
// session ending at end: the last candle ending before it, the next one
// closing at it or after it, when the market may be closed (eg: 17:00 in New
// York, the daily break of the CME and of gold) and an order sent then waits
// for the reopen; or a later one, that one missing.
func (s *VolumeProfile) ending(t, end time.Time) bool {
	return !t.Add(2 * s.tf).Before(end)
}

// opened returns a time in the session the position followed on the pair of df
// was opened in: the start of the session of the entry of the strategy; the
// opening of a position found, eg: after a restart, when the broker tells it;
// else the last candle, the position then closed at the end of its session.
func (s *VolumeProfile) opened(df *model.Dataframe) time.Time {
	trade := s.bracket.trades[df.Pair]
	if own, ok := s.entries[df.Pair]; ok && trade != nil && own.trade == trade {
		return own.start
	}
	if trade != nil && !trade.opened.IsZero() {
		return trade.opened
	}
	return df.Time[len(df.Time)-1]
}

func (s *VolumeProfile) OnCandle(df *model.Dataframe, broker service.Broker) {
	entry, stop, profit, start := s.signal(df)
	if !s.bracket.manage(df, broker, s.exit) {
		return
	}
	delete(s.entries, df.Pair) // flat: the position of the strategy closed, if any
	if entry == "" {
		return
	}

	traded, ok := s.traded[df.Pair]
	if !ok || !traded.start.Equal(start) {
		// a new session, or the first one of the run: its positions opened
		// before a restart, if any
		traded = breakoutSession{start: start, trades: openedBefore(broker, df.Pair, start)}
	}
	if traded.trades > 0 {
		return // one per session
	}
	if s.bracket.openAt(df, broker, entry, stop, profit) {
		traded.trades++
		s.entries[df.Pair] = vpEntry{trade: s.bracket.trades[df.Pair], start: start}
	}
	s.traded[df.Pair] = traded
}
