package strategies

import (
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/strategy"
)

// csVote is a rule of the consensus tests firing as the metadata of its name
// says on each candle: a BUY on 1, a SELL on -1, none on anything else.
type csVote struct {
	name string
}

func (csVote) warmup() int {
	return 1
}

func (csVote) indicators(*model.Dataframe) []strategy.ChartIndicator {
	return nil
}

func (r csVote) signal(df *model.Dataframe) model.SideType {
	votes := df.Metadata[r.name]
	if len(votes) == 0 || len(votes) != len(df.Close) {
		return ""
	}
	switch votes[len(votes)-1] {
	case 1:
		return model.SideTypeBuy
	case -1:
		return model.SideTypeSell
	}
	return ""
}

// csLookup finds the rules of the consensus tests: a, b, c and d voting as
// csVote, a2 the rule of a under another name, and the signal strategies.
func csLookup(name string) (signalSpec, bool) {
	switch name {
	case "a", "b", "c", "d":
		return signalSpec{about: name, rule: fixed(csVote{name: name})}, true
	case "a2":
		return signalSpec{about: name, rule: fixed(csVote{name: "a"})}, true
	}
	spec, ok := signals[name]
	return spec, ok
}

// newConsensusRunner feeds a consensus of the rules a, b and c of csLookup, 2
// of them within 3 candles, with the ATR at 1.
func newConsensusRunner(t *testing.T, configure func(*ConsensusConfig)) (*ivRunner, *Consensus) {
	t.Helper()
	s := newCSStrategy(t, configure)
	r := newIVRunner(t, s, 0)
	r.fixed["cs_atr"] = 1
	return r, s
}

func newCSStrategy(t *testing.T, configure func(*ConsensusConfig)) *Consensus {
	t.Helper()
	cfg := DefaultConsensusConfig()
	cfg.Timeframe, cfg.Rules, cfg.Quorum, cfg.Window = "5m", []string{"a", "b", "c"}, 2, 3
	if configure != nil {
		configure(&cfg)
	}
	s, err := newConsensus(cfg, csLookup)
	require.NoError(t, err)
	return s
}

// csCandle is a candle closing at 100, the rules of votes firing on it: a BUY
// on 1, a SELL on -1; with the asset held after it.
func csCandle(votes map[string]float64, position float64) ivCandle {
	meta := kv{}
	for name, vote := range votes {
		meta["cs_"+name+":"+name] = vote
	}
	return ivCandle{close: 100, meta: meta, position: position}
}

// csMirror returns the candles of a consensus test mirrored around 100: the
// prices, the votes and the positions.
func csMirror(candles []ivCandle) []ivCandle {
	price := func(v float64) float64 {
		if v == 0 {
			return 0 // the default of the runner
		}
		return 200 - v
	}
	out := make([]ivCandle, len(candles))
	for i, c := range candles {
		m := ivCandle{at: c.at, open: price(c.open), close: price(c.close), high: price(c.low), low: price(c.high),
			volume: c.volume, position: -c.position, meta: kv{}}
		for key, vote := range c.meta {
			m.meta[key] = -vote
		}
		out[i] = m
	}
	return out
}

func TestDefaultConsensusConfig(t *testing.T) {
	cfg := DefaultConsensusConfig()
	require.Equal(t, "15m", cfg.Timeframe, "the one of the signal strategies")
	require.Equal(t, []string{"ema200", "supertrend", "macdtrend", "adxema", "donchian"}, cfg.Rules)
	require.Equal(t, 3, cfg.Quorum)
	require.Equal(t, 3, cfg.Window)
	require.Zero(t, cfg.TrailingATR)

	exits := DefaultSignalConfig("ema200")
	require.Equal(t, exits.BracketConfig, cfg.BracketConfig, "the exits of the signal strategies")
	require.Equal(t, 1.0, cfg.StopATR)
	require.Equal(t, 1.5, cfg.TakeProfitATR)
	require.Zero(t, cfg.MaxBars, "the consensus of the other side closes the position")

	for _, name := range cfg.Rules {
		_, ok := SignalAbout(name)
		require.True(t, ok, name)
	}
	require.NotContains(t, cfg.Rules, "mtftrend", "a warmup of 4 timeframes")
	require.NotContains(t, cfg.Rules, "mtfema", "a warmup of 16 timeframes")

	_, err := NewConsensus(cfg)
	require.NoError(t, err)
}

