package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/internal/backtest"
	"github.com/raykavin/xtrader/agents/internal/entryfilter"
	"github.com/raykavin/xtrader/agents/internal/remote"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/agents/metalabel"
	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/ui"
)

// fixed is a strategy of the tests of a timeframe and a warmup, doing nothing.
type fixed struct {
	timeframe string
	warmup    int
}

func (f *fixed) Timeframe() string                                     { return f.timeframe }
func (f *fixed) WarmupPeriod() int                                     { return f.warmup }
func (f *fixed) Indicators(*model.Dataframe) []strategy.ChartIndicator { return nil }
func (f *fixed) OnCandle(*model.Dataframe, service.Broker)             {}

// newsFixed is a fixed strategy trading around the releases.
type newsFixed struct {
	fixed
	source strategies.Releases
}

func (n *newsFixed) SetReleases(source strategies.Releases) { n.source = source }

// standaloneRun runs agent on its own with args, its standard output returned
// with the error of run.
func standaloneRun(t *testing.T, agent Agent, args ...string) (string, error) {
	t.Helper()
	t.Setenv(agentapi.EnvToken, "")
	osArgs, commandLine, stdout := os.Args, flag.CommandLine, os.Stdout
	read, write, err := os.Pipe()
	require.NoError(t, err)
	os.Args = append([]string{agent.Name}, args...)
	flag.CommandLine = flag.NewFlagSet(agent.Name, flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Stdout = write
	defer func() { os.Args, flag.CommandLine, os.Stdout = osArgs, commandLine, stdout }()
	runErr := run(agent)
	require.NoError(t, write.Close())
	var out bytes.Buffer
	_, err = io.Copy(&out, read)
	require.NoError(t, err)
	return out.String(), runErr
}

// fixedAgent is an agent of the tests whose strategies are s, by name.
func fixedAgent(strategies map[string]strategy.Strategy, names ...string) Agent {
	agent := Agent{Name: "testagent"}
	for _, name := range names {
		s := strategies[name]
		agent.Strategies = append(agent.Strategies, Strategy{Name: name, About: name,
			New: func(Inputs) (strategy.Strategy, error) { return s, nil }})
	}
	return agent
}

// -print-warmup tells the warmup of the strategy filtered, the filters' when
// larger, the references it reads and whether it trades around the releases;
// without a filter, as before; a flag of a filter refused, the agent not
// started.
func TestRun_PrintWarmupOfTheStrategyFiltered(t *testing.T) {
	agent := fixedAgent(map[string]strategy.Strategy{"plain": &fixed{timeframe: "15m", warmup: 30},
		"news": &newsFixed{fixed: fixed{timeframe: "5m", warmup: 40}}}, "plain", "news")
	warmup := func(args ...string) agentapi.Warmup {
		t.Helper()
		out, err := standaloneRun(t, agent, append([]string{"-mode=backtest", "-pair=XAUUSDm", "-print-warmup"},
			args...)...)
		require.NoError(t, err)
		var told agentapi.Warmup
		require.NoError(t, json.Unmarshal([]byte(out), &told), out)
		return told
	}
	assert.Equal(t, agentapi.Warmup{Timeframe: "15m", Candles: 30}, warmup(), "no filter: as before")
	assert.Equal(t, agentapi.Warmup{Timeframe: "15m", Candles: 154}, warmup("-regime=trend", "-regime-method=adx"),
		"the ADX of 14: 11 lengths")
	assert.Equal(t, agentapi.Warmup{Timeframe: "15m", Candles: 160, References: []string{"DXYm"}},
		warmup("-regime=trend", "-regime-method=adx", "-intermarket-symbol=DXYm"),
		"the candles of the reference read: 3 EMA of 50 and 10")
	assert.Equal(t, agentapi.Warmup{Timeframe: "5m", Candles: 40, Releases: true}, warmup("-strategy=news"))
	assert.Equal(t, agentapi.Warmup{Timeframe: "5m", Candles: 250, Releases: true},
		warmup("-strategy=news", "-meta-record="+filepath.Join(t.TempDir(), "s.jsonl")), "the window of the features")

	_, err := standaloneRun(t, agent, "-mode=backtest", "-pair=XAUUSDm", "-print-warmup", "-regime-len=20")
	assert.ErrorContains(t, err, "-regime-len is for -regime")
	_, err = standaloneRun(t, agent, "-mode=backtest", "-pair=XAUUSDm", "-print-warmup",
		"-meta-model="+filepath.Join(t.TempDir(), "missing.json"))
	assert.ErrorContains(t, err, "-meta-model")
}

// A backtest of -csv files refuses the intermarket filter: a pull of the
// reference there would read ahead.
func TestRun_IntermarketRefusedInABacktestOfFiles(t *testing.T) {
	agent := fixedAgent(map[string]strategy.Strategy{"plain": &fixed{timeframe: "15m", warmup: 30}}, "plain")
	_, err := standaloneRun(t, agent, "-mode=backtest", "-pair=XAUUSDm", "-csv=x.csv", "-intermarket-symbol=DXYm")
	assert.ErrorContains(t, err, "-intermarket-symbol reads the candles of its reference")
}

// The hooks of a backtest give the strategy filtered the candles of its
// references and the releases of the config on the clock of the run.
func TestBacktestHooks(t *testing.T) {
	inner := &newsFixed{fixed: fixed{timeframe: "15m", warmup: 5}}
	wrapped, err := entryfilter.Wrap(inner, entryfilter.Config{IntermarketSymbol: "DXYm"})
	require.NoError(t, err)
	hooks := backtestHooks(wrapped, backtest.Config{Releases: map[string][]agentapi.BacktestRelease{
		"XAUUSDm": {{Time: 100, Currency: "USD", Code: "cpi", Importance: 3, Timed: true}}}})
	require.NotNil(t, hooks.Candles)
	hooks.Prepare(nil)
	require.NotNil(t, inner.source, "the releases given through the filter")

	none := &newsFixed{fixed: fixed{timeframe: "15m"}}
	backtestHooks(none, backtest.Config{}).Prepare(nil)
	assert.Nil(t, none.source, "none read by the controller: it waits for them")
}

// The releases of a backtest: the ones of the pair between from and to, as the
// calendar reads them (a release of a day any time of it), a tentative one
// untimed at the start of its day until it is released.
func TestBacktestReleases(t *testing.T) {
	day := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	at := day.Add(14*time.Hour + 30*time.Minute)
	now := day.Add(10 * time.Hour)
	source := backtestReleases{now: func() time.Time { return now }, byPair: map[string][]agentapi.BacktestRelease{
		"XAUUSDm": {
			{Time: at.Unix(), Currency: "USD", Code: "nfp", Name: "Payrolls", Importance: 3, Timed: true},
			{Time: day.Unix(), Currency: "USD", Code: "holiday", Importance: 1},
			{Time: at.Add(4 * time.Hour).Unix(), Currency: "USD", Code: "fomc", Importance: 3, Timed: true,
				Tentative: true},
		}}}
	read := func(from, to time.Time) []string {
		t.Helper()
		releases, err := source.Releases(context.Background(), "XAUUSDm", from, to)
		require.NoError(t, err)
		var codes []string
		for _, r := range releases {
			codes = append(codes, r.Code+" "+r.Time.Format("15:04")+map[bool]string{true: " timed", false: ""}[r.Timed])
		}
		return codes
	}
	assert.Equal(t, []string{"holiday 00:00", "fomc 00:00", "nfp 14:30 timed"}, read(day.Add(9*time.Hour),
		day.Add(15*time.Hour)), "the tentative one untimed before it")
	assert.Equal(t, []string{"holiday 00:00", "fomc 00:00"}, read(day.Add(9*time.Hour), day.Add(10*time.Hour)))
	now = at.Add(5 * time.Hour)
	assert.Equal(t, []string{"holiday 00:00", "nfp 14:30 timed", "fomc 18:30 timed"}, read(day, day.Add(20*time.Hour)),
		"released: its time known")
	assert.Empty(t, read(day.Add(25*time.Hour), day.Add(26*time.Hour)))
	releases, err := source.Releases(context.Background(), "BTCUSDm", day, day.Add(24*time.Hour))
	require.NoError(t, err)
	assert.Empty(t, releases, "another pair")
}

// The releases of the news of the dashboard: the switch off fails the read,
// never taken for a calendar without releases; a pair without a currency of
// the calendar has none.
func TestNewsReleases(t *testing.T) {
	source := newsReleases{news: llm.NewNewsSwitch(llm.NewsConfig{})}
	_, err := source.Releases(context.Background(), "XAUUSDm", time.Now(), time.Now().Add(time.Hour))
	assert.ErrorIs(t, err, llm.ErrNewsDisabled)
	releases, err := source.Releases(context.Background(), "UNKNOWNPAIR", time.Now(), time.Now().Add(time.Hour))
	assert.NoError(t, err)
	assert.Empty(t, releases)
}

// The entry filter judges each leg of the pairs strategy apart, as two
// strategies: refused for it, the agent not started; without it, it starts.
func TestRun_PairsRefusesTheEntryFilter(t *testing.T) {
	agent := fixedAgent(map[string]strategy.Strategy{"pairs": &fixed{timeframe: "1h", warmup: 250}}, "pairs")
	for _, filter := range []string{"-regime=trend", "-equity-curve=5", "-intermarket-symbol=DXYm",
		"-meta-record=" + filepath.Join(t.TempDir(), "s.jsonl")} {
		_, err := standaloneRun(t, agent, "-mode=backtest", "-pair=XAUUSDm;XAGUSDm", "-print-warmup", filter)
		assert.ErrorContains(t, err, "-strategy pairs trades two legs as one position", filter)
	}
	out, err := standaloneRun(t, agent, "-mode=backtest", "-pair=XAUUSDm;XAGUSDm", "-print-warmup")
	require.NoError(t, err)
	assert.Contains(t, out, `"candles":250`)
}

// A pair of -intermarket-pairs the agent doesn't trade, a typo, is refused
// rather than filtering none of its pairs.
func TestRun_IntermarketPairsOfTheAgentOnly(t *testing.T) {
	agent := fixedAgent(map[string]strategy.Strategy{"plain": &fixed{timeframe: "15m", warmup: 30}}, "plain")
	_, err := standaloneRun(t, agent, "-mode=backtest", "-pair=XAUUSDm;BTCUSDm", "-print-warmup",
		"-intermarket-symbol=DXYm", "-intermarket-pairs=XAUUSD")
	assert.ErrorContains(t, err, `-intermarket-pairs: "XAUUSD" isn't a pair of -pair (XAUUSDm;BTCUSDm)`)
	_, err = standaloneRun(t, agent, "-mode=backtest", "-pair=XAUUSDm;BTCUSDm", "-print-warmup",
		"-intermarket-symbol=DXYm", "-intermarket-pairs=XAUUSDm")
	assert.NoError(t, err)
}

// A backtest of -csv files refuses a strategy trading around the releases of
// the calendar, which only the backtests of the controller and the agents it
// launches have: it would never enter.
func TestRun_ReleasesRefusedInABacktestOfFiles(t *testing.T) {
	agent := fixedAgent(map[string]strategy.Strategy{"news": &newsFixed{fixed: fixed{timeframe: "5m", warmup: 5}}},
		"news")
	_, err := standaloneRun(t, agent, "-mode=backtest", "-pair=XAUUSDm", "-csv=x.csv")
	assert.ErrorContains(t, err, "-strategy news trades around the releases of the economic calendar")
	assert.ErrorContains(t, err, "-backtest-config")
}

// backtestStart is the first candle of the backtests of the runner tests.
var backtestStart = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// backtestFiles writes minutes candles of 1m from backtestStart going up and
// down around 100, and the config of a backtest of them as the controller
// writes it, changed by change: the paths of the candles and of the config,
// and the config.
func backtestFiles(t *testing.T, minutes int, change func(*agentapi.BacktestConfig)) (string, string,
	agentapi.BacktestConfig) {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("time,open,close,low,high,volume\n")
	for i := range minutes {
		p := 100 + float64(i%2)
		fmt.Fprintf(&b, "%d,%g,%g,%g,%g,1\n", backtestStart.Add(time.Duration(i)*time.Minute).Unix(), p, p, p-0.5,
			p+0.5)
	}
	candles := filepath.Join(dir, "candles.csv")
	require.NoError(t, os.WriteFile(candles, []byte(b.String()), 0o600))
	cfg := agentapi.BacktestConfig{From: backtestStart.Unix(),
		To: backtestStart.Add(time.Duration(minutes) * time.Minute).Unix(), Balance: 10000, Leverage: 1,
		Report:  filepath.Join(dir, "report.json"),
		Symbols: map[string]agentapi.BacktestSymbol{"XAUUSDm": {Point: 0.01}}}
	if change != nil {
		change(&cfg)
	}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	config := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(config, data, 0o600))
	return candles, config, cfg
}

