package runner

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/internal/backtest"
	"github.com/raykavin/xtrader/agents/internal/entryfilter"
	"github.com/raykavin/xtrader/agents/internal/flaglist"
	"github.com/raykavin/xtrader/agents/internal/newsguard"
	"github.com/raykavin/xtrader/agents/internal/protect"
	"github.com/raykavin/xtrader/agents/internal/remote"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/agents/metalabel"
	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

// Agent is a binary running strategies (see Run): its name, of its usage, and
// its strategies, the default one first.
type Agent struct {
	Name       string
	Strategies []Strategy
}

// DefaultStrategy returns the strategy the agent trades without -strategy, its
// first one, "" for none: the one of agentapi.DefaultStrategy for its kind, as
// the comment of its orders on MT5, and so its magic number, depend on it.
func (a Agent) DefaultStrategy() string {
	if len(a.Strategies) == 0 {
		return ""
	}
	return a.Strategies[0].Name
}

// Strategy is a strategy of an agent: its name for -strategy, what it trades,
// the flags it takes of its own (see BracketInputs), refused with the other
// strategies of the agent, and how it is created from the inputs of the agent.
type Strategy struct {
	Name   string
	About  string
	Inputs []string
	New    func(in Inputs) (strategy.Strategy, error)
}

// Inputs are the inputs of the strategies shared by the agents: the flags set,
// the timeframe (for the bracket strategies, only when set: each keeps its
// own), the side of the positions, the stops, the targets, the sizing and the
// max risk of an entry.
type Inputs struct {
	Set                          map[string]bool
	Timeframe                    string
	Direction                    strategies.RKDirection
	StopATR, TrailingATR         float64
	TakeProfitATR, MinATRPct     float64
	MaxBars                      int
	StopValue, TakeProfitValue   float64
	MinValue, Fraction, Leverage float64
	Lots                         map[string]float64
	MaxRiskPct                   float64
}

// BracketInputs are the flags of Inputs.Bracket, the ones of the strategies
// placing each entry with its stop loss and take profit at the broker.
var BracketInputs = []string{"stop-atr", "take-profit-atr", "max-bars", "min-atr-pct"}

// Bracket sets the exits and the sizing of a bracket strategy from the inputs,
// its timeframe when -timeframe is set, and its max risk when -max-risk-pct is
// set. Unset, each keeps its default.
func (in Inputs) Bracket(cfg *strategies.BracketConfig, timeframe *string) {
	if in.Set["timeframe"] {
		*timeframe = in.Timeframe
	}
	if in.Set["stop-atr"] {
		cfg.StopATR = in.StopATR
	}
	if in.Set["take-profit-atr"] {
		cfg.TakeProfitATR = in.TakeProfitATR
	}
	if in.Set["max-bars"] {
		cfg.MaxBars = in.MaxBars
	}
	cfg.MinATRPct = in.MinATRPct
	cfg.StopValue = in.StopValue
	cfg.TakeProfitValue = in.TakeProfitValue
	cfg.Direction = in.Direction
	cfg.MinPositionValue = in.MinValue
	cfg.PositionFraction = in.Fraction
	cfg.Leverage = in.Leverage
	cfg.PositionSize = in.Lots
	if in.Set["max-risk-pct"] {
		cfg.MaxRiskPct = in.MaxRiskPct
	}
}

// strategyInputs are the flags of Run that only some strategies take: set
// with a strategy that doesn't, they are refused (see checkInputs), even when
// no strategy of the agent takes them, eg: -trailing-atr on intradayagent.
var strategyInputs = append(slices.Clone(BracketInputs), "trailing-atr")

// Signals returns the signal strategies of names, see strategies.NewSignal:
// each takes the inputs of Inputs.Bracket, and -trailing-atr the ones trailing
// their stop loss, eg: donchianatr (see TrailingSignals).
func Signals(names ...string) []Strategy {
	return signals(false, names)
}

// TrailingSignals is Signals, each strategy taking -trailing-atr, which trails
// its stop loss (strategies.SignalConfig.TrailingATR): the strategies of
// momentumagent, which took it before it was declared.
func TrailingSignals(names ...string) []Strategy {
	return signals(true, names)
}

// signals returns the signal strategies of names, the ones trailing their stop
// loss by default, or all of them when trailing, taking -trailing-atr: set,
// applied by the ones taking it only.
func signals(trailing bool, names []string) []Strategy {
	list := make([]Strategy, 0, len(names))
	for _, name := range names {
		about, ok := strategies.SignalAbout(name)
		if !ok {
			panic("runner: no signal strategy " + name)
		}
		trails := trailing || strategies.DefaultSignalConfig(name).TrailingATR > 0
		inputs := BracketInputs
		if trails {
			inputs = append(slices.Clone(BracketInputs), "trailing-atr")
		}
		list = append(list, Strategy{Name: name, About: about, Inputs: inputs,
			New: func(in Inputs) (strategy.Strategy, error) {
				cfg := strategies.DefaultSignalConfig(name)
				in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
				if trails && in.Set["trailing-atr"] {
					cfg.TrailingATR = in.TrailingATR
				}
				return strategies.NewSignal(name, cfg)
			}})
	}
	return list
}

