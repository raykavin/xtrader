package strategies

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	str2duration "github.com/xhit/go-str2duration/v2"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

// When Fimathe maps the reference channel, see FimatheConfig.Mapping: once a
// week (day trade) or once a day (scalping).
const (
	FimatheWeek = "week"
	FimatheDay  = "day"
)

// How Fimathe marks the reference channel, see FimatheConfig.Mark.
const (
	FimatheAuto = "auto"
	FimatheBody = "body"
	FimatheWick = "wick"
)

// How Fimathe moves the stop loss, see FimatheConfig.Trail.
const (
	FimatheSurf = "surf"
	FimatheLock = "lock"
	FimatheOff  = "off"
)

// FimatheConfig holds the inputs of Fimathe, with its defaults in
// DefaultFimatheConfig. The rules follow the lesson 3 of Fimathe 4.0
// (Mapeando pontos de negociação, Marcelo Ferreira) and the handouts of the
// course, the parts left open there set here: see the README.
type FimatheConfig struct {
	// The analysis on AnalysisTimeframe, the execution on Timeframe, a
	// shorter one dividing it, eg: 1h and 15m ("No time maior faremos toda
	// nossa análise. No time menor faremos nossa execução").
	Timeframe         string
	AnalysisTimeframe string

	// The reference channel is mapped at the start of each week (Mapping
	// week: the markings of day trade, made on Sunday) or of each day (day:
	// the ones of scalping, at the market open), at midnight in the time zone
	// Timezone, an IANA name, the weeks starting on Sunday. A mapping holds for
	// its period: the first entry of its trend only, as the official robot of
	// the technique automates.
	Mapping  string
	Timezone string

	// The trend, on the bars of AnalysisTimeframe closed before the mapping:
	// up with the last two tops and the last two bottoms rising, down with
	// both falling, none otherwise, no channel then ("Uma tendência permanece
	// enquanto o mercado estiver formando topos e fundos ascendentes ou
	// descendentes"). A top (a bottom) is a bar higher (lower) than the
	// PivotLen ones on each side of it, found in the last AnalysisBars bars.
	PivotLen     int
	AnalysisBars int

	// The reference channel, from the last bottom to the last top ("marcamos
	// o último topo e o último fundo"), on the wicks or on the bodies of their
	// bars (Mark wick or body; auto: the wicks, but the bodies for a channel
	// of the wicks wider than MaxChannelATR, "se ficar muito largo marcamos no
	// corpo"). A channel narrower than MinChannelATR or wider than
	// MaxChannelATR, in ATR of ATRLen bars of AnalysisTimeframe, isn't
	// traded ("O canal não pode ser nem muito largo e nem muito estreito").
	Mark          string
	MinChannelATR float64
	MaxChannelATR float64

	// The stop loss beyond the neutral zone ("o stop deve ficar abaixo da zona
	// neutra", two levels below an entry at the top of the channel), by
	// StopBufferATR in ATR of ATRLen candles of Timeframe.
	StopBufferATR float64

	// The take profit, TargetLevels heights of the channel from the entry
	// line ("pegar os dois níveis anteriores e projetá-los ... a partir da
	// nossa entrada"), 0 for none.
	TargetLevels float64

	// The stop loss moved by the protection subcycle, the channel of the
	// candles of Timeframe before the entry candle, from its last bottom to
	// its last top, of SubcyclePivot candles on each side, projected from the
	// entry line: Trail surf moves it to the entry line (0x0) once a close
	// passed the first step of TrailStep times the subcycle beyond it, then
	// one step further at each step passed ("Quando ele perde os primeiros
	// 50% eu travo meu stop no 0x0. Perdendo mais 50% desço meu stop mais um
	// ponto"); lock to the entry line at the first step only; off never.
	// Surfing sets no take profit: "Ou será take ou será stop".
	Trail         string
	TrailStep     float64
	SubcyclePivot int

	// MaxRiskPct, above 0, skips an entry losing more than that % of the
	// balance at its stop loss ("No máximo 3% por operação").
	MaxRiskPct float64

	// The sizing, the direction and MaxBars, see BracketConfig. The stop loss
	// and the take profit are the levels of the channel, technical: the ATR
	// multiples and the fixed values of BracketConfig aren't used.
	BracketConfig
}

// DefaultFimatheConfig returns the defaults of Fimathe: the analysis on 1h,
// the execution on 15m, a weekly mapping in UTC, the stop loss beyond the
// neutral zone surfing the subcycle by 50%, no take profit.
func DefaultFimatheConfig() FimatheConfig {
	return FimatheConfig{
		Timeframe:         "15m",
		AnalysisTimeframe: "1h",

		Mapping:  FimatheWeek,
		Timezone: "UTC",

		PivotLen:     3,
		AnalysisBars: 120,

		Mark:          FimatheAuto,
		MinChannelATR: 1,
		MaxChannelATR: 8,

		StopBufferATR: 0.1,
		Trail:         FimatheSurf,
		TrailStep:     0.5,
		SubcyclePivot: 3,

		MaxRiskPct: 3,

		BracketConfig: DefaultBracketConfig(),
	}
}

