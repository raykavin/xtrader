package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
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
// executes, see package llm.
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
//		-mode paper -pair BTCUSDm -lot 0.01
//	# the same with the economic calendar of the terminal in the context, no
//	# entry from 15 minutes before a release of high importance to 15 after
//	MT5_MCP_TOKEN=... go run ./agents/llm/llmagent -llm-provider anthropic -llm-model claude-sonnet-5 \
//		-mode paper -pair XAUUSDm -lot 0.01 -timeframe 5m -calendar-mcp http://127.0.0.1:22346/mcp
//	# a backtest asks the model on every candle of the file: mind the costs
//	go run ./agents/llm/llmagent -llm-provider anthropic -llm-model claude-sonnet-5 \
//		-pair BTCUSDm -lot 0.01 -timeframe 1h -csv btc-1h.csv
//	# a prompt of the database saved to a file, eg: the Supertrend flips of
//	# agents/trendagent, its indicators (and whether its entries go without a
//	# take profit) read from its header
//	go run ./agents/llm/llmagent -llm-provider openai -llm-model gpt-5-mini \
//		-prompt-path supertrend.txt -llm-min-rr 0 -llm-min-stop-atr 0 \
//		-pair BTCUSDm -lot 0.01 -timeframe 15m -csv btc-15m.csv
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
		maxTokens     = flag.Int("llm-max-tokens", 0, "tokens of an answer at most, the reasoning included (default 4096)")
		reasoning     = flag.String("llm-reasoning", "", "effort of the models that reason, eg: gpt-5: minimal, low, medium or high; less spends fewer output tokens (default: the model's; ignored by anthropic)")
		timeout       = flag.Duration("llm-timeout", cfg.Timeout, "time a decision may take, the request to the provider")
		candles       = flag.Int("llm-candles", cfg.Candles, "candles of the context of the model, the last ones")
		confidence    = flag.Float64("llm-min-confidence", cfg.MinConfidence, "confidence, from 0 to 1, under which the entries, exits and moves are ignored")
		maxEntryATR   = flag.Float64("llm-max-entry-atr", cfg.MaxEntryATR, "how far the entry of a decision may be from the close, in ATR, as it is filled at the market (0: no limit)")
		maxLevelATR   = flag.Float64("llm-max-level-atr", cfg.MaxLevelATR, "how far the stop loss and take profit may be from the close, in ATR (0: no limit)")
		widenStop     = flag.Bool("llm-widen-stop", false, "let MODIFY_SL move the stop loss away from the price, adding risk")
		setup         = flag.String("llm-setup", cfg.Setup, "setup of the model: free, or trend_pullback: only with the trend of the SMA 200, on a pullback confirmed by momentum")
		minRR         = flag.Float64("llm-min-rr", cfg.MinRiskReward, "least reward over risk of an entry, net of the spread, eg: 1.5; the system refuses the ones under it (0: none)")
		structure     = flag.Int("llm-structure", cfg.StructureCandles, "candles the price structure of the context is computed on: its swings, levels, round numbers and candle patterns (0: none)")
		higherTFs     = flag.String("llm-higher-timeframes", "", "higher timeframes of the context, comma separated, eg: 1h,4h, read from the exchange with the levels of the previous day and week (default: the two above -timeframe; none: none)")
		higherRows    = flag.Int("llm-higher-candles", cfg.HigherCandles, "candles of each higher timeframe sent as rows, their levels computed on 200")
		correlated    = flag.String("llm-correlated", "", "markets correlated with the pairs, comma separated, read from the exchange on -timeframe, each pair leaving itself out, eg: USTECm,XAUUSDm,DXYm on MT5")
		memory        = flag.Int("llm-memory", cfg.Memory, "last decisions and trades closed of the pair recalled in the context (0: none)")
		warmup        = flag.Int("llm-warmup", 0, "candles kept at least: the room of the prompts switched while the agent runs, whose indicators may need more history than its first one (default: 1000 launched by the controller, else what the prompt needs)")
		indicators    = flag.String("llm-indicators", "", "indicators drawn on the chart and sent to the model, comma separated: groups alone, of "+strings.Join(llm.IndicatorGroups, ", ")+", or kinds with their parameters joined by spaces or +, eg: ema+20+50,supertrend+10+3, of: "+strings.Join(llm.IndicatorKinds, "; ")+" (default: the '# Indicators:' of the prompt, else all the groups)")
		history       = flag.Int("llm-history", cfg.History, "last values of each indicator sent to the model, eg: 6 for the rules that compare a value with the one 5 candles before")
		takeProfitOpt = flag.Bool("llm-take-profit-optional", false, "let the entries go without a take profit, the stop loss alone protecting the position (default: the '# Take profit: optional' of the prompt)")
		noTakeProfit  = flag.Bool("llm-no-take-profit", false, "trade without any take profit: every entry with the stop loss alone, moved with MODIFY_SL until it closes the position, an entry with a take profit refused; -llm-min-rr must be 0 (default: the '# Take profit: none' of the prompt)")
		promptPath    = flag.String("prompt-path", "", "on its own: file of the strategy of the model, its indicators and its take profit in its header, eg: a prompt of the database saved apart (default: the embedded agents/llm/prompts/default.txt; launched by the controller: the prompt of its settings)")
		minStop       = flag.Float64("llm-min-stop-atr", cfg.MinStopATR, "least room of the stop loss of an entry before it is hit, in ATR, eg: 1: a stop inside the noise refused (0: none)")
		higherTrend   = flag.Bool("llm-higher-trend-filter", false, "only the entries with the trend the higher timeframes agree on (all up: longs only; all down: shorts only)")
		higherTrendIn = flag.Bool("llm-higher-trend-required", false, "the trend the higher timeframes agree on required for every entry: -llm-higher-trend-filter, and no entry at all without such a trend (they don't agree, mixed or unknown)")
		maxSpread     = flag.Float64("llm-max-spread-atr", cfg.MaxSpreadATR, "no entry while the spread is wider than that many ATR, eg: 0.1 (0: no limit)")
		language      = flag.String("llm-language", cfg.Language, "language of the reasons of the decisions, eg: pt-BR or en")
		mode          = flag.String("mode", "backtest", "backtest, paper or live")
		exchangeName  = flag.String("exchange", "mt5", "exchange traded: mt5, MetaTrader 5 through tools/mt5bridge")
		pairList      = flag.String("pair", "XAUUSDm", "pairs to trade, comma separated, eg: XAUUSDm,BTCUSDm")
		timeframe     = flag.String("timeframe", cfg.Timeframe, "timeframe of the candles, one decision per candle close")
		direction     = flag.String("direction", "both", "side of the positions opened: both, buy or sell")
		lotList       = flag.String("lot", "", "fixed size of every position, in MT5 lots unless -mt5-size unit; one for all the pairs, or one per pair, comma separated in the order of -pair")
		fraction      = flag.Float64("position-fraction", cfg.PositionFraction, "share of the free balance each position commits (0-1], without -lot")
		leverage      = flag.Float64("leverage", cfg.Leverage, "live: leverage of the positions, at most the account's for the market; needs -position-fraction below 1, or -lot")
		minValue      = flag.Float64("min-position-value", cfg.MinPositionValue, "quote value under which a balance is dust, eg: for pairs not quoted in USD")
		fee           = flag.Float64("fee", 0.001, "backtest and paper: fee of each fill, a share of its value")
		csvFiles      = flag.String("csv", "", "backtest candles, as written by `xtrader download`: a file per pair, comma separated in the order of -pair")
		csvTimeframe  = flag.String("csv-timeframe", "", "timeframe of the backtest candles (default -timeframe)")
		api           = flag.String("api", "", "address to serve the bot API at for the controller (cmd/controller), eg: localhost:5564 (default: off)")
		tradePanel    = flag.Bool("trade-panel", false, "live: enable the trade panel of the web dashboard (manual orders, their moves and cancels, the reanalysis; the dashboard starts and stops the bot without it)")
		telegramCtl   = flag.Bool("telegram-control", false, "live: enable the Telegram controls of the controller")
		currency      = flag.String("currency", "USD", "backtest: currency the symbols are quoted in, eg: GBP for EURGBPm")
		mt5Bridge     = flag.String("mt5-bridge", exchange.MT5DefaultBaseURL, "mt5: address of the bridge (tools/mt5bridge) running with the terminal logged in the account")
		mt5TokenArg   = flag.String("mt5-token", "", "mt5, on its own: X-Bridge-Token of the bridge (default: "+mt5TokenEnv+"; launched by the controller: the token of its settings)")
		mt5MagicArg   = flag.Int64("mt5-magic", 0, "mt5: magic number of the bot's orders and positions, only those count for it (default: a hash of -mt5-comment)")
		mt5Comment    = flag.String("mt5-comment", "nb:llm", "mt5: comment of the bot's orders, 31 characters at most")
		mt5Live       = flag.Bool("mt5-live", false, "mt5: allow a real account, refused otherwise")
		mt5Size       = flag.String("mt5-size", "lot", "mt5: how -lot is read, lot (converted into units with the contract size of the pair) or unit")
		mt5Poll       = flag.Duration("mt5-poll", 0, "mt5: how often the account and the candles are polled (default 1s)")
		calendarURL   = flag.String("calendar-mcp", "", "URL of the MCP server of the MetaTrader 5 terminal, eg: http://127.0.0.1:22346/mcp, for the economic calendar of the context; its token from "+calendarTokenEnv+", or the settings of the controller (default: no calendar)")
		calendarCur   = flag.String("calendar-currencies", "", "currencies of the calendar, comma separated, eg: USD,EUR (default: the ones of the pair)")
		calendarAhd   = flag.Duration("calendar-ahead", cfg.CalendarAhead, "releases of the calendar in the context up to this long after the close")
		calendarBck   = flag.Duration("calendar-back", cfg.CalendarBack, "releases of the calendar in the context from this long before the close")
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
		// stopped by the controller, it sends its records before it exits
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
		keys = secrets{provider: os.Getenv(llm.KeyEnv(*providerName)), bridge: mt5Token(*mt5TokenArg),
			calendar: os.Getenv(calendarTokenEnv)}
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

	pairs := list(*pairList)
	if len(pairs) == 0 {
		return errors.New("-pair: no pair to trade")
	}
	lots, err := sizesByPair("-lot", pairs, list(*lotList))
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
	cfg.Correlated = list(*correlated)
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
	cfg.MinStopATR = *minStop
	cfg.HigherTrendFilter = *higherTrend
	cfg.HigherTrendRequired = *higherTrendIn
	cfg.Language = *language
	cfg.Timeout = *timeout
	cfg.Direction = strategies.RKDirection(*direction)
	cfg.MinPositionValue = *minValue
	cfg.PositionFraction = *fraction
	cfg.Leverage = *leverage
	if err := calendarConfig(&cfg, set, *calendarURL, keys.calendar, list(*calendarCur), *calendarAhd,
		*calendarBck, *calendarImp, *calendarOff, *blockBefore, *blockAfter, *blockImp); err != nil {
		return err
	}
	cfg.PositionSize = lots // shared: the MT5 lots are converted into units before the bot starts
	trader, err := llm.New(cfg, provider, llm.WithPrompt(prompt, overrides))
	if agent != nil {
		// the controller shows what came of the prompt of the settings
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

	if err := checkVenue(*exchangeName, *mode, *leverage); err != nil {
		return err
	}
	log.Infof("[SETUP] LLM: %v, a decision per %s candle close, prompt %s, %d candles kept", provider,
		cfg.Timeframe, describePrompt(prompt), trader.WarmupPeriod())

	settings := xtrader.Settings{Pairs: pairs}
	dash := dashboard{addr: *api, tradePanel: *tradePanel, telegram: *telegramCtl}
	venue := venue{leverage: *leverage, lots: lots,
		mt5: mt5Venue{bridge: *mt5Bridge, token: keys.bridge, magic: mt5Magic(*mt5MagicArg,
			strings.TrimSpace(*mt5Comment)), comment: strings.TrimSpace(*mt5Comment), live: *mt5Live,
			inLots: inLots, poll: *mt5Poll}}

	switch *mode {
	case "backtest":
		if err := venue.backtestPairs(pairs, *currency); err != nil {
			return err
		}
		log.Warnf("[SETUP] backtest: %v is asked on every candle of the files, each request paid", provider)
		return backtest(ctx, settings, trader, dash, list(*csvFiles), *csvTimeframe, *fee)
	case "paper":
		return paper(ctx, settings, trader, dash, venue, *fee, agent)
	case "live":
		return live(ctx, settings, trader, dash, venue, agent)
	}
	return fmt.Errorf("invalid mode %q: backtest, paper or live", *mode)
}

