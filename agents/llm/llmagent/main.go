package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/internal/backtest"
	"github.com/raykavin/xtrader/agents/internal/newsguard"
	"github.com/raykavin/xtrader/agents/internal/protect"
	"github.com/raykavin/xtrader/agents/internal/remote"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

// Llmagent, the agent of the LLM strategy, trades the decisions of a language
// model, OpenAI or Anthropic, on MetaTrader 5 symbols, eg: of Exness, through
// tools/mt5bridge. On every candle close the model gets the context of the pair
// (candles, indicators, prices, position) and answers WAIT, ENTER_LONG,
// ENTER_SHORT, HOLD, EXIT, MODIFY_SL or MODIFY_TP, which the bot validates and
// executes, see package llm. A prompt may let it place pending orders too
// (BUY_LIMIT, SELL_LIMIT, BUY_STOP and SELL_STOP, moved with MODIFY_PENDING
// and canceled with CANCEL_PENDING), and draw its analysis on the chart: see
// the headers of package llm, eg: the prompt smc_ict.
//
// The live mode runs launched by the controller (cmd/controller), from the
// settings of the agent in its database: the flags, the API key of the
// provider, the token of the bridge and of the calendar, and the prompt, which
// may be switched while it runs, from the next analysis on (see package
// remote). The controller keeps its orders, its decisions and its journal.
// Launched by it, the paper mode keeps its journal there, its orders and
// decisions in memory as on its own.
//
// On its own, the agent runs backtests and paper wallets, its API key read
// from OPENAI_API_KEY or ANTHROPIC_API_KEY:
//
//	# the market data of the bridge, trading a paper wallet
//	OPENAI_API_KEY=... go run ./agents/llm/llmagent -llm-provider openai -llm-model gpt-5 \
//		-mode paper -pair BTCUSDm -lot BTCUSDm=0.01
//	# the same with the economic calendar of the terminal in the context, no
//	# entry from 15 minutes before a release of high importance to 15 after
//	MT5_MCP_TOKEN=... go run ./agents/llm/llmagent -llm-provider anthropic -llm-model claude-sonnet-5 \
//		-mode paper -pair XAUUSDm -lot XAUUSDm=0.01 -timeframe 5m -calendar-mcp http://127.0.0.1:22346/mcp
//	# a backtest asks the model on every candle of the file: mind the costs
//	go run ./agents/llm/llmagent -llm-provider anthropic -llm-model claude-sonnet-5 \
//		-pair BTCUSDm -lot BTCUSDm=0.01 -timeframe 1h -csv btc-1h.csv
//	# a prompt of the database saved to a file, eg: the Supertrend flips of
//	# agents/trendagent, its indicators (and whether its entries go without a
//	# take profit) read from its header
//	go run ./agents/llm/llmagent -llm-provider openai -llm-model gpt-5-mini \
//		-prompt-path supertrend.txt -llm-min-rr 0 -llm-min-stop-atr 0 \
//		-pair BTCUSDm -lot BTCUSDm=0.01 -timeframe 15m -csv btc-15m.csv
//
// The model never trades itself: each entry is a market order with the stop
// loss and take profit of the decision at the broker, which places them with
// the order (mt5 or the paper wallet), each exit a market order and each new
// level a leg moved at the broker. A request that fails, or an answer that is
// not a valid decision, trades nothing.
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// controllerWarmup is the warmup of an agent launched by the controller
// without -llm-warmup: the history of every prompt of the database, the
// longest (an EMA of 200) taking 1000 candles.
const controllerWarmup = 1000