// readReport reads the report of a backtest of cfg.
func readReport(t *testing.T, cfg agentapi.BacktestConfig) agentapi.BacktestReport {
	t.Helper()
	data, err := os.ReadFile(cfg.Report)
	require.NoError(t, err)
	var report agentapi.BacktestReport
	require.NoError(t, json.Unmarshal(data, &report))
	return report
}

// The backtest of the pairs strategy ignores the protection of the batch (its
// legs carry no stop, the protection would close one alone), telling it in
// the report; another strategy keeps it.
func TestRun_PairsBacktestIgnoresTheProtection(t *testing.T) {
	protection := &ui.ProtectionSettings{Unit: ui.ProtectionUnitATR,
		Breakeven: ui.BreakevenSettings{Enabled: true, Trigger: 1}}
	for _, name := range []string{"pairs", "plain"} {
		agent := fixedAgent(map[string]strategy.Strategy{name: &fixed{timeframe: "1m", warmup: 1}}, name)
		candles, config, cfg := backtestFiles(t, 60, func(c *agentapi.BacktestConfig) { c.Protection = protection })
		_, err := standaloneRun(t, agent, "-mode=backtest", "-pair=XAUUSDm", "-csv="+candles, "-csv-timeframe=1m",
			"-backtest-config="+config)
		require.NoError(t, err, name)
		report := readReport(t, cfg)
		require.Equal(t, agentapi.BacktestStatusSucceeded, report.Status, report.Error)
		warnings := strings.Join(report.Warnings, "\n")
		if name == "pairs" {
			assert.Nil(t, report.Assumptions.Protection, "ignored")
			assert.Contains(t, warnings, "a proteção do lote (breakeven e trailing stop) foi ignorada")
			continue
		}
		assert.Equal(t, protection, report.Assumptions.Protection, "kept")
		assert.NotContains(t, warnings, "proteção do lote")
	}
}