// secrets are the secrets of the agent: the API key of the provider, the token
// of the bridge and the one of the MCP server of the calendar.
type secrets struct {
	provider, bridge, calendar string
}

// secretsOf returns the secrets of the settings of the controller, never read
// from the flags nor from the environment.
func secretsOf(cfg agentapi.Config) secrets {
	var s secrets
	if cfg.Account != nil {
		s.bridge = cfg.Account.BridgeToken
	}
	if cfg.LLM != nil {
		s.provider, s.calendar = cfg.LLM.APIKey, cfg.LLM.CalendarToken
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

func backtest(ctx context.Context, settings xtrader.Settings, strategy strategy.Strategy,
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
	chart, err := dash.chart(strategy, wallet)
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
	chart, err := dash.chart(trader, wallet)
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

	if chart != nil {
		chart.SetOrderController(bot.Controller()) // its orders are simulated too: every channel
		go func() {
			log.Fatal(dash.serve(chart))
		}()
	}
	if agent != nil {
		return agent.Serve(ctx, nil, bot.Run) // the switches of the channels are the ones of the live mode
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

	var extra []ui.Option
	if mt5, ok := broker.(*exchange.MT5); ok {
		// the profit of the position as the terminal shows it, polled already
		extra = append(extra, ui.WithBrokerPositions(func(pair string) (ui.BrokerPosition, bool) {
			position, open := mt5.OpenPosition(pair)
			return ui.BrokerPosition{Side: string(position.Side), Quantity: position.Quantity, Entry: position.Entry,
				Profit: position.Profit, Currency: position.Currency}, open
		}))
		// the account and the profits as the terminal books them, in the
		// currency of the account, eg: BRL
		extra = append(extra, ui.WithAccountReporter(mt5))
	}
	chart, err := dash.chart(trader, nil, extra...)
	if err != nil {
		return err
	}
	options := append(subscribe(chart), xtrader.WithStorage(orders))
	bot, err := xtrader.NewBot(ctx, settings, broker, trader, options...)
	if err != nil {
		return err
	}

	apply := func(agentapi.Runtime) {}
	if chart != nil {
		trader.SetNotifier(chart.Notify) // the results of the reanalyses
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

		// the channels of the settings, switched while the bot runs
		chart.SetOrderController(bot.Controller(), ui.ChannelWeb)
		chart.SetChannels(dash.channels()...)
		apply = func(runtime agentapi.Runtime) {
			chart.SetChannels(dashboard{tradePanel: runtime.TradePanel, telegram: runtime.TelegramControl}.channels()...)
		}
		go func() {
			log.Fatal(dash.serve(chart))
		}()
	}
	return agent.Serve(ctx, apply, bot.Run)
}

// dashboard holds the options of the API served to the web dashboard.
type dashboard struct {
	addr       string // of the API, none when off
	tradePanel bool   // control panel from the trade panel of the dashboard, in live mode
	telegram   bool   // control panel from the Telegram of the controller, in live mode
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
	options := append([]ui.Option{ui.WithStrategyIndicators(strategy)}, extra...)
	if wallet != nil {
		options = append(options, ui.WithPaperWallet(wallet))
	}
	return ui.New(options...)
}

// serve serves the API of the chart until the server fails.
func (d dashboard) serve(chart *ui.Chart) error {
	fmt.Printf("Dashboard API available at http://%s/api, open it with cmd/controller\n", d.addr)
	return http.ListenAndServe(d.addr, chart.Handler())
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
func checkVenue(exchangeName, mode string, leverage float64) error {
	if exchangeName != "mt5" {
		return fmt.Errorf("invalid exchange %q: mt5", exchangeName)
	}
	if leverage > 1 && mode != "live" {
		return errors.New("-leverage needs the margin of the MT5 account: -mode live")
	}
	return nil
}

// calendarTokenEnv is the environment variable of the token of the MCP server
// of the terminal, set in its settings, for the agent on its own: kept out of
// its flags.
const calendarTokenEnv = "MT5_MCP_TOKEN"

// calendarConfig sets the economic calendar of cfg from the flags: the MCP
// server of the terminal at url, and the windows of the context and of the
// news block. Without url the flags of the calendar are refused, but the ones
// of the news block, which need a calendar too.
func calendarConfig(cfg *llm.Config, set map[string]bool, url, token string, currencies []string, ahead,
	back time.Duration, importance string, offset, before, after time.Duration, blockImportance string) error {

	if strings.TrimSpace(url) == "" {
		for _, name := range []string{"calendar-currencies", "calendar-ahead", "calendar-back",
			"calendar-importance", "calendar-server-offset", "news-block-before", "news-block-after",
			"news-block-importance"} {
			if set[name] {
				return fmt.Errorf("-%s needs the calendar of -calendar-mcp", name)
			}
		}
		return nil
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("-calendar-mcp: the token of the MCP server is required in %s (MetaTrader 5: "+
			"Tools > Options > the MCP server)", calendarTokenEnv)
	}

	var err error
	if cfg.CalendarImportance, err = llm.ParseImportance(importance); err != nil {
		return fmt.Errorf("-calendar-importance: %w", err)
	}
	if cfg.NewsBlockImportance, err = llm.ParseImportance(blockImportance); err != nil {
		return fmt.Errorf("-news-block-importance: %w", err)
	}
	calendar, err := llm.NewMCPCalendar(url, token, llm.WithMCPServerOffset(offset))
	if err != nil {
		return err
	}
	for i := range currencies {
		currencies[i] = strings.ToUpper(currencies[i])
	}
	cfg.Calendar, cfg.CalendarCurrencies = calendar, currencies
	cfg.CalendarAhead, cfg.CalendarBack = ahead, back
	cfg.NewsBlockBefore, cfg.NewsBlockAfter = before, after
	log.Infof("[SETUP] economic calendar: %v, news block %s before and %s after the %s releases", calendar,
		before, after, cfg.NewsBlockImportance)
	return nil
}

// list splits a comma separated flag, dropping the blanks.
func list(value string) []string {
	var items []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}
