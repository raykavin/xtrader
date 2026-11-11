package controller

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The runs of the backtests.
const (
	backtestTick          = 5 * time.Second  // between two looks for runs queued, besides the wakes
	backtestProgressGap   = 2 * time.Second  // between two records of the progress of a run
	backtestStopWait      = 15 * time.Second // an interrupted run has before it is killed
	backtestErrLines      = 30               // the last lines of the error output kept, the error of a run failed
	backtestWarmupCandles = 1000             // candles of the timeframe of the agent read before the period, by default
	backtestWarmupMax     = 60 * 24 * time.Hour
	backtestWarmupCap     = 365 * 24 * time.Hour // the most read before the period for the warmup of a strategy
	backtestWarmupProbe   = 2 * time.Minute      // the agent has to tell the warmup of its strategy
	backtestBatchesShown  = 30
	backtestReleasesRead  = 5 * time.Minute // the releases of the calendar of a run may take, a call a day
)

// backtestFlagsDropped are the flags of an agent a backtest never passes:
// the ones of its account and of the controller (a backtest reaches no
// broker nor controller), and the ones the backtest sets itself.
var backtestFlagsDropped = []string{"mode", "api", "trade-panel", "telegram-control", "reanalysis", "mt5-bridge",
	"mt5-magic", "mt5-comment", "mt5-live", "mt5-token", "mt5-poll", "outbox-quarantine", "csv", "csv-timeframe",
	"backtest-config", "mt5-contract-size", "fee", "calendar-mcp"}

// backtestLLMFlagsDropped are the flags of an LLM agent its backtests never
// pass: the calendar and the blocks of the news (their forecasts and revisions
// exist after the releases only), and the filters of the trends of the higher
// timeframes and the correlated markets, which a backtest doesn't send.
var backtestLLMFlagsDropped = []string{"calendar-currencies", "calendar-ahead", "calendar-back",
	"calendar-importance", "calendar-server-offset", "news-block-before", "news-block-after",
	"news-block-importance", "llm-higher-trend-filter", "llm-higher-trend-required", "llm-correlated",
	"prompt-path"}

// backtestTimeframesAllowed are the timeframes a batch may replace the ones
// of the agents with.
var backtestTimeframesAllowed = []string{"1m", "5m", "15m", "30m", "1h", "4h", "1d"}

// backtestSnapshot is the config a run ran with, copied as the batch was
// asked (see store.BacktestRun.Config): the agent, what it trades, its flags
// (never a secret: see agentapi.Config.Args) and the command of its kind with
// the hash of its program, its prompt, its protection, the bridge of its
// candles, and the assumptions of the batch.
type backtestSnapshot struct {
	Agent       string                 `json:"agent"`
	Kind        string                 `json:"kind"`
	Mode        string                 `json:"mode"`
	Strategy    string                 `json:"strategy,omitempty"`
	Pairs       []string               `json:"pairs"`
	Timeframe   string                 `json:"timeframe,omitempty"`
	Args        []string               `json:"args"`
	Command     []string               `json:"command"`
	Code        string                 `json:"code"`
	Prompt      *backtestPrompt        `json:"prompt,omitempty"`
	Protection  *ui.ProtectionSettings `json:"protection,omitempty"`
	Bridge      string                 `json:"bridge"`
	LLM         *backtestLLM           `json:"llm,omitempty"`
	Assumptions ui.BacktestAssumptions `json:"assumptions"`
	Notes       []string               `json:"notes,omitempty"`
}