func run() error {
	cfg := llm.DefaultConfig()
	var (
		providerName  = flag.String("llm-provider", "", "openai or anthropic, the API key read from "+llm.OpenAIKeyEnv+" or "+llm.AnthropicKeyEnv)
		modelName     = flag.String("llm-model", "", "model of the provider, eg: claude-sonnet-5 or gpt-5 (required)")
		baseURL       = flag.String("llm-base-url", "", "base URL of the API of the provider, eg: a proxy (default: the provider's)")
		maxTokens     = flag.Int("llm-max-tokens", 0, "tokens of an answer at most, the reasoning included (default 4096; 16000 for a model of anthropic that refuses the forced tool call, eg: claude-opus-5-5, as it thinks before it answers)")
		reasoning     = flag.String("llm-reasoning", "", "effort of the models that reason, none, minimal, low, medium, high or xhigh, as the model takes them (eg: minimal only the older ones, none and xhigh only the newer ones; the API refuses the others); less spends fewer output tokens (default: the model's; ignored by anthropic)")
		timeout       = flag.Duration("llm-timeout", cfg.Timeout, "time a decision may take, the request to the provider")
		consult       = flag.Duration("llm-consult-interval", 0, "time between the consultations of the model about each pair, eg: 5m or 2h, 10s at least, on a schedule of the clock (5m: at :00, :05, :10...), required live and paper: the model is asked on it only, on the candle in progress, never at the close of a candle (besides the start and the reanalyses); each consultation is a billed request (a backtest asks at the closes on the schedule, at each close without it)")
		candles       = flag.Int("llm-candles", cfg.Candles, "candles of the context of the model, the last ones")
		confidence    = flag.Float64("llm-min-confidence", cfg.MinConfidence, "confidence, from 0 to 1, under which the entries, exits and moves are ignored")
		maxEntryATR   = flag.Float64("llm-max-entry-atr", cfg.MaxEntryATR, "how far the entry of a decision may be from the close, in ATR, as it is filled at the market (0: no limit)")
		maxLevelATR   = flag.Float64("llm-max-level-atr", cfg.MaxLevelATR, "how far the stop loss and take profit may be from the close, in ATR (0: no limit)")
		widenStop     = flag.Bool("llm-widen-stop", false, "let MODIFY_SL move the stop loss away from the price, adding risk")
		setup         = flag.String("llm-setup", cfg.Setup, "setup of the model: free, or trend_pullback: only with the trend of the SMA 200, on a pullback confirmed by momentum")
		minRR         = flag.Float64("llm-min-rr", cfg.MinRiskReward, "least reward over risk of an entry, net of the spread, eg: 1.5; the system refuses the ones under it (0: none)")
		structure     = flag.Int("llm-structure", cfg.StructureCandles, "candles the price structure of the context is computed on: its swings, levels, round numbers and candle patterns (0: none)")
		higherTFs     = flag.String("llm-higher-timeframes", "", "higher timeframes of the context, separated by ;, eg: 1h;4h, read from the exchange with the levels of the previous day and week (default: the two above -timeframe; none: none)")
		higherRows    = flag.Int("llm-higher-candles", cfg.HigherCandles, "candles of each higher timeframe sent as rows, their levels computed on 200")
		correlated    = flag.String("llm-correlated", "", "markets correlated with the pairs, read from the exchange on -timeframe: the ones of each pair by its name, their markets joined by +, the pairs separated by ;, eg: BTCUSDm=DXYm+XAUUSDm;XAUUSDm=DXYm+USTECm, a pair left out without any (one list for all the pairs and a comma are refused)")
		memory        = flag.Int("llm-memory", cfg.Memory, "last decisions and trades closed of the pair recalled in the context (0: none)")
		warmup        = flag.Int("llm-warmup", 0, "candles kept at least: the room of the prompts switched while the agent runs, whose indicators may need more history than its first one (default: 1000 launched by the controller, else what the prompt needs)")
		indicators    = flag.String("llm-indicators", "", "indicators drawn on the chart and sent to the model, separated by ;: groups alone, of "+strings.Join(llm.IndicatorGroups, ", ")+", or kinds with their parameters joined by spaces or +, eg: ema+20+50;supertrend+10+3, of: "+strings.Join(llm.IndicatorKinds, "; ")+" (default: the '# Indicators:' of the prompt, else all the groups)")
		history       = flag.Int("llm-history", cfg.History, "last values of each indicator sent to the model, eg: 6 for the rules that compare a value with the one 5 candles before")
		takeProfitOpt = flag.Bool("llm-take-profit-optional", false, "let the entries go without a take profit, the stop loss alone protecting the position (default: the '# Take profit: optional' of the prompt)")
		noTakeProfit  = flag.Bool("llm-no-take-profit", false, "trade without any take profit: every entry with the stop loss alone, moved with MODIFY_SL until it closes the position, an entry with a take profit refused; -llm-min-rr must be 0 (default: the '# Take profit: none' of the prompt)")
		promptPath    = flag.String("prompt-path", "", "on its own: file of the strategy of the model, its indicators and its take profit in its header, eg: a prompt of the database saved apart (default: the embedded agents/llm/prompts/default.txt; launched by the controller: the prompt of its settings)")
		minStop       = flag.Float64("llm-min-stop-atr", cfg.MinStopATR, "least room of the stop loss of an entry before it is hit, in ATR, eg: 1: a stop inside the noise refused (0: none)")
		higherTrend   = flag.Bool("llm-higher-trend-filter", false, "only the entries with the trend the higher timeframes agree on (all up: longs only; all down: shorts only)")
		higherTrendIn = flag.Bool("llm-higher-trend-required", false, "the trend the higher timeframes agree on required for every entry: -llm-higher-trend-filter, and no entry at all without such a trend (they don't agree, mixed or unknown)")
		maxSpread     = flag.Float64("llm-max-spread-atr", cfg.MaxSpreadATR, "no entry while the spread is wider than that many ATR, eg: 0.1 (0: no limit)")
		pendingExpiry = flag.Int("llm-pending-expiry", cfg.PendingExpiry, "candles after which a pending order not filled is canceled, for a prompt placing them ('# Pending orders:' of its header); the model keeps it, moves it or cancels it at each analysis meanwhile (0: no expiry)")
		language      = flag.String("llm-language", cfg.Language, "language of the reasons of the decisions, eg: pt-BR or en")
		mode          = flag.String("mode", "backtest", "backtest, paper or live")
		exchangeName  = flag.String("exchange", "mt5", "exchange traded: mt5, MetaTrader 5 through tools/mt5bridge")
		pairList      = flag.String("pair", agentapi.DefaultPair, "pairs to trade, separated by ;, eg: XAUUSDm;BTCUSDm")
		timeframe     = flag.String("timeframe", cfg.Timeframe, "timeframe of the candles, one decision per candle close")
		direction     = flag.String("direction", "both", "side of the positions opened: both, buy or sell")
		lotList       = flag.String("lot", "", "fixed size of every position, in MT5 lots unless -mt5-size unit: the lot of each pair of -pair by its name, separated by ;, eg: BTCUSDm=0.01;XAUUSDm=0.02, the decimals with . (one for all the pairs, the order of -pair and a comma are refused)")
		fraction      = flag.Float64("position-fraction", cfg.PositionFraction, "share of the free balance each position commits (0-1], without -lot")
		leverage      = flag.Float64("leverage", cfg.Leverage, "live: leverage of the positions, at most the account's for the market; needs -position-fraction below 1, or -lot")
		minValue      = flag.Float64("min-position-value", cfg.MinPositionValue, "value under which a balance is dust, in the currency of the account (paper: the quote)")
		fee           = flag.Float64("fee", 0.001, "backtest and paper: fee of each fill, a share of its value")
		csvFiles      = flag.String("csv", "", "backtest candles, as written by `xtrader download`: a file per pair, separated by ; in the order of -pair")
		csvTimeframe  = flag.String("csv-timeframe", "", "timeframe of the backtest candles (default -timeframe)")
		mt5CSize      = flag.String("mt5-contract-size", "", "backtest: the contract sizes of the pairs, the units of the asset in a lot, one for all of them or one per pair in the order of -pair, separated by ; (default: the ones the package knows)")
		printWarmup   = flag.Bool(agentapi.PrintWarmupFlag, false, "prints the warmup of the strategy, its candles and its timeframe, as JSON (agentapi.Warmup), and exits: what a backtest reads before its period")
		btConfig      = flag.String("backtest-config", "", "backtest: the JSON file of a backtest as the controller asks for it (period, balance, costs, symbols, ceilings of the model, report): the run writes its report there and exits (see agents/internal/backtest)")
		api           = flag.String("api", "", "address to serve the bot API at for the controller (cmd/controller), eg: localhost:5564 (default: off)")
		tradePanel    = flag.Bool("trade-panel", false, "live: enable the trade panel of the web dashboard (manual orders, their moves and cancels; the dashboard starts and stops the bot and asks the model again without it)")
		telegramCtl   = flag.Bool("telegram-control", false, "live: enable the Telegram controls of the controller")
		reanalysis    = flag.Bool("reanalysis", false, "allow asking the language model again between the closes, from the web dashboard whatever -trade-panel and from Telegram with its controls on (launched by the controller: the switch Permitir reanálise of its settings, switched while it runs)")
		quarantine    = flag.Bool("outbox-quarantine", false, "launched by the controller: an outbox that can't be read (data/outbox/<agent>-<id>.db, eg: written over in the middle) is moved aside to <file>.corrupt-<time>, kept for a look, and the agent starts with an empty one, its records never sent to the controller (default: the agent doesn't start, telling which file)")
		currency      = flag.String("currency", "USD", "backtest: currency the symbols are quoted in, eg: GBP for EURGBPm")
		mt5Bridge     = flag.String("mt5-bridge", exchange.MT5DefaultBaseURL, "mt5: address of the bridge (tools/mt5bridge) running with the terminal logged in the account")
		mt5TokenArg   = flag.String("mt5-token", "", "mt5, on its own: X-Bridge-Token of the bridge (default: "+mt5TokenEnv+"; launched by the controller: the token of its settings)")
		mt5MagicArg   = flag.Int64("mt5-magic", 0, "mt5: magic number of the bot's orders and positions, only those count for it (default: a hash of -mt5-comment)")
		mt5CommentArg = flag.String("mt5-comment", mt5Comment(""), "mt5: comment of the bot's orders, 31 characters at most")
		mt5Live       = flag.Bool("mt5-live", false, "mt5: allow a real account, refused otherwise")
		mt5Size       = flag.String("mt5-size", "lot", "mt5: how -lot is read, lot (converted into units with the contract size of the pair) or unit")
		mt5Poll       = flag.Duration("mt5-poll", 0, "mt5: how often the account and the candles are polled (default 1s)")
		calendarURL   = flag.String("calendar-mcp", "", "URL of the MCP server of the MetaTrader 5 terminal, eg: http://127.0.0.1:22346/mcp, for the economic calendar of the context; its token from "+calendarTokenEnv+", or the settings of the controller (default: no calendar)")
		calendarCur   = flag.String("calendar-currencies", "", "currencies of the calendar, separated by ;, eg: USD;EUR (default: the ones of the pair)")
		calendarAhd   = flag.Duration("calendar-ahead", cfg.CalendarAhead, "releases of the calendar in the context up to this long after the analysis (the close, or now on the candle in progress)")
		calendarBck   = flag.Duration("calendar-back", cfg.CalendarBack, "releases of the calendar in the context from this long before the analysis")
		calendarImp   = flag.String("calendar-importance", cfg.CalendarImportance.String(), "releases of the calendar in the context of at least this importance: low, medium or high")
		calendarOff   = flag.Duration("calendar-server-offset", 0, "how far the time of the trade server is from UTC, eg: 2h (Exness: 0)")
		blockBefore   = flag.Duration("news-block-before", cfg.NewsBlockBefore, "no new entry from this long before a release of -news-block-importance (0 and -news-block-after 0: never)")
		blockAfter    = flag.Duration("news-block-after", cfg.NewsBlockAfter, "no new entry until this long after a release of -news-block-importance")
		blockImp      = flag.String("news-block-importance", cfg.NewsBlockImportance.String(), "importance of the releases blocking new entries: low, medium or high")
	)
	// launched by the controller, the flags and the secrets of its settings
	ctx := context.Background()
	agent, err := remote.Connect(ctx)
	if err != nil {
		return err
	}
	var keys secrets
	if agent != nil {
		// stopped by the controller, it stops trading, then sends its records (see remote.Agent.Serve)
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		if len(os.Args) > 1 {
			return errors.New("launched by the controller, the agent takes the flags of its settings, not of " +
				"the command line")
		}
		if err := flag.CommandLine.Parse(agent.Args()); err != nil {
			return err
		}
		agent.QuarantineOutbox = *quarantine
		if err := agent.Open(filepath.Join("data", "outbox")); err != nil {
			return err
		}
		defer func() {
			if err := agent.Close(); err != nil {
				log.Warn(err)
			}
		}()
		keys = secretsOf(agent.Config)
	} else {
		flag.Parse()
		keys = secrets{provider: os.Getenv(llm.KeyEnv(*providerName)), bridge: mt5Token(*mt5TokenArg)}
	}
	set := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
	switch {
	case agent == nil && *mode == "live":
		return errors.New("-mode live runs launched by the controller (cmd/controller), which keeps the orders, " +
			"the decisions and the journal of the agent")
	case agent != nil && *mode == "backtest":
		return errors.New("the controller runs the agents in paper and live modes only")
	case agent != nil && set["prompt-path"]:
		return errors.New("-prompt-path: the prompt of an agent of the controller is the one of its settings")
	case *btConfig != "" && *mode != "backtest":
		return errors.New("-backtest-config is for -mode backtest")
	}
	var btCfg backtest.Config
	var recorder *llm.Recorder // the calls of the model of a backtest, see -backtest-config
	if *btConfig != "" {
		var err error
		if btCfg, err = backtest.ReadConfig(*btConfig); err != nil {
			return err
		}
	}

	// the provider first: a wrong key or model fails before any connection
	var options []llm.ProviderOption
	if *baseURL != "" {
		options = append(options, llm.WithBaseURL(*baseURL))
	}
	if *maxTokens != 0 {
		options = append(options, llm.WithMaxTokens(*maxTokens))
	}
	if *reasoning != "" {
		options = append(options, llm.WithReasoningEffort(*reasoning))
	}
	if *btConfig != "" {
		options = append(options, llm.WithUsageHook(func(usage llm.Usage) { recorder.Usage(usage) }))
	}
	prompt := llm.DefaultPrompt()
	switch {
	case agent != nil:
		prompt = agent.Prompt()
	case *promptPath != "":
		strategy, err := llm.ReadStrategy(*promptPath)
		if err != nil {
			return err
		}
		prompt = llm.Prompt{Ref: ui.DecisionPrompt{Name: promptName(*promptPath)}, Content: strategy}
	}
	// the flags set take precedence over the header of every prompt
	var overrides llm.PromptOverrides
	if *indicators != "" {
		overrides.Indicators = []string{}
		for _, spec := range list(*indicators) {
			overrides.Indicators = append(overrides.Indicators, llm.NormalizeIndicator(spec))
		}
	}
	overrides.TakeProfitOptional, overrides.NoTakeProfit = *takeProfitOpt, *noTakeProfit
	cfg.History = *history
	cfg.Warmup = *warmup
	if agent != nil && !set["llm-warmup"] {
		cfg.Warmup = controllerWarmup
	}
	provider, err := llm.NewProvider(*providerName, *modelName, keys.provider, options...)
	if err != nil {
		return err
	}
	// a backtest of the controller: the calls recorded and bounded, the run
	// stopped at its budget, on the clock of its candles (see backtestClock)
	var stopBudget context.CancelFunc
	var backtestClock = time.Now
	if *btConfig != "" {
		ctx, stopBudget = context.WithCancel(ctx)
		defer stopBudget()
		var stopSignals context.CancelFunc
		ctx, stopSignals = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM) // canceled by the controller
		defer stopSignals()
		recorder = llm.NewRecorder(provider, func() time.Time { return backtestClock() }, stopBudget)
		recorder.MaxCalls, recorder.MaxCost = btCfg.LLMMaxCalls, btCfg.LLMMaxCost
		recorder.PriceInput, recorder.PriceOutput = btCfg.LLMPriceInput, btCfg.LLMPriceOutput
		provider = recorder
	}

	pairs := list(*pairList)
	if len(pairs) == 0 {
		return errors.New("-pair: no pair to trade")
	}
	lots, err := lotsByPair(pairs, *lotList)
	if err != nil {
		return err
	}
	if lots != nil && set["position-fraction"] {
		return errors.New("-lot and -position-fraction are exclusive: a position has a fixed size or a share of " +
			"the balance")
	}
	inLots, err := parseMT5Size(*mt5Size)
	if err != nil {
		return err
	}

	cfg.Timeframe = *timeframe
	cfg.Candles = *candles
	cfg.MinConfidence = *confidence
	cfg.MaxEntryATR = *maxEntryATR
	cfg.MaxLevelATR = *maxLevelATR
	cfg.WidenStop = *widenStop
	cfg.Setup = *setup
	cfg.Live = *mode != "backtest" // the history handed after a restart isn't traded
	cfg.StructureCandles = *structure
	cfg.HigherCandles = *higherRows
	cfg.Memory = *memory
	if cfg.Correlated, err = correlatedByPair(pairs, *correlated); err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(*higherTFs)) {
	case "":
		cfg.HigherTimeframes = nil // the ones of the timeframe
	case "none":
		cfg.HigherTimeframes = []string{}
	default:
		cfg.HigherTimeframes = list(*higherTFs)
	}
	cfg.TickVolume = true // the CFDs of MT5: the count of the ticks
	cfg.MinRiskReward = *minRR
	cfg.MaxSpreadATR = *maxSpread
	cfg.PendingExpiry = *pendingExpiry
	cfg.MinStopATR = *minStop
	cfg.HigherTrendFilter = *higherTrend
	cfg.HigherTrendRequired = *higherTrendIn
	cfg.Language = *language
	cfg.Timeout = *timeout
	cfg.ConsultInterval = *consult
	cfg.DayZone = llm.LocalZone() // the operational day of the daily limits, by the name of its zone
	cfg.Direction = strategies.RKDirection(*direction)
	cfg.MinPositionValue = *minValue
	cfg.PositionFraction = *fraction
	cfg.Leverage = *leverage
	if err := calendarConfig(&cfg, set, keys.news, *calendarURL, os.Getenv(calendarTokenEnv), list(*calendarCur),
		*calendarAhd, *calendarBck, *calendarImp, *calendarOff, *blockBefore, *blockAfter, *blockImp); err != nil {
		return err
	}
	cfg.PositionSize = lots // shared: the MT5 lots are converted into units before the bot starts
	trader, err := llm.New(cfg, provider, llm.WithPrompt(prompt, overrides))
	if agent != nil && (err != nil || *mode != "live") {
		// the controller shows what came of the prompt of the settings; live,
		// once checked against the account too, see checkPromptAccount
		agent.Prompts().ReportPrompt(prompt.Ref, err)
	}
	if err != nil {
		return err
	}
	if agent != nil {
		if err := trader.SetPromptSource(agent.Prompts()); err != nil {
			return err
		}
	}
	if *printWarmup { // nothing connected, nothing asked to the model
		return json.NewEncoder(os.Stdout).Encode(agentapi.Warmup{Timeframe: trader.Timeframe(),
			Candles: trader.WarmupPeriod()})
	}

	if err := checkVenue(*exchangeName, *mode, *leverage, *btConfig != ""); err != nil {
		return err
	}
	cadence := "a decision per " + cfg.Timeframe + " candle close"
	if cfg.ConsultInterval > 0 {
		cadence = fmt.Sprintf("a decision every %s on the clock, on %s candles", cfg.ConsultInterval, cfg.Timeframe)
	}
	log.Infof("[SETUP] LLM: %v, %s, prompt %s, %d candles kept", provider, cadence, describePrompt(prompt),
		trader.WarmupPeriod())
	if gain, loss, currency, _ := llm.StrategyDailyLimits(prompt.Content); gain > 0 || loss > 0 {
		log.Infof("[SETUP] LLM: daily limits of each pair, gain %g %s, loss %g %s, the day from midnight %s (live on "+
			"MT5 only)", gain, currency, loss, currency, cfg.DayZone)
	}

	settings := xtrader.Settings{Pairs: pairs}
	dash := dashboard{addr: *api, tradePanel: *tradePanel, telegram: *telegramCtl, reanalysis: *reanalysis}
	if agent != nil {
		dash.key = agent.APIKey
	}
	comment := mt5Comment(*mt5CommentArg)
	venue := venue{leverage: *leverage, lots: lots,
		mt5: mt5Venue{bridge: *mt5Bridge, token: keys.bridge, magic: mt5Magic(*mt5MagicArg, comment),
			comment: comment, live: *mt5Live, inLots: inLots, poll: *mt5Poll}}
	if agent != nil {
		// the positions the controller keeps apart from the ones of the other agents
		book := agentapi.MT5Book{Bridge: venue.mt5.bridge, Magic: venue.mt5.magic, Comment: comment, Pairs: pairs}
		if err := agent.Config.CheckMT5Book(book); err != nil {
			return err
		}
	}

	switch *mode {
	case "backtest":
		if err := venue.backtestPairs(pairs, *currency, list(*mt5CSize)); err != nil {
			return err
		}
		log.Warnf("[SETUP] backtest: %v is asked on every candle of the files, each request paid", provider)
		if *btConfig != "" {
			btCfg.Leverage = max(*leverage, 1)
			model := *providerName + " " + *modelName
			hooks := backtest.Hooks{
				Prepare: func(feed *exchange.BacktestFeed) {
					backtestClock = feed.Now
					trader.SetClock(feed.Now)
				},
				Extra: func() (json.RawMessage, string) {
					report := recorder.Report(model, backtestContext)
					data, _ := json.Marshal(report)
					if report.Exhausted {
						return data, backtest.StatusBudgetExhausted
					}
					return data, ""
				},
			}
			return backtest.Run(ctx, settings, trader, list(*csvFiles), *csvTimeframe, btCfg, hooks)
		}
		return backtestRun(ctx, settings, trader, dash, list(*csvFiles), *csvTimeframe, *fee)
	case "paper":
		return paper(ctx, settings, trader, dash, venue, *fee, agent)
	case "live":
		return live(ctx, settings, trader, dash, venue, agent)
	}
	return fmt.Errorf("invalid mode %q: backtest, paper or live", *mode)
}

