package llm

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/ui"
)

// testPrompts is a PromptSource of the prompt the test sets, recording what
// the Trader reports of it.
type testPrompts struct {
	mu      sync.Mutex
	prompt  Prompt
	reports []promptReport
}

type promptReport struct {
	ref ui.DecisionPrompt
	err error
}

func (p *testPrompts) Current(known ui.DecisionPrompt) (Prompt, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prompt.Ref == known {
		return Prompt{}, false
	}
	return p.prompt, true
}

func (p *testPrompts) ReportPrompt(ref ui.DecisionPrompt, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reports = append(p.reports, promptReport{ref: ref, err: err})
}

func (p *testPrompts) set(prompt Prompt) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prompt = prompt
}

// promptOf returns a prompt of the database, name at version, of content.
func promptOf(id int64, name string, version int, content string) Prompt {
	return Prompt{Ref: ui.DecisionPrompt{ID: id, Name: name, Version: version}, Content: content}
}

// promptRunner returns the runner of a Trader starting with the prompt first,
// its prompts switched with the source returned.
func promptRunner(t *testing.T, configure func(*Config), first Prompt, overrides PromptOverrides) (*traderRunner,
	*testPrompts) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Candles = 50
	cfg.PositionSize = map[string]float64{testPair: 10}
	if configure != nil {
		configure(&cfg)
	}
	provider := &fakeProvider{t: t}
	trader, err := New(cfg, provider, WithPrompt(first, overrides))
	require.NoError(t, err)
	prompts := &testPrompts{prompt: first}
	require.NoError(t, trader.SetPromptSource(prompts))

	r := &traderRunner{t: t, trader: trader, provider: provider, wallet: newTraderRunner(t, configure, 0).wallet,
		df: &model.Dataframe{Pair: testPair, Metadata: map[string]model.Series[float64]{}}}
	r.broker = r.wallet
	for i := 0; i < trader.WarmupPeriod(); i++ {
		r.add(100 + 2*math.Sin(float64(i)/8))
	}
	return r, prompts
}

// chartNames returns the names of the groups and of the metrics of the chart
// of the indicators.
func chartNames(indicators []strategy.ChartIndicator) []string {
	var names []string
	for _, indicator := range indicators {
		names = append(names, indicator.GroupName)
		for _, metric := range indicator.Metrics {
			if metric.Name != "" {
				names = append(names, metric.Name)
			}
		}
	}
	return names
}

const (
	strategyA = "# trend: A\n# Indicators: atr, volume\n\nStrategy A: follow the trend."
	strategyB = "# pullback: B\n# Indicators: ema 10 30, atr\n# Take profit: optional\n\nStrategy B: buy the pullbacks."
)

// The prompt switched applies from the next analysis on: its instructions, its
// indicators, computed on the candles of the switch, its take profit, and its
// name in the records, the analysis in progress finishing with the one it
// started with.
func TestTrader_PromptSwitch(t *testing.T) {
	a := promptOf(1, "trend", 3, strategyA)
	r, prompts := promptRunner(t, nil, a, PromptOverrides{})
	journal := &testJournal{}
	require.NoError(t, r.trader.SetJournal(journal))
	warmup := r.trader.WarmupPeriod()

	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	require.Equal(t, systemPrompt(strategyA), r.provider.systems[0])
	in := r.provider.last()
	require.Contains(t, in.Indicators, "atr_14")
	require.NotContains(t, in.Indicators, "ema_10")
	require.False(t, in.Rules.TakeProfitOptional)
	require.Equal(t, &a.Ref, r.lastDecision().Prompt)
	require.Equal(t, &a.Ref, journal.entries[0].Prompt)
	require.Empty(t, prompts.reports, "the first prompt, not switched")
	require.Equal(t, a.Ref, r.trader.Prompt())

	// switched while the model is asked: the analysis in progress goes on
	// with A, in its records too
	b := promptOf(2, "pullback", 1, strategyB)
	r.provider.asked = func() { prompts.set(b) }
	r.step(100.2, decide(ActionWait, 0.4, nil, nil, nil))
	r.provider.asked = nil
	require.Equal(t, systemPrompt(strategyA), r.provider.systems[1])
	require.NotContains(t, r.provider.last().Indicators, "ema_10")
	require.Equal(t, &a.Ref, r.lastDecision().Prompt)
	require.Equal(t, &a.Ref, journal.entries[1].Prompt)

	// the next analysis switches to B
	r.step(100.4, decide(ActionWait, 0.4, nil, nil, nil))
	require.Equal(t, systemPrompt(strategyB), r.provider.systems[2])
	require.NotContains(t, r.provider.systems[2], "Strategy A")
	in = r.provider.last()
	for _, name := range []string{"ema_10", "ema_30"} {
		require.Contains(t, in.Indicators, name)
		values := in.Indicators[name]
		require.Len(t, values, r.trader.cfg.History)
		require.False(t, math.IsNaN(float64(values[len(values)-1])), "%s computed on the candle of the switch", name)
	}
	require.NotContains(t, in.Indicators, "volume_sma_20")
	require.True(t, in.Rules.TakeProfitOptional)
	require.Equal(t, &b.Ref, r.lastDecision().Prompt)
	require.Equal(t, &b.Ref, journal.entries[2].Prompt)
	require.Equal(t, []promptReport{{ref: b.Ref}}, prompts.reports)
	require.Equal(t, b.Ref, r.trader.Prompt())

	names := chartNames(r.trader.Indicators(r.df))
	require.Contains(t, names, "ema_10", "the chart draws the indicators of B")
	require.NotContains(t, names, "Volume SMA 20")
	require.Equal(t, warmup, r.trader.WarmupPeriod(), "the candles kept don't change")

	// the same prompt again: nothing switched, nothing reported
	r.step(100.6, decide(ActionWait, 0.4, nil, nil, nil))
	require.Len(t, prompts.reports, 1)

	// a new version of B
	b2 := promptOf(2, "pullback", 2, strings.Replace(strategyB, "the pullbacks", "the deep pullbacks", 1))
	prompts.set(b2)
	r.step(100.8, decide(ActionWait, 0.4, nil, nil, nil))
	require.Contains(t, r.provider.systems[4], "Strategy B: buy the deep pullbacks.")
	require.Equal(t, &b2.Ref, r.lastDecision().Prompt)
}

