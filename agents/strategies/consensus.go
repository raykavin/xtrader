package strategies

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
)

// ConsensusConfig holds the inputs of Consensus, with its defaults in
// DefaultConsensusConfig. Window is counted in candles of Timeframe.
type ConsensusConfig struct {
	Timeframe string

	// Rules are the rules voting, by their names of the signal strategies (see
	// NewSignal and signals), each with its classic periods, fixed: at least
	// 2, each once. Only the rule of a signal strategy votes, its exits never
	// apply (the ones of the consensus do), so a rule under two names is the
	// same vote, refused together, eg: donchian and donchianatr. A rule of a
	// long warmup makes the warmup of the consensus as long, eg: mtfema, 16
	// times the candles of its EMA 50.
	Rules []string

	// Quorum is how many of the rules must have fired the same side within
	// the last Window candles, the last one included, none of them the other
	// side there, for a consensus of that side: 1 to the number of rules.
	// Window 1 asks for them on the same candle; the rules firing on events (a
	// cross, a flip, a break), seldom on the same candle, a window of a few
	// candles lets them agree a few candles apart.
	Quorum int
	Window int

	// TrailingATR, above 0, trails the stop loss at the broker that many ATR
	// of ATRLen candles behind the best price since the entry, as
	// SignalConfig.TrailingATR does; 0 leaves it where the entry placed it.
	TrailingATR float64

	// The exits and the sizing, see BracketConfig, TakeProfitATR 0 placing no
	// take profit, as for the signal strategies. The consensus of the other
	// side also closes the position at the market, and opens that side when it
	// forms on that candle.
	BracketConfig
}

// DefaultConsensusConfig returns the defaults of Consensus, on the 15m
// timeframe of the signal strategies, with their exits (DefaultSignalConfig):
// 3 of 5 rules of different kinds, a trend (ema200), a volatility trend
// (supertrend), a momentum (macdtrend), an ADX momentum (adxema) and a
// breakout (donchian), agreeing within 3 candles. Neither the rules nor the
// quorum nor the window come from a source: they are a choice, to be checked
// in backtests.
func DefaultConsensusConfig() ConsensusConfig {
	return ConsensusConfig{
		Timeframe:     "15m",
		Rules:         []string{"ema200", "supertrend", "macdtrend", "adxema", "donchian"},
		Quorum:        3,
		Window:        3,
		BracketConfig: DefaultBracketConfig(),
	}
}

func (c ConsensusConfig) validate() error {
	// the timeframe and the exits, checked as the ones of a signal strategy
	exits := SignalConfig{Timeframe: c.Timeframe, TrailingATR: c.TrailingATR, BracketConfig: c.BracketConfig}
	if err := exits.validate(); err != nil {
		return err
	}

	switch {
	case len(c.Rules) < 2:
		return errors.New("at least 2 rules must vote")
	case c.Quorum < 1 || c.Quorum > len(c.Rules):
		return fmt.Errorf("the quorum must be between 1 and the number of rules (%d)", len(c.Rules))
	case c.Window < 1:
		return errors.New("the window must be at least 1 candle")
	}
	return nil
}

// consensusPrefix prefixes the metadata of Consensus: cs_atr, its ATR, and the
// indicators of each rule, under cs_<rule>:<key of the rule>, eg:
// cs_ema200:sg_trend, the rules sharing the keys of their indicators (eg:
// sg_trend, the EMA 200 of ema200 and the EMA 50 of adxema).
const consensusPrefix = "cs_"

// consensusRule is a rule voting in a Consensus: its name in signals, the rule
// on the timeframe of the consensus, and the prefix of its indicators in the
// metadata of the dataframe, see consensusPrefix.
type consensusRule struct {
	name   string
	rule   signalRule
	prefix string
}