// secrets are the secrets of the agent: the API key of the provider and the
// token of the bridge; launched by the controller, the news of the dashboard
// too (on or off, the MCP server of the calendar and its token, the window of
// the news block), nil on its own.
type secrets struct {
	provider, bridge string
	news             *agentapi.News
}

// secretsOf returns the secrets of the settings of the controller, never read
// from the flags nor from the environment.
func secretsOf(cfg agentapi.Config) secrets {
	var s secrets
	if cfg.Account != nil {
		s.bridge = cfg.Account.BridgeToken
	}
	if cfg.LLM != nil {
		s.provider = cfg.LLM.APIKey
	}
	if cfg.News != nil {
		s.news = cfg.News
	} else {
		s.news = &agentapi.News{}
	}
	return s
}

// promptName returns the name of the prompt of the file path, eg:
// trend_following for prompts/trend_following.txt.
func promptName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// describePrompt names the prompt for the logs, eg: trend_following v3.
func describePrompt(p llm.Prompt) string {
	if p.Ref.Version > 0 {
		return fmt.Sprintf("%s v%d", p.Ref.Name, p.Ref.Version)
	}
	return p.Ref.Name
}

// backtestContext tells what of the context of the model a backtest of the
// controller sends: the data of the pair as it was then, without what can't
// be rebuilt without the future or isn't there.
var backtestContext = []string{
	"enviados: candles fechados, indicadores, estrutura/SMC, sessão, desenhos e memória de decisões do próprio par",
	"desligados: timeframes superiores, níveis de referência e mercados correlacionados (sem feed histórico no instante)",
	"desligados: calendário econômico e bloqueio de notícias (previsões e revisões só existem depois da divulgação)",
	"ausentes: memória de operações fechadas e histórico do dia da conta (sem a corretora)",
}