func (c FimatheConfig) validate() error {
	exits := c.BracketConfig
	exits.StopATR, exits.TakeProfitATR = 1, 1 // unused: the levels of the channel in their place
	if err := exits.validate(); err != nil {
		return err
	}

	tf, err := str2duration.ParseDuration(c.Timeframe)
	if err != nil || tf <= 0 {
		return fmt.Errorf("invalid timeframe %q", c.Timeframe)
	}
	htf, err := str2duration.ParseDuration(c.AnalysisTimeframe)
	if err != nil || htf <= 0 {
		return fmt.Errorf("invalid analysis timeframe %q", c.AnalysisTimeframe)
	}
	switch {
	case htf <= tf || htf%tf != 0:
		return errors.New("the analysis timeframe must be longer than the timeframe and a multiple of it")
	case rkDay%htf != 0:
		return errors.New("the analysis timeframe must divide a day")
	case c.Mapping != FimatheWeek && c.Mapping != FimatheDay:
		return fmt.Errorf("invalid mapping %q: week or day", c.Mapping)
	case c.PivotLen < 1 || c.SubcyclePivot < 1:
		return errors.New("the pivots must be of 1 candle at least")
	case c.AnalysisBars < 6*c.PivotLen:
		return errors.New("the analysis bars must hold two tops and two bottoms: 6 times the pivot at least")
	case c.Mark != FimatheAuto && c.Mark != FimatheBody && c.Mark != FimatheWick:
		return fmt.Errorf("invalid mark %q: auto, body or wick", c.Mark)
	case c.MinChannelATR < 0 || c.MaxChannelATR <= c.MinChannelATR:
		return errors.New("the channel widths must be 0 <= min < max")
	case c.StopBufferATR < 0:
		return errors.New("the stop buffer can't be negative")
	case c.TargetLevels < 0:
		return errors.New("the target levels can't be negative")
	case c.Trail != FimatheSurf && c.Trail != FimatheLock && c.Trail != FimatheOff:
		return fmt.Errorf("invalid trail %q: surf, lock or off", c.Trail)
	case c.TrailStep <= 0:
		return errors.New("the trail step must be positive")
	case c.Trail == FimatheSurf && c.TargetLevels > 0:
		return errors.New("a stop loss surfing the subcycle sets no take profit (Ou será take ou será stop): " +
			"target levels 0, or trail lock or off")
	case c.MaxRiskPct < 0:
		return errors.New("the max risk can't be negative")
	case c.StopValue > 0 || c.TakeProfitValue > 0:
		return errors.New("the stop loss and the take profit are technical, the levels of the channel: no fixed " +
			"values")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", c.Timezone, err)
	}
	return nil
}

// Fimathe trades the first entry of the trend of each mapping of the Fimathe
// technique: the reference channel of the last top and bottom of the analysis
// timeframe, its neutral zone on the other side of the trend, a BUY in an
// uptrend on the close of a candle of the execution timeframe back above the
// 50% of the channel, or above its top while the price held above the 50%,
// the stop loss beyond the neutral zone, moved by the protection subcycle; a
// close beyond the neutral zone, in the reversal zone, ends the mapping. Its
// levels are drawn on the chart, see ChartAnalysis. See FimatheConfig.
type Fimathe struct {
	cfg     FimatheConfig
	tf, htf time.Duration
	loc     *time.Location
	warmup  int
	bracket *bracket

	maps   map[string]fimatheMap    // the mapping of the current period, by pair
	trades map[string]*fimatheTrade // the trade followed, by pair
	done   map[string]*fimatheTrade // the last trade closed, by pair, drawn until the next one
	traded map[string]time.Time     // the period of the last entry, by pair: one entry a period
	said   map[string]string        // the last state logged, by pair

	mu       sync.Mutex
	analyses map[string]ui.Analysis // drawn on the chart, by pair
}

// fimatheMap is the mapping of a period: the side of its trend (BUY up, SELL
// down, none without a trend), its reference channel from bottom to top, the
// open times of the bars of its last top and bottom, the mark of the channel,
// the ATR of the analysis timeframe, whether it is traded and why not.
type fimatheMap struct {
	period          time.Time
	side            model.SideType
	top, bottom     float64
	topAt, bottomAt time.Time
	mark            string
	atr             float64
	ok              bool
	reason          string
}

func (m fimatheMap) height() float64 { return m.top - m.bottom }

func (m fimatheMap) mid() float64 { return (m.top + m.bottom) / 2 }

// edge returns the side of the channel in the direction of the trend: its
// top in an uptrend, its bottom in a downtrend.
func (m fimatheMap) edge() float64 {
	if m.side == model.SideTypeSell {
		return m.bottom
	}
	return m.top
}

// base returns the side of the channel against the trend, where the neutral
// zone starts.
func (m fimatheMap) base() float64 {
	if m.side == model.SideTypeSell {
		return m.top
	}
	return m.bottom
}

// far returns the far side of the neutral zone, the start of the reversal
// zone: the height of the channel beyond its base.
func (m fimatheMap) far() float64 {
	return m.base() - rkSign(m.side)*m.height()
}

// The level a mapping is armed at, see fimatheScene.
const (
	fimatheArmNone = iota // the price beyond the channel in the direction of the trend: waiting for its return
	fimatheArmMid         // at the 50% of the channel, the price under it (or in the neutral zone)
	fimatheArmEdge        // at the edge of the channel, the price held above its 50% (below, in a downtrend)
)

// fimatheScene is a mapping followed on the candles of its period from start
// to end: the level it is armed at after them, the candle of its entry
// (trigger) and its entry line, or the one of its end in the reversal zone
// (invalid), -1 for none.
type fimatheScene struct {
	fimatheMap
	start, end int
	arm        int
	trigger    int
	line       float64
	invalid    int
}