// A prompt the Trader can't analyze with is refused, told to the source once,
// the previous one kept until another is selected.
func TestTrader_PromptRefused(t *testing.T) {
	a := promptOf(1, "trend", 1, strategyA)
	tests := []struct {
		name      string
		configure func(*Config)
		content   string
		want      string
	}{
		{"unknown indicator", nil, "# x\n# Indicators: atr, heikinashi\n\nTrade.", `unknown kind "heikinashi"`},
		{"no take profit with a min risk:reward", func(c *Config) { c.MinRiskReward = 1.5 },
			"# x\n# Take profit: none\n\nTrail.", "a min risk:reward needs a take profit"},
		{"more history than kept", nil, "# x\n# Indicators: ema 100, atr\n\nTrade.",
			"its indicators need 500 candles of history, more than the 206 the agent keeps"},
		{"empty", nil, "# x\n# Indicators: atr\n", "the prompt is empty"},
		{"not text", nil, "# x\n\nTrade \xff.", "isn't UTF-8 text"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, prompts := promptRunner(t, tc.configure, a, PromptOverrides{})
			r.step(100, decide(ActionWait, 0.4, nil, nil, nil))

			bad := promptOf(2, "bad", 1, tc.content)
			prompts.set(bad)
			r.step(100.2, decide(ActionWait, 0.4, nil, nil, nil))
			require.Equal(t, systemPrompt(strategyA), r.provider.systems[1], "A kept")
			require.Equal(t, &a.Ref, r.lastDecision().Prompt)
			require.Len(t, prompts.reports, 1)
			require.Equal(t, bad.Ref, prompts.reports[0].ref)
			require.ErrorContains(t, prompts.reports[0].err, tc.want)

			r.step(100.4, decide(ActionWait, 0.4, nil, nil, nil))
			require.Len(t, prompts.reports, 1, "not tried again")

			prompts.set(a) // back to A: applied
			r.step(100.6, decide(ActionWait, 0.4, nil, nil, nil))
			require.Len(t, prompts.reports, 2)
			require.Equal(t, promptReport{ref: a.Ref}, prompts.reports[1])
		})
	}

	// the room of Config.Warmup takes it
	r, prompts := promptRunner(t, func(c *Config) { c.Warmup = 600 }, a, PromptOverrides{})
	require.Equal(t, 600, r.trader.WarmupPeriod())
	long := promptOf(3, "long", 1, "# x\n# Indicators: ema 100, atr\n\nTrade.")
	prompts.set(long)
	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	require.Equal(t, []promptReport{{ref: long.Ref}}, prompts.reports)
	require.Contains(t, r.provider.last().Indicators, "ema_100")
}