func backtestRun(ctx context.Context, settings xtrader.Settings, strategy strategy.Strategy,
	dash dashboard, files []string, fileTimeframe string, fee float64) error {

	if len(files) != len(settings.Pairs) {
		return fmt.Errorf("backtest: a candles CSV per pair is required, see -csv: %d files for %d pairs",
			len(files), len(settings.Pairs))
	}
	if fileTimeframe == "" {
		fileTimeframe = strategy.Timeframe()
	}

	feeds := make([]exchange.PairFeed, 0, len(files))
	for i, file := range files {
		feeds = append(feeds, exchange.PairFeed{Pair: settings.Pairs[i], File: file, Timeframe: fileTimeframe})
	}
	csvFeed, err := exchange.NewCSVFeed(strategy.Timeframe(), feeds...)
	if err != nil {
		return err
	}

	storage, err := storage.FromMemory()
	if err != nil {
		return err
	}
	wallet, err := newPaperWallet(ctx, settings.Pairs, csvFeed, fee)
	if err != nil {
		return err
	}
	chart, err := dash.chart(strategy, wallet, ui.WithCandleWindow(0)) // the whole backtest on the chart
	if err != nil {
		return err
	}

	options := append([]xtrader.Option{
		xtrader.WithBacktest(wallet),
		xtrader.WithStorage(storage),
		xtrader.WithLogLevel(log.WarnLevel),
	}, subscribe(chart)...)
	bot, err := xtrader.NewBot(ctx, settings, wallet, strategy, options...)
	if err != nil {
		return err
	}
	if err := bot.Run(ctx); err != nil {
		return err
	}
	bot.Summary()

	if chart != nil {
		return dash.serve(chart)
	}
	return nil
}