// Consensus trades the consensus of signal rules (see signals): a BUY on the
// candle Quorum rules at least have fired a BUY within the last Window
// candles, the last one included, with none of them a SELL there; a SELL on the
// mirror. One position at a time per pair, placed at the market with its stop
// loss and take profit at the broker, see BracketConfig; the consensus of the
// other side closes it at the market, and opens that side on the candle it
// forms, as the opposite signal of a signal strategy does. See
// ConsensusConfig.
//
// Each rule votes as it trades alone: its signal on a candle is the one it
// fires on the close of that candle, computed on the candles up to it only,
// without a look ahead. The rules fire on events (a cross, a flip, a break), so
// each is asked about the last Window candles: its indicators, computed once
// per candle on the candles handed to the strategy, are read up to each of
// them, the indicators of the rules all being computed candle by candle from
// the oldest (EMA, RMA, ATR, channels...), the values up to a candle the same
// on the candles up to it as on more. Each rule computes its indicators on a
// dataframe of its own, the same candles with metadata of its own, as the rules
// share the keys of their indicators, eg: sg_trend.
//
// The entry is on the candle the consensus forms: one still standing on the
// next candles doesn't enter again, eg: after the stop loss, as a rule fires
// once on its event. The consensus of the other side standing, though, closes
// the position held, eg: one found after a restart.
type Consensus struct {
	cfg     ConsensusConfig
	warmup  int
	rules   []consensusRule
	bracket *bracket
	trails  map[string]*signalTrail // the trailing of the positions, by pair
}

// NewConsensus validates the configuration and creates the strategy, the names
// of its rules trimmed of spaces.
func NewConsensus(cfg ConsensusConfig) (*Consensus, error) {
	return newConsensus(cfg, func(name string) (signalSpec, bool) {
		spec, ok := signals[name]
		return spec, ok
	})
}

// newConsensus is NewConsensus, the rules of the names found by lookup.
func newConsensus(cfg ConsensusConfig, lookup func(name string) (signalSpec, bool)) (*Consensus, error) {
	names := make([]string, len(cfg.Rules))
	for k, name := range cfg.Rules {
		names[k] = strings.TrimSpace(name)
	}
	cfg.Rules = names
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("consensus: %w", err)
	}

	_, timeframe, err := timeframeCandles(cfg.Timeframe, time.Hour)
	if err != nil {
		return nil, fmt.Errorf("consensus: %w", err)
	}
	rules := make([]consensusRule, 0, len(names))
	warmup := 0
	for _, name := range names {
		spec, ok := lookup(name)
		if !ok {
			return nil, fmt.Errorf("consensus: invalid rule %q: not a signal strategy", name)
		}
		rule := spec.rule(timeframe)
		for _, other := range rules {
			switch {
			case other.name == name:
				return nil, fmt.Errorf("consensus: rule %q listed twice", name)
			case reflect.DeepEqual(other.rule, rule):
				return nil, fmt.Errorf("consensus: rules %q and %q are the same rule, a single vote", other.name,
					name)
			}
		}
		rules = append(rules, consensusRule{name: name, rule: rule, prefix: consensusPrefix + name + ":"})
		warmup = max(warmup, rule.warmup())
	}

	// each rule asked about the last Window candles and the one before them
	// (see votes), on the candles up to each: the oldest of them holds the
	// warmup of the rule after the Window candles to its last one; and the ATR
	// of the exits seeded
	return &Consensus{
		cfg:     cfg,
		warmup:  max(warmup+cfg.Window, seedWarmup(cfg.ATRLen)),
		rules:   rules,
		bracket: newBracket("CONSENSUS", cfg.Timeframe, cfg.BracketConfig),
		trails:  make(map[string]*signalTrail),
	}, nil
}

func (s *Consensus) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *Consensus) WarmupPeriod() int {
	return s.warmup
}

// Indicators adds the ATR of the exits to the dataframe, cs_atr, and the
// indicators of each rule, computed on a dataframe of its own, under its prefix
// (see consensusPrefix); the chart draws them under the name of the rule, eg:
// "ema200: EMA 200 Trend", and the ATR.
func (s *Consensus) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata[consensusPrefix+"atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)

	var chart []strategy.ChartIndicator
	for _, r := range s.rules {
		own := &model.Dataframe{Pair: df.Pair, Close: df.Close, Open: df.Open, High: df.High, Low: df.Low,
			Volume: df.Volume, Time: df.Time, LastUpdate: df.LastUpdate,
			Metadata: make(map[string]model.Series[float64])}
		for _, indicator := range r.rule.indicators(own) {
			indicator.GroupName = r.name + ": " + indicator.GroupName
			chart = append(chart, indicator)
		}
		for key, values := range own.Metadata {
			df.Metadata[r.prefix+key] = values
		}
	}
	return append(chart, pane(df, fmt.Sprintf("ATR %d", s.cfg.ATRLen), s.cfg.ATRLen,
		line(df.Metadata[consensusPrefix+"atr"], "ATR", colorOther)))
}