// Run runs a strategy of agent on MetaTrader 5 symbols, from the command line:
// the shared flags defined here, the ones of its strategies by the agent
// before:
//
//	# backtest on the candles written by `xtrader download`, 0.05 lot of gold
//	# and 0.01 of bitcoin, converted with the contract sizes the package knows
//	# (100 ounces, 1 bitcoin)
//	go run ./agents/trendagent -pair XAUUSDm,BTCUSDm -csv xau-1h.csv,btc-1h.csv -lot 0.05,0.01
//	# the same on gold, with a stop 3 ATR from the entry close
//	go run ./agents/trendagent -pair XAUUSDm -csv xau-1h.csv -lot 0.05 -stop-atr 3
//	# a symbol of unknown contract size, eg: silver, 5000 ounces in a lot
//	go run ./agents/trendagent -pair XAGUSDm -csv xag-1h.csv -lot 0.01 -mt5-contract-size 5000
//	# the market data of the bridge, trading a paper wallet
//	go run ./agents/trendagent -mode paper -pair XAUUSDm -lot 0.05
//	# the same, the stop loss and the take profit placed with the entry, a
//	# stop loss losing 1 and a take profit earning 2 in the currency of the
//	# account (of the simulation in paper: the quote, USD for XAUUSDm) in place
//	# of the ones in ATR
//	go run ./agents/trendagent -strategy matrend -mode paper -pair XAUUSDm -lot 0.05 \
//		-stop-value 1 -take-profit-value 2
//
// The live mode takes the same flags from the settings of the agent in the
// database of the controller, eg: -strategy matrend -pair XAUUSDm -lot 0.05
// -broker-stop, or -position-fraction 0.1 -leverage 20 for positions of 10%
// of the free margin at 1:20, or -mt5-comment nb:orb-ny for a second bot of
// a strategy on the account, with a magic number of its own.
//
// MT5 trades the account the terminal is logged in, through the bridge of
// -mt5-bridge (tools/mt5bridge/mt5bridge.py, running with the terminal), and
// refuses a real account unless -mt5-live is set. The account must be hedging,
// as the ones of Exness: the adapter nets the positions of the bot, the ones of
// its magic number, -mt5-magic (by default a hash of its comment, -mt5-comment,
// "nb:" and the strategy name unless set), so bots of magic numbers of their
// own share an account, next to the manual trades: two bots of a strategy on
// one account need comments of their own, eg: nb:emacross-15m and
// nb:emacross-1h. -lot is read in MT5 lots and converted into units of the
// base asset with the contract size of the symbol (100 ounces a lot of
// XAUUSDm), or in units with -mt5-size unit; a backtest takes the contract
// sizes of -mt5-contract-size, else the ones the package knows. The paper mode
// reads the market data of the bridge only. Leverage needs the margin of the
// account, which the paper wallet doesn't simulate, so it is for the live mode
// only.
//
// The live mode runs launched by the controller (cmd/controller), from the
// settings of the agent in its database, its flags and the token of the
// bridge, which keeps its orders (see package remote): the order controller
// replays them on a restart, so the positions it followed and the history of
// the chart are the bot's own. The backtest and paper modes keep them in
// memory.
//
// -api serves the bot to the controller, at an address such as localhost:5556:
// once the backtest ends, or while the bot trades in paper and live modes. The
// controller serves the bots of its database on the web dashboard and on
// Telegram, relaying the orders, messages and errors of the bot. Keep the
// address on localhost, as its control panel (start/stop and manual orders)
// operates the bot: in paper mode from the trade panel of the dashboard and
// from Telegram, in live mode from the channels its settings turn on, the
// trade panel and the Telegram controls, switched while it runs; the
// dashboard starts and stops the bot whatever its trade panel, which gates the
// orders only.
func Run(agent Agent) {
	if err := run(agent); err != nil {
		log.Fatal(err)
	}
}