// The flags set take precedence over the header of every prompt: their
// indicators replace the ones of the header, their take profit adds to it.
func TestTrader_PromptOverrides(t *testing.T) {
	a := promptOf(1, "trend", 1, strategyA)
	r, prompts := promptRunner(t, nil, a, PromptOverrides{Indicators: []string{"rsi", "ema 10"},
		TakeProfitOptional: true})
	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	in := r.provider.last()
	require.Contains(t, in.Indicators, "rsi_14")
	require.Contains(t, in.Indicators, "ema_10")
	require.NotContains(t, in.Indicators, "atr_14", "not the indicators of the header")
	require.True(t, in.Rules.TakeProfitOptional)

	prompts.set(promptOf(2, "pullback", 1, strategyB))
	r.step(100.2, decide(ActionWait, 0.4, nil, nil, nil))
	in = r.provider.last()
	require.Contains(t, in.Indicators, "rsi_14")
	require.Contains(t, in.Indicators, "ema_10")
	require.NotContains(t, in.Indicators, "ema_30", "not the indicators of the header of B")
	require.Equal(t, systemPrompt(strategyB), r.provider.systems[1], "the strategy of B, with the flags")
}

// The default prompt, of an empty content, and a provider that takes no
// instructions with its requests.
func TestTrader_PromptDefaultAndProvider(t *testing.T) {
	r, _ := promptRunner(t, nil, DefaultPrompt(), PromptOverrides{})
	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	require.Equal(t, defaultSystemPrompt, r.provider.systems[0])
	require.Equal(t, &ui.DecisionPrompt{Name: "default"}, r.lastDecision().Prompt)

	empty := promptOf(0, "default", 0, "")
	trader, err := New(DefaultConfig(), &fakeProvider{t: t}, WithPrompt(empty, PromptOverrides{}))
	require.NoError(t, err)
	require.Equal(t, defaultSystemPrompt, trader.state.Load().system)

	plain := plainProvider{&fakeProvider{t: t}}
	_, err = New(DefaultConfig(), plain, WithPrompt(DefaultPrompt(), PromptOverrides{}))
	require.ErrorContains(t, err, "takes no prompt with its requests")
	trader, err = New(DefaultConfig(), plain)
	require.NoError(t, err)
	require.ErrorContains(t, trader.SetPromptSource(&testPrompts{}), "takes no prompt with its requests")

	_, err = New(DefaultConfig(), &fakeProvider{t: t}, WithPrompt(promptOf(1, "x", 1, "# x\n# Indicators: nope\n\ny"),
		PromptOverrides{}))
	require.ErrorContains(t, err, "the prompt x v1")

	// without a prompt: the instructions of the provider, no name recorded
	r = newTraderRunner(t, nil, 0)
	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	require.Empty(t, r.provider.systems[0])
	require.Nil(t, r.lastDecision().Prompt)
}

// plainProvider is a Provider taking no instructions with its requests.
type plainProvider struct {
	f *fakeProvider
}

func (p plainProvider) Analyze(ctx context.Context, in Input) (Decision, error) {
	return p.f.Analyze(ctx, in)
}

// The chart reads the indicators while the prompts switch in the flow of the
// strategy, each of its reads whole.
func TestTrader_PromptSwitchWhileCharted(t *testing.T) {
	a := promptOf(1, "trend", 1, strategyA)
	b := promptOf(2, "pullback", 1, strategyB)
	r, prompts := promptRunner(t, nil, a, PromptOverrides{})
	chart := func() *model.Dataframe { // the candles of the chart, its own
		n := len(r.df.Close)
		return &model.Dataframe{Pair: testPair, Metadata: map[string]model.Series[float64]{},
			Close:  append(model.Series[float64](nil), r.df.Close[:n]...),
			Open:   append(model.Series[float64](nil), r.df.Open[:n]...),
			High:   append(model.Series[float64](nil), r.df.High[:n]...),
			Low:    append(model.Series[float64](nil), r.df.Low[:n]...),
			Volume: append(model.Series[float64](nil), r.df.Volume[:n]...),
			Time:   append(r.df.Time[:0:0], r.df.Time[:n]...)}
	}
	df := chart()

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			names := chartNames(r.trader.Indicators(df))
			ema, volume := false, false
			for _, name := range names {
				ema = ema || name == "ema_10"
				volume = volume || name == "Volume SMA 20"
			}
			if ema == volume {
				t.Errorf("the indicators of no prompt: %v", names)
				return
			}
		}
	}()
	for i := 0; i < 20; i++ {
		if i%2 == 0 {
			prompts.set(b)
		} else {
			prompts.set(a)
		}
		r.step(100+float64(i)/10, decide(ActionWait, 0.4, nil, nil, nil))
	}
	close(done)
	wg.Wait()
}