func TestNewConsensus_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ConsensusConfig)
		err       string
	}{
		{"a single rule", func(c *ConsensusConfig) { c.Rules, c.Quorum = []string{"ema200"}, 1 }, "at least 2 rules"},
		{"no rules", func(c *ConsensusConfig) { c.Rules = nil }, "at least 2 rules"},
		{"unknown rule", func(c *ConsensusConfig) { c.Rules = []string{"ema200", "supertrend", "nothing"} },
			`invalid rule "nothing"`},
		{"empty rule", func(c *ConsensusConfig) { c.Rules = []string{"ema200", "supertrend", ""} }, `invalid rule ""`},
		{"a rule twice", func(c *ConsensusConfig) { c.Rules = []string{"ema200", "supertrend", " ema200"} },
			`rule "ema200" listed twice`},
		{"the same rule under two names", func(c *ConsensusConfig) {
			c.Rules = []string{"donchian", "ema200", "donchianatr"}
		}, `rules "donchian" and "donchianatr" are the same rule`},
		{"quorum 0", func(c *ConsensusConfig) { c.Quorum = 0 }, "quorum must be between 1 and the number of rules (5)"},
		{"quorum above the rules", func(c *ConsensusConfig) { c.Quorum = 6 }, "quorum"},
		{"window 0", func(c *ConsensusConfig) { c.Window = 0 }, "window must be at least 1"},
		{"timeframe", func(c *ConsensusConfig) { c.Timeframe = "" }, "timeframe is required"},
		{"invalid timeframe", func(c *ConsensusConfig) { c.Timeframe = "15x" }, "invalid timeframe"},
		{"negative trailing", func(c *ConsensusConfig) { c.TrailingATR = -1 }, "trailing"},
		{"no stop loss", func(c *ConsensusConfig) { c.StopATR = 0 }, "stop loss and take profit"},
		{"negative take profit", func(c *ConsensusConfig) { c.TakeProfitATR = -1 }, "stop loss and take profit"},
		{"the sizing", func(c *ConsensusConfig) { c.MinPositionValue = 0 }, "min position value"},
		{"the direction", func(c *ConsensusConfig) { c.Direction = "long" }, "invalid direction"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConsensusConfig()
			tt.configure(&cfg)
			_, err := NewConsensus(cfg)
			require.ErrorContains(t, err, tt.err)
			require.True(t, strings.HasPrefix(err.Error(), "consensus: "), err.Error())
		})
	}

	t.Run("valid", func(t *testing.T) {
		cfg := DefaultConsensusConfig()
		cfg.Rules, cfg.TakeProfitATR = []string{" ema200", "supertrend "}, 0
		cfg.Quorum = 2
		s, err := NewConsensus(cfg)
		require.NoError(t, err, "no take profit, as the signal strategies, and the names trimmed")
		require.Equal(t, []string{"ema200", "supertrend"}, s.cfg.Rules)
		require.Equal(t, []string{" ema200", "supertrend "}, cfg.Rules, "the configuration given left as it is")

		_, err = newConsensus(ConsensusConfig{Timeframe: "5m", Rules: []string{"a", "a2"}, Quorum: 1, Window: 1,
			BracketConfig: DefaultBracketConfig()}, csLookup)
		require.ErrorContains(t, err, `rules "a" and "a2" are the same rule`)
	})
}

func TestConsensus_WarmupPeriod(t *testing.T) {
	warmup := func(timeframe string, window int, rules ...string) int {
		cfg := DefaultConsensusConfig()
		cfg.Timeframe, cfg.Window, cfg.Rules, cfg.Quorum = timeframe, window, rules, 1
		s, err := NewConsensus(cfg)
		require.NoError(t, err)
		return s.WarmupPeriod()
	}
	s, err := NewConsensus(DefaultConsensusConfig())
	require.NoError(t, err)
	require.Equal(t, seedWarmup(200)+1+3, s.WarmupPeriod(), "the EMA 200 seeded on the oldest of the 3+1 candles voting")
	require.Equal(t, seedWarmup(200)+1+5, warmup("15m", 5, "ema200", "donchian"))
	require.Equal(t, seedWarmup(20)+1+3, warmup("1h", 3, "donchian", "keltner"), "the EMA 20 of the Keltner")
	require.Equal(t, seedWarmup(14), warmup("1h", 1, "donchian", "bbbreakout"), "the ATR of the exits seeded")
	require.Equal(t, htfWarmup(50, 16)+3, warmup("15m", 3, "mtfema", "donchian"), "the EMA 50 of the 16x timeframe")
	require.Equal(t, 3*24*12+1+2, warmup("5m", 2, "highlow", "donchian"), "3 days of candles")
}