// paper trades a paper wallet on the market data of MT5, its orders and its
// decisions in memory: launched by the controller, its journal kept there, and
// its prompt switched as the controller tells.
func paper(ctx context.Context, settings xtrader.Settings, trader *llm.Trader, dash dashboard,
	venue venue, fee float64, agent *remote.Agent) error {

	feed, err := venue.connect(ctx, settings.Pairs, false)
	if err != nil {
		return err
	}
	trader.SetFeeder(feed) // the higher timeframes and the correlated markets
	if err := venue.checkSizes(feed); err != nil {
		return err
	}
	if agent != nil {
		if err := trader.SetJournal(agent.Journal()); err != nil {
			return err
		}
	}

	storage, err := storage.FromMemory()
	if err != nil {
		return err
	}
	wallet, err := newPaperWallet(ctx, settings.Pairs, feed, fee)
	if err != nil {
		return err
	}
	// the other timeframes shown read from the market data of MT5
	chart, err := dash.chart(trader, wallet, ui.WithCandleSource(feed))
	if err != nil {
		return err
	}
	if chart != nil {
		trader.SetNotifier(chart.Notify) // the results of the reanalyses
	}

	options := append([]xtrader.Option{
		xtrader.WithStorage(storage),
		xtrader.WithPaperWallet(wallet),
	}, subscribe(chart)...)
	bot, err := xtrader.NewBot(ctx, settings, wallet, trader, options...)
	if err != nil {
		return err
	}
	if agent != nil {
		agent.Supervise(bot.Controller()) // its entries, blocked by the controller or not
	}
	// the protection of its positions, the costs those of the paper wallet
	protect.Attach(ctx, bot, wallet, trader.Timeframe(), false, agent, chart)

	news := trader.News()
	if agent != nil && news != nil {
		// the news of the dashboard: held out of the market around the releases
		newsguard.Start(ctx, agent.Config, news, bot.Controller(), storage, settings.Pairs)
	}
	apply := func(runtime agentapi.Runtime) { applyNews(news, runtime) }
	if chart != nil {
		chart.SetOrderController(bot.Controller()) // its orders are simulated too: every channel
		chart.SetSupervision(bot.Controller())
		// the reanalysis asks the model all the same, its orders simulated or
		// not: its switch of the settings, switched while the bot runs
		chart.SetReanalysis(dash.reanalysis)
		apply = func(runtime agentapi.Runtime) {
			applyNews(news, runtime)
			chart.SetReanalysis(runtime.Reanalysis)
		}
		go func() {
			log.Fatal(dash.serve(chart))
		}()
	}
	if agent != nil {
		return agent.Serve(ctx, apply, runBot(bot, trader)) // the switches of the channels are the ones of the live mode
	}
	return bot.Run(ctx)
}