func run(agent Agent) error {
	if len(agent.Strategies) == 0 {
		return errors.New(agent.Name + ": no strategy")
	}
	names := make([]string, 0, len(agent.Strategies))
	about := make([]string, 0, len(agent.Strategies))
	for _, s := range agent.Strategies {
		names = append(names, s.Name)
		about = append(about, fmt.Sprintf("%s (%s)", s.Name, s.About))
	}
	var (
		strategyName = flag.String("strategy", agent.DefaultStrategy(), "strategy traded: "+strings.Join(about, ", "))
		mode         = flag.String("mode", "backtest", "backtest, paper or live")
		exchangeName = flag.String("exchange", "mt5", "exchange traded: mt5, MetaTrader 5 through tools/mt5bridge")
		currency     = flag.String("currency", "USD", "backtest: currency the symbols are quoted in, eg: GBP for EURGBPm")
		mt5Bridge    = flag.String("mt5-bridge", exchange.MT5DefaultBaseURL, "mt5: address of the bridge (tools/mt5bridge) running with the terminal logged in the account")
		mt5TokenArg  = flag.String("mt5-token", "", "mt5, on its own: token of the bridge, its --token (default: "+mt5TokenEnv+"; launched by the controller: the token of its settings)")
		mt5MagicArg  = flag.Int64("mt5-magic", 0, "mt5: magic number of the bot's orders and positions, only those count for it, so bots with magic numbers of their own share an account (default: a hash of -mt5-comment)")
		mt5CommentAr = flag.String("mt5-comment", "", "mt5: comment of the bot's orders, at most 31 characters, whose hash is the default magic number (default \"nb:\" and the strategy name)")
		mt5Live      = flag.Bool("mt5-live", false, "mt5: allow a real account, refused otherwise")
		mt5Size      = flag.String("mt5-size", "lot", "mt5: how -lot is read, lot (MT5 lots, converted into units of the base asset with the contract size of the pair) or unit")
		mt5CSize     = flag.String("mt5-contract-size", "", "mt5 backtest: units of the base asset in a lot, to convert -lot, eg: 100 for XAUUSDm; one for all the pairs, one per pair in the order of -pair, or one by pair, eg: XAUUSDm=100 (default: the ones the package knows)")
		mt5Poll      = flag.Duration("mt5-poll", 0, "mt5: how often the account and the candles are polled (default 1s)")
		pairList     = flag.String("pair", agentapi.DefaultPair, "pairs to trade, separated by ;, eg: XAUUSDm;BTCUSDm")
		timeframe    = flag.String("timeframe", "1h", "strategy timeframe (the strategies with a stop loss and a take profit at the broker: default the strategy's)")
		direction    = flag.String("direction", "both", "side of the positions opened: both, buy or sell")
		stopATR      = flag.Float64("stop-atr", 0, "stop from the entry close, in ATR: trendconfluence: checked on the close (0: off); the strategies with a stop loss at the broker: placed there (default: the strategy's)")
		trailingATR  = flag.Float64("trailing-atr", 0, "trailing stop from the best price since the entry, in ATR, for the strategies that take it, eg: donchianatr, refused with the others (default: the strategy's)")
		takeProfit   = flag.Float64("take-profit-atr", 0, "take profit at the broker from the entry close, in ATR (default: the strategy's); the signal strategies take 0 for none")
		maxBars      = flag.Int("max-bars", 0, "close a trade still open after that many candles, 0 never (default: the strategy's)")
		minATRPct    = flag.Float64("min-atr-pct", 0, "skip the entries while the ATR is under that % of the close, for the costs, eg: 0.025 for gold")
		stopValue    = flag.Float64("stop-value", 0, "fixed stop loss at the broker, in place of the stops in ATR: what the position loses at it, in the currency of the account (paper: the quote), eg: 5 for five reais on a BRL account (0: off; mt5 or the paper wallet)")
		profitValue  = flag.Float64("take-profit-value", 0, "fixed take profit at the broker, in place of the one in ATR: what the position earns at it, in the currency of the account (paper: the quote), eg: 10 for ten reais on a BRL account (0: off; mt5 or the paper wallet)")
		minValue     = flag.Float64("min-position-value", 10, "value under which a balance is dust, in the currency of the account (paper: the quote)")
		fee          = flag.Float64("fee", 0.001, "backtest and paper: fee of each fill, a share of its value; MT5 CFDs charge the spread instead: half of it over the price, eg: 0.0003")
		fraction     = flag.Float64("position-fraction", 1, "share of the free balance each position commits (0-1]")
		lotList      = flag.String("lot", "", "fixed size of every position, a lot, instead of a share of the balance (-position-fraction): in MT5 lots unless -mt5-size unit, eg: 0.01; one for all the pairs, one per pair, separated by ; in the order of -pair, or one by pair, eg: BTCUSDm=0.03;XAUUSDm=0.01; the decimals with . (a decimal comma, eg: 1,5, is refused)")
		leverage     = flag.Float64("leverage", 1, "live: leverage of the positions, at most the account's for the market; needs -position-fraction below 1, or -lot")
		maxRisk      = flag.Float64("max-risk-pct", 0, "skip an entry losing more than that % of the free balance at its stop loss, both in the currency of the account (0: no limit; trendconfluence needs a stop: -stop-atr, -trailing-atr, -broker-stop-atr, -broker-trailing-atr or -stop-value)")
		csvFiles     = flag.String("csv", "", "backtest candles, as written by `xtrader download`: a file per pair, separated by ; in the order of -pair")
		csvTimeframe = flag.String("csv-timeframe", "", "timeframe of the backtest candles (default -timeframe)")
		printWarmup  = flag.Bool(agentapi.PrintWarmupFlag, false, "prints the warmup of the strategy, its candles and its timeframe, as JSON (agentapi.Warmup), and exits: what a backtest reads before its period")
		btConfig     = flag.String("backtest-config", "", "backtest: the JSON file of a backtest as the controller asks for it (period, balance, costs, symbols, protection, daily limits, report): the run writes its report there and exits (see agents/internal/backtest)")
		api          = flag.String("api", "", "address to serve the bot API at for the controller (cmd/controller), eg: localhost:5556 (default: off)")
		tradePanel   = flag.Bool("trade-panel", false, "live: enable the trade panel of the web dashboard (manual orders, their moves and cancels; the dashboard starts and stops the bot without it)")
		telegramCtl  = flag.Bool("telegram-control", false, "live: enable the Telegram controls of the controller (/start, /stop, /buy, /sell, /status, /balance)")
		quarantine   = flag.Bool("outbox-quarantine", false, "launched by the controller: an outbox that can't be read (data/outbox/<agent>-<id>.db, eg: written over in the middle) is moved aside to <file>.corrupt-<time>, kept for a look, and the agent starts with an empty one, its records never sent to the controller (default: the agent doesn't start, telling which file)")
	)
	// the entry filter, shared by every strategy (see entryfilter.Config)
	filterConfig := entryfilter.RegisterFlags(flag.CommandLine)
	// launched by the controller, the flags and the token of its settings
	ctx := context.Background()
	remoteAgent, err := remote.Connect(ctx)
	if err != nil {
		return err
	}
	var token string
	if remoteAgent != nil {
		// stopped by the controller, it stops trading, then sends its records (see remote.Agent.Serve)
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		if len(os.Args) > 1 {
			return errors.New("launched by the controller, the agent takes the flags of its settings, not of " +
				"the command line")
		}
		if err := flag.CommandLine.Parse(remoteAgent.Args()); err != nil {
			return err
		}
		remoteAgent.QuarantineOutbox = *quarantine
		if err := remoteAgent.Open(filepath.Join("data", "outbox")); err != nil {
			return err
		}
		defer func() {
			if err := remoteAgent.Close(); err != nil {
				log.Warn(err)
			}
		}()
		if account := remoteAgent.Config.Account; account != nil {
			token = account.BridgeToken
		}
	} else {
		flag.Parse()
		token = mt5Token(*mt5TokenArg)
	}
	switch {
	case remoteAgent == nil && *mode == "live":
		return errors.New("-mode live runs launched by the controller (cmd/controller), which keeps the orders " +
			"of the agent")
	case remoteAgent != nil && *mode == "backtest":
		return errors.New("the controller runs the agents in paper and live modes only")
	case *btConfig != "" && *mode != "backtest":
		return errors.New("-backtest-config is for -mode backtest")
	}
	var btCfg backtest.Config
	if *btConfig != "" {
		if btCfg, err = backtest.ReadConfig(*btConfig); err != nil {
			return err
		}
	}

	// the inputs of each strategy are refused with the others
	var chosen *Strategy
	for i := range agent.Strategies {
		if agent.Strategies[i].Name == *strategyName {
			chosen = &agent.Strategies[i]
		}
	}
	if chosen == nil {
		return fmt.Errorf("invalid strategy %q: %s", *strategyName, strings.Join(names, ", "))
	}
	set := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if err := agent.CheckInputs(chosen.Name, set); err != nil {
		return err
	}

	pairs := list(*pairList)
	if len(pairs) == 0 {
		return errors.New("-pair: no pair to trade")
	}
	lots, err := sizesOf("-lot", pairs, *lotList)
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
	if set["mt5-contract-size"] && (lots == nil || !inLots) {
		return errors.New("-mt5-contract-size converts the lots of -lot, which needs -lot and -mt5-size lot")
	}
	// a fixed stop loss or take profit replaces the ones in ATR, and the levels
	// of fibonacci
	for fixed, inATR := range map[string][]string{
		"stop-value": {"stop-atr", "trailing-atr", "stop-buffer-atr", "broker-stop-atr", "broker-trailing-atr",
			"fib-stop-level", "fib-stop-buffer-atr"},
		"take-profit-value": {"take-profit-atr", "fib-target", "fib-target-level", "fib-reward-risk"},
	} {
		for _, name := range inATR {
			if set[fixed] && set[name] {
				return fmt.Errorf("-%s and -%s are exclusive: the fixed value replaces it", fixed, name)
			}
		}
	}

	side := strategies.RKDirection(*direction)
	trader, err := chosen.New(Inputs{Set: set, Timeframe: *timeframe, Direction: side,
		StopATR: *stopATR, TrailingATR: *trailingATR, TakeProfitATR: *takeProfit, MinATRPct: *minATRPct,
		MaxBars: *maxBars, StopValue: *stopValue, TakeProfitValue: *profitValue, MinValue: *minValue,
		Fraction: *fraction, Leverage: *leverage, Lots: lots, MaxRiskPct: *maxRisk})
	if err != nil {
		return err
	}
	// filtered, its warmup the filters' too, before -print-warmup tells it
	filters, err := filterConfig()
	if err != nil {
		return err
	}
	if err := checkFilter(chosen.Name, pairs, filters); err != nil {
		return err
	}
	filters.Agent, filters.Strategy, filters.Source = agent.Name, chosen.Name, sampleSource(*mode)
	switch {
	case remoteAgent != nil:
		filters.Agent = remoteAgent.Config.Name
	case btCfg.Agent != "":
		filters.Agent = btCfg.Agent
	}
	if *btConfig != "" {
		filters.RecordFrom = time.Unix(btCfg.From, 0).UTC() // the samples of the period, not of its warm-up
		filters.Run, filters.Segment = btCfg.Run, btCfg.Segment
	}
	if trader, err = entryfilter.Wrap(trader, filters); err != nil {
		return err
	}
	if *printWarmup { // nothing connected, nothing run
		return json.NewEncoder(os.Stdout).Encode(agentapi.Warmup{Timeframe: trader.Timeframe(),
			Candles: trader.WarmupPeriod(), References: filters.References(), Releases: usesReleases(trader)})
	}

	settings := xtrader.Settings{
		Pairs: pairs,
	}
	if err := checkVenue(*exchangeName, *mode, set, *leverage, *btConfig != ""); err != nil {
		return err
	}
	dash := dashboard{addr: *api, tradePanel: *tradePanel, telegram: *telegramCtl}
	if remoteAgent != nil {
		dash.key = remoteAgent.APIKey
	}
	comment := mt5Comment(*mt5CommentAr, chosen.Name)
	venue := venue{leverage: *leverage, lots: lots,
		mt5: mt5Venue{bridge: *mt5Bridge, token: token, magic: mt5Magic(*mt5MagicArg, comment),
			comment: comment, live: *mt5Live, inLots: inLots, poll: *mt5Poll}}
	if remoteAgent != nil {
		// the positions the controller keeps apart from the ones of the other agents
		book := agentapi.MT5Book{Bridge: venue.mt5.bridge, Magic: venue.mt5.magic, Comment: comment, Pairs: pairs}
		if err := remoteAgent.Config.CheckMT5Book(book); err != nil {
			return err
		}
	}

	switch *mode {
	case "backtest":
		// the symbols are registered when connecting, which a backtest doesn't
		for _, pair := range pairs {
			exchange.RegisterMT5Pair(pair, "", *currency)
		}
		if lots != nil && inLots {
			contractSizes, err := flaglist.Sizes("-mt5-contract-size", *mt5CSize)
			if err != nil {
				return err
			}
			contracts, err := mt5Contracts(pairs, contractSizes)
			if err != nil {
				return err
			}
			// the strategy shares the map, and trades the units
			if err := lotsToUnits(lots, contracts); err != nil {
				return err
			}
			for _, pair := range pairs {
				log.Infof("[SETUP] %s: positions of %s", pair, describeSize(pair, lots[pair], contracts[pair]))
			}
		}
		if *btConfig != "" {
			if err := checkInSample(filters.MetaModel, btCfg); err != nil {
				return err
			}
			cfg := btCfg
			cfg.Leverage = max(*leverage, 1) // the margin of the leverage the strategy sizes with
			hooks := backtestHooks(trader, cfg)
			if chosen.Name == pairsStrategy {
				cfg, hooks = withoutProtection(cfg, hooks)
			}
			// canceled by the controller: the report of what ran, then the exit
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			return backtest.Run(ctx, settings, trader, list(*csvFiles), *csvTimeframe, cfg, hooks)
		}
		if len(filters.References()) > 0 {
			return errors.New("-intermarket-symbol reads the candles of its reference as the strategy runs, which " +
				"a backtest of -csv files can't without looking ahead: run it through -backtest-config (the " +
				"backtests of the controller), or in paper mode")
		}
		if usesReleases(trader) {
			return fmt.Errorf("-strategy %s trades around the releases of the economic calendar, which a backtest "+
				"of -csv files doesn't have: they come only in the backtests of the controller (-backtest-config), "+
				"and in paper and live modes launched by the controller, from the news of its dashboard; here it "+
				"would never enter", chosen.Name)
		}
		return backtestRun(ctx, settings, trader, dash, list(*csvFiles), *csvTimeframe, *fee)
	case "paper":
		return paper(ctx, settings, trader, chosen.Name, dash, venue, *fee, remoteAgent)
	case "live":
		return live(ctx, settings, trader, chosen.Name, dash, venue, remoteAgent)
	}
	return fmt.Errorf("invalid mode %q: backtest, paper or live (a real account needs -mt5-live)", *mode)
}

