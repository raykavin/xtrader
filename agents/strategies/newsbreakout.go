package strategies

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
)

// NewsBreakout trades the breakout of the range the market draws right after
// a scheduled economic release (eg: the payrolls, the CPI, the rate decision
// of the Fed): the release moves the price, the high and the low of the first
// minutes after it hold the reaction, and the first close beyond them, after
// that wait, enters in its direction. It is the opening range breakout of ORB
// with the release in place of the opening of the session: the range of ORB
// is the high and the low of the candles of the first minutes of the session,
// here of the candles of the first minutes after the release; its breakouts
// are traded until the end of the session, here until the expiry after the
// wait; its stop loss and take profit are StopATR and TakeProfitATR ATR from
// the entry close, and a close back past the middle of the range closes the
// position, the breakout failed, as in ORB.
//
// The releases come from a source the runner gives the strategy (see
// ReleasesUser): live and in paper, the calendar of the news of the controller
// (the tab Notícias of the dashboard), the one the news guard reads; in a
// backtest, the releases of its period read once by the controller. Only what
// is known before a release travels: its time, its event and its importance,
// never its values, so a backtest doesn't look ahead. Without a source, eg: an
// agent run on its own, out of the controller, or with the news turned off,
// the strategy has no release to trade, and doesn't enter: it logs that it
// waits for them.
//
// The news guard of the agents holds every entry from BlockBefore before a
// release of its importance to BlockAfter after it, and closes the positions
// at its start: Wait, 15 minutes by default, the BlockAfter of the news by
// default (sql/seeds/0006_news.sql), places the first breakout after the guard
// lets the entries go again. A Wait shorter than the BlockAfter of the news
// sees its entries refused by the guard until the end of its window (they
// count as no trade: a later breakout may enter). The backtests don't run the
// guard.

// NewsBreakoutConfig holds the inputs of NewsBreakout, with its defaults in
// DefaultNewsBreakoutConfig.
type NewsBreakoutConfig struct {
	Timeframe string

	// The releases traded: the timed ones (see Release.Timed: an all-day or
	// tentative release has no time to trade around) of Importance at least,
	// of the currencies of the pair, as the source tells them (eg: USD for
	// XAUUSDm and BTCUSDm), and, when Codes holds any, of one of these MQL5
	// event codes only (Release.Code, the event_code of the calendar of the
	// terminal), compared without case. The releases at the same time, eg: the
	// payrolls and the unemployment rate, are one release: one range, one
	// trade.
	Importance ReleaseImportance
	Codes      []string

	// The range is the high and the low of the candles inside Wait from the
	// release, opened at or after it and closed by the end of Wait: the candle
	// holding the release time, opened before it, is left out. Its breakouts
	// are traded from the end of Wait until Expiry after it, or until the next
	// release traded if sooner, when a position still open is closed at the
	// market, as ORB closes it at the end of its session. Both in minutes.
	Wait   time.Duration
	Expiry time.Duration

	// A breakout is the first close beyond the range, the close before it
	// inside, as in ORB, by BufferATR at least, in ATR of ATRLen candles: 0 by
	// default, any close beyond the range, ORB asking 0.1 of its own. One
	// position is opened per release and pair at most: after a restart, the
	// positions of the pair opened since the end of the Wait before it count
	// too, when the broker tells them (service.PositionOpenings, see
	// ORBConfig). MaxRangeATR, above 0, skips the releases whose range is
	// wider than that many ATR, the move already done, as in ORB.
	BufferATR   float64
	MaxRangeATR float64

	// The exits and the sizing, see BracketConfig. The close back past the
	// middle of the range also closes the position: the breakout failed.
	BracketConfig
}

// DefaultNewsBreakoutConfig returns the defaults of NewsBreakout: the
// releases of high importance, the range of the 15 minutes after each, the
// window of the news guard after a release by default, broken by any close
// beyond it within the 60 minutes after it, on the 5m timeframe; the stop loss
// and the take profit of ORB (see DefaultORBConfig).
func DefaultNewsBreakoutConfig() NewsBreakoutConfig {
	exits := DefaultBracketConfig()
	exits.TakeProfitATR = 2

	return NewsBreakoutConfig{
		Timeframe: "5m",

		Importance: ReleaseHigh,

		Wait:   15 * time.Minute,
		Expiry: 60 * time.Minute,

		BracketConfig: exits,
	}
}