// live trades the account of the bridge, launched by the controller, which
// keeps its orders, its decisions and its journal, and switches its prompt and
// the channels of its control panel while it runs.
func live(ctx context.Context, settings xtrader.Settings, trader *llm.Trader, dash dashboard,
	venue venue, agent *remote.Agent) error {

	broker, err := venue.connect(ctx, settings.Pairs, true)
	if err != nil {
		return err
	}
	if err := venue.checkSizes(broker); err != nil {
		return err
	}

	// the orders of the bot, kept by the controller: the positions it
	// followed, their legs and the history of the chart survive a restart
	orders, err := agent.Orders(ctx)
	if err != nil {
		return err
	}
	trader.SetFeeder(broker) // the higher timeframes and the correlated markets
	trader.SetOrderSource(func(pair string) ([]model.Order, error) {
		stored, err := orders.Orders(storage.WithPair(pair))
		if err != nil {
			return nil, err
		}
		list := make([]model.Order, 0, len(stored))
		for _, order := range stored {
			list = append(list, *order)
		}
		return list, nil
	})
	// the decisions with their justification, linked to the orders, and the
	// journal of the calls to the model, which the memory is read back from
	decisions, err := agent.Decisions(ctx)
	if err != nil {
		return err
	}
	trader.SetDecisionStore(decisions)
	if err := trader.SetJournal(agent.Journal()); err != nil {
		return err
	}

	extra, err := startAccount(trader, broker, agent.Prompts())
	if err != nil {
		return err
	}
	// the other timeframes shown read from the broker
	chart, err := dash.chart(trader, nil, append(extra, ui.WithCandleSource(broker))...)
	if err != nil {
		return err
	}
	options := append(subscribe(chart), xtrader.WithStorage(orders))
	bot, err := xtrader.NewBot(ctx, settings, broker, trader, options...)
	if err != nil {
		return err
	}
	agent.Supervise(bot.Controller()) // its entries, blocked by the controller or not, before it trades
	// the protection of its positions
	protect.Attach(ctx, bot, broker, trader.Timeframe(), true, agent, chart)

	news := trader.News()
	if news != nil {
		// the news of the dashboard: held out of the market around the releases
		newsguard.Start(ctx, agent.Config, news, bot.Controller(), orders, settings.Pairs)
	}
	apply := func(runtime agentapi.Runtime) { applyNews(news, runtime) }
	if chart != nil {
		trader.SetNotifier(chart.Notify) // the results of the reanalyses
		orders.SetNotifier(chart)        // the orders the outbox fails to write
		// the orders of the previous runs, on the chart and in its history
		stored, err := orders.Orders()
		if err != nil {
			return err
		}
		sort.Slice(stored, func(i, j int) bool { return stored[i].ID < stored[j].ID })
		for _, order := range stored {
			if slices.Contains(settings.Pairs, order.Pair) {
				chart.OnOrder(*order)
			}
		}

		// the channels and the reanalysis of the settings, switched while the
		// bot runs
		chart.SetOrderController(bot.Controller(), ui.ChannelWeb)
		chart.SetChannels(dash.channels()...)
		chart.SetSupervision(bot.Controller())
		chart.SetReanalysis(dash.reanalysis)
		apply = func(runtime agentapi.Runtime) {
			applyNews(news, runtime)
			chart.SetChannels(dashboard{tradePanel: runtime.TradePanel, telegram: runtime.TelegramControl}.channels()...)
			chart.SetReanalysis(runtime.Reanalysis)
		}
		go func() {
			log.Fatal(dash.serve(chart))
		}()
	}
	return agent.Serve(ctx, apply, runBot(bot, trader))
}