// fimatheTrade is the trade followed on a pair: its side, the period of its
// mapping, its entry line and the candle of its entry, its subcycle (from
// low to high, of height sub, read from subAt; 0 unknown, the stop loss left
// where it is), the steps of the subcycle passed, and the moves of its stop
// loss, drawn on the chart.
type fimatheTrade struct {
	trade           *scalpTrade
	side            model.SideType
	period          time.Time
	line            float64
	entryAt         time.Time
	sub             float64
	subLow, subHigh float64
	subAt           time.Time
	steps           int
	moves           []fimatheMove
}

// fimatheMove is a move of the stop loss: when, to which price, at which step.
type fimatheMove struct {
	at    time.Time
	price float64
	step  int
}

// NewFimathe validates the configuration and creates the strategy.
func NewFimathe(cfg FimatheConfig) (*Fimathe, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("fimathe: %w", err)
	}
	tf, _ := str2duration.ParseDuration(cfg.Timeframe)
	htf, _ := str2duration.ParseDuration(cfg.AnalysisTimeframe)
	loc, _ := time.LoadLocation(cfg.Timezone)

	// the bars of the analysis and of the seed of their ATR before the start
	// of a period, then two periods: the one of a position opened in the
	// previous period, managed in this one, and the current one
	period := 7 * rkDay
	if cfg.Mapping == FimatheDay {
		period = rkDay
	}
	mult := int(htf / tf)
	warmup := (cfg.AnalysisBars+seedWarmup(cfg.ATRLen)+2)*mult + 2*int(period/tf) + 1
	return &Fimathe{
		cfg:      cfg,
		tf:       tf,
		htf:      htf,
		loc:      loc,
		warmup:   max(warmup, seedWarmup(cfg.ATRLen)+1),
		bracket:  newBracket("FIMATHE", cfg.Timeframe, cfg.BracketConfig),
		maps:     make(map[string]fimatheMap),
		trades:   make(map[string]*fimatheTrade),
		done:     make(map[string]*fimatheTrade),
		traded:   make(map[string]time.Time),
		said:     make(map[string]string),
		analyses: make(map[string]ui.Analysis),
	}, nil
}

func (s *Fimathe) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *Fimathe) WarmupPeriod() int {
	return s.warmup
}

func (s *Fimathe) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["fm_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)
	return []strategy.ChartIndicator{pane(df, fmt.Sprintf("ATR %d", s.cfg.ATRLen), s.cfg.ATRLen,
		line(df.Metadata["fm_atr"], "ATR", colorOther))}
}

// periodStart returns the start of the period of a mapping holding t: the
// midnight of its day, or of the Sunday of its week, in the time zone.
func (s *Fimathe) periodStart(t time.Time) time.Time {
	local := t.In(s.loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, s.loc)
	if s.cfg.Mapping == FimatheDay {
		return day
	}
	return day.AddDate(0, 0, -int(day.Weekday()))
}

// periodEnd returns the end of the period starting at start.
func (s *Fimathe) periodEnd(start time.Time) time.Time {
	if s.cfg.Mapping == FimatheDay {
		return start.AddDate(0, 0, 1)
	}
	return start.AddDate(0, 0, 7)
}

// fimatheBar is a bar of the analysis timeframe made of the candles of the
// execution timeframe: its open time and its prices.
type fimatheBar struct {
	at                     time.Time
	open, high, low, close float64
}

// bars returns the bars of the analysis timeframe made of the candles of df
// before the index end, closed by the time before: the last one left out
// while it holds a part of its time after it, and the first one while the
// candles of df start after its open.
func (s *Fimathe) bars(df *model.Dataframe, end int, before time.Time) []fimatheBar {
	var bars []fimatheBar
	for i := 0; i < end; i++ {
		at := htfBarStart(df.Time[i], s.htf)
		if n := len(bars); n > 0 && bars[n-1].at.Equal(at) {
			bar := &bars[n-1]
			bar.high = math.Max(bar.high, df.High[i])
			bar.low = math.Min(bar.low, df.Low[i])
			bar.close = df.Close[i]
			continue
		}
		bars = append(bars, fimatheBar{at: at, open: df.Open[i], high: df.High[i], low: df.Low[i], close: df.Close[i]})
	}
	if len(bars) > 0 && df.Time[0].After(bars[0].at) {
		bars = bars[1:] // partial
	}
	for len(bars) > 0 && bars[len(bars)-1].at.Add(s.htf).After(before) {
		bars = bars[:len(bars)-1]
	}
	return bars
}