// The consensus of the rules a, b and c, 2 of them within 3 candles: a BUY of
// 100, the whole balance, on the candle it forms, and a SELL of 100 on the
// candles mirrored; none on the others.
func TestConsensus_Entries(t *testing.T) {
	type votes = map[string]float64
	tests := []struct {
		name      string
		configure func(*ConsensusConfig)
		candles   []ivCandle
	}{
		{name: "two rules on the same candle", candles: []ivCandle{csCandle(votes{"a": 1, "b": 1}, 100)}},
		{name: "a candle apart", candles: []ivCandle{csCandle(votes{"a": 1}, 0), csCandle(votes{"b": 1}, 100)}},
		{name: "within the window", candles: []ivCandle{csCandle(votes{"a": 1}, 0), csCandle(nil, 0),
			csCandle(votes{"b": 1}, 100)}},
		{name: "out of the window", candles: []ivCandle{csCandle(votes{"a": 1}, 0), csCandle(nil, 0),
			csCandle(nil, 0), csCandle(votes{"b": 1}, 0)}},
		{name: "a rule firing again, a single vote", candles: []ivCandle{csCandle(votes{"a": 1}, 0),
			csCandle(votes{"a": 1}, 0), csCandle(votes{"a": 1}, 0)}},
		{name: "a rule of the other side in the window", candles: []ivCandle{csCandle(votes{"a": 1}, 0),
			csCandle(votes{"c": -1}, 0), csCandle(votes{"b": 1}, 0), csCandle(nil, 0), csCandle(nil, 0)}},
		{name: "a rule of the other side out of the window", candles: []ivCandle{csCandle(votes{"c": -1}, 0),
			csCandle(nil, 0), csCandle(nil, 0), csCandle(votes{"a": 1, "b": 1}, 100)}},
		{name: "a rule firing both sides", candles: []ivCandle{csCandle(votes{"a": 1}, 0),
			csCandle(votes{"a": -1, "b": 1}, 0), csCandle(votes{"c": 1}, 0)}},
		{name: "the other side leaving the window", candles: []ivCandle{csCandle(votes{"c": -1}, 0),
			csCandle(votes{"a": 1}, 0), csCandle(votes{"b": 1}, 0), csCandle(nil, 100)}},
		{name: "quorum 3", configure: func(c *ConsensusConfig) { c.Quorum = 3 }, candles: []ivCandle{
			csCandle(votes{"a": 1}, 0), csCandle(votes{"b": 1}, 0), csCandle(votes{"c": 1}, 100)}},
		{name: "quorum 3, 2 rules", configure: func(c *ConsensusConfig) { c.Quorum = 3 }, candles: []ivCandle{
			csCandle(votes{"a": 1}, 0), csCandle(votes{"b": 1}, 0), csCandle(nil, 0)}},
		{name: "quorum 1", configure: func(c *ConsensusConfig) { c.Quorum = 1 }, candles: []ivCandle{
			csCandle(votes{"b": 1}, 100)}},
		{name: "window 1", configure: func(c *ConsensusConfig) { c.Window = 1 }, candles: []ivCandle{
			csCandle(votes{"a": 1}, 0), csCandle(votes{"b": 1}, 0), csCandle(votes{"b": 1, "c": 1}, 100)}},
		{name: "window 2", configure: func(c *ConsensusConfig) { c.Window = 2 }, candles: []ivCandle{
			csCandle(votes{"a": 1}, 0), csCandle(nil, 0), csCandle(votes{"b": 1}, 0)}},
	}

	for _, tt := range tests {
		t.Run(tt.name+" buy", func(t *testing.T) {
			r, _ := newConsensusRunner(t, tt.configure)
			r.feed(tt.candles...)
		})
		t.Run(tt.name+" sell", func(t *testing.T) {
			r, _ := newConsensusRunner(t, tt.configure)
			r.feed(csMirror(tt.candles)...)
		})
	}

	t.Run("sell only", func(t *testing.T) {
		r, _ := newConsensusRunner(t, func(c *ConsensusConfig) { c.Direction = RKDirectionSellOnly })
		r.feed(csCandle(votes{"a": 1, "b": 1}, 0))
	})
}

// the entry of the tests of the exits: 100 bought at 100 on the consensus of a
// and b, the stop loss at 99 and the take profit at 101.5
var csLong = []ivCandle{csCandle(map[string]float64{"a": 1}, 0), csCandle(map[string]float64{"b": 1}, 100)}