// runBot returns the run of bot, trading with trader, for remote.Agent.Serve:
// as its context is done, the requests to the model in progress are ended (see
// llm.Trader.Stop), so that the bot stops at once (see xtrader.XTrader.Run)
// rather than with the reply of the candle in progress, its order refused
// anyway.
func runBot(bot *xtrader.XTrader, trader *llm.Trader) func(context.Context) error {
	return func(ctx context.Context) error {
		stop := context.AfterFunc(ctx, trader.Stop)
		defer stop()
		return bot.Run(ctx)
	}
}

// accountBroker is a broker reporting the account and the results of the
// trades as the terminal books them, and the positions open: the MT5 adapter.
type accountBroker interface {
	service.AccountReporter
	OpenPosition(pair string) (exchange.MT5OpenPosition, bool)
}

var _ accountBroker = (*exchange.MT5)(nil)

// startAccount sets the account of broker, when it reports one (see
// accountBroker), as the one of the daily limits of trader and of the
// dashboard (the options returned), then checks the prompt trader starts with
// against it, telling the controller what came of it: see checkPromptAccount.
// Live, the status of that prompt waits for it (see run).
func startAccount(trader *llm.Trader, broker any, prompts llm.PromptReporter) ([]ui.Option, error) {
	var extra []ui.Option
	if account, ok := broker.(accountBroker); ok {
		// the profit of the position as the terminal shows it, polled already
		extra = append(extra, ui.WithBrokerPositions(func(pair string) (ui.BrokerPosition, bool) {
			position, open := account.OpenPosition(pair)
			return ui.BrokerPosition{Side: string(position.Side), Quantity: position.Quantity, Entry: position.Entry,
				Profit: position.Profit, Currency: position.Currency}, open
		}))
		// the account and the profits as the terminal books them, in the
		// currency of the account, eg: BRL: on the dashboard, and for the
		// daily limits of the prompts and the day of the context
		extra = append(extra, ui.WithAccountReporter(account))
		trader.SetAccountReporter(account)
	}
	if err := checkPromptAccount(trader, prompts); err != nil {
		return nil, err
	}
	return extra, nil
}

// checkPromptAccount checks the prompt trader starts with against the account,
// once its reporter is set (see llm.Trader.SetAccountReporter): its daily
// limits in a currency other than the one of the account refuse it. What came
// of it is told to the controller, as for a prompt switched; refused, the
// agent stops, as with a prompt it can't analyze with.
func checkPromptAccount(trader *llm.Trader, prompts llm.PromptReporter) error {
	err := trader.CheckPromptAccount()
	prompts.ReportPrompt(trader.Prompt(), err)
	return err
}

// dashboard holds the options of the API served to the web dashboard.
type dashboard struct {
	addr       string // of the API, none when off
	tradePanel bool   // control panel from the trade panel of the dashboard, in live mode
	telegram   bool   // control panel from the Telegram of the controller, in live mode
	reanalysis bool   // the model asked again between the closes, see ui.Chart.SetReanalysis
	key        string // of the API, of the controller that launched the agent, none run on its own
}

// channels returns the channels the control panel is operated from in live
// mode.
func (d dashboard) channels() []ui.Channel {
	var channels []ui.Channel
	if d.tradePanel {
		channels = append(channels, ui.ChannelWeb)
	}
	if d.telegram {
		channels = append(channels, ui.ChannelTelegram)
	}
	return channels
}

// chart creates the chart of the dashboard, nil when it is disabled. Without a
// paper wallet there is no equity curve to show.
func (d dashboard) chart(strategy strategy.Strategy, wallet *exchange.PaperWallet, extra ...ui.Option) (*ui.Chart,
	error) {
	if d.addr == "" {
		return nil, nil
	}
	options := append([]ui.Option{ui.WithStrategyIndicators(strategy), ui.WithAPIKey(d.key)}, extra...)
	if wallet != nil {
		options = append(options, ui.WithPaperWallet(wallet))
	}
	return ui.New(options...)
}

// serve serves the API of the chart until the server fails.
func (d dashboard) serve(chart *ui.Chart) error {
	fmt.Printf("Dashboard API available at http://%s/api, open it with cmd/controller\n", d.addr)
	return ui.NewHTTPServer(d.addr, chart.Handler()).ListenAndServe()
}

// subscribe feeds the candles of the bot to the chart, if any, and makes it
// the notifier of the bot, whose messages and errors the controller relays to
// Telegram.
func subscribe(chart *ui.Chart) []xtrader.Option {
	if chart == nil {
		return nil
	}
	return []xtrader.Option{xtrader.WithCandleSubscription(chart), xtrader.WithNotifier(chart)}
}

// newPaperWallet simulates a wallet holding 10.000 of the quote of the pairs,
// charged fee on every fill. It holds a single quote currency, which all the
// pairs must share.
func newPaperWallet(ctx context.Context, pairs []string, feed service.Feeder,
	fee float64) (*exchange.PaperWallet, error) {

	_, quote := exchange.SplitAssetQuote(pairs[0])
	for _, pair := range pairs[1:] {
		if _, other := exchange.SplitAssetQuote(pair); other != quote {
			return nil, fmt.Errorf("the paper wallet holds a single currency: %s is quoted in %s, %s in %s", pair,
				other, pairs[0], quote)
		}
	}
	return exchange.NewPaperWallet(
		ctx,
		quote,
		exchange.WithPaperAsset(quote, 10000),
		exchange.WithPaperFee(fee, fee),
		exchange.WithDataFeed(feed),
	), nil
}

