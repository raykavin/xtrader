package backtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/raykavin/xtrader/agentapi"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/xhit/go-str2duration/v2"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/agents/internal/protect"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
)

// maxWarnings bounds the warnings of a report of the same kind, eg: the
// orders refused, the rest counted.
const maxWarnings = 20

// Hooks are what a strategy adds to its backtest: options of the bot (eg:
// the decisions of an LLM); Prepare, told the feed before the run (eg: the
// clock of an LLM); Candles, told before the run what reads the candles closed
// by the simulated now of any symbol of the run, the references included (see
// CandleSource), eg: the reference of an intermarket filter; Extra, the
// extra of the report and a status replacing the one of the run (eg:
// StatusBudgetExhausted), "" for none; and Warnings, the warnings of the
// report the agent adds, eg: a setting of the config it ignores.
type Hooks struct {
	Options  []xtrader.Option
	Prepare  func(feed *exchange.BacktestFeed)
	Candles  func(source CandleSource)
	Extra    func() (json.RawMessage, string)
	Warnings []string
	// Gate, when set, takes the assets of the window of the trading hours
	// open, as the controller tells them live (see agentapi.PairGate)
	Gate *agentapi.PairGate
}

// CandleSource reads the last candles of a symbol, complete ones only: in a
// backtest, the ones closed by its simulated now.
type CandleSource interface {
	CandlesByLimit(ctx context.Context, pair, period string, limit int) ([]model.Candle, error)
}

// Run runs strat over files, a CSV of the candles of each pair of settings in
// their order, of fileTimeframe (the strategy's when empty), as cfg says (see
// the package and Config), and writes the report of cfg: whatever the
// outcome, a failure in it too. The error returned is the one of the run;
// nil for a run canceled, the report saying so.
func Run(ctx context.Context, settings xtrader.Settings, strat strategy.Strategy, files []string,
	fileTimeframe string, cfg Config, hooks Hooks) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if fileTimeframe == "" {
		fileTimeframe = strat.Timeframe()
	}
	_, quote := exchange.SplitAssetQuote(settings.Pairs[0])
	assumptions := cfg
	assumptions.Releases = nil // the data of the run, not an assumption: too many to repeat
	report := Report{Status: StatusFailed, Currency: quote, Pairs: settings.Pairs, Timeframe: strat.Timeframe(),
		From: cfg.From, To: cfg.To, Assumptions: assumptions, Trades: []Trade{}, Open: []OpenPosition{},
		Equity: []Point{}, Warnings: append([]string{}, hooks.Warnings...)}
	err := run(ctx, settings, strat, files, fileTimeframe, cfg, hooks, &report)
	switch {
	case errors.Is(err, xtrader.ErrBacktestCanceled) || (err != nil && ctx.Err() != nil):
		report.Status, err = StatusCanceled, nil
	case err != nil:
		report.Error = err.Error()
	}
	if hooks.Extra != nil {
		extra, status := hooks.Extra()
		report.Extra = extra
		if status != "" && report.Status != StatusFailed {
			report.Status = status
		}
	}
	if writeErr := writeReport(report, cfg.Report); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return err
}