// validate validates the configuration and returns the duration of a candle.
func (c NewsBreakoutConfig) validate() (time.Duration, error) {
	if err := c.BracketConfig.validate(); err != nil {
		return 0, err
	}
	if c.Timeframe == "" {
		return 0, errors.New("timeframe is required")
	}
	_, tf, err := timeframeCandles(c.Timeframe, 0)
	if err != nil {
		return 0, err
	}

	switch {
	case c.Importance < ReleaseLow || c.Importance > ReleaseHigh:
		return 0, fmt.Errorf("invalid importance %d: low, medium or high", c.Importance)
	case c.Wait <= 0 || c.Wait%time.Minute != 0:
		return 0, errors.New("the wait after a release must be a positive number of minutes")
	case c.Wait < tf:
		return 0, fmt.Errorf("the wait after a release must hold a candle of %s at least", c.Timeframe)
	case c.Expiry <= 0 || c.Expiry%time.Minute != 0:
		return 0, errors.New("the expiry of the breakouts must be a positive number of minutes")
	case c.Expiry <= tf:
		return 0, fmt.Errorf("the expiry of the breakouts must be longer than a candle of %s", c.Timeframe)
	case c.BufferATR < 0:
		return 0, errors.New("buffer ATR multiple can't be negative")
	case c.MaxRangeATR < 0:
		return 0, errors.New("max range ATR multiple can't be negative")
	}
	for _, code := range c.Codes {
		if strings.TrimSpace(code) == "" {
			return 0, errors.New("an event code can't be empty")
		}
	}
	return tf, nil
}

// ParseReleaseImportance reads the importance of a release as the flags give
// it: low, medium or high, as the importance of the news of the dashboard.
func ParseReleaseImportance(value string) (ReleaseImportance, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low":
		return ReleaseLow, nil
	case "medium":
		return ReleaseMedium, nil
	case "high":
		return ReleaseHigh, nil
	}
	return ReleaseNone, fmt.Errorf("invalid importance %q: low, medium or high", value)
}

// ParseReleaseCodes reads a list of event codes separated by commas or
// semicolons, as the flags give it (the other lists of the agents take ;),
// the blanks around each and the empty ones dropped: none for an empty list,
// all the events.
func ParseReleaseCodes(value string) []string {
	var codes []string
	for _, code := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' }) {
		if code = strings.TrimSpace(code); code != "" {
			codes = append(codes, code)
		}
	}
	return codes
}

// The reads of the releases by NewsBreakout, on the clock of the candles, the
// close of the last one, never the wall clock, so a backtest reads them as live
// does, whatever the speed of its candles: on a candle, once newsRefresh has
// passed since the last read (live, the calendar changes: a release added,
// moved, timed; a backtest replaying a source that changes as live does sees
// the change on the same candle), or at once when the releases read no longer
// cover the candle, each read given newsReadTimeout of the wall clock at most,
// the releases of newsAhead after the candle read with it. A read that fails
// keeps the releases read before it, and is tried again once newsRefresh has
// passed.
const (
	newsRefresh     = 5 * time.Minute
	newsReadTimeout = 5 * time.Second
	newsAhead       = 24 * time.Hour
)

// NewsBreakout trades the breakouts of the range after the economic releases,
// see the top of this file and NewsBreakoutConfig.
type NewsBreakout struct {
	cfg     NewsBreakoutConfig
	tf      time.Duration
	warmup  int
	codes   map[string]bool // lower case, none for all
	bracket *bracket

	// the span of the releases each candle needs: from a release to the end
	// of its breakouts and a candle, before and after the candle (the next
	// release ending them sooner); the reads reach back, for the chart, and
	// ahead further, to be read again less often
	reach, back, ahead time.Duration

	// the source, counted by generation (each SetReleases one more), and
	// the releases read, by pair: Indicators reads them from the chart, at
	// any time; a schedule, once stored, never changes
	mu         sync.RWMutex
	source     Releases
	generation int
	schedules  map[string]*newsSchedule

	// the state of OnCandle, by pair: its calls are serialized
	reads  map[string]newsRead
	traded map[string]newsTraded
	said   map[string]string // the last state logged, by pair and kind
}