// checkVenue checks the exchange, MT5 only, and the leverage against the mode:
// live only, whose margin it needs.
func checkVenue(exchangeName, mode string, leverage float64, backtestConfig bool) error {
	if exchangeName != "mt5" {
		return fmt.Errorf("invalid exchange %q: mt5", exchangeName)
	}
	if leverage > 1 && mode != "live" && !(mode == "backtest" && backtestConfig) {
		return errors.New("-leverage needs the margin of the MT5 account (-mode live), or the margin a backtest " +
			"simulates (-backtest-config)")
	}
	return nil
}

// calendarTokenEnv is the environment variable of the token of the MCP server
// of the terminal, set in its settings, for the agent on its own: kept out of
// its flags. Launched by the controller, the agent gets the token of the
// news of the dashboard, shared by the agents.
const calendarTokenEnv = "MT5_MCP_TOKEN"

// calendarConfig sets the economic calendar of cfg: launched by the
// controller, the news of its dashboard (dashboard: on or off, the MCP server,
// its token and the window of the news block, applied while it runs, see
// newsguard.Apply), the flags -calendar-mcp and -news-block-* ignored; on its
// own, the MCP server of the terminal at url with token (of its environment)
// and the window of the flags. Both behind the switch of the news (see
// llm.NewsSwitch: off, without the server or its token, it reads nothing, and
// the context tells why); the window of the context (-calendar-*) of the
// flags. On its own without url, the flags of the calendar are refused.
func calendarConfig(cfg *llm.Config, set map[string]bool, dashboard *agentapi.News, url, token string,
	currencies []string, ahead, back time.Duration, importance string, offset, before, after time.Duration,
	blockImportance string) error {

	if dashboard == nil && strings.TrimSpace(url) == "" {
		for _, name := range []string{"calendar-currencies", "calendar-ahead", "calendar-back",
			"calendar-importance", "calendar-server-offset", "news-block-before", "news-block-after",
			"news-block-importance"} {
			if set[name] {
				return fmt.Errorf("-%s needs the calendar of -calendar-mcp", name)
			}
		}
		return nil
	}
	var err error
	if cfg.CalendarImportance, err = llm.ParseImportance(importance); err != nil {
		return fmt.Errorf("-calendar-importance: %w", err)
	}
	var news llm.NewsConfig
	if dashboard != nil {
		for _, name := range []string{"calendar-mcp", "news-block-before", "news-block-after", "news-block-importance"} {
			if set[name] {
				log.Warnf("[SETUP] -%s ignored: the news are set on the dashboard (Configurações > Notícias)", name)
			}
		}
		news = newsguard.ConfigOf(dashboard)
	} else {
		blocking, err := llm.ParseImportance(blockImportance)
		if err != nil {
			return fmt.Errorf("-news-block-importance: %w", err)
		}
		news = llm.NewsConfig{Enabled: true, URL: url, Token: token, Before: before, After: after,
			Importance: blocking}
	}
	for i := range currencies {
		currencies[i] = strings.ToUpper(currencies[i])
	}
	cfg.Calendar = llm.NewNewsSwitch(news, llm.WithMCPServerOffset(offset))
	cfg.CalendarCurrencies = currencies
	cfg.CalendarAhead, cfg.CalendarBack = ahead, back
	log.Infof("[SETUP] economic calendar: %s", newsguard.State(news))
	return nil
}

// correlatedByPair reads -llm-correlated: the markets of each pair by its name,
// joined by +, the pairs separated by ;, eg:
// BTCUSDm=DXYm+XAUUSDm;XAUUSDm=DXYm+USTECm, a pair left out without any. One
// list for all the pairs and a comma are refused. It returns nil without any.
func correlatedByPair(pairs []string, value string) (map[string][]string, error) {
	const want = "the markets of each pair by its name, joined by +, the pairs separated by ;, " +
		"eg: BTCUSDm=DXYm+XAUUSDm;XAUUSDm=DXYm+USTECm"
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if strings.Contains(value, ",") {
		return nil, fmt.Errorf("-llm-correlated: %q has a comma: %s", value, want)
	}
	byPair := make(map[string][]string, len(pairs))
	for _, entry := range strings.Split(value, ";") {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		pair, markets, ok := strings.Cut(entry, "=")
		pair = strings.TrimSpace(pair)
		switch _, seen := byPair[pair]; {
		case !ok || pair == "" || strings.Contains(markets, "="):
			return nil, fmt.Errorf("-llm-correlated: %q isn't PAIR=MARKET+MARKET: %s", entry, want)
		case !slices.Contains(pairs, pair):
			return nil, fmt.Errorf("-llm-correlated: %q isn't a pair of -pair (%s)", pair, strings.Join(pairs, ","))
		case seen:
			return nil, fmt.Errorf("-llm-correlated: %s twice", pair)
		}
		for _, market := range strings.Split(markets, "+") {
			switch market = strings.TrimSpace(market); market {
			case "":
				continue
			case pair:
				return nil, fmt.Errorf("-llm-correlated: %s among its own markets", pair)
			}
			byPair[pair] = append(byPair[pair], market)
		}
		if len(byPair[pair]) == 0 {
			return nil, fmt.Errorf("-llm-correlated: no market for %s", pair)
		}
	}
	return byPair, nil
}

// list splits a flag of a list, its items separated by ";" (or "," as
// before), dropping the blanks.
func list(value string) []string {
	var items []string
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == ',' }) {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// applyNews applies the news of runtime to the calendar of the agent while it
// runs, see newsguard.Apply: none without a calendar.
func applyNews(news *llm.NewsSwitch, runtime agentapi.Runtime) {
	if news != nil {
		newsguard.Apply(news, runtime)
	}
}