// attempt is a strategy of the tests attempting a buy with its stop loss and
// take profit at the market on the candle opened at at.
type attempt struct {
	fixed
	at time.Time
}

func (a *attempt) OnCandle(df *model.Dataframe, broker service.Broker) {
	if df.Time[len(df.Time)-1].Equal(a.at) {
		price := df.Close.Last(0)
		_, _ = broker.(service.ProtectionBroker).CreateOrderMarketProtected(model.SideTypeBuy, df.Pair, 0.01,
			price-5, price+5)
	}
}

// The samples of -meta-record in a backtest of the controller tell where they
// were recorded: a backtest, its run and its segment.
func TestRun_BacktestSamplesTellTheirRun(t *testing.T) {
	from := backtestStart.Add(time.Duration(metalabel.Window+10) * time.Minute)
	agent := fixedAgent(map[string]strategy.Strategy{"plain": &attempt{fixed: fixed{timeframe: "1m", warmup: 1},
		at: from.Add(5 * time.Minute)}}, "plain")
	candles, config, cfg := backtestFiles(t, metalabel.Window+40, func(c *agentapi.BacktestConfig) {
		c.From, c.Agent, c.Run, c.Segment = from.Unix(), "gold", 77, metalabel.SegmentValidation
	})
	record := filepath.Join(t.TempDir(), "samples.jsonl")
	_, err := standaloneRun(t, agent, "-mode=backtest", "-pair=XAUUSDm", "-csv="+candles, "-csv-timeframe=1m",
		"-backtest-config="+config, "-meta-record="+record, "-meta-max-bars=3")
	require.NoError(t, err)
	require.Equal(t, agentapi.BacktestStatusSucceeded, readReport(t, cfg).Status)
	file, err := os.Open(record)
	require.NoError(t, err)
	defer func() { _ = file.Close() }()
	samples, err := metalabel.ReadSamples(file)
	require.NoError(t, err)
	require.Len(t, samples, 1)
	got := samples[0]
	assert.Equal(t, [5]any{"gold", "plain", metalabel.SourceBacktest, int64(77), metalabel.SegmentValidation},
		[5]any{got.Agent, got.Strategy, got.Source, got.Run, got.Segment})
	assert.Equal(t, from.Add(5*time.Minute).Unix(), got.Time)
}