// backtestHooks returns the hooks of a backtest of s as the controller asks for
// it (see backtest.Run): the candles of the symbols it reads but doesn't trade
// (the references of cfg, closed by the simulated now only), and the releases
// of the calendar of cfg on the clock of the run, when it reads them; none
// read by the controller (the news off or unreadable), it waits for them.
func backtestHooks(s strategy.Strategy, cfg backtest.Config) backtest.Hooks {
	return backtest.Hooks{
		Candles: func(source backtest.CandleSource) { giveCandles(s, source) },
		Prepare: func(feed *exchange.BacktestFeed) {
			if cfg.Releases != nil {
				giveReleases(s, backtestReleases{byPair: cfg.Releases, now: feed.Now})
			}
		},
	}
}

// pairsStrategy is the strategy of intradayagent trading the spread of two
// cointegrated legs (strategies.Pairs): its legs, a pair each, are one
// position, opened and closed together at the market, without a stop loss of
// their own.
const pairsStrategy = "pairs"

// checkFilter fails on an entry filter (see entryfilter.Config) the strategy
// name can't take on pairs, the pairs of the agent: any for the pairs
// strategy, which the filter would judge leg by leg, as two strategies (the
// intermarket filter would refuse every entry of its second leg, the strategy
// then opening the first and closing it alone on every signal; the equity
// curve and the samples would follow a leg apart from the other); and a pair
// of -intermarket-pairs not in pairs, a typo filtering none of them.
func checkFilter(name string, pairs []string, filters entryfilter.Config) error {
	if name == pairsStrategy && filters.Enabled() {
		return errors.New("-strategy pairs trades two legs as one position, which the entry filter can't judge: " +
			"it would judge each leg apart (eg: the intermarket filter refusing every entry of the second leg, " +
			"the first opened and closed alone on every signal): run it without -regime, -regime-max-atr-pct, " +
			"-equity-curve, -meta-model, -meta-record and -intermarket-symbol")
	}
	for _, pair := range filters.IntermarketPairs {
		if !slices.Contains(pairs, pair) {
			return fmt.Errorf("-intermarket-pairs: %q isn't a pair of -pair (%s): the filter would filter none of "+
				"the pairs it names", pair, strings.Join(pairs, ";"))
		}
	}
	return nil
}