// mapAt maps the period starting at the candle start of df: the trend and the
// reference channel of the bars of the analysis timeframe closed before it,
// see FimatheConfig, and the price of the last close before it.
func (s *Fimathe) mapAt(df *model.Dataframe, start int, period time.Time) fimatheMap {
	m := fimatheMap{period: period}
	if start < 1 {
		m.reason = "no candle before the mapping"
		return m
	}
	bars := s.bars(df, start, period)
	seed := seedWarmup(s.cfg.ATRLen)
	if need := max(s.cfg.AnalysisBars, seed); len(bars) < need {
		m.reason = fmt.Sprintf("%d bars of %s before the mapping, %d needed", len(bars), s.cfg.AnalysisTimeframe,
			need)
		return m
	}

	// the ATR at the mapping, of the bars of its seed only: the same whatever
	// the candles before them
	highs, lows, closes := make([]float64, seed), make([]float64, seed), make([]float64, seed)
	for k, bar := range bars[len(bars)-seed:] {
		highs[k], lows[k], closes[k] = bar.high, bar.low, bar.close
	}
	m.atr = pineATR(highs, lows, closes, s.cfg.ATRLen)[seed-1]

	// the tops and the bottoms of the analysis bars
	window := bars[len(bars)-s.cfg.AnalysisBars:]
	highs, lows = make([]float64, len(window)), make([]float64, len(window))
	for k, bar := range window {
		highs[k], lows[k] = bar.high, bar.low
	}
	var tops, bottoms []int
	for k, v := range pivotHigh(highs, s.cfg.PivotLen) {
		if !isNa(v) {
			tops = append(tops, k)
		}
	}
	for k, v := range pivotLow(lows, s.cfg.PivotLen) {
		if !isNa(v) {
			bottoms = append(bottoms, k)
		}
	}
	if len(tops) < 2 || len(bottoms) < 2 {
		m.reason = fmt.Sprintf("no trend: %d tops and %d bottoms on %s, two of each needed", len(tops),
			len(bottoms), s.cfg.AnalysisTimeframe)
		return m
	}
	t1, t2 := window[tops[len(tops)-2]], window[tops[len(tops)-1]]
	b1, b2 := window[bottoms[len(bottoms)-2]], window[bottoms[len(bottoms)-1]]
	switch {
	case t2.high > t1.high && b2.low > b1.low:
		m.side = model.SideTypeBuy
	case t2.high < t1.high && b2.low < b1.low:
		m.side = model.SideTypeSell
	default:
		m.reason = fmt.Sprintf("no trend: the last tops (%g, %g) and bottoms (%g, %g) on %s don't go the same "+
			"way", t1.high, t2.high, b1.low, b2.low, s.cfg.AnalysisTimeframe)
		return m
	}

	// the reference channel, from the last bottom to the last top
	m.topAt, m.bottomAt = t2.at, b2.at
	m.mark = s.cfg.Mark
	if m.mark == FimatheAuto {
		m.mark = FimatheWick
		if t2.high-b2.low > s.cfg.MaxChannelATR*m.atr {
			m.mark = FimatheBody
		}
	}
	m.top, m.bottom = t2.high, b2.low
	if m.mark == FimatheBody {
		m.top, m.bottom = math.Max(t2.open, t2.close), math.Min(b2.open, b2.close)
	}
	width := m.height() / m.atr
	switch {
	case m.height() <= 0:
		m.reason = fmt.Sprintf("the last top (%g) isn't above the last bottom (%g)", m.top, m.bottom)
	case isNa(width) || m.atr <= 0:
		m.reason = "no ATR of the analysis timeframe"
	case width < s.cfg.MinChannelATR:
		m.reason = fmt.Sprintf("channel too narrow: %.2f ATR, under %g", width, s.cfg.MinChannelATR)
	case width > s.cfg.MaxChannelATR:
		m.reason = fmt.Sprintf("channel too wide: %.2f ATR, above %g", width, s.cfg.MaxChannelATR)
	case rkSign(m.side)*(df.Close[start-1]-m.far()) < 0:
		m.reason = fmt.Sprintf("the price (%g) in the reversal zone, beyond the neutral zone (%g), at the mapping",
			df.Close[start-1], m.far())
	default:
		m.ok = true
	}
	return m
}

// armOf returns the level a mapping is armed at for a price.
func (m fimatheMap) armOf(price float64) int {
	sign := rkSign(m.side)
	switch {
	case sign*(price-m.edge()) > 0:
		return fimatheArmNone
	case sign*(price-m.mid()) >= 0:
		return fimatheArmEdge
	}
	return fimatheArmMid
}

// scene follows the mapping m on the closes of the candles of df from start
// to end: armed at the 50% of the channel while the price is under it (in an
// uptrend; above it in a downtrend), at its edge while it held above it since
// the mapping, waiting while it is beyond the channel; the entry on the first
// close beyond the level armed ("Apenas quando o preço voltar e romper os 50%
// do canal"; "Se ele cair e ficar abaixo dos 50% então a compra será
// rompendo esse nível novamente"), the end on a close beyond the neutral zone
// ("só iremos efetuar uma venda quando o preço perder 100% da zona neutra").
func (s *Fimathe) scene(df *model.Dataframe, m fimatheMap, start, end int) fimatheScene {
	sc := fimatheScene{fimatheMap: m, start: start, end: end, trigger: -1, invalid: -1}
	if !m.ok || start < 1 {
		return sc
	}
	sign := rkSign(m.side)
	sc.arm = m.armOf(df.Close[start-1])
	for i := start; i <= end; i++ {
		closePrice := df.Close[i]
		if sign*(closePrice-m.far()) < 0 {
			sc.invalid = i
			return sc
		}
		if sc.arm == fimatheArmNone {
			if sign*(closePrice-m.edge()) <= 0 {
				sc.arm = m.armOf(closePrice) // back into the channel
			}
			continue
		}
		level := m.mid()
		if sc.arm == fimatheArmEdge {
			level = m.edge()
		}
		if sign*(closePrice-level) > 0 {
			sc.trigger, sc.line = i, level
			return sc
		}
		if sign*(closePrice-m.mid()) < 0 {
			sc.arm = fimatheArmMid
		}
	}
	return sc
}

// level returns the level the scene is armed at, NaN for none.
func (sc fimatheScene) level() float64 {
	switch sc.arm {
	case fimatheArmMid:
		return sc.mid()
	case fimatheArmEdge:
		return sc.edge()
	}
	return na
}

