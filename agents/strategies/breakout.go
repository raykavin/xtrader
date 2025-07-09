package strategies

import (
	"fmt"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

// breakoutRules are the rules of a range breakout, the ones of ORB and
// AsiaLondon: see their configurations.
type breakoutRules struct {
	layout    rangeBreakout
	bufferATR float64
	maxTrades int

	// the filters, each disabled at 0 or false
	minRangeATR, maxRangeATR float64
	bodyATR                  float64
	trendLen                 int
	vwap                     bool
	volumeLen                int
	volumeMult               float64
}

// breakoutTrader trades the breakouts of the range of a daily session, once
// it closed, over the trade session that follows it: a BUY on the first close
// above the high of the range, a SELL on the first close below its low. The
// position is closed at the market on the close back past the middle of the
// range, the breakout failed, and at the end of the trade session.
type breakoutTrader struct {
	rules   breakoutRules
	prefix  string // of the metadata, eg: orb_
	name    string // of the chart
	tf      time.Duration
	warmup  int
	bracket *bracket
	traded  map[string]breakoutSession // the positions opened in the current trade session, by pair
}

// breakoutSession counts the positions opened in the trade session started at
// start.
type breakoutSession struct {
	start  time.Time
	trades int
}

func newBreakoutTrader(name, prefix, timeframe string, rules breakoutRules,
	exits BracketConfig) (*breakoutTrader, error) {
	span, tf, err := timeframeCandles(timeframe, rules.layout.span())
	if err != nil {
		return nil, err
	}
	if rules.layout.rng.length() < tf {
		return nil, fmt.Errorf("the range session must hold a candle of %s at least", timeframe)
	}

	// the window reaches back to the start of the range from the end of the
	// trade session, with the recursive series (the EMA and the ATR) seeded
	return &breakoutTrader{
		rules:   rules,
		prefix:  prefix,
		name:    name,
		tf:      tf,
		warmup:  max(span, seedWarmup(max(exits.ATRLen, rules.trendLen)), rules.volumeLen) + 2,
		bracket: newBracket(name, timeframe, exits),
		traded:  make(map[string]breakoutSession),
	}, nil
}

func (b *breakoutTrader) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	high, low := b.rules.layout.levels(df)
	df.Metadata[b.prefix+"high"], df.Metadata[b.prefix+"low"] = high, low
	df.Metadata[b.prefix+"atr"] = pineATR(df.High, df.Low, df.Close, b.bracket.cfg.ATRLen)

	metrics := []strategy.IndicatorMetric{
		{Values: high, Name: "Range high", Color: "#00bd4e", Style: strategy.StyleScatter},
		{Values: low, Name: "Range low", Color: "#c90032", Style: strategy.StyleScatter},
	}
	if b.rules.trendLen > 0 {
		df.Metadata[b.prefix+"ema"] = pineEMA(df.Close, b.rules.trendLen)
		metrics = append(metrics, strategy.IndicatorMetric{Values: df.Metadata[b.prefix+"ema"],
			Name: fmt.Sprintf("EMA %d", b.rules.trendLen), Color: "#fd8700", Style: strategy.StyleLine})
	}
	if b.rules.vwap {
		rng := b.rules.layout.rng
		df.Metadata[b.prefix+"vwap"] = anchoredVWAP(df, rng.start, rng.loc)
		metrics = append(metrics, strategy.IndicatorMetric{Values: df.Metadata[b.prefix+"vwap"], Name: "VWAP",
			Color: "#8e44ad", Style: strategy.StyleLine})
	}
	if b.rules.volumeLen > 0 {
		df.Metadata[b.prefix+"volume"] = pineSMA(df.Volume, b.rules.volumeLen)
	}

	return []strategy.ChartIndicator{
		{
			Overlay:   true,
			GroupName: b.name,
			Time:      df.Time,
			Metrics:   metrics,
		},
	}
}

// signal returns the entry of the last candle, if any.
func (b *breakoutTrader) signal(df *model.Dataframe) model.SideType {
	high, low, atrs := df.Metadata[b.prefix+"high"], df.Metadata[b.prefix+"low"], df.Metadata[b.prefix+"atr"]
	i := len(df.Close) - 1
	if i < 1 || len(high) != len(df.Close) || len(low) != len(df.Close) || len(atrs) != len(df.Close) ||
		isNa(high[i]) || isNa(low[i]) || isNa(atrs[i]) || !b.rules.layout.trading(df.Time[i], b.tf) {
		return ""
	}

	atr, width := atrs[i], high[i]-low[i]
	if width < b.rules.minRangeATR*atr || b.rules.maxRangeATR > 0 && width > b.rules.maxRangeATR*atr {
		return ""
	}

	// the first close beyond the range and its buffer: the close before it is
	// inside, or from before the trade session
	var entry model.SideType
	buffer := b.rules.bufferATR * atr
	switch closePrice, previous := df.Close[i], df.Close[i-1]; {
	case closePrice > high[i]+buffer && previous <= high[i]+buffer:
		entry = model.SideTypeBuy
	case closePrice < low[i]-buffer && previous >= low[i]-buffer:
		entry = model.SideTypeSell
	default:
		return ""
	}

	sign := rkSign(entry)
	if b.rules.bodyATR > 0 && (i >= len(df.Open) || sign*(df.Close[i]-df.Open[i]) < b.rules.bodyATR*atr) {
		return ""
	}
	// on the side of the breakout, a missing value failing the filter
	if b.rules.trendLen > 0 && !(sign*(df.Close[i]-df.Metadata[b.prefix+"ema"][i]) > 0) ||
		b.rules.vwap && !(sign*(df.Close[i]-df.Metadata[b.prefix+"vwap"][i]) > 0) {
		return ""
	}
	if b.rules.volumeLen > 0 {
		average := df.Metadata[b.prefix+"volume"]
		if i >= len(df.Volume) || !(df.Volume[i] >= b.rules.volumeMult*average[i-1]) {
			return ""
		}
	}
	return entry
}

// exit closes the position out of the trade session, or on the close back
// past the middle of the range.
func (b *breakoutTrader) exit(df *model.Dataframe, held model.SideType) string {
	i := len(df.Close) - 1
	if !b.rules.layout.trading(df.Time[i], b.tf) {
		return "SESSION END"
	}

	high, low := df.Metadata[b.prefix+"high"], df.Metadata[b.prefix+"low"]
	if len(high) != len(df.Close) || len(low) != len(df.Close) {
		return ""
	}
	if middle := (high[i] + low[i]) / 2; rkSign(held)*(df.Close[i]-middle) < 0 {
		return "INVALIDATION"
	}
	return ""
}

func (b *breakoutTrader) onCandle(df *model.Dataframe, broker service.Broker) {
	entry := b.signal(df)
	if !b.bracket.manage(df, broker, b.exit) || entry == "" {
		return
	}

	_, _, start, _ := b.rules.layout.sessions(df.Time[len(df.Time)-1])
	traded := b.traded[df.Pair]
	if !traded.start.Equal(start) {
		traded = breakoutSession{start: start}
	}
	if traded.trades >= b.rules.maxTrades {
		return
	}
	if b.bracket.open(df, broker, entry, df.Metadata[b.prefix+"atr"].Last(0)) {
		traded.trades++
	}
	b.traded[df.Pair] = traded
}
