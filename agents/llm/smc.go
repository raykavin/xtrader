package llm

import (
	"math"
	"sort"
	"time"

	"github.com/raykavin/xtrader/model"
)

// The smart money concepts of the price structure (SMC, ICT) are computed by
// the code, as the structure is (see Structure), for a prompt asking for them
// with "# Context: smc" (see StrategyContext): a model scanning rows of numbers
// misses a gap of three candles or the close that broke a swing. They are the
// mechanical definitions of the concepts on the swings of pivotStrength, with
// no threshold of size: the model weighs them, their size in ATR among the
// rest. The context holds the newest of the events and the zones nearest to
// the price.
const (
	smcBreaks        = 4 // the newest breaks of the structure
	smcGapsPerSide   = 3 // the fair value gaps nearest to the price, on each side
	smcBlocksPerSide = 2 // the order blocks nearest to the price, on each side
	smcSweeps        = 3 // the newest sweeps of liquidity
)

// The kinds of a StructureBreak.
const (
	// breakBOS continues the structure: a break in the direction of the one
	// before it.
	breakBOS = "bos"
	// breakCHoCH changes the character of the structure: the first break
	// against the direction of the one before it.
	breakCHoCH = "choch"
	// breakFirst is the first break of the candles, the direction before it
	// unknown.
	breakFirst = "break"
)

// SMC is the price structure of the candles of a timeframe in smart money
// concepts: the last breaks of its structure, the fair value gaps not filled
// and the order blocks not broken nearest to the price, the last sweeps of the
// liquidity resting beyond its swings, and the dealing range of the leg of its
// last break.
type SMC struct {
	Breaks        []StructureBreak `json:"structure_breaks"` // newest first
	FairValueGaps []FairValueGap   `json:"fair_value_gaps"`  // the nearest to the price first
	OrderBlocks   []OrderBlock     `json:"order_blocks"`     // the nearest to the price first
	Sweeps        []LiquiditySweep `json:"liquidity_sweeps"` // newest first
	DealingRange  *DealingRange    `json:"dealing_range"`
}

// StructureBreak is a close beyond the last swing high (Direction up) or low
// (down) not broken yet: Kind bos when the break before it went the same way,
// choch when it went the other way, break for the first one of the candles.
// Level is the price of the swing broken, at SwingTime, and Time the open of
// the candle that closed beyond it, CandlesAgo before the last one.
type StructureBreak struct {
	Kind       string    `json:"kind"`
	Direction  string    `json:"direction"`
	Level      number    `json:"level"`
	SwingTime  time.Time `json:"swing_time"`
	Time       time.Time `json:"time"`
	CandlesAgo int       `json:"candles_ago"`
}

// FairValueGap is a gap of three candles not filled yet: bullish when the low
// of the third is above the high of the first, the gap between them a support
// below the price; bearish when its high is under the low of the first, a
// resistance above it. Time is the open of the middle candle, the
// displacement, CandlesAgo before the last one. Low and High are the part not
// traded into yet, FilledPct the share of the gap traded into since (0 for
// none), SizeATR the size of the whole gap and DistanceATR the distance of the
// middle of the part left from the price, in ATR, positive above.
type FairValueGap struct {
	Direction   string    `json:"direction"`
	Low         number    `json:"low"`
	High        number    `json:"high"`
	Time        time.Time `json:"time"`
	CandlesAgo  int       `json:"candles_ago"`
	FilledPct   ratio     `json:"filled_pct"`
	SizeATR     ratio     `json:"size_atr"`
	DistanceATR ratio     `json:"distance_atr"`
}

// OrderBlock is the last candle against the move that broke the structure,
// before it: for a break up, the last bearish candle at or before the lowest
// low of the leg between the swing broken and the break (bullish, a support);
// for a break down, the last bullish one at or before its highest high
// (bearish, a resistance). Low and High are its range, Time its open,
// CandlesAgo before the last one, BreakTime the open of the candle of the
// break it led to. Mitigated tells the price traded back into it since the
// break; a close beyond it, below a bullish one or above a bearish one, broke
// it, and it is left out.
type OrderBlock struct {
	Direction   string    `json:"direction"`
	Low         number    `json:"low"`
	High        number    `json:"high"`
	Time        time.Time `json:"time"`
	CandlesAgo  int       `json:"candles_ago"`
	BreakTime   time.Time `json:"break_time"`
	Mitigated   bool      `json:"mitigated"`
	DistanceATR ratio     `json:"distance_atr"`
}