// periodScene returns the scene of the period starting at period on df, from
// its first candle to the last one of df or of the period, and the index of
// its first candle, -1 when df doesn't hold its start. maps caches the
// mapping of the current period of the pair.
func (s *Fimathe) periodScene(df *model.Dataframe, period time.Time, current bool) (fimatheScene, int) {
	n := len(df.Time)
	start := sort.Search(n, func(i int) bool { return !df.Time[i].Before(period) })
	if start == n || start == 0 {
		return fimatheScene{fimatheMap: fimatheMap{period: period, reason: "the start of the period out of the " +
			"candles"}, trigger: -1, invalid: -1}, -1
	}
	end := sort.Search(n, func(i int) bool { return !df.Time[i].Before(s.periodEnd(period)) }) - 1
	m, cached := s.maps[df.Pair]
	if !current || !cached || !m.period.Equal(period) {
		m = s.mapAt(df, start, period)
		if current {
			s.maps[df.Pair] = m
		}
	}
	return s.scene(df, m, start, end), start
}

// subcycle returns the protection subcycle of an entry on the candle entry of
// df: the channel of the candles before it, from their last bottom to their
// last top, of SubcyclePivot candles on each side ("Um subciclo será o canal
// anterior ao candlestick de entrada"), and the open time of its older end;
// ok false without both.
func (s *Fimathe) subcycle(df *model.Dataframe, entry int) (low, high float64, at time.Time, ok bool) {
	from := max(0, entry-s.cfg.AnalysisBars*int(s.htf/s.tf))
	highs, lows := df.High[from:entry], df.Low[from:entry]
	top, bottom := -1, -1
	for k, v := range pivotHigh(highs, s.cfg.SubcyclePivot) {
		if !isNa(v) {
			top = k
		}
	}
	for k, v := range pivotLow(lows, s.cfg.SubcyclePivot) {
		if !isNa(v) {
			bottom = k
		}
	}
	if top < 0 || bottom < 0 || highs[top] <= lows[bottom] {
		return 0, 0, time.Time{}, false
	}
	return lows[bottom], highs[top], df.Time[from+min(top, bottom)], true
}

func (s *Fimathe) OnCandle(df *model.Dataframe, broker service.Broker) {
	n := len(df.Close)
	if n < 2 || len(df.Time) != n || len(df.Open) != n || len(df.High) != n || len(df.Low) != n {
		return
	}
	last := n - 1
	period := s.periodStart(df.Time[last])
	sc, _ := s.periodScene(df, period, true)

	// the position held: followed, its stop loss moved by its subcycle
	if !s.bracket.manage(df, broker, func(*model.Dataframe, model.SideType) string { return "" }) {
		trade := s.follow(df)
		s.trail(df, broker, trade)
		s.publish(df, s.tradeScene(df, trade, sc), trade, "HOLD")
		return
	}
	if trade := s.trades[df.Pair]; trade != nil {
		log.Infof("[FIMATHE] %s %s | EXIT | the %s of the mapping of %s closed at the broker, by its stop loss or "+
			"its take profit | close %f", df.Pair, s.cfg.Timeframe, trade.side, trade.period.Format(time.DateOnly),
			df.Close[last])
		s.done[df.Pair] = trade
		delete(s.trades, df.Pair)
	}

	action := s.decide(df, broker, sc)
	trade := s.trades[df.Pair]
	if trade == nil {
		trade = s.done[df.Pair] // drawn with the mapping of its period, see publish
	}
	s.publish(df, sc, trade, action)
}

// decide enters the first entry of the scene sc of the current period on its
// trigger candle, the last one of df, flat: once a period, the entries of the
// previous runs counted (see openedBefore). It logs why it waits, once a
// state, and returns the action of the analysis drawn.
func (s *Fimathe) decide(df *model.Dataframe, broker service.Broker, sc fimatheScene) string {
	last := len(df.Close) - 1
	switch {
	case !sc.ok:
		s.say(df, "nomap:"+sc.reason, "WAIT | no mapping for the period of %s: %s", sc.period.Format(time.DateOnly),
			sc.reason)
		return "WAIT"
	case sc.invalid >= 0:
		s.say(df, "invalid", "WAIT | mapping of %s ended: close %f beyond the neutral zone (%f), in the reversal "+
			"zone; no entry until the next mapping", sc.period.Format(time.DateOnly), df.Close[sc.invalid], sc.far())
		return "INVALIDATED"
	case sc.trigger >= 0 && sc.trigger < last:
		s.say(df, "done", "WAIT | the first entry of the mapping of %s came on %s: none more until the next "+
			"mapping", sc.period.Format(time.DateOnly), df.Time[sc.trigger].Format(time.DateTime))
		return "DONE"
	case sc.trigger < 0:
		s.sayArm(df, sc)
		return "WAIT"
	}

	// the trigger, on the last candle
	if t, ok := s.traded[df.Pair]; ok && t.Equal(sc.period) || openedBefore(broker, df.Pair, sc.period) > 0 {
		s.say(df, "traded", "WAIT | the mapping of %s was entered already (a restart): no second entry",
			sc.period.Format(time.DateOnly))
		return "DONE"
	}
	if s.enter(df, broker, sc) {
		return string(sc.side)
	}
	return "WAIT"
}