// A backtest whose entries a meta-labeling model filters is refused on a
// period starting before the newest outcome the model learned: in-sample; on
// a period after it, it runs.
func TestRun_BacktestOfAModelInSampleRefused(t *testing.T) {
	agent := fixedAgent(map[string]strategy.Strategy{"plain": &fixed{timeframe: "1m", warmup: 1}}, "plain")
	candles, config, cfg := backtestFiles(t, 60, nil)
	path := filepath.Join(t.TempDir(), "model.json")
	model := &metalabel.Model{Version: metalabel.ModelVersion, Features: []string{"side"}, Mean: []float64{0},
		Std: []float64{1}, Weights: []float64{1}, Threshold: 0.5, TrainedThrough: cfg.From + 60}
	require.NoError(t, model.Save(path))
	args := []string{"-mode=backtest", "-pair=XAUUSDm", "-csv=" + candles, "-csv-timeframe=1m",
		"-backtest-config=" + config, "-meta-model=" + path}
	_, err := standaloneRun(t, agent, args...)
	require.ErrorContains(t, err, "the period would be in-sample")
	assert.NoFileExists(t, cfg.Report, "never run")

	model.TrainedThrough = cfg.From
	require.NoError(t, model.Save(path))
	_, err = standaloneRun(t, agent, args...)
	require.NoError(t, err)
	assert.Equal(t, agentapi.BacktestStatusSucceeded, readReport(t, cfg).Status)
}

// The breakeven and the trailing stop of the agent stay off for the pairs
// strategy in paper and live modes, its legs carrying no stop; on for another.
func TestAttachProtection_NoneForPairs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 1000))
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	s := &fixed{timeframe: "1m", warmup: 1}
	bot, err := xtrader.NewBot(ctx, xtrader.Settings{Pairs: []string{"BTCUSDT"}}, wallet, s,
		xtrader.WithStorage(memory), xtrader.WithPaperWallet(wallet))
	require.NoError(t, err)
	assert.Nil(t, attachProtection(ctx, bot, wallet, s, "pairs", false, &remote.Agent{}, nil))
	assert.NotNil(t, attachProtection(ctx, bot, wallet, s, "emacross", false, &remote.Agent{}, nil))
}