// LiquiditySweep is a wick beyond a swing that closed back without breaking
// it: Side buy_side for a swing high (the stops of the shorts and the buy
// stops above it), sell_side for a swing low. Level is the price of the swing,
// at SwingTime; Extreme the high (low) of the candle of the sweep, opened at
// Time, CandlesAgo before the last one.
type LiquiditySweep struct {
	Side       string    `json:"side"`
	Level      number    `json:"level"`
	SwingTime  time.Time `json:"swing_time"`
	Extreme    number    `json:"extreme"`
	Time       time.Time `json:"time"`
	CandlesAgo int       `json:"candles_ago"`
}

// DealingRange is the range of the leg of the last break of the structure:
// for a break up, from the lowest low of the leg that broke the swing (at
// LowTime) to the highest high since (at HighTime); for a break down, from
// its highest high to the lowest low since. Equilibrium is its middle, and
// Position where the price is in it, 0 at its low and 1 at its high: under
// 0.5 the discount, over it the premium, beyond 0 and 1 out of the range.
type DealingRange struct {
	Direction   string    `json:"direction"`
	High        number    `json:"high"`
	HighTime    time.Time `json:"high_time"`
	Low         number    `json:"low"`
	LowTime     time.Time `json:"low_time"`
	Equilibrium number    `json:"equilibrium"`
	Position    ratio     `json:"position"`
}

// smcBreak is a break of the structure found by breaksOf: the index of the
// swing broken and of the candle of the break.
type smcBreak struct {
	StructureBreak
	swing, at int
	up        bool
}

// computeSMC returns the smart money concepts of candles, oldest first, with
// atr the ATR of each (NaN or 0 while unknown), at the price of now: nil with
// too few candles for a swing.
func computeSMC(candles []model.Candle, atr []float64, price float64) *SMC {
	if len(candles) < 2*pivotStrength+1 || !(price > 0) {
		return nil
	}
	width := lastKnown(positiveATR(atr))
	if !(width > 0) {
		width = averageRange(candles, 14) // the ATR unknown: the average range
	}
	last := len(candles) - 1
	breaks := breaksOf(candles, last)
	smc := &SMC{
		Breaks:        make([]StructureBreak, 0, smcBreaks),
		FairValueGaps: fairValueGaps(candles, width, price, last),
		OrderBlocks:   orderBlocks(candles, breaks, width, price, last),
		Sweeps:        sweeps(candles, last),
		DealingRange:  dealingRange(candles, breaks, price),
	}
	for i := len(breaks) - 1; i >= 0 && len(smc.Breaks) < smcBreaks; i-- {
		smc.Breaks = append(smc.Breaks, breaks[i].StructureBreak)
	}
	return smc
}

// breaksOf returns the breaks of the structure of candles, oldest first: the
// candles walked in order, the last swing high and low confirmed so far (a
// swing is confirmed pivotStrength candles after it) broken by the first
// close beyond them, once each.
func breaksOf(candles []model.Candle, last int) []smcBreak {
	confirmed := make(map[int][]pivot) // the swings by the candle confirming them
	for _, p := range pivots(candles, pivotStrength) {
		at := p.index + pivotStrength
		confirmed[at] = append(confirmed[at], p)
	}
	var breaks []smcBreak
	var high, low *pivot // the swings to break, nil once broken
	trend := ""          // the direction of the last break
	for i, candle := range candles {
		for _, p := range confirmed[i] {
			p := p
			if p.high {
				high = &p
			} else {
				low = &p
			}
		}
		for _, swing := range []**pivot{&high, &low} {
			p := *swing
			if p == nil {
				continue
			}
			up := p.high
			if up && candle.Close <= p.price || !up && candle.Close >= p.price {
				continue
			}
			direction := "down"
			if up {
				direction = "up"
			}
			kind := breakFirst
			switch {
			case trend == direction:
				kind = breakBOS
			case trend != "":
				kind = breakCHoCH
			}
			breaks = append(breaks, smcBreak{StructureBreak: StructureBreak{Kind: kind, Direction: direction,
				Level: number(p.price), SwingTime: candles[p.index].Time.UTC(), Time: candle.Time.UTC(),
				CandlesAgo: last - i}, swing: p.index, at: i, up: up})
			trend = direction
			*swing = nil
		}
	}
	return breaks
}