// enter opens the entry of the scene sc on the last candle of df at the
// market, with its stop loss beyond the neutral zone and its take profit, if
// any, and reports whether it is held.
func (s *Fimathe) enter(df *model.Dataframe, broker service.Broker, sc fimatheScene) bool {
	last := len(df.Close) - 1
	closePrice := df.Close[last]
	sign := rkSign(sc.side)
	stop, profit, ok := s.exits(df, sc, sc.line)
	where := "its 50%"
	if sc.line == sc.edge() {
		where = "its edge"
	}
	if !ok {
		log.Infof("[FIMATHE] %s %s | SKIP | close %f beyond %s (%f), but the stop loss %f or the take profit %f "+
			"on the wrong side of it", df.Pair, s.cfg.Timeframe, closePrice, where, sc.line, stop, profit)
		return false
	}
	if s.cfg.MaxRiskPct > 0 {
		_, quote, err := broker.Position(df.Pair)
		if err != nil {
			log.Error(err)
			return false
		}
		size, _, sized := s.bracket.sizing.position(df.Pair, quote, closePrice)
		if !sized {
			return false
		}
		if risk := math.Abs(closePrice-stop) * size; risk > quote*s.cfg.MaxRiskPct/100 {
			log.Infof("[FIMATHE] %s %s | SKIP | the entry would lose %f at its stop loss %f, %.2f%% of the balance "+
				"of %f, above the max of %g%%", df.Pair, s.cfg.Timeframe, risk, stop, risk/quote*100, quote,
				s.cfg.MaxRiskPct)
			return false
		}
	}

	log.Infof("[FIMATHE] %s %s | %s | mapping of %s, trend %s on %s | close %f beyond %s (%f) of the reference "+
		"channel %f-%f | stop %f beyond the neutral zone (%f) | take profit %f", df.Pair, s.cfg.Timeframe, sc.side,
		sc.period.Format(time.DateOnly), map[float64]string{1: "up", -1: "down"}[sign], s.cfg.AnalysisTimeframe,
		closePrice, where, sc.line, sc.bottom, sc.top, stop, sc.far(), profit)
	if !s.bracket.openAt(df, broker, sc.side, stop, profit) {
		return false
	}
	s.traded[df.Pair] = sc.period
	trade := &fimatheTrade{trade: s.bracket.trades[df.Pair], side: sc.side, period: sc.period, line: sc.line,
		entryAt: df.Time[last]}
	s.withSubcycle(df, trade, last)
	s.trades[df.Pair] = trade
	return true
}

// exits returns the stop loss and the take profit (0 for none) of an entry at
// the entry line on the last candle of df in the scene sc, and whether they
// are on their sides of its close.
func (s *Fimathe) exits(df *model.Dataframe, sc fimatheScene, line float64) (stop, profit float64, ok bool) {
	sign := rkSign(sc.side)
	atr := na
	if atrs := df.Metadata["fm_atr"]; len(atrs) == len(df.Close) {
		atr = atrs.Last(0)
	}
	if isNa(atr) {
		atr = 0
	}
	stop = sc.far() - sign*s.cfg.StopBufferATR*atr
	if s.cfg.TargetLevels > 0 {
		profit = line + sign*s.cfg.TargetLevels*sc.height()
	}
	closePrice := df.Close.Last(0)
	return stop, profit, stop > 0 && sign*(closePrice-stop) > 0 &&
		(profit == 0 || profit > 0 && sign*(profit-closePrice) > 0)
}

// withSubcycle sets the subcycle of trade, of the candles before its entry
// candle entry of df.
func (s *Fimathe) withSubcycle(df *model.Dataframe, trade *fimatheTrade, entry int) {
	low, high, at, ok := s.subcycle(df, entry)
	if !ok {
		log.Infof("[FIMATHE] %s %s | no subcycle before the entry: the stop loss stays where it is", df.Pair,
			s.cfg.Timeframe)
		return
	}
	trade.sub, trade.subLow, trade.subHigh, trade.subAt = high-low, low, high, at
}

// follow returns the trade followed on the pair of df, held: the one entered,
// or, for a position found (eg: opened before a restart), the entry of the
// mapping of the period it opened in, read again from the candles of df.
func (s *Fimathe) follow(df *model.Dataframe) *fimatheTrade {
	held := s.bracket.trades[df.Pair]
	if trade := s.trades[df.Pair]; trade != nil && trade.trade == held {
		return trade
	}
	trade := &fimatheTrade{trade: held, side: held.side}
	s.trades[df.Pair] = trade
	if held.opened.IsZero() {
		log.Infof("[FIMATHE] %s %s | position found without its opening: followed, its stop loss left where it is",
			df.Pair, s.cfg.Timeframe)
		return trade
	}
	trade.period = s.periodStart(held.opened)
	sc, start := s.periodScene(df, trade.period, false)
	opened := sort.Search(len(df.Time), func(i int) bool { return df.Time[i].Add(s.tf).After(held.opened) })
	if start < 0 || sc.trigger < 0 || sc.side != held.side || sc.trigger > opened {
		log.Infof("[FIMATHE] %s %s | position found, opened %s: no entry of a mapping of its own before it, "+
			"its stop loss left where it is", df.Pair, s.cfg.Timeframe, held.opened.Format(time.DateTime))
		return trade
	}
	trade.line, trade.entryAt = sc.line, df.Time[sc.trigger]
	s.withSubcycle(df, trade, sc.trigger)
	log.Infof("[FIMATHE] %s %s | position found, the %s of the mapping of %s, entry line %f, subcycle %f: "+
		"followed", df.Pair, s.cfg.Timeframe, trade.side, trade.period.Format(time.DateOnly), trade.line, trade.sub)
	return trade
}