// sampleSource returns where the samples of the entry filter are recorded in
// mode (see metalabel.Sample.Source): live, paper, else a backtest.
func sampleSource(mode string) string {
	switch mode {
	case "live":
		return metalabel.SourceLive
	case "paper":
		return metalabel.SourcePaper
	}
	return metalabel.SourceBacktest
}

// checkInSample fails a backtest of cfg whose entries the model of path
// (-meta-model, "" for none) filters, on a period starting before the newest
// outcome the model learned (metalabel.Model.TrainedThrough): the model would
// judge the entries it was trained on, in-sample, and the backtest tell
// nothing of how it does on entries it never saw. A model written before it
// kept that time passes.
func checkInSample(path string, cfg backtest.Config) error {
	if path == "" {
		return nil
	}
	model, err := metalabel.Load(path)
	if err != nil {
		return fmt.Errorf("-meta-model: %w", err)
	}
	if cfg.From < model.TrainedThrough {
		through := time.Unix(model.TrainedThrough, 0).UTC().Format(time.RFC3339)
		return fmt.Errorf("-meta-model: the model %s learned the outcomes of entries until %s, after the start of "+
			"the backtest (%s): the period would be in-sample, the model judging the entries it was trained on; "+
			"backtest from %s on, or train it on the samples before the period", path, through,
			time.Unix(cfg.From, 0).UTC().Format(time.RFC3339), through)
	}
	return nil
}