func TestConsensus_Exits(t *testing.T) {
	t.Run("the legs at the broker", func(t *testing.T) {
		r, s := newConsensusRunner(t, nil)
		r.feed(csLong...)
		legs := s.bracket.trades["BTCUSDT"].legs
		require.Len(t, legs, 2)
		require.InDelta(t, 101.5, legs[0].Price, 1e-9, "the take profit 1.5 ATR away")
		require.InDelta(t, 99, *legs[1].Stop, 1e-9, "the stop loss 1 ATR away")
	})

	t.Run("no take profit", func(t *testing.T) {
		r, s := newConsensusRunner(t, func(c *ConsensusConfig) { c.TakeProfitATR = 0 })
		r.feed(csLong...)
		legs := s.bracket.trades["BTCUSDT"].legs
		require.Len(t, legs, 1)
		require.Equal(t, model.OrderTypeStopLoss, legs[0].Type)
	})

	t.Run("a consensus standing doesn't enter again after the stop loss", func(t *testing.T) {
		r, s := newConsensusRunner(t, nil)
		r.feed(csLong...)
		stopped := csCandle(map[string]float64{"c": 1}, 0)
		stopped.low = 98.9
		r.feed(stopped, csCandle(nil, 0), csCandle(nil, 0))
		require.InDelta(t, 10000-100*1, r.quote(), 1e-9, "stopped out at 99")
		require.Empty(t, s.bracket.trades)

		r.feed(csCandle(nil, 0), csCandle(map[string]float64{"a": 1, "c": 1}, 99)) // a new one: the balance left
	})

	t.Run("the consensus of the other side closes the long and opens the short", func(t *testing.T) {
		r, s := newConsensusRunner(t, nil)
		r.feed(csLong...)
		legs := s.bracket.trades["BTCUSDT"].legs
		r.feed(csCandle(nil, 100), csCandle(nil, 100), // b still in the window
			csCandle(map[string]float64{"c": -1}, 100), csCandle(map[string]float64{"a": -1}, -100))
		require.Equal(t, model.SideTypeSell, s.bracket.trades["BTCUSDT"].side)
		for _, leg := range legs {
			got, err := r.wallet.Order("BTCUSDT", leg.ExchangeID)
			require.NoError(t, err)
			require.Equal(t, model.OrderStatusTypeCanceled, got.Status, "the legs of the long, %s", leg.Type)
		}
	})

	t.Run("a vote of the other side alone keeps the long", func(t *testing.T) {
		r, _ := newConsensusRunner(t, nil)
		r.feed(csLong...)
		r.feed(csCandle(map[string]float64{"c": -1}, 100), csCandle(nil, 100), csCandle(nil, 100))
	})

	t.Run("the consensus of the other side only closes the long of a buy only strategy", func(t *testing.T) {
		r, _ := newConsensusRunner(t, func(c *ConsensusConfig) { c.Direction = RKDirectionBuyOnly })
		r.feed(csLong...)
		r.feed(csCandle(nil, 100), csCandle(nil, 100), csCandle(map[string]float64{"c": -1, "a": -1}, 0))
		require.InDelta(t, 10000, r.quote(), 1e-9)
	})

	t.Run("the take profit closes the long", func(t *testing.T) {
		r, s := newConsensusRunner(t, nil)
		r.feed(csLong...)
		r.feed(ivCandle{close: 101, high: 102, low: 100})
		require.InDelta(t, 10000+100*1.5, r.quote(), 1e-9)
		require.Empty(t, s.bracket.trades)
	})

	t.Run("max bars", func(t *testing.T) {
		r, _ := newConsensusRunner(t, func(c *ConsensusConfig) { c.MaxBars = 2 })
		r.feed(csLong...)
		r.feed(ivCandle{close: 100.5, position: 100}, ivCandle{close: 100.5, position: 0})
		require.InDelta(t, 10000+100*0.5, r.quote(), 1e-9)
	})

	t.Run("trailing", func(t *testing.T) {
		r, s := newConsensusRunner(t, func(c *ConsensusConfig) { c.TrailingATR = 2 })
		r.feed(csLong...)
		stop := func() float64 {
			legs := s.bracket.trades["BTCUSDT"].legs
			require.Len(t, legs, 2)
			return *legs[1].Stop
		}
		require.InDelta(t, 99, stop(), 1e-9, "placed by the entry")
		r.feed(ivCandle{open: 100, close: 101.2, high: 101.4, low: 100, position: 100})
		require.InDelta(t, 99.4, stop(), 1e-9, "2 ATR behind the high of 101.4")
		r.feed(ivCandle{open: 101.2, close: 100.9, high: 101, low: 100.6, position: 100})
		require.InDelta(t, 99.4, stop(), 1e-9, "never back")
	})
}