// fairValueGaps returns the fair value gaps of candles not filled yet, the
// smcGapsPerSide nearest to price of each direction, the nearest first.
func fairValueGaps(candles []model.Candle, atr, price float64, last int) []FairValueGap {
	var bullish, bearish []FairValueGap
	for i := 2; i < len(candles); i++ {
		first, third := candles[i-2], candles[i]
		var gap FairValueGap
		switch {
		case third.Low > first.High:
			gap = FairValueGap{Direction: BiasBullish, Low: number(first.High), High: number(third.Low)}
		case third.High < first.Low:
			gap = FairValueGap{Direction: BiasBearish, Low: number(third.High), High: number(first.Low)}
		default:
			continue
		}
		low, high := float64(gap.Low), float64(gap.High)
		size := high - low
		left, right := low, high // the part not traded into yet
		filled := false
		for _, later := range candles[i+1:] {
			if gap.Direction == BiasBullish {
				right = math.Min(right, later.Low)
				filled = right <= low
			} else {
				left = math.Max(left, later.High)
				filled = left >= high
			}
			if filled {
				break
			}
		}
		if filled {
			continue
		}
		gap.Low, gap.High = number(left), number(right)
		gap.Time, gap.CandlesAgo = candles[i-1].Time.UTC(), last-(i-1)
		gap.FilledPct = ratio((size - (right - left)) / size * 100)
		gap.SizeATR, gap.DistanceATR = ratio(size/atr), ratio(((left+right)/2-price)/atr)
		if gap.Direction == BiasBullish {
			bullish = append(bullish, gap)
		} else {
			bearish = append(bearish, gap)
		}
	}
	gaps := append(nearestOf(bullish, smcGapsPerSide, func(g FairValueGap) ratio { return g.DistanceATR }),
		nearestOf(bearish, smcGapsPerSide, func(g FairValueGap) ratio { return g.DistanceATR })...)
	sortNearest(gaps, func(g FairValueGap) ratio { return g.DistanceATR })
	return gaps
}

// orderBlocks returns the order blocks of the breaks of candles not broken
// since, the smcBlocksPerSide nearest to price of each direction, the nearest
// first.
func orderBlocks(candles []model.Candle, breaks []smcBreak, atr, price float64, last int) []OrderBlock {
	var bullish, bearish []OrderBlock
	seen := make(map[int]bool) // a candle is the block of one break at most
	for _, b := range breaks {
		origin := legOrigin(candles, b)
		block := -1
		for k := origin; k >= b.swing && block < 0; k-- {
			if candle := candles[k]; b.up && candle.Close < candle.Open || !b.up && candle.Close > candle.Open {
				block = k
			}
		}
		if block < 0 || seen[block] {
			continue
		}
		seen[block] = true
		candle := candles[block]
		ob := OrderBlock{Direction: BiasBullish, Low: number(candle.Low), High: number(candle.High),
			Time: candle.Time.UTC(), CandlesAgo: last - block, BreakTime: candles[b.at].Time.UTC(),
			DistanceATR: ratio(((candle.Low+candle.High)/2 - price) / atr)}
		if !b.up {
			ob.Direction = BiasBearish
		}
		broken := false
		for _, later := range candles[b.at+1:] {
			if b.up {
				ob.Mitigated = ob.Mitigated || later.Low <= candle.High
				broken = later.Close < candle.Low
			} else {
				ob.Mitigated = ob.Mitigated || later.High >= candle.Low
				broken = later.Close > candle.High
			}
			if broken {
				break
			}
		}
		switch {
		case broken:
		case b.up:
			bullish = append(bullish, ob)
		default:
			bearish = append(bearish, ob)
		}
	}
	blocks := append(nearestOf(bullish, smcBlocksPerSide, func(b OrderBlock) ratio { return b.DistanceATR }),
		nearestOf(bearish, smcBlocksPerSide, func(b OrderBlock) ratio { return b.DistanceATR })...)
	sortNearest(blocks, func(b OrderBlock) ratio { return b.DistanceATR })
	return blocks
}