// trail moves the stop loss of the trade on the pair of df by its subcycle,
// see FimatheConfig.Trail: one step behind the last step passed by a close
// since the entry, the first one at the entry line (0x0); it only tightens,
// and stays on the losing side of the last close, where the broker takes it.
func (s *Fimathe) trail(df *model.Dataframe, broker service.Broker, trade *fimatheTrade) {
	protected, ok := broker.(service.ProtectionBroker)
	if s.cfg.Trail == FimatheOff || trade.sub <= 0 || !ok {
		return
	}
	sign := rkSign(trade.side)
	step := s.cfg.TrailStep * trade.sub
	from := sort.Search(len(df.Time), func(i int) bool { return df.Time[i].After(trade.entryAt) })
	best := trade.line
	for i := from; i < len(df.Close); i++ {
		if sign*(df.Close[i]-best) > 0 {
			best = df.Close[i]
		}
	}
	steps := int(math.Floor(sign * (best - trade.line) / step))
	if s.cfg.Trail == FimatheLock {
		steps = min(steps, 1)
	}
	if steps < 1 || steps <= trade.steps {
		return
	}
	level := trade.line + sign*float64(steps-1)*step
	closePrice := df.Close.Last(0)
	for i, leg := range trade.trade.legs {
		if leg.Type != model.OrderTypeStopLoss {
			continue
		}
		current := leg.Price
		if leg.Stop != nil {
			current = *leg.Stop
		}
		if sign*(level-current) <= 0 {
			trade.steps = steps // there already
			return
		}
		if sign*(closePrice-level) <= 0 {
			return // the close back short of it: moved once a close is beyond it again
		}
		moved, err := protected.UpdateProtection(leg, level)
		if err != nil {
			log.Error(err)
			return
		}
		trade.trade.legs[i] = moved
		trade.steps = steps
		trade.moves = append(trade.moves, fimatheMove{at: df.Time[len(df.Time)-1], price: level, step: steps})
		log.Infof("[FIMATHE] %s %s | STOP | a close passed %d×%g of the subcycle (%f) beyond the entry line %f: "+
			"stop %f -> %f%s", df.Pair, s.cfg.Timeframe, steps, s.cfg.TrailStep, trade.sub, trade.line, current,
			level, map[bool]string{true: " (0x0)", false: ""}[steps == 1])
		return
	}
}

// tradeScene returns the scene drawn with the trade held: the one of the
// mapping of its period, the current one sc when it is.
func (s *Fimathe) tradeScene(df *model.Dataframe, trade *fimatheTrade, sc fimatheScene) fimatheScene {
	if trade == nil || trade.period.IsZero() || trade.period.Equal(sc.period) {
		return sc
	}
	if other, start := s.periodScene(df, trade.period, false); start >= 0 {
		return other
	}
	return sc
}

// sayArm logs the level the scene sc is armed at, once a level.
func (s *Fimathe) sayArm(df *model.Dataframe, sc fimatheScene) {
	date := sc.period.Format(time.DateOnly)
	switch sc.arm {
	case fimatheArmNone:
		s.say(df, "arm:none", "WAIT | mapping of %s, trend %s: the price beyond the reference channel %f-%f, "+
			"waiting for its return into it", date, sc.side, sc.bottom, sc.top)
	case fimatheArmMid:
		s.say(df, "arm:mid", "WAIT | mapping of %s, trend %s: the price under the 50%% of the reference channel "+
			"%f-%f (or in the neutral zone): entry on a close beyond the 50%% (%f)", date, sc.side, sc.bottom, sc.top,
			sc.mid())
	case fimatheArmEdge:
		s.say(df, "arm:edge", "WAIT | mapping of %s, trend %s: the price held beyond the 50%% of the reference "+
			"channel %f-%f: entry on a close beyond its edge (%f)", date, sc.side, sc.bottom, sc.top, sc.edge())
	}
}

// say logs the message of a state of the pair of df, once until another one.
func (s *Fimathe) say(df *model.Dataframe, state, format string, args ...any) {
	key := s.periodStart(df.Time[len(df.Time)-1]).Format(time.DateOnly) + "|" + state
	if s.said[df.Pair] == key {
		return
	}
	s.said[df.Pair] = key
	log.Infof("[FIMATHE] %s %s | "+format, append([]any{df.Pair, s.cfg.Timeframe}, args...)...)
}

// ChartAnalysis returns the levels of the mapping of the pair drawn on the
// chart, see publish; false before the first candle.
func (s *Fimathe) ChartAnalysis(pair string) (ui.Analysis, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	analysis, ok := s.analyses[pair]
	return analysis, ok
}

