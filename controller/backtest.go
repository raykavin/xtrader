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
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The runs of the backtests.
const (
	backtestTick          = 5 * time.Second  // between two looks for runs queued, besides the wakes
	backtestProgressGap   = 2 * time.Second  // between two records of the progress of a run
	backtestStopWait      = 15 * time.Second // an interrupted run has before it is killed
	backtestErrLines      = 30               // the last lines of the error output kept, the error of a run failed
	backtestWarmupCandles = 1000             // candles of the timeframe of the agent read before the period
	backtestWarmupMax     = 60 * 24 * time.Hour
	backtestBatchesShown  = 30
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

	mu       sync.Mutex
	running  map[int64]context.CancelFunc
	canceled map[int64]bool // the runs canceled by a user, apart from a stop of the controller
	wg       sync.WaitGroup
}

// ServeBacktests serves the backtests on the dashboard, see Backtests: Run
// runs them.
func (d *Dashboard) ServeBacktests(s *store.Store, agents config.Agents, cfg config.Backtest) *Backtests {
	dir := cfg.Dir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(agents.Dir, dir)
	}
	b := &Backtests{store: s, agents: agents, cfg: cfg, dir: dir, candles: newCandleCache(dir),
		wake: make(chan struct{}, 1), running: make(map[int64]context.CancelFunc), canceled: make(map[int64]bool)}
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

	length := timeframeLength(snap.Timeframe)
	warm := min(time.Duration(backtestWarmupCandles)*length, backtestWarmupMax)
	dataFrom := run.From.Add(-warm)
	file, mt5, fileLength := fileTimeframeOf(run.To.Sub(dataFrom))
	var files, contracts, warnings []string
	symbols := make(map[string]agentapi.BacktestSymbol)
	gaps := 0
	for _, pair := range snap.Pairs {
		data, err := b.candles.read(ctx, snap.Bridge, secrets.BridgeToken, pair, file, mt5, fileLength, dataFrom,
			run.To)
		if err != nil {
			return failed("os candles de %s não puderam ser lidos na ponte: %v", pair, err)
		}
		if data.First.After(run.From) {
			return failed("dados insuficientes: os candles de %s começam em %s, depois do início do período", pair,
				data.First.Format(time.DateOnly))
		}
		files = append(files, data.File)
		contract := max(data.Symbol.ContractSize, 1)
		contracts = append(contracts, strconv.FormatFloat(contract, 'f', -1, 64))
		symbols[pair] = agentapi.BacktestSymbol{Point: data.Symbol.Point, VolumeStep: data.Symbol.VolumeStep * contract,
			VolumeMin: data.Symbol.VolumeMin * contract}
		warnings = append(warnings, data.Gaps...)
		gaps += len(data.Gaps)
	}

	a := snap.Assumptions
	cfg := agentapi.BacktestConfig{From: run.From.Unix(), To: run.To.Unix(), Balance: a.Balance, Leverage: 1,
		Fee: a.Fee, SlippagePoints: a.SlippagePoints, SpreadPoints: a.SpreadPoints, Conservative: a.Conservative,
		GapFills: a.GapFills, Symbols: symbols, Protection: snap.Protection, DailyLoss: a.DailyLoss,
		DailyGain: a.DailyGain, Report: filepath.Join(dir, "report.json"), Progress: true}
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
	report.Warnings = append(warnings, report.Warnings...)
	summary := summarizeBacktest(report, gaps, snap.LLM)
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
	case request.LLMMaxCalls < 0 || !positive(request.LLMMaxCost):
		return store.BacktestBatch{}, nil, invalid("os tetos do LLM devem ser 0 ou mais")
	case request.Timeframe != "" && !slices.Contains(backtestTimeframesAllowed, request.Timeframe):
		return store.BacktestBatch{}, nil, invalid("o timeframe: um de %s", strings.Join(backtestTimeframesAllowed, ", "))
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
		DailyGain: request.DailyGain, Timeframe: request.Timeframe, ExecutionTimeframe: file, GapFills: true,
		Conservative: true, EndPolicy: "mark_to_market",
		LLMMaxCalls: cmpOr(request.LLMMaxCalls, b.cfg.LLMMaxCalls),
		LLMMaxCost:  cmpOr(request.LLMMaxCost, b.cfg.LLMMaxCost)}

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
		pairs := compat.Pairs
		if len(request.Pairs) > 0 {
			pairs = slices.DeleteFunc(slices.Clone(pairs), func(p string) bool {
				return !slices.Contains(request.Pairs, p)
			})
			if len(pairs) == 0 {
				return store.BacktestBatch{}, nil, invalid("%s não opera nenhum dos ativos escolhidos", name)
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
		snap := backtestSnapshot{Agent: a.Name, Kind: a.Kind, Mode: a.Mode, Strategy: compat.Strategy, Pairs: pairs,
			Timeframe: timeframe, Args: filterArgs(cfg.Args(), pairs, request.Timeframe, a.Kind == "llm"),
			Command: command,
			Code:    codeOf(command, b.agents.Dir), Protection: cfg.ProtectionSettings, Bridge: a.Account.BridgeURL,
			Assumptions: assumptions}
		if limits := a.DailyLimitOf(); limits.Enabled && request.DailyLoss == 0 && request.DailyGain == 0 {
			snap.Notes = append(snap.Notes, "os limites diários do agente estão na moeda da conta e não foram "+
				"aplicados (o backtest é na moeda de cotação); informe limites no lote para simulá-los")
		}
		if timeframe == "" {
			snap.Notes = append(snap.Notes, "timeframe padrão da estratégia")
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