// run runs the backtest into report, its status succeeded when it ran to the
// end.
func run(ctx context.Context, settings xtrader.Settings, strat strategy.Strategy, files []string,
	fileTimeframe string, cfg Config, hooks Hooks, report *Report) error {
	if len(files) != len(settings.Pairs) {
		return fmt.Errorf("backtest: a candles CSV per pair is required: %d files for %d pairs", len(files),
			len(settings.Pairs))
	}
	for _, pair := range settings.Pairs[1:] {
		if _, other := exchange.SplitAssetQuote(pair); other != report.Currency {
			return fmt.Errorf("backtest: the pairs must share a quote currency: %s is quoted in %s, %s in %s", pair,
				other, settings.Pairs[0], report.Currency)
		}
	}
	fileLength, err := str2duration.ParseDuration(fileTimeframe)
	if err != nil {
		return fmt.Errorf("backtest: the timeframe of the files %q: %w", fileTimeframe, err)
	}
	length, err := str2duration.ParseDuration(strat.Timeframe())
	if err != nil {
		return fmt.Errorf("backtest: the timeframe %q: %w", strat.Timeframe(), err)
	}

	feeds := make([]exchange.PairFeed, 0, len(files)+len(cfg.References))
	for i, file := range files {
		feeds = append(feeds, exchange.PairFeed{Pair: settings.Pairs[i], File: file, Timeframe: fileTimeframe})
	}
	// the references, read by the strategy, never subscribed nor traded (the
	// bot subscribes the pairs of settings only), their candles of the same
	// timeframe as the pairs, trimmed at the end of the period with them
	for _, symbol := range slices.Sorted(maps.Keys(cfg.References)) {
		if slices.Contains(settings.Pairs, symbol) {
			continue // traded: its own file already
		}
		feeds = append(feeds, exchange.PairFeed{Pair: symbol, File: cfg.References[symbol], Timeframe: fileTimeframe})
	}
	csvFeed, err := exchange.NewCSVFeed(strat.Timeframe(), feeds...)
	if err != nil {
		return fmt.Errorf("backtest: the candles: %w", err)
	}
	// the period only: nothing after it is ever handed
	for key, candles := range csvFeed.CandlePairTimeFrame {
		csvFeed.CandlePairTimeFrame[key] = slices.DeleteFunc(slices.Clone(candles), func(c model.Candle) bool {
			return !c.Time.Before(to(cfg))
		})
	}
	report.Warnings = append(report.Warnings, coverage(csvFeed, settings.Pairs, fileTimeframe, strat, cfg,
		length, report)...)

	feed := exchange.NewBacktestFeed(csvFeed)
	if hooks.Prepare != nil {
		hooks.Prepare(feed)
	}
	if hooks.Candles != nil {
		hooks.Candles(closedCandles{feed: feed, csv: csvFeed})
	}
	point := func(pair string) float64 { return cfg.Symbols[pair].Point }
	spreadPoints := func(pair string) float64 {
		if s := cfg.Symbols[pair].SpreadPoint; s > 0 {
			return s
		}
		return cfg.SpreadPoints
	}
	for _, pair := range settings.Pairs {
		if point(pair) <= 0 && (cfg.SlippagePoints > 0 || spreadPoints(pair) > 0) {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: sem o ponto do símbolo, o spread fixo e a "+
				"slippage não puderam ser aplicados", pair))
		}
	}
	execution := exchange.PaperExecution{
		Spread: func(c model.Candle) float64 {
			if spread, ok := c.Metadata["spread"]; ok && spread > 0 {
				return spread * point(c.Pair)
			}
			return spreadPoints(c.Pair) * point(c.Pair)
		},
		Slippage:     func(c model.Candle) float64 { return cfg.SlippagePoints * point(c.Pair) },
		GapFills:     cfg.GapFills,
		Conservative: cfg.Conservative,
	}
	paper := exchange.NewPaperWallet(ctx, report.Currency,
		exchange.WithPaperAsset(report.Currency, cfg.Balance*cfg.Leverage),
		exchange.WithPaperFee(cfg.Fee, cfg.Fee),
		exchange.WithDataFeed(feed),
		exchange.WithPaperExecution(execution))
	w := &wallet{PaperWallet: paper, balance: cfg.Balance, leverage: cfg.Leverage, pairs: settings.Pairs,
		symbols: cfg.Symbols}

	store, err := storage.FromMemory()
	if err != nil {
		return err
	}
	var g *guard // set before the run, see newGuard
	progress := progressPrinter(cfg.Progress)
	options := append([]xtrader.Option{
		xtrader.WithBacktest(paper),
		xtrader.WithStorage(store),
		xtrader.WithLogLevel(log.WarnLevel),
		xtrader.WithBacktestClock(func(at time.Time) {
			now := at.Add(fileLength)
			feed.SetNow(now)
			if g != nil {
				g.tick(now)
			}
		}),
		xtrader.WithBacktestProgress(func(done, total int) {
			report.Processed = done
			progress(done, total)
		}),
	}, hooks.Options...)
	bot, err := xtrader.NewBot(ctx, settings, w, strat, options...)
	if err != nil {
		return err
	}

	var protection *protect.Manager
	if cfg.Protection != nil && cfg.Protection.Active() {
		protection = protect.New(ctx, bot.Controller(), paper, strat.Timeframe(), "backtest")
		protection.Synchronous(feed.Now)
		protection.Apply(cfg.Protection, nil)
		bot.Controller().SetStopGuard(protection.Guarded)
	}
	g, err = newGuard(cfg, bot.Controller(), store, w, feed, protection)
	if g != nil {
		g.gate = hooks.Gate
	}
	if err != nil {
		return err
	}
	bot.SetCandleHook(g)

	runErr := bot.Run(ctx)

	// the report, whatever the end of the run
	orders, err := store.Orders()
	if err != nil {
		return errors.Join(runErr, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	closed, held := trades(orders, from(cfg), g.own)
	report.Trades = append(report.Trades, closed...)
	report.Open = g.openPositions(held, func(pair string) float64 { return spreadPoints(pair) * point(pair) },
		func(pair string) float64 { return cfg.SlippagePoints * point(pair) }, cfg.Fee)
	report.Equity = downsample(g.equity)
	report.Warnings = append(report.Warnings, g.warnings...)
	report.Warnings = append(report.Warnings, refusals(w.refused)...)
	s := &report.Summary
	s.InitialBalance, s.FinalEquity, s.Trades, s.Refused = cfg.Balance, g.lastEquity, len(closed), len(w.refused)
	for _, t := range closed {
		s.Realized += t.Net
		s.Gross += t.Gross
		s.Fees += t.Fees
	}
	for _, p := range report.Open {
		s.OpenProfit += p.Profit
	}
	s.MaxDrawdown, s.MaxDrawdownPct = drawdown(append([]Point{{Time: cfg.From, Value: cfg.Balance}}, g.equity...))
	if runErr == nil {
		report.Status = StatusSucceeded
	}
	return runErr
}

// coverage returns the warnings of the candles read: the period of the data,
// a warm-up shorter than the strategy needs, an end short of the period;
// into report the bounds of the data and its count.
func coverage(feed *exchange.CSVFeed, pairs []string, fileTimeframe string, strat strategy.Strategy, cfg Config,
	length time.Duration, report *Report) []string {
	var warnings []string
	for _, pair := range pairs {
		candles := feed.CandlePairTimeFrame[fmt.Sprintf("%s--%s", pair, strat.Timeframe())]
		if len(candles) == 0 {
			warnings = append(warnings, fmt.Sprintf("%s: nenhum candle no período", pair))
			continue
		}
		first, last := candles[0].Time, candles[len(candles)-1].Time
		if report.DataFrom == 0 || first.Unix() < report.DataFrom {
			report.DataFrom = first.Unix()
		}
		if last.Unix() > report.DataTo {
			report.DataTo = last.Unix()
		}
		report.Candles += len(feed.CandlePairTimeFrame[fmt.Sprintf("%s--%s", pair, fileTimeframe)])
		// the candles of the timeframe of the strategy before the period, as
		// the engine hands them to it (the file may be of a finer one)
		warm := 0
		var bucket time.Time
		for i, c := range candles {
			if !c.Time.Before(from(cfg)) {
				break
			}
			if at := c.Time.Truncate(length); i == 0 || !at.Equal(bucket) {
				bucket = at
				warm++
			}
		}
		if warm < strat.WarmupPeriod() {
			warnings = append(warnings, fmt.Sprintf("%s: aquecimento incompleto, %d de %d candles antes do início: "+
				"os indicadores começam o período sem todo o histórico que pedem", pair, warm, strat.WarmupPeriod()))
		}
		if end := last.Add(length); end.Before(to(cfg).Add(-length)) {
			warnings = append(warnings, fmt.Sprintf("%s: os candles terminam em %s, antes do fim do período", pair,
				end.UTC().Format(time.RFC3339)))
		}
	}
	return warnings
}

// refusals returns the warnings of the orders refused: maxWarnings of them,
// the rest counted.
func refusals(refused []string) []string {
	if len(refused) <= maxWarnings {
		return refused
	}
	return append(slices.Clone(refused[:maxWarnings]), fmt.Sprintf("e mais %d ordens recusadas",
		len(refused)-maxWarnings))
}

// progressPrinter returns what prints the progress on the standard error when
// on: PROGRESS done total, about every 0.5% of the candles, and the last one.
func progressPrinter(on bool) func(done, total int) {
	if !on {
		return func(int, int) {}
	}
	last := -1
	return func(done, total int) {
		step := max(total/200, 1)
		if done/step == last && done != total {
			return
		}
		last = done / step
		fmt.Fprintf(os.Stderr, "PROGRESS %d %d\n", done, total)
	}
}