// newsSchedule holds the releases traded of a pair read from from to to, one
// per time, by time, read at read on the clock of the candles (the close of
// the candle it was read on).
type newsSchedule struct {
	from, to time.Time
	read     time.Time
	releases []newsRelease
}

// newsRelease is the time of the releases traded at it, and their names.
type newsRelease struct {
	at    time.Time
	names string
}

// newsRead is the last read of the releases of a pair: when, on the clock of
// the candles, its error, empty when it succeeded, and the generation of its
// source.
type newsRead struct {
	at         time.Time
	err        string
	generation int
}

// newsTraded counts the positions opened on the breakouts of the release at
// release.
type newsTraded struct {
	release time.Time
	trades  int
}

// newsWindow is the window of a release holding a candle: known when the
// releases read cover the candle, release the last one at or before it (zero
// for none), the end of its range (the start of its breakouts) and the end of
// its breakouts, whether the candle is one of them, and the range, NaN unless
// the window of the candles holds it.
type newsWindow struct {
	known         bool
	release       newsRelease
	rangeEnd, end time.Time
	trading       bool
	high, low     float64
}

// NewNewsBreakout validates the configuration and creates the strategy.
func NewNewsBreakout(cfg NewsBreakoutConfig) (*NewsBreakout, error) {
	tf, err := cfg.validate()
	if err != nil {
		return nil, fmt.Errorf("newsbreakout: %w", err)
	}

	// the window of the candles reaches back from the last breakout to the
	// release, a candle more for one not at the open of a candle, with the
	// ATR seeded
	span, _, err := timeframeCandles(cfg.Timeframe, cfg.Wait+cfg.Expiry)
	if err != nil {
		return nil, fmt.Errorf("newsbreakout: %w", err)
	}
	warmup := max(span+1, seedWarmup(cfg.ATRLen)) + 2

	var codes map[string]bool
	if len(cfg.Codes) > 0 {
		codes = make(map[string]bool, len(cfg.Codes))
		for _, code := range cfg.Codes {
			codes[strings.ToLower(strings.TrimSpace(code))] = true
		}
	}

	reach := cfg.Wait + cfg.Expiry + tf
	return &NewsBreakout{
		cfg:       cfg,
		tf:        tf,
		warmup:    warmup,
		codes:     codes,
		bracket:   newBracket("NEWSBREAKOUT", cfg.Timeframe, cfg.BracketConfig),
		reach:     reach,
		back:      max(reach, time.Duration(warmup)*tf),
		ahead:     max(newsAhead, 2*reach),
		schedules: make(map[string]*newsSchedule),
		reads:     make(map[string]newsRead),
		traded:    make(map[string]newsTraded),
		said:      make(map[string]string),
	}, nil
}

// SetReleases sets the source of the releases, see ReleasesUser: the ones
// read from another source before are dropped.
func (s *NewsBreakout) SetReleases(source Releases) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.source = source
	s.generation++
	s.schedules = make(map[string]*newsSchedule)
}

func (s *NewsBreakout) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *NewsBreakout) WarmupPeriod() int {
	return s.warmup
}

// Indicators draws the range of each release on the candles of its breakouts,
// from the releases read by OnCandle: it reads none itself, the chart calling
// it at any time.
func (s *NewsBreakout) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	s.mu.RLock()
	schedule := s.schedules[df.Pair]
	s.mu.RUnlock()

	high, low := s.levels(df, schedule)
	df.Metadata["nb_high"], df.Metadata["nb_low"] = high, low
	df.Metadata["nb_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)

	return []strategy.ChartIndicator{
		{
			Overlay:   true,
			GroupName: "News breakout",
			Time:      df.Time,
			Metrics: []strategy.IndicatorMetric{
				{Values: high, Name: "News range high", Color: colorUp, Style: strategy.StyleScatter},
				{Values: low, Name: "News range low", Color: colorDown, Style: strategy.StyleScatter},
			},
		},
	}
}