// The rules of the signal strategies, read from their own indicators under the
// prefix of each: the MACD crossing over its signal line above zero a candle
// after the EMA 9 crossed over the EMA 21.
func TestConsensus_SignalRules(t *testing.T) {
	newRunner := func(t *testing.T) *ivRunner {
		cfg := DefaultConsensusConfig()
		cfg.Timeframe, cfg.Rules, cfg.Quorum, cfg.Window = "5m", []string{"emacross", "macdtrend"}, 2, 2
		s, err := NewConsensus(cfg)
		require.NoError(t, err)
		r := newIVRunner(t, s, 0)
		r.fixed["cs_atr"] = 1
		return r
	}
	below := kv{"cs_emacross:sg_fast": 99, "cs_emacross:sg_slow": 100, "cs_macdtrend:sg_macd": 1,
		"cs_macdtrend:sg_macd_signal": 1.5}
	crossed := kv{"cs_emacross:sg_fast": 101, "cs_emacross:sg_slow": 100, "cs_macdtrend:sg_macd": 1,
		"cs_macdtrend:sg_macd_signal": 1.5}

	r := newRunner(t)
	r.feed(ivCandle{close: 100, meta: below}, ivCandle{close: 100, meta: crossed},
		ivCandle{close: 100, meta: kv{"cs_emacross:sg_fast": 101.5, "cs_emacross:sg_slow": 100,
			"cs_macdtrend:sg_macd": 2, "cs_macdtrend:sg_macd_signal": 1.5}, position: 100})

	r = newRunner(t)
	r.feed(ivCandle{close: 100, meta: below}, ivCandle{close: 100, meta: crossed},
		ivCandle{close: 100, meta: kv{"cs_emacross:sg_fast": 101.5, "cs_emacross:sg_slow": 100,
			"cs_macdtrend:sg_macd": -1, "cs_macdtrend:sg_macd_signal": -1.5}}) // under zero: no vote

	r = newRunner(t)
	r.feed(ivCandle{close: 100, meta: below}, ivCandle{close: 100, meta: crossed},
		ivCandle{close: 100, meta: kv{"cs_emacross:sg_fast": 101.5, "cs_emacross:sg_slow": 100,
			"cs_macdtrend:sg_macd": 1, "cs_macdtrend:sg_macd_signal": 1.5}},
		ivCandle{close: 100, meta: kv{"cs_emacross:sg_fast": 101.5, "cs_emacross:sg_slow": 100,
			"cs_macdtrend:sg_macd": 2, "cs_macdtrend:sg_macd_signal": 1.5}}) // the cross out of the window
}

// Each rule on its own dataframe: ema200 and adxema both keep their EMA under
// sg_trend, of 200 and of 50 closes; and the chart draws the indicators of each
// rule under its name.
func TestConsensus_Indicators(t *testing.T) {
	s, err := NewConsensus(DefaultConsensusConfig())
	require.NoError(t, err)
	df := window(btcDataframe(t), 0, s.WarmupPeriod())
	chart := s.Indicators(df)

	requireSeries(t, pineEMA(df.Close, 200), df.Metadata["cs_ema200:sg_trend"], 0)
	requireSeries(t, pineEMA(df.Close, 50), df.Metadata["cs_adxema:sg_trend"], 0)
	requireSeries(t, pineATR(df.High, df.Low, df.Close, 14), df.Metadata["cs_atr"], 0)
	for key := range df.Metadata {
		require.True(t, strings.HasPrefix(key, "cs_"), key)
	}

	groups := make([]string, len(chart))
	for i, indicator := range chart {
		groups[i] = indicator.GroupName
		require.Len(t, indicator.Time, len(df.Close), indicator.GroupName)
	}
	require.Equal(t, []string{"ema200: EMA 200 Trend", "supertrend: Supertrend", "macdtrend: MACD 12/26/9",
		"adxema: ADX + EMA", "adxema: ADX 14", "donchian: Donchian 20", "ATR 14"}, groups)
}