// publish draws the scene sc on the chart of the pair of df, with the trade
// held, if any: the reference channel and its neutral zone (on the analysis
// timeframe), its 50%, its expansions in the direction of the trend, the line
// of the reversal zone; the entry planned while armed (the intent: none of it
// an order), or the entry made and the subcycle with the moves of the stop
// loss; the end in the reversal zone. Each mapping keeps its ids, of its
// period.
func (s *Fimathe) publish(df *model.Dataframe, sc fimatheScene, trade *fimatheTrade, action string) {
	last := len(df.Time) - 1
	analysis := ui.Analysis{
		Pair:       df.Pair,
		Source:     "Fimathe",
		DecisionID: fmt.Sprintf("fimathe-%s-%s", df.Pair, sc.period.Format(time.DateOnly)),
		Timeframe:  s.cfg.Timeframe,
		Candle:     df.Time[last].Unix(),
		DecidedAt:  df.Time[last].Add(s.tf).Unix(),
		Action:     action,
		Drawings:   []ui.Drawing{},
	}
	defer func() {
		s.mu.Lock()
		s.analyses[df.Pair] = analysis
		s.mu.Unlock()
	}()
	if sc.side == "" || sc.top <= sc.bottom {
		return
	}

	id := sc.period.Format(time.DateOnly)
	price := func(v float64) *float64 { return &v }
	bias, against := ui.BiasBullish, ui.BiasBearish
	if sc.side == model.SideTypeSell {
		bias, against = ui.BiasBearish, ui.BiasBullish
	}
	from := sc.topAt
	if sc.bottomAt.Before(from) {
		from = sc.bottomAt
	}
	var to *int64
	if sc.invalid >= 0 {
		end := df.Time[sc.invalid].Unix()
		to = &end
	}
	h, sign := sc.height(), rkSign(sc.side)
	channel := fmt.Sprintf("Canal de referência (%s)", sc.period.Format("02/01"))
	if !sc.ok {
		channel += " — fora das regras"
	}
	neutral := []float64{sc.base(), sc.far()}
	analysis.Drawings = append(analysis.Drawings,
		ui.Drawing{ID: id + "-channel", Kind: ui.DrawingZone, Label: channel, Bias: bias,
			Timeframe: s.cfg.AnalysisTimeframe, From: from.Unix(), To: to, Low: price(sc.bottom), High: price(sc.top)},
		ui.Drawing{ID: id + "-neutral", Kind: ui.DrawingZone, Label: "Zona neutra", Bias: ui.BiasNeutral,
			Timeframe: s.cfg.AnalysisTimeframe, From: from.Unix(), To: to, Low: price(math.Min(neutral[0], neutral[1])),
			High: price(math.Max(neutral[0], neutral[1]))},
		ui.Drawing{ID: id + "-mid", Kind: ui.DrawingLevel, Label: "50% do canal", Bias: ui.BiasNeutral,
			Timeframe: s.cfg.AnalysisTimeframe, From: from.Unix(), To: to, Price: price(sc.mid())},
		ui.Drawing{ID: id + "-expansion-1", Kind: ui.DrawingLevel, Label: "Expansão 100%", Bias: bias,
			Timeframe: s.cfg.AnalysisTimeframe, From: from.Unix(), To: to, Price: price(sc.edge() + sign*h)},
		ui.Drawing{ID: id + "-expansion-2", Kind: ui.DrawingLevel, Label: "Expansão 200%", Bias: bias,
			Timeframe: s.cfg.AnalysisTimeframe, From: from.Unix(), To: to, Price: price(sc.edge() + 2*sign*h)},
		ui.Drawing{ID: id + "-reversal", Kind: ui.DrawingLevel, Label: "Zona de reversão", Bias: against,
			Timeframe: s.cfg.AnalysisTimeframe, From: from.Unix(), To: to, Price: price(sc.far())},
	)
	if sc.invalid >= 0 {
		analysis.Drawings = append(analysis.Drawings, ui.Drawing{ID: id + "-invalid", Kind: ui.DrawingMarker,
			Label: "Reversão: mapeamento encerrado", Bias: against, Timeframe: s.cfg.Timeframe,
			From: df.Time[sc.invalid].Unix(), Price: price(df.Close[sc.invalid])})
	}
	if sc.trigger >= 0 {
		where := "50%"
		if sc.line == sc.edge() {
			where = "borda"
		}
		analysis.Drawings = append(analysis.Drawings, ui.Drawing{ID: id + "-entry", Kind: ui.DrawingMarker,
			Label: "Entrada: rompimento " + where, Bias: bias, Timeframe: s.cfg.Timeframe,
			From: df.Time[sc.trigger].Unix(), Price: price(sc.line)})
	}
	if trade != nil && trade.sub > 0 && trade.period.Equal(sc.period) {
		entry := trade.entryAt.Unix()
		analysis.Drawings = append(analysis.Drawings, ui.Drawing{ID: id + "-subcycle", Kind: ui.DrawingZone,
			Label: "Subciclo", Bias: ui.BiasNeutral, Timeframe: s.cfg.Timeframe, From: trade.subAt.Unix(),
			To: &entry, Low: price(trade.subLow), High: price(trade.subHigh)})
		for k := 1; k <= 4; k++ {
			label := fmt.Sprintf("Subciclo %g%%", float64(k)*s.cfg.TrailStep*100)
			analysis.Drawings = append(analysis.Drawings, ui.Drawing{ID: fmt.Sprintf("%s-step-%d", id, k),
				Kind: ui.DrawingLevel, Label: label, Bias: bias, Timeframe: s.cfg.Timeframe, From: entry,
				Price: price(trade.line + sign*float64(k)*s.cfg.TrailStep*trade.sub)})
		}
		for _, move := range trade.moves {
			label := fmt.Sprintf("Stop +%d", move.step-1)
			if move.step == 1 {
				label = "Stop 0x0"
			}
			analysis.Drawings = append(analysis.Drawings, ui.Drawing{ID: fmt.Sprintf("%s-stop-%d", id, move.step),
				Kind: ui.DrawingMarker, Label: label, Bias: ui.BiasNeutral, Timeframe: s.cfg.Timeframe,
				From: move.at.Unix(), Price: price(move.price)})
		}
	}

	// the entry planned, while armed: none of it an order
	if sc.ok && sc.invalid < 0 && sc.trigger < 0 && sc.arm != fimatheArmNone && action == "WAIT" &&
		(trade == nil || !trade.period.Equal(sc.period)) {
		stop, profit, _ := s.exits(df, sc, sc.level())
		intent := &ui.Intent{Action: "ENTER_LONG", Side: string(model.SideTypeBuy), OrderType: ui.IntentMarket,
			Entry: price(sc.level()), StopLoss: price(stop), Outcome: "waiting"}
		if sc.side == model.SideTypeSell {
			intent.Action, intent.Side = "ENTER_SHORT", string(model.SideTypeSell)
		}
		if profit > 0 {
			intent.TakeProfit = price(profit)
		}
		analysis.Intent = intent
	}
}