func (s *Consensus) OnCandle(df *model.Dataframe, broker service.Broker) {
	fired := s.votes(df)
	now, voters := s.consensus(fired, s.cfg.Window) // standing on the last candle
	before, _ := s.consensus(fired, s.cfg.Window-1) // on the candle before

	opposite := func(_ *model.Dataframe, held model.SideType) string {
		if now != "" && now != held {
			return "OPPOSITE"
		}
		return ""
	}
	if !s.bracket.manage(df, broker, opposite) {
		s.trail(df, broker)
		return
	}
	if now == "" || now == before {
		return // none, or standing since a candle before, its entry made then
	}

	log.Infof("[CONSENSUS] %s %s | %s | %d of %d rules within %d candles: %s", df.Pair, s.cfg.Timeframe, now,
		len(voters), len(s.rules), s.cfg.Window, strings.Join(voters, ", "))
	if s.bracket.open(df, broker, now, df.Metadata[consensusPrefix+"atr"].Last(0)) {
		s.trails[df.Pair] = &signalTrail{trade: s.bracket.trades[df.Pair], extreme: df.Close.Last(0)}
	}
}

// trail moves the stop loss at the broker of the position held on the pair to
// TrailingATR from the best price since the entry, see bracket.trail.
func (s *Consensus) trail(df *model.Dataframe, broker service.Broker) {
	s.bracket.trail(df, broker, s.trails, s.cfg.TrailingATR, df.Metadata[consensusPrefix+"atr"])
}

// votes returns the signals of each rule, in the order of the rules, on the
// last Window+1 candles of df, the oldest first, none where it didn't fire:
// the consensus of the last candle (Window candles) and of the one before. The
// signal on a candle is the one of the rule on the candles up to it, its
// indicators of the metadata of df read up to it, see Consensus.
func (s *Consensus) votes(df *model.Dataframe) [][]model.SideType {
	count := s.cfg.Window + 1
	n := len(df.Close)
	fired := make([][]model.SideType, len(s.rules))
	for k, r := range s.rules {
		own := make(map[string]model.Series[float64])
		for key, values := range df.Metadata {
			if name, ok := strings.CutPrefix(key, r.prefix); ok {
				own[name] = values
			}
		}
		fired[k] = make([]model.SideType, count)
		for j := range count {
			if end := n - count + 1 + j; end >= 1 {
				fired[k][j] = r.rule.signal(candlesUpTo(df, end, own))
			}
		}
	}
	return fired
}

// consensus returns the side of the consensus on the candle last of fired (see
// votes), none without one: Quorum rules at least firing it on the Window
// candles up to that candle, none of them the other side; and the names of the
// rules that fired it.
func (s *Consensus) consensus(fired [][]model.SideType, last int) (model.SideType, []string) {
	first := last - s.cfg.Window + 1
	voted := make(map[model.SideType][]string, len(sides))
	for k, r := range s.rules {
		seen := make(map[model.SideType]bool, len(sides))
		for j := max(first, 0); j <= last; j++ {
			if side := fired[k][j]; side != "" && !seen[side] {
				seen[side] = true
				voted[side] = append(voted[side], r.name)
			}
		}
	}
	for _, side := range sides {
		if len(voted[side]) >= s.cfg.Quorum && len(voted[oppositeSide(side)]) == 0 {
			return side, voted[side]
		}
	}
	return "", nil
}

// candlesUpTo returns the first end candles of df, the ones up to the candle
// end-1, with the series of metadata cut there: a dataframe on which a rule
// reads that candle as its last one, the candles after it out of its reach
// (cut to their capacity too). A series not as long as the candles of df is out
// of step with them: left out, as missing.
func candlesUpTo(df *model.Dataframe, end int, metadata map[string]model.Series[float64]) *model.Dataframe {
	n := len(df.Close)
	cut := func(values model.Series[float64]) model.Series[float64] {
		if len(values) != n {
			return nil
		}
		return values[:end:end]
	}
	upTo := &model.Dataframe{Pair: df.Pair, Close: df.Close[:end:end], Open: cut(df.Open), High: cut(df.High),
		Low: cut(df.Low), Volume: cut(df.Volume), Metadata: make(map[string]model.Series[float64], len(metadata))}
	if len(df.Time) == n {
		upTo.Time, upTo.LastUpdate = df.Time[:end:end], df.Time[end-1]
	}
	for key, values := range metadata {
		upTo.Metadata[key] = cut(values)
	}
	return upTo
}