// Indicators is pure: the chart calls it from HTTP, concurrently with the
// candles; it changes neither the strategy nor the candles.
func TestConsensus_IndicatorsPure(t *testing.T) {
	s, err := NewConsensus(DefaultConsensusConfig())
	require.NoError(t, err)
	btc := btcDataframe(t)
	want := window(btc, 100, 100+s.WarmupPeriod())
	s.Indicators(want)

	var wg sync.WaitGroup
	results := make([]*model.Dataframe, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			df := window(btc, 100, 100+s.WarmupPeriod())
			s.Indicators(df)
			results[i] = df
		}()
	}
	wg.Wait()
	for _, df := range results {
		require.Len(t, df.Metadata, len(want.Metadata))
		for key, values := range want.Metadata {
			requireSeries(t, values, df.Metadata[key], 0)
		}
		require.Equal(t, btc.Close[100:100+s.WarmupPeriod()], df.Close)
	}
	require.Empty(t, s.bracket.trades)
	require.Empty(t, s.trails)
}

// No look ahead: the vote of each rule on each of the last candles is the one
// of the rule computed on the candles up to that candle only, as it trades
// alone at its close, every rule of the signal strategies on the candles of
// the test data.
func TestConsensus_VotesWithoutLookAhead(t *testing.T) {
	if testing.Short() {
		t.Skip("long: left out by -short")
	}
	var names []string
	for name := range signals {
		if name != "donchianatr" { // the rule of donchian
			names = append(names, name)
		}
	}
	slices.Sort(names)
	cfg := DefaultConsensusConfig()
	cfg.Timeframe, cfg.Rules, cfg.Quorum, cfg.Window = "1h", names, 1, 80
	s, err := NewConsensus(cfg)
	require.NoError(t, err)

	df := window(btcDataframe(t), 1000, 2400)
	s.Indicators(df)
	fired := s.votes(df)
	count := cfg.Window + 1
	n := len(df.Close)
	votes, voting := 0, map[string]bool{}
	for k, r := range s.rules {
		require.Len(t, fired[k], count)
		for j := range count {
			end := n - count + 1 + j
			alone := window(df, 0, end)
			r.rule.indicators(alone)
			require.Equal(t, r.rule.signal(alone), fired[k][j], "%s on the candle %d", r.name, end-1)
			if fired[k][j] != "" {
				votes++
				voting[r.name] = true
			}
		}
	}
	require.Greater(t, votes, 50, "the rules fire on the test data")
	require.Greater(t, len(voting), len(names)/2)
}

// A position of the consensus found after a restart: the consensus of the
// other side closes it, and opens the short.
func TestConsensus_AfterARestart(t *testing.T) {
	r, s := newConsensusRunner(t, nil)
	bot := newTradingBot(t, r.wallet, r.step)
	r.broker, r.settle = bot.broker(), bot.settle
	r.feed(csLong...)
	legs := s.bracket.trades["BTCUSDT"].legs

	bot.restart()
	s = newCSStrategy(t, nil)
	r.s, r.broker = s, bot.broker()
	r.feed(csCandle(nil, 100), csCandle(nil, 100))
	require.Equal(t, model.SideTypeBuy, s.bracket.trades["BTCUSDT"].side, "the long found")
	requireOpen(t, r.wallet, s.bracket.trades["BTCUSDT"].legs, 101.5, 99)

	r.feed(csCandle(map[string]float64{"c": -1, "a": -1}, -100))
	for _, leg := range legs {
		got, err := r.wallet.Order("BTCUSDT", leg.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, got.Status, leg.Type)
	}
	require.Equal(t, model.SideTypeSell, s.bracket.trades["BTCUSDT"].side)
}

func TestConsensus_Backtest(t *testing.T) {
	if testing.Short() {
		t.Skip("long: left out by -short")
	}
	t.Run("defaults", func(t *testing.T) {
		cfg := DefaultConsensusConfig()
		cfg.Timeframe = "1h"
		s, err := NewConsensus(cfg)
		require.NoError(t, err)
		checkBacktest(t, s)
	})
	t.Run("trailing", func(t *testing.T) {
		cfg := DefaultConsensusConfig()
		cfg.Timeframe, cfg.Rules, cfg.Quorum, cfg.Window = "1h", []string{"emacross", "rsitrend", "keltner"}, 2, 2
		cfg.TakeProfitATR, cfg.TrailingATR, cfg.StopATR = 0, 2, 2
		s, err := NewConsensus(cfg)
		require.NoError(t, err)
		checkBacktest(t, s)
	})
}