// legOrigin returns the index of the origin of the leg of the break b: the
// lowest low between the swing broken and the break for a break up, the
// highest high for a break down, the last one of a tie.
func legOrigin(candles []model.Candle, b smcBreak) int {
	origin := b.swing
	for k := b.swing; k <= b.at; k++ {
		if b.up && candles[k].Low <= candles[origin].Low || !b.up && candles[k].High >= candles[origin].High {
			origin = k
		}
	}
	return origin
}

// sweeps returns the last smcSweeps sweeps of the liquidity beyond the swings
// of candles, newest first: the first candle after a swing is confirmed that
// trades beyond it and closes back, before any close beyond it.
func sweeps(candles []model.Candle, last int) []LiquiditySweep {
	var found []LiquiditySweep
	var at []int
	for _, p := range pivots(candles, pivotStrength) {
		for j := p.index + pivotStrength + 1; j < len(candles); j++ {
			candle := candles[j]
			if p.high && candle.Close > p.price || !p.high && candle.Close < p.price {
				break // broken: no sweep
			}
			if p.high && candle.High > p.price || !p.high && candle.Low < p.price {
				sweep := LiquiditySweep{Side: "buy_side", Level: number(p.price), SwingTime: candles[p.index].Time.UTC(),
					Extreme: number(candle.High), Time: candle.Time.UTC(), CandlesAgo: last - j}
				if !p.high {
					sweep.Side, sweep.Extreme = "sell_side", number(candle.Low)
				}
				found, at = append(found, sweep), append(at, j)
				break
			}
		}
	}
	order := make([]int, len(found))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return at[order[a]] > at[order[b]] })
	list := make([]LiquiditySweep, 0, smcSweeps)
	for _, i := range order {
		if len(list) == smcSweeps {
			break
		}
		list = append(list, found[i])
	}
	return list
}

// dealingRange returns the dealing range of the leg of the last break of
// candles at price, nil without a break.
func dealingRange(candles []model.Candle, breaks []smcBreak, price float64) *DealingRange {
	if len(breaks) == 0 {
		return nil
	}
	b := breaks[len(breaks)-1]
	origin := legOrigin(candles, b)
	extreme := origin
	for k := origin; k < len(candles); k++ {
		if b.up && candles[k].High >= candles[extreme].High || !b.up && candles[k].Low <= candles[extreme].Low {
			extreme = k
		}
	}
	r := &DealingRange{Direction: b.Direction}
	highIndex, lowIndex := extreme, origin
	if !b.up {
		highIndex, lowIndex = origin, extreme
	}
	high, low := candles[highIndex].High, candles[lowIndex].Low
	r.High, r.HighTime = number(high), candles[highIndex].Time.UTC()
	r.Low, r.LowTime = number(low), candles[lowIndex].Time.UTC()
	r.Equilibrium = number((high + low) / 2)
	r.Position = ratio(math.NaN())
	if high > low {
		r.Position = ratio((price - low) / (high - low))
	}
	return r
}

// nearestOf returns the n of list nearest to the price by their distance.
func nearestOf[T any](list []T, n int, distance func(T) ratio) []T {
	sortNearest(list, distance)
	return list[:min(len(list), n)]
}

// sortNearest sorts list by the distance to the price, the nearest first.
func sortNearest[T any](list []T, distance func(T) ratio) {
	sort.SliceStable(list, func(i, j int) bool {
		return math.Abs(float64(distance(list[i]))) < math.Abs(float64(distance(list[j])))
	})
}