// withoutProtection returns cfg without the protection of the agent (the
// breakeven and the trailing stop of the batch) and hooks with a warning of
// the report when the batch set one: for the pairs strategy, whose legs carry
// no stop loss, a stop of the protection on one leg closing it apart from the
// other (see attachProtection).
func withoutProtection(cfg backtest.Config, hooks backtest.Hooks) (backtest.Config, backtest.Hooks) {
	if cfg.Protection != nil && cfg.Protection.Active() {
		hooks.Warnings = append(hooks.Warnings, "a proteção do lote (breakeven e trailing stop) foi ignorada: a "+
			"estratégia pairs opera duas pernas como uma posição, sem stop, e a proteção encerraria uma perna "+
			"sozinha")
	}
	cfg.Protection = nil
	return cfg, hooks
}

// attachProtection protects the positions of bot with the breakeven and the
// trailing stop of the agent (see protect.Attach), but for the pairs strategy
// (named name): its legs carry no stop loss and are one position, which a
// stop moved on one leg alone would close apart from the other; told once, at
// the start, the settings of the agent left unapplied. It returns the
// protection, nil for none.
func attachProtection(ctx context.Context, bot *xtrader.XTrader, market protect.Market, s strategy.Strategy,
	name string, live bool, agent *remote.Agent, chart *ui.Chart) *protect.Manager {
	if name == pairsStrategy {
		if agent != nil {
			log.Warnf("[SETUP] -strategy pairs: the breakeven and the trailing stop of the agent stay off, its " +
				"legs carrying no stop loss: a stop moved on one leg would close it alone")
		}
		return nil
	}
	return protect.Attach(ctx, bot, market, s.Timeframe(), live, agent, chart)
}

// CheckInputs fails on a flag set that the strategy name of the agent doesn't
// take, see checkInputs.
func (a Agent) CheckInputs(name string, set map[string]bool) error {
	return checkInputs(a.Strategies, name, set)
}