// backtestPrompt is the prompt of an LLM agent a run ran with: its reference,
// the hash of its content and the content.
type backtestPrompt struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Version int    `json:"version"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

// backtestLLM is the model of an LLM agent a run ran with, and its ceilings.
type backtestLLM struct {
	Provider string           `json:"provider"`
	Model    string           `json:"model"`
	MaxCalls int              `json:"max_calls"`
	MaxCost  float64          `json:"max_cost"`
	Prices   *config.LLMPrice `json:"prices,omitempty"`
}

// Backtests runs the backtests asked on the dashboard (see GET and POST
// /api/backtests), the runs queued in the store, concurrency at once: each a
// process of the program of the kind of its agent in backtest mode, apart
// from the agents trading (see agents/internal/backtest), on candles read at
// the bridge of the agent; never a broker, never a change to the agents.
type Backtests struct {
	store   *store.Store
	agents  config.Agents
	cfg     config.Backtest
	dir     string
	candles *candleCache
	wake    chan struct{}

	mu        sync.Mutex
	running   map[int64]context.CancelFunc
	canceled  map[int64]bool              // the runs canceled by a user, apart from a stop of the controller
	calendars map[string]*llm.MCPCalendar // the calendars of the news read, by the URL of their MCP server
	wg        sync.WaitGroup
}

// ServeBacktests serves the backtests on the dashboard, see Backtests: Run
// runs them.
func (d *Dashboard) ServeBacktests(s *store.Store, agents config.Agents, cfg config.Backtest) *Backtests {
	dir := cfg.Dir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(agents.Dir, dir)
	}
	b := &Backtests{store: s, agents: agents, cfg: cfg, dir: dir, candles: newCandleCache(dir),
		wake: make(chan struct{}, 1), running: make(map[int64]context.CancelFunc), canceled: make(map[int64]bool),
		calendars: make(map[string]*llm.MCPCalendar)}
	d.backtests = b
	return b
}

// Wake makes the runner look for runs queued now.
func (b *Backtests) Wake() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// Run runs the runs queued until ctx is done: the runs left running by a
// controller stopped meanwhile told interrupted first; at the end, the runs
// in progress interrupted, and waited for.
func (b *Backtests) Run(ctx context.Context) {
	if n, err := b.store.InterruptRunningBacktestRuns(context.WithoutCancel(ctx),
		"o controller parou durante a execução"); err != nil {
		log.Errorf("backtests: %v", err)
	} else if n > 0 {
		log.Warnf("backtests: %d runs left running by a controller stopped, interrupted", n)
	}
	ticker := time.NewTicker(backtestTick)
	defer ticker.Stop()
	for {
		b.fill(ctx)
		select {
		case <-ctx.Done():
			b.mu.Lock()
			for _, cancel := range b.running {
				cancel()
			}
			b.mu.Unlock()
			b.wg.Wait()
			return
		case <-ticker.C:
		case <-b.wake:
		}
	}
}

// fill starts the runs queued while fewer than the concurrency run.
func (b *Backtests) fill(ctx context.Context) {
	for ctx.Err() == nil {
		b.mu.Lock()
		busy := len(b.running)
		b.mu.Unlock()
		if busy >= b.cfg.Concurrency {
			return
		}
		run, ok, err := b.store.ClaimBacktestRun(ctx)
		if err != nil {
			log.Errorf("backtests: %v", err)
			return
		}
		if !ok {
			return
		}
		runCtx, cancel := context.WithCancel(ctx)
		b.mu.Lock()
		b.running[run.ID] = cancel
		b.mu.Unlock()
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			defer func() {
				cancel()
				b.mu.Lock()
				delete(b.running, run.ID)
				delete(b.canceled, run.ID)
				b.mu.Unlock()
				b.Wake()
			}()
			b.execute(runCtx, run)
		}()
	}
}

// Cancel cancels the run of id: queued, at once; running, its process
// interrupted (its report partial, canceled). False for a run ended.
func (b *Backtests) Cancel(ctx context.Context, id int64) (bool, error) {
	if ok, err := b.store.CancelQueuedBacktestRun(ctx, id); err != nil || ok {
		return ok, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	cancel, ok := b.running[id]
	if !ok {
		return false, nil
	}
	b.canceled[id] = true
	cancel()
	return true, nil
}

// execute runs run and records its end, see runProcess; a panic recorded as
// its failure.
func (b *Backtests) execute(ctx context.Context, run store.BacktestRun) {
	status, why, summary, report := store.BacktestFailed, "", json.RawMessage(nil), json.RawMessage(nil)
	defer func() {
		if r := recover(); r != nil {
			status, why = store.BacktestFailed, fmt.Sprintf("erro interno: %v", r)
			log.Errorf("backtests: run %d: %v", run.ID, r)
		}
		if err := b.store.FinishBacktestRun(context.WithoutCancel(ctx), run.ID, status, why, summary,
			report); err != nil {
			log.Errorf("backtests: run %d: %v", run.ID, err)
		}
	}()
	status, why, summary, report = b.runProcess(ctx, run)
	log.Infof("backtests: run %d of %s (%s) ended: %s %s", run.ID, run.Agent, run.Segment, status, why)
}

// runProcess runs run: its candles read (see candleCache), its config
// written, the program of its kind run in backtest mode on them, its
// progress recorded; its status, error, summary and report returned.
func (b *Backtests) runProcess(ctx context.Context, run store.BacktestRun) (string, string, json.RawMessage,
	json.RawMessage) {
	failed := func(format string, args ...any) (string, string, json.RawMessage, json.RawMessage) {
		if ctx.Err() != nil {
			return b.stopped(run.ID), "interrompido antes de terminar", nil, nil
		}
		return store.BacktestFailed, fmt.Sprintf(format, args...), nil, nil
	}
	var snap backtestSnapshot
	if err := json.Unmarshal(run.Config, &snap); err != nil {
		return failed("a configuração da execução não pôde ser lida: %v", err)
	}
	agent, err := b.store.AgentByName(ctx, snap.Agent)
	if err != nil {
		return failed("o agente %s não existe mais: %v", snap.Agent, err)
	}
	secrets, err := b.store.SecretsOf(agent)
	if err != nil {
		return failed("os segredos do agente: %v", err)
	}

	dir := filepath.Join(b.dir, "runs", strconv.FormatInt(run.ID, 10))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return failed("o diretório da execução: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	warm, told, note := b.warmupOf(ctx, snap, backtestEnv(b.agents, snap, secrets))
	dataFrom := run.From.Add(-warm)
	// the finest timeframe of the period, else a coarser one when the broker
	// keeps the finer candles of a pair for less time than the period, eg: M1
	// for a few months only
	file, _, _ := fileTimeframeOf(run.To.Sub(dataFrom))
	start := slices.IndexFunc(backtestTimeframes, func(tf backtestTimeframe) bool { return tf.file == file })
	var files, contracts, warnings []string
	var symbols map[string]agentapi.BacktestSymbol
	var specs map[string]bridgeSymbol
	var gaps int
	var short string // the pair whose candles start after the period, and when, at the last timeframe tried
	var chosen backtestTimeframe
	for _, tf := range backtestTimeframes[start:] {
		files, contracts, warnings, short, gaps = nil, nil, nil, "", 0
		symbols, specs = make(map[string]agentapi.BacktestSymbol), make(map[string]bridgeSymbol)
		for _, pair := range snap.Pairs {
			data, err := b.candles.read(ctx, snap.Bridge, secrets.BridgeToken, pair, tf.file, tf.mt5, tf.length,
				dataFrom, run.To)
			if err != nil {
				return failed("os candles de %s não puderam ser lidos na ponte: %v", pair, err)
			}
			if data.First.After(run.From) {
				short = fmt.Sprintf("os candles de %s começam em %s", pair, data.First.Format(time.DateOnly))
				break
			}
			files = append(files, data.File)
			contract := max(data.Symbol.ContractSize, 1)
			contracts = append(contracts, strconv.FormatFloat(contract, 'f', -1, 64))
			symbols[pair] = agentapi.BacktestSymbol{Point: data.Symbol.Point,
				VolumeStep: data.Symbol.VolumeStep * contract, VolumeMin: data.Symbol.VolumeMin * contract}
			specs[pair] = data.Symbol
			warnings = append(warnings, data.Gaps...)
			gaps += len(data.Gaps)
		}
		if short == "" {
			if tf.file != file {
				warnings = append([]string{fmt.Sprintf("execução em %s: a corretora não tem candles de %s de todo "+
					"o período", tf.mt5, backtestTimeframes[start].mt5)}, warnings...)
			}
			file, chosen = tf.file, tf
			break
		}
	}
	if short != "" {
		return failed("dados insuficientes: %s, depois do início do período", short)
	}
	references, referenceWarnings := b.referencesOf(ctx, snap, told.References, secrets.BridgeToken, chosen,
		dataFrom, run)
	warnings = append(warnings, referenceWarnings...)
	var releases map[string][]agentapi.BacktestRelease
	if told.Releases {
		var warning string
		releases, warning = b.releasesOf(ctx, snap.Pairs, specs, dataFrom, run.To)
		warnings = append(warnings, warning)
	}

	a := snap.Assumptions
	cfg := agentapi.BacktestConfig{From: run.From.Unix(), To: run.To.Unix(), Balance: a.Balance, Leverage: 1,
		Fee: a.Fee, SlippagePoints: a.SlippagePoints, SpreadPoints: a.SpreadPoints, Conservative: a.Conservative,
		GapFills: a.GapFills, Symbols: symbols, Protection: snap.Protection, DailyLoss: a.DailyLoss,
		DailyGain: a.DailyGain, Hours: a.Hours, Report: filepath.Join(dir, "report.json"), Progress: true,
		Agent: snap.Agent, Run: run.ID, Segment: run.Segment, References: references, Releases: releases}
	var extra []string
	if snap.LLM != nil {
		cfg.LLMMaxCalls, cfg.LLMMaxCost = snap.LLM.MaxCalls, snap.LLM.MaxCost
		if snap.LLM.Prices != nil {
			cfg.LLMPriceInput, cfg.LLMPriceOutput = snap.LLM.Prices.Input, snap.LLM.Prices.Output
		} else {
			cfg.LLMMaxCost = 0 // unknown without the price: the calls bound the run
		}
		if snap.Prompt != nil {
			promptFile := filepath.Join(dir, "prompt.txt")
			if err := os.WriteFile(promptFile, []byte(snap.Prompt.Content), 0o640); err != nil {
				return failed("o prompt da execução: %v", err)
			}
			extra = append(extra, "-prompt-path="+promptFile)
		}
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return failed("a configuração do backtest: %v", err)
	}
	configFile := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configFile, data, 0o640); err != nil {
		return failed("a configuração do backtest: %v", err)
	}

	args := append(slices.Clone(snap.Command[1:]), snap.Args...)
	args = append(args, "-mode=backtest", "-pair="+strings.Join(snap.Pairs, ";"),
		"-csv="+strings.Join(files, ";"), "-csv-timeframe="+file,
		"-mt5-contract-size="+strings.Join(contracts, ";"), "-backtest-config="+configFile)
	args = append(args, extra...)
	env := backtestEnv(b.agents, snap, secrets)
	errLines, err := b.process(ctx, run.ID, snap.Command[0], args, env)

	reportData, readErr := os.ReadFile(cfg.Report)
	if readErr != nil {
		if ctx.Err() != nil {
			return b.stopped(run.ID), "interrompido antes de terminar", nil, nil
		}
		why := strings.Join(errLines, "\n")
		if err != nil {
			why = strings.TrimSpace(why + "\n" + err.Error())
		}
		return store.BacktestFailed, "o backtest terminou sem relatório: " + why, nil, nil
	}
	var report agentapi.BacktestReport
	if err := json.Unmarshal(reportData, &report); err != nil {
		return store.BacktestFailed, fmt.Sprintf("o relatório do backtest não pôde ser lido: %v", err), nil, nil
	}
	if note != "" {
		warnings = append(warnings, note)
	}
	report.Warnings = append(warnings, report.Warnings...)
	summary := summarizeBacktest(report, gaps, snap.LLM)
	summary.ExecutionTimeframe = file
	summaryData, _ := json.Marshal(summary)
	reportData, _ = json.Marshal(report)

	status := map[string]string{agentapi.BacktestStatusSucceeded: store.BacktestSucceeded,
		agentapi.BacktestStatusFailed:          store.BacktestFailed,
		agentapi.BacktestStatusBudgetExhausted: store.BacktestBudgetExhausted}[report.Status]
	switch {
	case report.Status == agentapi.BacktestStatusCanceled || (ctx.Err() != nil && status != store.BacktestSucceeded):
		status = b.stopped(run.ID)
	case status == "":
		status = store.BacktestFailed
	}
	why := report.Error
	if status == store.BacktestFailed && why == "" {
		why = strings.Join(errLines, "\n")
	}
	return status, why, summaryData, reportData
}

// warmupOf returns how long before the period of a run of snap its candles
// are read: the warmup the strategy of the agent asks for, as the agent tells
// it (see agentapi.PrintWarmupFlag), its candles over the calendar (the
// markets closed at the weekends and an hour a day: half of it more, and a
// day), the default at least (backtestWarmupCandles of the timeframe of the
// agent, backtestWarmupMax at most), backtestWarmupCap at most; the default
// for an LLM agent (its warmup set by its prompt, the candles it keeps) or one
// that can't tell it, and why then. It returns what the agent told too: the
// symbols its strategy reads but doesn't trade, and whether it trades around
// the releases of the calendar.
func (b *Backtests) warmupOf(ctx context.Context, snap backtestSnapshot, env []string) (time.Duration,
	agentapi.Warmup, string) {
	warm := min(time.Duration(backtestWarmupCandles)*timeframeLength(snap.Timeframe), backtestWarmupMax)
	if snap.LLM != nil || len(snap.Command) == 0 {
		return warm, agentapi.Warmup{}, ""
	}
	ctx, cancel := context.WithTimeout(ctx, backtestWarmupProbe)
	defer cancel()
	args := append(slices.Clone(snap.Command[1:]), snap.Args...)
	args = append(args, "-mode=backtest", "-pair="+strings.Join(snap.Pairs, ";"), "-"+agentapi.PrintWarmupFlag)
	cmd := exec.CommandContext(ctx, snap.Command[0], args...)
	cmd.Dir, cmd.Env = b.agents.Dir, env
	out, err := cmd.Output()
	var told agentapi.Warmup
	if err == nil {
		err = json.Unmarshal(lastLine(out), &told)
	}
	length := timeframeLength(told.Timeframe)
	if err != nil || told.Candles <= 0 || length <= 0 {
		log.Warnf("backtests: the warmup of %s: %v", snap.Agent, err)
		return warm, agentapi.Warmup{}, fmt.Sprintf("o aquecimento da estratégia não pôde ser lido do agente: %s "+
			"antes do período, o padrão", durationText(warm))
	}
	needed := time.Duration(told.Candles)*length*3/2 + 24*time.Hour
	if needed > backtestWarmupCap {
		return backtestWarmupCap, told, fmt.Sprintf("a estratégia pede %d candles de %s de aquecimento: lidos %s "+
			"antes do período, o máximo", told.Candles, told.Timeframe, durationText(backtestWarmupCap))
	}
	return max(warm, needed), told, ""
}

// referencesOf reads the candles of the symbols the strategy of a run of snap
// reads but doesn't trade (references, eg: the one of -intermarket-symbol) at
// the bridge of the agent, with the same cache, timeframe (tf) and range as
// its pairs: their files by symbol, and the warnings of the reads. A reference
// that can't be read, or starting after the period, is a warning, not a
// failure: the filter refuses the entries without it, as it does live.
func (b *Backtests) referencesOf(ctx context.Context, snap backtestSnapshot, references []string, token string,
	tf backtestTimeframe, dataFrom time.Time, run store.BacktestRun) (map[string]string, []string) {
	var files map[string]string
	var warnings []string
	for _, symbol := range references {
		if slices.Contains(snap.Pairs, symbol) {
			continue // traded: read already
		}
		data, err := b.candles.read(ctx, snap.Bridge, token, symbol, tf.file, tf.mt5, tf.length, dataFrom, run.To)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("os candles da referência %s não puderam ser lidos na ponte: "+
				"%v; sem eles, o filtro recusa as entradas", symbol, err))
			continue
		}
		if data.First.After(run.From) {
			warnings = append(warnings, fmt.Sprintf("os candles da referência %s começam em %s, depois do início "+
				"do período: até lá, o filtro recusa as entradas", symbol, data.First.Format(time.DateOnly)))
		}
		warnings = append(warnings, data.Gaps...)
		if files == nil {
			files = make(map[string]string)
		}
		files[symbol] = data.File
	}
	return files, warnings
}

// releasesOf reads the scheduled releases of the economic calendar of the
// pairs from from to to, from the news of the dashboard (the MCP server of its
// URL with its token, one calendar a URL kept across the runs, so the days
// read stay cached a day), the currencies of each pair by its symbol (its base
// and profit currencies, else its name): their times, codes, names,
// importance and whether timed, never a value released (the forecasts and
// revisions came after the releases). It returns them by pair and a warning
// telling what was read; the news off, without a server or a token, or the
// calendar unreadable, none and why.
func (b *Backtests) releasesOf(ctx context.Context, pairs []string, specs map[string]bridgeSymbol, from,
	to time.Time) (map[string][]agentapi.BacktestRelease, string) {
	none := func(why string) (map[string][]agentapi.BacktestRelease, string) {
		return nil, "calendário econômico: " + why + "; a estratégia roda sem as divulgações"
	}
	news, err := newsOf(ctx, b.store)
	switch {
	case err != nil:
		return none("as notícias do painel não puderam ser lidas: " + err.Error())
	case !news.Enabled:
		return none("as notícias estão desligadas no painel")
	case strings.TrimSpace(news.URL) == "":
		return none("sem o endereço do servidor MCP das notícias")
	case strings.TrimSpace(news.Token) == "":
		return none("sem o token do servidor MCP das notícias")
	}
	calendar, err := b.calendar(news.URL, news.Token)
	if err != nil {
		return none(err.Error())
	}
	read, cancel := context.WithTimeout(ctx, backtestReleasesRead)
	defer cancel()
	releases := make(map[string][]agentapi.BacktestRelease, len(pairs))
	count := 0
	for _, pair := range pairs {
		releases[pair] = []agentapi.BacktestRelease{}
		currencies := calendarCurrencies(pair, specs[pair])
		if len(currencies) == 0 {
			continue
		}
		events, err := calendar.Events(read, currencies, from, to)
		if err != nil {
			return none(fmt.Sprintf("o calendário de %s não pôde ser lido: %v", pair, err))
		}
		for _, e := range events {
			releases[pair] = append(releases[pair], agentapi.BacktestRelease{Time: e.Time.Unix(),
				Currency: e.Currency, Code: e.Code, Name: e.Name, Importance: int(e.Importance), Timed: e.Timed(),
				Tentative: e.TimeMode == llm.TimeTentative || e.TimeMode == llm.TimeNone})
		}
		count += len(events)
	}
	return releases, fmt.Sprintf("calendário econômico: %d divulgações de %s a %s lidas das notícias do painel "+
		"(horários e importância, sem os valores divulgados)", count, from.UTC().Format(time.DateOnly),
		to.UTC().Format(time.DateOnly))
}

// calendar returns the calendar of the MCP server at url, with token: the one
// kept for url (its token replaced when another), else a new one.
func (b *Backtests) calendar(url, token string) (*llm.MCPCalendar, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if calendar, ok := b.calendars[url]; ok {
		calendar.SetToken(token)
		return calendar, nil
	}
	calendar, err := llm.NewMCPCalendar(url, token)
	if err != nil {
		return nil, err
	}
	b.calendars[url] = calendar
	return calendar, nil
}

// calendarCurrencies returns the currencies of the economic calendar of pair,
// as the agents read them (llm.CalendarCurrencies, eg: USD for XAUUSDm), the
// pair registered with the currencies of its symbol, else the ones of its name.
func calendarCurrencies(pair string, symbol bridgeSymbol) []string {
	base, profit := symbol.CurrencyBase, symbol.CurrencyProfit
	if base == "" || profit == "" {
		base, profit = splitQuote(pair)
	}
	exchange.RegisterMT5Pair(pair, base, profit)
	return llm.CalendarCurrencies(pair)
}

// lastLine returns the last line of out not blank, eg: the JSON a program
// printed after its logs.
func lastLine(out []byte) []byte {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return []byte(lines[len(lines)-1])
}

// durationText returns d in days, eg: 60 dias.
func durationText(d time.Duration) string {
	days := int(math.Round(d.Hours() / 24))
	if days == 1 {
		return "1 dia"
	}
	return fmt.Sprintf("%d dias", days)
}

// stopped returns the status of the run of id stopped before its end:
// canceled by a user, else interrupted by the stop of the controller.
func (b *Backtests) stopped(id int64) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.canceled[id] {
		return store.BacktestCanceled
	}
	return store.BacktestInterrupted
}

// process runs program with args in env, from the dir of the agents, in a
// process group of its own (killed with the controller), until it exits or
// ctx is done (interrupted, then killed after backtestStopWait): its progress
// (PROGRESS done total on its error output) recorded for the run of id; the
// last lines of its error output returned.
func (b *Backtests) process(ctx context.Context, id int64, program string, args, env []string) ([]string,
	error) {
	cmd := exec.Command(program, args...)
	cmd.Dir = b.agents.Dir
	cmd.Env = env
	cmd.Stdout = io.Discard
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	group(cmd)

	var mu sync.Mutex
	var lines []string
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		var last time.Time
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			var done, total int64
			if n, _ := fmt.Sscanf(line, "PROGRESS %d %d", &done, &total); n == 2 {
				if time.Since(last) >= backtestProgressGap || done == total {
					last = time.Now()
					if err := b.store.ProgressBacktestRun(context.WithoutCancel(ctx), id, done, total); err != nil {
						log.Warnf("backtests: the progress of run %d: %v", id, err)
					}
				}
				continue
			}
			mu.Lock()
			lines = append(lines, line)
			if len(lines) > backtestErrLines {
				lines = lines[len(lines)-backtestErrLines:]
			}
			mu.Unlock()
		}
	}()

	started, exited := make(chan error, 1), make(chan error, 1)
	go func() {
		// the signal of the death of the controller comes with the end of the
		// thread that started the process (see group): kept until it exits
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := cmd.Start(); err != nil {
			started <- err
			return
		}
		started <- nil
		<-scanned
		exited <- cmd.Wait()
	}()
	if err := <-started; err != nil {
		return nil, fmt.Errorf("o programa do agente não pôde ser iniciado: %w", err)
	}
	log.Infof("backtests: run %d launched: %s %s", id, program, strings.Join(args, " "))
	select {
	case err = <-exited:
	case <-ctx.Done():
		_ = interrupt(cmd.Process)
		select {
		case err = <-exited:
		case <-time.After(backtestStopWait):
			_ = kill(cmd.Process)
			err = <-exited
		}
	}
	mu.Lock()
	defer mu.Unlock()
	return slices.Clone(lines), err
}

// backtestEnv returns the environment of the process of a run: the base one
// of the processes of the controller and the one of the agents, never a token
// of the controller nor of a bridge; the key of the model of an LLM agent.
func backtestEnv(agents config.Agents, snap backtestSnapshot, secrets store.AgentSecretValues) []string {
	env := make(map[string]string)
	for _, name := range baseEnv {
		if value, ok := os.LookupEnv(name); ok {
			env[name] = value
		}
	}
	for name, value := range agents.Env.Map() {
		env[name] = value
	}
	if snap.LLM != nil && secrets.LLMAPIKey != "" {
		switch snap.LLM.Provider {
		case "openai":
			env["OPENAI_API_KEY"] = secrets.LLMAPIKey
		case "anthropic":
			env["ANTHROPIC_API_KEY"] = secrets.LLMAPIKey
		}
	}
	list := make([]string, 0, len(env))
	for name, value := range env {
		if strings.HasPrefix(name, "XTRADER_") || name == "MT5_BRIDGE_TOKEN" || name == "MT5_MCP_TOKEN" {
			continue
		}
		list = append(list, name+"="+value)
	}
	slices.Sort(list)
	return list
}

// timeframeLength returns the length of timeframe, an hour when unknown.
func timeframeLength(timeframe string) time.Duration {
	units := map[byte]time.Duration{'m': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}
	if len(timeframe) < 2 {
		return time.Hour
	}
	n, err := strconv.Atoi(timeframe[:len(timeframe)-1])
	unit, ok := units[timeframe[len(timeframe)-1]]
	if err != nil || !ok || n <= 0 {
		return time.Hour
	}
	return time.Duration(n) * unit
}

// listParam splits a param of a list, its items separated by ";" or ",".
func listParam(value string) []string {
	var items []string
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == ',' }) {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// compatibility returns whether a is backtested, and why not.
func (b *Backtests) compatibility(a store.Agent) ui.BacktestCompatibility {
	c := ui.BacktestCompatibility{Agent: a.Name, Kind: a.Kind, Mode: a.Mode, Enabled: a.Enabled,
		Strategy: a.Params["strategy"], Pairs: listParam(a.Params["pair"]), Timeframe: a.Params["timeframe"],
		LLM: a.Kind == "llm"}
	if c.Strategy == "" && a.Kind != "llm" {
		c.Strategy = agentapi.DefaultStrategy(a.Kind)
	}
	switch {
	case a.Kind == "llm" && a.LLM == nil:
		c.Reason = "sem o modelo configurado"
	case !slices.Contains(config.Kinds, a.Kind):
		c.Reason = "tipo de agente desconhecido"
	case a.Account == nil || a.Account.BridgeURL == "":
		c.Reason = "sem ponte MT5 para ler o histórico"
	case len(c.Pairs) == 0:
		c.Reason = "sem ativos configurados"
	default:
		c.Compatible = true
	}
	return c
}

// codeOf returns the version of the program of command: the hash of its file,
// or why there is none, eg: go run.
func codeOf(command []string, dir string) string {
	if len(command) == 0 {
		return "desconhecida"
	}
	program := command[0]
	if !filepath.IsAbs(program) {
		if strings.ContainsRune(program, filepath.Separator) {
			program = filepath.Join(dir, program)
		} else if found, err := exec.LookPath(program); err == nil {
			program = found
		}
	}
	if len(command) > 1 && filepath.Base(command[0]) == "go" {
		return "go " + strings.Join(command[1:], " ") + " (compilado na hora, sem hash)"
	}
	f, err := os.Open(program)
	if err != nil {
		return "desconhecida: " + err.Error()
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "desconhecida: " + err.Error()
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// filterArgs returns the flags of an agent a backtest passes, see
// backtestFlagsDropped, the pairs (-pair) and the timeframe (-timeframe)
// replaced when set, -lot cut to the pairs kept.
func filterArgs(args []string, pairs []string, timeframe string, llm bool) []string {
	var kept []string
	for _, arg := range args {
		name, value, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		switch {
		case slices.Contains(backtestFlagsDropped, name), name == "pair":
			continue
		case llm && slices.Contains(backtestLLMFlagsDropped, name):
			continue
		case name == "timeframe" && timeframe != "":
			continue
		case name == "lot" && strings.Contains(value, "="):
			var lots []string
			for _, item := range listParam(value) {
				if pair, _, _ := strings.Cut(item, "="); slices.Contains(pairs, strings.TrimSpace(pair)) {
					lots = append(lots, item)
				}
			}
			arg = "-lot=" + strings.Join(lots, ";")
		}
		kept = append(kept, arg)
	}
	if timeframe != "" {
		kept = append(kept, "-timeframe="+timeframe)
	}
	return kept
}

// batchParamsMax is the number of params a batch adds to its agents at most.
const batchParamsMax = 20

// batchParamName is the name of a param: the one of a flag.
var batchParamName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// batchParamsError returns why the params a batch adds to its agents are
// refused (see ui.BacktestRequest), "" when they aren't: a name of a flag,
// none a backtest sets itself (backtestFlagsDropped, the pairs, the
// timeframe, the strategy, the warmup probe) nor one of a language model, the
// value a line of text of 1024 bytes at most.
func batchParamsError(params map[string]string) string {
	if len(params) > batchParamsMax {
		return fmt.Sprintf("no máximo %d", batchParamsMax)
	}
	for _, name := range slices.Sorted(maps.Keys(params)) {
		value := params[name]
		switch {
		case !batchParamName.MatchString(name):
			return fmt.Sprintf("%q: o nome de uma flag, letras minúsculas, dígitos e -", name)
		case slices.Contains(backtestFlagsDropped, name) || slices.Contains([]string{"pair", "timeframe", "strategy",
			agentapi.PrintWarmupFlag}, name):
			return fmt.Sprintf("%s é definido pelo próprio backtest", name)
		case strings.HasPrefix(name, "llm-") || strings.HasPrefix(name, "calendar-") ||
			strings.HasPrefix(name, "news-"):
			return fmt.Sprintf("%s é de um agente LLM", name)
		case len(value) > 1024 || strings.ContainsAny(value, "\r\n"):
			return fmt.Sprintf("%s: uma linha de até 1024 bytes", name)
		}
	}
	return ""
}

// withParams returns args with params added, by name, sorted: an arg of the
// same name replaced.
func withParams(args []string, params map[string]string) []string {
	if len(params) == 0 {
		return args
	}
	kept := slices.DeleteFunc(slices.Clone(args), func(arg string) bool {
		name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		_, replaced := params[name]
		return replaced
	})
	for _, name := range slices.Sorted(maps.Keys(params)) {
		kept = append(kept, "-"+name+"="+params[name])
	}
	return kept
}

// batchHoursProblem returns why the trading hours of a batch are refused, ""
// when they aren't: its windows (its start and end as its only one, a client of
// a single window), one at least, as the ones of the agents (see
// agentapi.WindowsProblem).
func batchHoursProblem(h ui.BacktestHours) string {
	windows := agentapi.HoursWindows(h.Windows, h.Start, h.End, h.Flatten, h.CancelPending)
	if len(windows) == 0 {
		return "adicione ao menos uma janela"
	}
	return agentapi.WindowsProblem(windows)
}

// plan returns the batch request asks for, and its runs, each with its
// config copied (see backtestSnapshot): refused (store.ErrInvalid) out of the
// limits of the controller or for an agent that can't be backtested.
func (b *Backtests) plan(ctx context.Context, request ui.BacktestRequest, user string, now time.Time) (
	store.BacktestBatch, []store.BacktestRun, error) {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", store.ErrInvalid, fmt.Sprintf(format, args...))
	}
	from, to := time.Unix(request.From, 0).UTC(), time.Unix(request.To, 0).UTC()
	share := request.ValidationShare
	positive := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }
	switch {
	case len(request.Agents) == 0:
		return store.BacktestBatch{}, nil, invalid("escolha ao menos um agente")
	case request.From <= 0 || !to.After(from):
		return store.BacktestBatch{}, nil, invalid("o período: o início antes do fim")
	case to.After(now.Add(time.Minute)):
		return store.BacktestBatch{}, nil, invalid("o período não pode terminar no futuro")
	case to.Sub(from) > time.Duration(b.cfg.MaxPeriodDays)*24*time.Hour:
		return store.BacktestBatch{}, nil, invalid("o período tem no máximo %d dias", b.cfg.MaxPeriodDays)
	case !positive(share) || share > 0.5:
		return store.BacktestBatch{}, nil, invalid("a validação: de 0 a 50%% do período")
	case !(request.Balance > 0) || math.IsInf(request.Balance, 0):
		return store.BacktestBatch{}, nil, invalid("o saldo inicial deve ser maior que zero")
	case !positive(request.Fee) || request.Fee >= 0.1:
		return store.BacktestBatch{}, nil, invalid("a taxa: uma fração do valor, de 0 a 0,1")
	case !positive(request.SpreadPoints) || !positive(request.SlippagePoints):
		return store.BacktestBatch{}, nil, invalid("o spread e a slippage devem ser 0 ou mais")
	case !positive(request.DailyLoss) || !positive(request.DailyGain):
		return store.BacktestBatch{}, nil, invalid("os limites diários devem ser 0 ou mais")
	case request.Hours != nil && batchHoursProblem(*request.Hours) != "":
		return store.BacktestBatch{}, nil, invalid("o horário de operação: %s", batchHoursProblem(*request.Hours))
	case request.LLMMaxCalls < 0 || !positive(request.LLMMaxCost):
		return store.BacktestBatch{}, nil, invalid("os tetos do LLM devem ser 0 ou mais")
	case request.Timeframe != "" && !slices.Contains(backtestTimeframesAllowed, request.Timeframe):
		return store.BacktestBatch{}, nil, invalid("o timeframe: um de %s", strings.Join(backtestTimeframesAllowed, ", "))
	}
	if why := batchParamsError(request.Params); why != "" {
		return store.BacktestBatch{}, nil, invalid("os parâmetros do lote: %s", why)
	}
	segments := []struct {
		name     string
		from, to time.Time
	}{{store.SegmentFit, from, to}}
	if share > 0 {
		split := from.Add(time.Duration(float64(to.Sub(from)) * (1 - share))).Truncate(time.Minute)
		segments = []struct {
			name     string
			from, to time.Time
		}{{store.SegmentFit, from, split}, {store.SegmentValidation, split, to}}
	}
	if n := len(request.Agents) * len(segments); n > b.cfg.MaxRuns {
		return store.BacktestBatch{}, nil, invalid("o lote teria %d execuções, o máximo é %d", n, b.cfg.MaxRuns)
	}

	file, _, _ := fileTimeframeOf(to.Sub(from) + backtestWarmupMax)
	assumptions := ui.BacktestAssumptions{Balance: request.Balance, Currency: "", Fee: request.Fee,
		SpreadPoints: request.SpreadPoints, SlippagePoints: request.SlippagePoints, DailyLoss: request.DailyLoss,
		DailyGain: request.DailyGain, Hours: request.Hours, Timeframe: request.Timeframe, ExecutionTimeframe: file,
		GapFills:     true,
		Conservative: true, EndPolicy: "mark_to_market",
		LLMMaxCalls: cmpOr(request.LLMMaxCalls, b.cfg.LLMMaxCalls),
		LLMMaxCost:  cmpOr(request.LLMMaxCost, b.cfg.LLMMaxCost), Params: request.Params}

	// the agents per asset the runs follow, as they are now
	var rules store.AssetAgents
	if request.AssetAgents {
		var err error
		if rules, _, err = b.store.AssetAgents(ctx); err != nil {
			return store.BacktestBatch{}, nil, err
		}
		assumptions.AssetAgents = true
		if settings := rules.Settings(); len(settings.Rules) > 0 {
			assumptions.AssetRules = settings.Rules
		}
	}

	var runs []store.BacktestRun
	seen := make(map[string]bool)
	for _, name := range request.Agents {
		if seen[name] {
			continue
		}
		seen[name] = true
		a, err := b.store.AgentByName(ctx, name)
		if err != nil {
			return store.BacktestBatch{}, nil, invalid("o agente %s não existe", name)
		}
		compat := b.compatibility(a)
		if !compat.Compatible {
			return store.BacktestBatch{}, nil, invalid("%s não pode ser testado: %s", name, compat.Reason)
		}
		if len(request.Params) > 0 && a.Kind == "llm" {
			return store.BacktestBatch{}, nil, invalid("os parâmetros do lote não valem para o agente LLM %s: "+
				"tire-o do lote ou os parâmetros", name)
		}
		pairs := compat.Pairs
		if len(request.Pairs) > 0 {
			pairs = slices.DeleteFunc(slices.Clone(pairs), func(p string) bool {
				return !slices.Contains(request.Pairs, p)
			})
			if len(pairs) == 0 {
				return store.BacktestBatch{}, nil, invalid("%s não opera nenhum dos ativos escolhidos", name)
			}
		}
		// the assets the agents per asset allow it, the others left out of its run
		var excluded []string
		if request.AssetAgents {
			pairs, excluded = rules.Allowed(name, pairs)
			if len(pairs) == 0 {
				return store.BacktestBatch{}, nil, invalid("o agente %s não pode operar nenhum dos seus ativos pelos "+
					"agentes por ativo de Configurações › Limites: tire-o do lote ou desmarque a opção", name)
			}
		}
		cfg, err := configOf(ctx, b.store, a)
		if err != nil {
			return store.BacktestBatch{}, nil, err
		}
		_, quote := splitQuote(pairs[0])
		if assumptions.Currency == "" {
			assumptions.Currency = quote
		}
		command := strings.Fields(b.agents.Command(a.Kind))
		timeframe := cmpOr(request.Timeframe, compat.Timeframe)
		// the trading hours of the run: the windows of the batch its agent may
		// open positions in
		runAssumptions := assumptions
		if h := assumptions.Hours; h != nil {
			windows := agentapi.WindowsFor(agentapi.HoursWindows(h.Windows, h.Start, h.End, h.Flatten,
				h.CancelPending), name)
			if len(windows) == 0 {
				return store.BacktestBatch{}, nil, invalid("o agente %s não está em nenhuma janela do horário: "+
					"inclua-o numa janela ou tire-o do lote", name)
			}
			runAssumptions.Hours = &ui.BacktestHours{Windows: windows}
		}
		snap := backtestSnapshot{Agent: a.Name, Kind: a.Kind, Mode: a.Mode, Strategy: compat.Strategy, Pairs: pairs,
			Timeframe: timeframe,
			Args:      withParams(filterArgs(cfg.Args(), pairs, request.Timeframe, a.Kind == "llm"), request.Params),
			Command:   command,
			Code:      codeOf(command, b.agents.Dir), Protection: cfg.ProtectionSettings, Bridge: a.Account.BridgeURL,
			Assumptions: runAssumptions}
		if limits := a.DailyLimitOf(); limits.Enabled && request.DailyLoss == 0 && request.DailyGain == 0 {
			snap.Notes = append(snap.Notes, "os limites diários do agente estão na moeda da conta e não foram "+
				"aplicados (o backtest é na moeda de cotação); informe limites no lote para simulá-los")
		}
		if timeframe == "" {
			snap.Notes = append(snap.Notes, "timeframe padrão da estratégia")
		}
		if len(excluded) > 0 {
			snap.Notes = append(snap.Notes, fmt.Sprintf("fora pelos agentes por ativo de Configurações › Limites: %s",
				strings.Join(excluded, ", ")))
		}
		if a.Kind == "llm" {
			secrets, err := b.store.SecretsOf(a)
			if err != nil {
				return store.BacktestBatch{}, nil, err
			}
			if secrets.LLMAPIKey == "" {
				return store.BacktestBatch{}, nil, invalid("%s não pode ser testado: sem a chave do provedor do "+
					"modelo", name)
			}
			snap.LLM = &backtestLLM{Provider: cfg.LLM.Provider, Model: cfg.LLM.Model,
				MaxCalls: assumptions.LLMMaxCalls, MaxCost: assumptions.LLMMaxCost}
			if price, ok := b.cfg.LLMPrices[cfg.LLM.Model]; ok {
				snap.LLM.Prices = &price
			}
			if cfg.Prompt != nil {
				sum := sha256.Sum256([]byte(cfg.Prompt.Content))
				snap.Prompt = &backtestPrompt{ID: cfg.Prompt.ID, Name: cfg.Prompt.Name, Version: cfg.Prompt.Version,
					SHA256: hex.EncodeToString(sum[:]), Content: cfg.Prompt.Content}
			}
			snap.Notes = append(snap.Notes,
				"não determinístico: as respostas do modelo variam entre execuções, e ele pode ter visto o período "+
					"no treino",
				"contexto reduzido: sem timeframes superiores, mercados correlacionados, calendário econômico, "+
					"memória de operações nem histórico do dia da conta; filtros de tendência dos timeframes "+
					"superiores desligados",
				"custo: cada fechamento de candle analisado é uma chamada paga ao modelo, limitada pelos tetos do lote")
			if snap.LLM.Prices == nil {
				snap.Notes = append(snap.Notes, "sem o preço do modelo em backtest.llm_prices: o custo não é "+
					"estimado, só o número de chamadas limita a execução")
			}
		}
		data, err := json.Marshal(snap)
		if err != nil {
			return store.BacktestBatch{}, nil, err
		}
		for _, segment := range segments {
			runs = append(runs, store.BacktestRun{Agent: a.Name, Kind: a.Kind, Segment: segment.name,
				From: segment.from, To: segment.to, Config: data})
		}
	}
	assumptionsData, err := json.Marshal(assumptions)
	if err != nil {
		return store.BacktestBatch{}, nil, err
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = fmt.Sprintf("%s a %s", from.Format("02/01/2006"), to.Format("02/01/2006"))
	}
	if len([]rune(name)) > 120 {
		return store.BacktestBatch{}, nil, invalid("o nome tem no máximo 120 caracteres")
	}
	return store.BacktestBatch{Name: name, From: from, To: to, ValidationShare: share, Assumptions: assumptionsData,
		CreatedBy: user}, runs, nil
}

// cmpOr returns value, else fallback for the zero value.
func cmpOr[T comparable](value, fallback T) T {
	var zero T
	if value == zero {
		return fallback
	}
	return value
}

// splitQuote returns the asset and the quote of a pair of MT5, eg: XAU and
// USD of XAUUSDm, by the currencies the controller knows.
func splitQuote(pair string) (string, string) {
	for _, quote := range []string{"USD", "EUR", "BRL", "JPY", "GBP", "USDT"} {
		if i := strings.Index(pair, quote); i > 0 {
			return pair[:i], quote
		}
	}
	return pair, "USD"
}

// errBacktestsUnavailable is the error of the backtests asked of a
// controller that doesn't run them.
var errBacktestsUnavailable = errors.New("backtests unavailable")