func (s *NewsBreakout) OnCandle(df *model.Dataframe, broker service.Broker) {
	if len(df.Close) == 0 {
		return
	}
	s.refresh(df.Pair, df.Time[len(df.Time)-1])

	w := s.window(df)
	entry := s.signal(df, w)
	exit := func(df *model.Dataframe, held model.SideType) string { return s.exit(df, held, w) }
	if !s.bracket.manage(df, broker, exit) || entry == "" {
		return
	}

	traded, ok := s.traded[df.Pair]
	if !ok || !traded.release.Equal(w.release.at) {
		// a new release, or the first one of the run: its positions opened
		// before a restart, if any
		traded = newsTraded{release: w.release.at, trades: openedBefore(broker, df.Pair, w.rangeEnd)}
	}
	if traded.trades >= 1 {
		return
	}
	if s.bracket.open(df, broker, entry, df.Metadata["nb_atr"].Last(0)) {
		traded.trades++
	}
	s.traded[df.Pair] = traded
}

// refresh reads the releases of pair around the candle opened at at, when
// they are due on the clock of the candles, the close of that one, see
// newsRefresh.
func (s *NewsBreakout) refresh(pair string, at time.Time) {
	s.mu.RLock()
	source, generation, schedule := s.source, s.generation, s.schedules[pair]
	s.mu.RUnlock()
	if source == nil {
		s.say(pair, "source", "none", "WAIT | no economic releases given to the strategy (the news of the "+
			"controller, or the releases of the backtest): no entries until they come")
		return
	}

	clock := at.Add(s.tf) // the close of the candle, the time of its decision
	if schedule != nil && schedule.covers(at.Add(-s.reach), at.Add(s.reach)) &&
		clock.Sub(schedule.read) < newsRefresh {
		return
	}
	last, ok := s.reads[pair]
	if ok && last.generation != generation {
		last, ok = newsRead{}, false // of the source replaced
	}
	if ok && last.err != "" && clock.Sub(last.at) < newsRefresh {
		return // failed a moment ago: the releases read before it kept
	}

	from, to := at.Add(-s.back), at.Add(s.ahead)
	ctx, cancel := context.WithTimeout(context.Background(), newsReadTimeout)
	releases, err := source.Releases(ctx, pair, from, to)
	cancel()
	if err != nil {
		s.reads[pair] = newsRead{at: clock, err: err.Error(), generation: generation}
		switch {
		case last.err == err.Error():
			// told already
		case schedule == nil:
			log.Warnf("[NEWSBREAKOUT] %s %s | the economic releases can't be read: %v: no entries until they "+
				"are, tried again every %s of the candles", pair, s.cfg.Timeframe, err, newsRefresh)
		default:
			log.Warnf("[NEWSBREAKOUT] %s %s | the economic releases can't be read: %v: the ones read before kept, "+
				"to %s, tried again every %s of the candles", pair, s.cfg.Timeframe, err,
				schedule.to.Format(time.RFC3339), newsRefresh)
		}
		return
	}
	if last.err != "" {
		log.Infof("[NEWSBREAKOUT] %s %s | the economic releases read again", pair, s.cfg.Timeframe)
	}
	s.reads[pair] = newsRead{at: clock, generation: generation}

	next := &newsSchedule{from: from, to: to, read: clock, releases: s.kept(releases)}
	s.mu.Lock()
	if s.generation == generation { // the source not replaced meanwhile
		s.schedules[pair] = next
	}
	s.mu.Unlock()

	if upcoming, ok := next.after(at); ok {
		when := upcoming.at.Format(time.RFC3339)
		s.say(pair, "next", when, "NEXT | %s at %s: its range over the %s after it, broken in the %s after the "+
			"range", upcoming.names, when, s.cfg.Wait, s.cfg.Expiry)
	} else {
		s.say(pair, "next", "none", "WAIT | no release traded up to %s", to.Format(time.RFC3339))
	}
}