// checkInputs fails on a flag set that belongs to other strategies of the
// agent than name: the inputs of each strategy are refused with the others,
// and the inputs of Run that none of them takes (strategyInputs, eg:
// -trailing-atr on intradayagent) refused too, rather than ignored.
func checkInputs(list []Strategy, name string, set map[string]bool) error {
	flags := make([]string, 0, len(set))
	for flagName := range set {
		flags = append(flags, flagName)
	}
	slices.Sort(flags) // the same error whatever the order of the map
	for _, flagName := range flags {
		var owners []string
		for _, s := range list {
			if slices.Contains(s.Inputs, flagName) {
				owners = append(owners, s.Name)
			}
		}
		switch {
		case len(owners) > 0 && !slices.Contains(owners, name):
			slices.Sort(owners)
			return fmt.Errorf("-%s is an input of -strategy %s", flagName, strings.Join(owners, " and "))
		case len(owners) == 0 && slices.Contains(strategyInputs, flagName):
			return fmt.Errorf("-%s is an input of no strategy of the agent", flagName)
		}
	}
	return nil
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
		xtrader.WithBacktest(wallet), // Required for Backtest mode
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

func paper(ctx context.Context, settings xtrader.Settings, strategy strategy.Strategy, name string,
	dash dashboard, venue venue, fee float64, agent *remote.Agent) error {

	feed, err := venue.connect(ctx, settings.Pairs, false)
	if err != nil {
		return err
	}
	if err := venue.checkSizes(feed); err != nil {
		return err
	}
	giveCandles(strategy, feed) // the symbols it reads but doesn't trade, from the market data of MT5

	storage, err := storage.FromMemory()
	if err != nil {
		return err
	}

	wallet, err := newPaperWallet(ctx, settings.Pairs, feed, fee)
	if err != nil {
		return err
	}
	// the other timeframes shown read from the market data of MT5
	chart, err := dash.chart(strategy, wallet, ui.WithCandleSource(feed))
	if err != nil {
		return err
	}

	options := append([]xtrader.Option{
		xtrader.WithStorage(storage),
		xtrader.WithPaperWallet(wallet),
	}, subscribe(chart)...)

	bot, err := xtrader.NewBot(ctx, settings, wallet, strategy, options...)
	if err != nil {
		return err
	}
	if agent != nil {
		agent.Supervise(bot.Controller()) // its entries, blocked by the controller or not
	}
	// the protection of its positions, the costs those of the paper wallet
	attachProtection(ctx, bot, wallet, strategy, name, false, agent, chart)

	if chart != nil {
		chart.SetOrderController(bot.Controller()) // its orders are simulated too: every channel
		chart.SetSupervision(bot.Controller())
		go func() {
			log.Fatal(dash.serve(chart))
		}()
	}
	if agent != nil {
		// the news of the dashboard: held out of the market around the releases
		news := llm.NewNewsSwitch(newsguard.ConfigOf(agent.Config.News))
		newsguard.Start(ctx, agent.Config, news, bot.Controller(), storage, settings.Pairs)
		giveReleases(strategy, newsReleases{news: news}) // their calendar, for a strategy trading around them
		apply := func(runtime agentapi.Runtime) { newsguard.Apply(news, runtime) }
		return agent.Serve(ctx, apply, bot.Run) // the switches of the channels are the ones of the live mode
	}
	return bot.Run(ctx)
}

func live(ctx context.Context, settings xtrader.Settings, strategy strategy.Strategy, name string,
	dash dashboard, venue venue, agent *remote.Agent) error {

	broker, err := venue.connect(ctx, settings.Pairs, true)
	if err != nil {
		return err
	}
	if err := venue.checkSizes(broker); err != nil {
		return err
	}
	giveCandles(strategy, broker) // the symbols it reads but doesn't trade, from the broker

	// the orders of the bot, kept by the controller
	orders, err := agent.Orders(ctx)
	if err != nil {
		return err
	}
	// the other timeframes shown read from the broker
	chart, err := dash.chart(strategy, nil, append(accountOptions(broker), ui.WithCandleSource(broker))...)
	if err != nil {
		return err
	}

	options := append(subscribe(chart), xtrader.WithStorage(orders))
	bot, err := xtrader.NewBot(ctx, settings, broker, strategy, options...)
	if err != nil {
		return err
	}
	agent.Supervise(bot.Controller()) // its entries, blocked by the controller or not, before it trades
	// the protection of its positions
	attachProtection(ctx, bot, broker, strategy, name, true, agent, chart)
	// the news of the dashboard: held out of the market around the releases
	news := llm.NewNewsSwitch(newsguard.ConfigOf(agent.Config.News))
	newsguard.Start(ctx, agent.Config, news, bot.Controller(), orders, settings.Pairs)
	giveReleases(strategy, newsReleases{news: news}) // their calendar, for a strategy trading around them

	apply := func(runtime agentapi.Runtime) { newsguard.Apply(news, runtime) }
	if chart != nil {
		orders.SetNotifier(chart) // the orders the outbox fails to write
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
		chart.SetSupervision(bot.Controller())
		apply = func(runtime agentapi.Runtime) {
			newsguard.Apply(news, runtime)
			chart.SetChannels(dashboard{tradePanel: runtime.TradePanel, telegram: runtime.TelegramControl}.channels()...)
		}
		go func() {
			log.Fatal(dash.serve(chart))
		}()
	}
	return agent.Serve(ctx, apply, bot.Run)
}

// venue is the exchange of the paper and live modes: MetaTrader 5.
type venue struct {
	leverage float64            // of the positions, 1 for none
	lots     map[string]float64 // the fixed sizes of the positions, by pair, if any
	mt5      mt5Venue
}

// connect connects to MT5, authenticated when trading on it: the paper mode
// only needs its candles, reading the market data only.
func (v venue) connect(ctx context.Context, pairs []string, trading bool) (service.Exchange, error) {
	options := v.mt5.options(pairs, trading)
	if v.leverage > 1 {
		options = append(options, exchange.WithMT5Leverage())
	}
	mt5, err := exchange.NewMT5(ctx, options...)
	if err != nil {
		return nil, err
	}
	// a position worth more than its margin allows would be rejected
	for _, pair := range pairs {
		if account := mt5.Leverage(pair); v.leverage > account {
			return nil, fmt.Errorf("mt5: the account trades %s at 1:%g at most, not 1:%g", pair, account,
				v.leverage)
		}
	}
	if trading {
		log.Infof("[SETUP] MT5: magic %d, comment %q", mt5.Magic(), mt5.Comment())
	}
	return mt5, nil
}

// checkSizes converts the sizes of -lot read in MT5 lots into units of the base
// asset, with the contract sizes of the account, then checks them against the
// markets of exch. The strategy shares the map of the sizes, so it must run
// before the bot starts.
func (v venue) checkSizes(exch service.Exchange) error {
	var contracts map[string]float64
	if mt5, ok := exch.(*exchange.MT5); ok && v.lots != nil {
		contracts = make(map[string]float64, len(v.lots))
		for pair := range v.lots {
			contracts[pair] = mt5.ContractSize(pair)
		}
		if v.mt5.inLots {
			if err := lotsToUnits(v.lots, contracts); err != nil {
				return err
			}
		}
	}
	return checkLots(exch, v.lots, contracts)
}

// lotsByPair maps the sizes of -lot to the pairs: one for all of them, or one
// per pair in the same order. It returns nil without any.
func lotsByPair(pairs, sizes []string) (map[string]float64, error) {
	return sizesByPair("-lot", pairs, sizes)
}

// sizesOf reads the flag name of a list of sizes (see flaglist.Sizes) and maps
// them to the pairs (see sizesByPair).
func sizesOf(name string, pairs []string, value string) (map[string]float64, error) {
	sizes, err := flaglist.Sizes(name, value)
	if err != nil {
		return nil, err
	}
	return sizesByPair(name, pairs, sizes)
}

// sizesByPair maps the positive sizes of the flag name to the pairs: one for
// all of them, one per pair in the same order, or one by pair, each PAIR=SIZE,
// eg: BTCUSDm=0.03,XAUUSDm=0.01, every pair with its own. It returns nil
// without any.
func sizesByPair(name string, pairs, sizes []string) (map[string]float64, error) {
	if len(sizes) == 0 {
		return nil, nil
	}
	parse := func(pair, size string) (float64, error) {
		value, err := strconv.ParseFloat(strings.TrimSpace(size), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			return 0, fmt.Errorf("%s: invalid size %q for %s", name, size, pair)
		}
		return value, nil
	}

	values := make(map[string]float64, len(pairs))
	named := 0
	for _, size := range sizes {
		if strings.Contains(size, "=") {
			named++
		}
	}
	if named > 0 {
		if named != len(sizes) {
			return nil, fmt.Errorf("%s: every size by its pair, PAIR=SIZE, or none", name)
		}
		for _, item := range sizes {
			pair, size, _ := strings.Cut(item, "=")
			pair = strings.TrimSpace(pair)
			switch _, seen := values[pair]; {
			case !slices.Contains(pairs, pair):
				return nil, fmt.Errorf("%s: %q isn't a pair of -pair (%s)", name, pair, strings.Join(pairs, ","))
			case seen:
				return nil, fmt.Errorf("%s: %s twice", name, pair)
			}
			value, err := parse(pair, size)
			if err != nil {
				return nil, err
			}
			values[pair] = value
		}
		for _, pair := range pairs {
			if _, ok := values[pair]; !ok {
				return nil, fmt.Errorf("%s: no size for %s", name, pair)
			}
		}
		return values, nil
	}

	if len(sizes) != 1 && len(sizes) != len(pairs) {
		return nil, fmt.Errorf("%s: one size for all the pairs or one per pair, not %d for %d pairs", name,
			len(sizes), len(pairs))
	}
	for i, pair := range pairs {
		size := sizes[0]
		if len(sizes) > 1 {
			size = sizes[i]
		}
		value, err := parse(pair, size)
		if err != nil {
			return nil, err
		}
		values[pair] = value
	}
	return values, nil
}

// checkLots fails on a lot the market doesn't take: under its minimum size,
// above its maximum or off its size increment, which an order would round down.
// The sizes are described with their lots when contracts, the contract sizes of
// the pairs, has theirs, eg: "0.05 lot = 5 XAU" on MT5.
func checkLots(exch service.Exchange, lots, contracts map[string]float64) error {
	for pair, lot := range lots {
		info := exch.AssetsInfo(pair)
		size := func(units float64) string {
			return describeSize(pair, units, contracts[pair])
		}
		switch {
		case lot < info.MinQuantity:
			return fmt.Errorf("-lot: %s takes positions of %s at least, not %s", pair, size(info.MinQuantity), size(lot))
		case info.MaxQuantity > 0 && lot > info.MaxQuantity:
			return fmt.Errorf("-lot: %s takes positions of %s at most, not %s", pair, size(info.MaxQuantity), size(lot))
		case info.StepSize > 0 && math.Abs(lot/info.StepSize-math.Round(lot/info.StepSize)) > 1e-6:
			return fmt.Errorf("-lot: %s takes positions in steps of %s, not %s", pair, size(info.StepSize), size(lot))
		}
		log.Infof("[SETUP] %s: positions of %s (min %s, step %s)", pair, size(lot), size(info.MinQuantity),
			size(info.StepSize))
	}
	return nil
}

// dashboard holds the options of the API served to the web dashboard.
type dashboard struct {
	addr       string // of the API, none when off
	tradePanel bool   // control panel from the trade panel of the dashboard, in live mode
	telegram   bool   // control panel from the Telegram of the controller, in live mode
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

// chart creates the chart of the dashboard, nil when it is disabled, with the
// extra options. Without a paper wallet there is no equity curve to show.
func (d dashboard) chart(strategy strategy.Strategy, wallet *exchange.PaperWallet,
	extra ...ui.Option) (*ui.Chart, error) {
	if d.addr == "" {
		return nil, nil
	}

	options := []ui.Option{ui.WithStrategyIndicators(strategy), ui.WithAPIKey(d.key)}
	if wallet != nil {
		options = append(options, ui.WithPaperWallet(wallet))
	}
	return ui.New(append(options, extra...)...)
}

// accountOptions shows on the chart of a bot trading live the open positions,
// the account and the profits of the trades as the broker reports them, in the
// currency of the account, eg: the MT5 terminal in BRL; none on the exchanges
// that don't report them, their profits in the quote of the pairs.
func accountOptions(broker service.Exchange) []ui.Option {
	mt5, ok := broker.(*exchange.MT5)
	if !ok {
		return nil
	}
	return []ui.Option{
		ui.WithBrokerPositions(func(pair string) (ui.BrokerPosition, bool) {
			position, open := mt5.OpenPosition(pair)
			return ui.BrokerPosition{Side: string(position.Side), Quantity: position.Quantity, Entry: position.Entry,
				Profit: position.Profit, Currency: position.Currency}, open
		}),
		ui.WithAccountReporter(mt5),
	}
}

// serve serves the API of the chart until the server fails.
func (d dashboard) serve(chart *ui.Chart) error {
	fmt.Printf("Dashboard API available at http://%s/api, open it with cmd/controller\n", d.addr)
	return ui.NewHTTPServer(d.addr, chart.Handler()).ListenAndServe()
}

// subscribe feeds the candles and orders of the bot to the chart, if any, and
// makes it the notifier of the bot, whose messages and errors the controller
// relays to Telegram.
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