// kept returns the times of the releases traded, see NewsBreakoutConfig,
// by time, the releases at the same time one.
func (s *NewsBreakout) kept(releases []Release) []newsRelease {
	names := make(map[int64][]string)
	for _, release := range releases {
		if !release.Timed || release.Importance < s.cfg.Importance ||
			s.codes != nil && !s.codes[strings.ToLower(strings.TrimSpace(release.Code))] {
			continue
		}
		at := release.Time.UTC().Unix()
		name := release.Name
		if name == "" {
			name = release.Code
		}
		if release.Currency != "" {
			name = release.Currency + " " + name
		}
		names[at] = append(names[at], name)
	}

	out := make([]newsRelease, 0, len(names))
	for at, of := range names {
		out = append(out, newsRelease{at: time.Unix(at, 0).UTC(), names: strings.Join(of, ", ")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

// after returns the first release after t, false for none.
func (sc *newsSchedule) after(t time.Time) (newsRelease, bool) {
	k := sort.Search(len(sc.releases), func(k int) bool { return sc.releases[k].at.After(t) })
	if k == len(sc.releases) {
		return newsRelease{}, false
	}
	return sc.releases[k], true
}

// covers reports whether the schedule holds every release from from to to.
func (sc *newsSchedule) covers(from, to time.Time) bool {
	return !sc.from.After(from) && !sc.to.Before(to)
}

// last returns the last release at or before t, the end of its range and the
// end of its breakouts: Expiry after the range, or the next release if
// sooner; false before the first release.
func (sc *newsSchedule) last(t time.Time, wait, expiry time.Duration) (newsRelease, time.Time, time.Time, bool) {
	k := sort.Search(len(sc.releases), func(k int) bool { return sc.releases[k].at.After(t) })
	if k == 0 {
		return newsRelease{}, time.Time{}, time.Time{}, false
	}
	release := sc.releases[k-1]
	rangeEnd := release.at.Add(wait)
	end := rangeEnd.Add(expiry)
	if k < len(sc.releases) && sc.releases[k].at.Before(end) {
		end = sc.releases[k].at
	}
	return release, rangeEnd, end, true
}

// trading reports whether the candle opened at t is a breakout of the window
// from rangeEnd to end: opened in it, and closed before its end (the candle
// closing at its end closes the position, as in ORB).
func (s *NewsBreakout) trading(t, rangeEnd, end time.Time) bool {
	return !t.Before(rangeEnd) && t.Add(s.tf).Before(end)
}

// rangeOf returns the high and the low of the candles of df inside the range
// of the release at at, opened at or after it and closed by rangeEnd: NaN
// when df doesn't reach back to the release, or holds no candle of it. The
// candles of the range all closed before a breakout opens: a breakout reads no
// candle after it.
func (s *NewsBreakout) rangeOf(df *model.Dataframe, at, rangeEnd time.Time) (high, low float64) {
	high, low = na, na
	if len(df.Time) == 0 || df.Time[0].After(at) {
		return high, low
	}
	for j := sort.Search(len(df.Time), func(j int) bool { return !df.Time[j].Before(at) }); j < len(df.Time); j++ {
		if df.Time[j].Add(s.tf).After(rangeEnd) {
			break
		}
		if isNa(high) {
			high, low = df.High[j], df.Low[j]
		}
		high, low = math.Max(high, df.High[j]), math.Min(low, df.Low[j])
	}
	return high, low
}

// levels returns the range of the release of each candle of df, NaN out of
// the breakouts of a release, see rangeOf.
func (s *NewsBreakout) levels(df *model.Dataframe, schedule *newsSchedule) (high, low []float64) {
	high, low = missing(len(df.Time)), missing(len(df.Time))
	if schedule == nil {
		return high, low
	}

	type extremes struct{ high, low float64 }
	ranges := make(map[int64]extremes) // by the time of the release
	for i, t := range df.Time {
		release, rangeEnd, end, ok := schedule.last(t, s.cfg.Wait, s.cfg.Expiry)
		if !ok || !s.trading(t, rangeEnd, end) {
			continue
		}
		levels, done := ranges[release.at.Unix()]
		if !done {
			levels.high, levels.low = s.rangeOf(df, release.at, rangeEnd)
			ranges[release.at.Unix()] = levels
		}
		high[i], low[i] = levels.high, levels.low
	}
	return high, low
}

// window returns the window of the release holding the last candle of df,
// from the releases read: unknown when they don't cover it.
func (s *NewsBreakout) window(df *model.Dataframe) newsWindow {
	w := newsWindow{high: na, low: na}
	t := df.Time[len(df.Time)-1]

	s.mu.RLock()
	schedule := s.schedules[df.Pair]
	s.mu.RUnlock()
	if schedule == nil || !schedule.covers(t.Add(-s.reach), t.Add(s.reach)) {
		return w
	}
	w.known = true

	release, rangeEnd, end, ok := schedule.last(t, s.cfg.Wait, s.cfg.Expiry)
	if !ok {
		return w
	}
	w.release, w.rangeEnd, w.end = release, rangeEnd, end
	if w.trading = s.trading(t, rangeEnd, end); w.trading {
		w.high, w.low = s.rangeOf(df, release.at, rangeEnd)
	}
	return w
}

// signal returns the entry of the last candle, if any: the first close beyond
// the range of the release and its buffer, as in ORB.
func (s *NewsBreakout) signal(df *model.Dataframe, w newsWindow) model.SideType {
	atrs := df.Metadata["nb_atr"]
	i := len(df.Close) - 1
	if i < 1 || !w.trading || len(atrs) != len(df.Close) || isNa(atrs[i]) {
		return ""
	}
	key := w.release.at.Format(time.RFC3339)
	if isNa(w.high) || isNa(w.low) {
		s.say(df.Pair, "release", "norange@"+key, "SKIP | %s at %s: no candle of the %s after it in the window",
			w.release.names, key, s.cfg.Wait)
		return ""
	}

	atr, width := atrs[i], w.high-w.low
	if s.cfg.MaxRangeATR > 0 && width > s.cfg.MaxRangeATR*atr {
		s.say(df.Pair, "release", "wide@"+key, "SKIP | %s at %s: range %f-%f wider than %g ATR (%f)",
			w.release.names, key, w.low, w.high, s.cfg.MaxRangeATR, atr)
		return ""
	}
	s.say(df.Pair, "release", "range@"+key, "RANGE | %s at %s: range %f-%f, its breakouts until %s",
		w.release.names, key, w.low, w.high, w.end.Format(time.RFC3339))

	// the close before it inside, or from the range
	buffer := s.cfg.BufferATR * atr
	switch closePrice, previous := df.Close[i], df.Close[i-1]; {
	case closePrice > w.high+buffer && previous <= w.high+buffer:
		return model.SideTypeBuy
	case closePrice < w.low-buffer && previous >= w.low-buffer:
		return model.SideTypeSell
	}
	return ""
}

// exit closes the position out of the breakouts of a release, once the
// releases read tell it, or on the close back past the middle of its range.
func (s *NewsBreakout) exit(df *model.Dataframe, held model.SideType, w newsWindow) string {
	switch {
	case !w.known:
		return "" // left to its stop loss and take profit
	case !w.trading:
		return "EXPIRY"
	case isNa(w.high) || isNa(w.low):
		return ""
	}
	if middle := (w.high + w.low) / 2; rkSign(held)*(df.Close.Last(0)-middle) < 0 {
		return "INVALIDATION"
	}
	return ""
}

// say logs the message of a state of pair of a kind (the source, the next
// release, the release traded), once until another one of the kind.
func (s *NewsBreakout) say(pair, kind, state, format string, args ...any) {
	key := pair + "|" + kind
	if s.said[key] == state {
		return
	}
	s.said[key] = state
	log.Infof("[NEWSBREAKOUT] %s %s | "+format, append([]any{pair, s.cfg.Timeframe}, args...)...)
}
