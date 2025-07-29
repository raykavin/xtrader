package runner

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/internal/remote"
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
// own), the side of the positions, the stops, the targets and the sizing.
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
}

// BracketInputs are the flags of Inputs.Bracket, the ones of the strategies
// placing each entry with its stop loss and take profit at the broker.
var BracketInputs = []string{"stop-atr", "take-profit-atr", "max-bars", "min-atr-pct"}

// Bracket sets the exits and the sizing of a bracket strategy from the inputs,
// and its timeframe when -timeframe is set. Unset, each keeps its default.
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
}

// Signals returns the signal strategies of names, see strategies.NewSignal:
// each takes the inputs of Inputs.Bracket, and -trailing-atr the ones trailing
// their stop loss, eg: donchianatr.
func Signals(names ...string) []Strategy {
	list := make([]Strategy, 0, len(names))
	for _, name := range names {
		about, ok := strategies.SignalAbout(name)
		if !ok {
			panic("runner: no signal strategy " + name)
		}
		inputs := BracketInputs
		if strategies.DefaultSignalConfig(name).TrailingATR > 0 {
			inputs = append(slices.Clone(BracketInputs), "trailing-atr")
		}
		list = append(list, Strategy{Name: name, About: about, Inputs: inputs,
			New: func(in Inputs) (strategy.Strategy, error) {
				cfg := strategies.DefaultSignalConfig(name)
				in.Bracket(&cfg.BracketConfig, &cfg.Timeframe)
				if in.Set["trailing-atr"] {
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
//	# stop loss losing 1 and a take profit earning 2 in the quote currency (USD
//	# for XAUUSDm) in place of the ones in ATR
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
		strategyName = flag.String("strategy", names[0], "strategy traded: "+strings.Join(about, ", "))
		mode         = flag.String("mode", "backtest", "backtest, paper or live")
		exchangeName = flag.String("exchange", "mt5", "exchange traded: mt5, MetaTrader 5 through tools/mt5bridge")
		currency     = flag.String("currency", "USD", "backtest: currency the symbols are quoted in, eg: GBP for EURGBPm")
		mt5Bridge    = flag.String("mt5-bridge", exchange.MT5DefaultBaseURL, "mt5: address of the bridge (tools/mt5bridge) running with the terminal logged in the account")
		mt5TokenArg  = flag.String("mt5-token", "", "mt5, on its own: token of the bridge, its --token (default: "+mt5TokenEnv+"; launched by the controller: the token of its settings)")
		mt5MagicArg  = flag.Int64("mt5-magic", 0, "mt5: magic number of the bot's orders and positions, only those count for it, so bots with magic numbers of their own share an account (default: a hash of -mt5-comment)")
		mt5CommentAr = flag.String("mt5-comment", "", "mt5: comment of the bot's orders, at most 31 characters, whose hash is the default magic number (default \"nb:\" and the strategy name)")
		mt5Live      = flag.Bool("mt5-live", false, "mt5: allow a real account, refused otherwise")
		mt5Size      = flag.String("mt5-size", "lot", "mt5: how -lot is read, lot (MT5 lots, converted into units of the base asset with the contract size of the pair) or unit")
		mt5CSize     = flag.String("mt5-contract-size", "", "mt5 backtest: units of the base asset in a lot, to convert -lot, eg: 100 for XAUUSDm; one for all the pairs, or one per pair in the order of -pair (default: the ones the package knows)")
		mt5Poll      = flag.Duration("mt5-poll", 0, "mt5: how often the account and the candles are polled (default 1s)")
		pairList     = flag.String("pair", "XAUUSDm", "pairs to trade, comma separated, eg: XAUUSDm,BTCUSDm")
		timeframe    = flag.String("timeframe", "1h", "strategy timeframe (the strategies with a stop loss and a take profit at the broker: default the strategy's)")
		direction    = flag.String("direction", "both", "side of the positions opened: both, buy or sell")
		stopATR      = flag.Float64("stop-atr", 0, "stop from the entry close, in ATR: trendconfluence: checked on the close (0: off); the strategies with a stop loss at the broker: placed there (default: the strategy's)")
		trailingATR  = flag.Float64("trailing-atr", 0, "trailing stop from the best price since the entry, in ATR (default: the strategy's)")
		takeProfit   = flag.Float64("take-profit-atr", 0, "take profit at the broker from the entry close, in ATR (default: the strategy's); the signal strategies take 0 for none")
		maxBars      = flag.Int("max-bars", 0, "close a trade still open after that many candles, 0 never (default: the strategy's)")
		minATRPct    = flag.Float64("min-atr-pct", 0, "skip the entries while the ATR is under that % of the close, for the costs, eg: 0.025 for gold")
		stopValue    = flag.Float64("stop-value", 0, "fixed stop loss at the broker, in place of the stops in ATR: what the position loses at it, in quote currency, eg: 1 for a dollar on XAUUSDm (0: off; mt5 or the paper wallet)")
		profitValue  = flag.Float64("take-profit-value", 0, "fixed take profit at the broker, in place of the one in ATR: what the position earns at it, in quote currency, eg: 2 for two dollars on XAUUSDm (0: off; mt5 or the paper wallet)")
		minValue     = flag.Float64("min-position-value", 10, "quote value under which a balance is dust, eg: for pairs not quoted in USD")
		fee          = flag.Float64("fee", 0.001, "backtest and paper: fee of each fill, a share of its value; MT5 CFDs charge the spread instead: half of it over the price, eg: 0.0003")
		fraction     = flag.Float64("position-fraction", 1, "share of the free balance each position commits (0-1]")
		lotList      = flag.String("lot", "", "fixed size of every position, a lot, instead of a share of the balance (-position-fraction): in MT5 lots unless -mt5-size unit, eg: 0.01; one for all the pairs, or one per pair, comma separated in the order of -pair")
		leverage     = flag.Float64("leverage", 1, "live: leverage of the positions, at most the account's for the market; needs -position-fraction below 1, or -lot")
		csvFiles     = flag.String("csv", "", "backtest candles, as written by `xtrader download`: a file per pair, comma separated in the order of -pair")
		csvTimeframe = flag.String("csv-timeframe", "", "timeframe of the backtest candles (default -timeframe)")
		api          = flag.String("api", "", "address to serve the bot API at for the controller (cmd/controller), eg: localhost:5556 (default: off)")
		tradePanel   = flag.Bool("trade-panel", false, "live: enable the trade panel of the web dashboard (manual orders, their moves and cancels; the dashboard starts and stops the bot without it)")
		telegramCtl  = flag.Bool("telegram-control", false, "live: enable the Telegram controls of the controller (/start, /stop, /buy, /sell, /status, /balance)")
	)
	// launched by the controller, the flags and the token of its settings
	ctx := context.Background()
	remoteAgent, err := remote.Connect(ctx)
	if err != nil {
		return err
	}
	var token string
	if remoteAgent != nil {
		// stopped by the controller, it sends its records before it exits
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
	if err := checkInputs(agent.Strategies, chosen.Name, set); err != nil {
		return err
	}

	pairs := list(*pairList)
	if len(pairs) == 0 {
		return errors.New("-pair: no pair to trade")
	}
	lots, err := lotsByPair(pairs, list(*lotList))
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
	// a fixed stop loss or take profit replaces the ones in ATR
	for fixed, inATR := range map[string][]string{
		"stop-value":        {"stop-atr", "trailing-atr", "stop-buffer-atr", "broker-stop-atr", "broker-trailing-atr"},
		"take-profit-value": {"take-profit-atr"},
	} {
		for _, name := range inATR {
			if set[fixed] && set[name] {
				return fmt.Errorf("-%s and -%s are exclusive: the fixed value replaces the ATR", fixed, name)
			}
		}
	}

	side := strategies.RKDirection(*direction)
	trader, err := chosen.New(Inputs{Set: set, Timeframe: *timeframe, Direction: side,
		StopATR: *stopATR, TrailingATR: *trailingATR, TakeProfitATR: *takeProfit, MinATRPct: *minATRPct,
		MaxBars: *maxBars, StopValue: *stopValue, TakeProfitValue: *profitValue, MinValue: *minValue,
		Fraction: *fraction, Leverage: *leverage, Lots: lots})
	if err != nil {
		return err
	}

	settings := xtrader.Settings{
		Pairs: pairs,
	}
	if err := checkVenue(*exchangeName, *mode, set, *leverage); err != nil {
		return err
	}
	dash := dashboard{addr: *api, tradePanel: *tradePanel, telegram: *telegramCtl}
	comment := mt5Comment(*mt5CommentAr, chosen.Name)
	venue := venue{leverage: *leverage, lots: lots,
		mt5: mt5Venue{bridge: *mt5Bridge, token: token, magic: mt5Magic(*mt5MagicArg, comment),
			comment: comment, live: *mt5Live, inLots: inLots, poll: *mt5Poll}}

	switch *mode {
	case "backtest":
		// the symbols are registered when connecting, which a backtest doesn't
		for _, pair := range pairs {
			exchange.RegisterMT5Pair(pair, "", *currency)
		}
		if lots != nil && inLots {
			contracts, err := mt5Contracts(pairs, list(*mt5CSize))
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
		return backtest(ctx, settings, trader, dash, list(*csvFiles), *csvTimeframe, *fee)
	case "paper":
		return paper(ctx, settings, trader, dash, venue, *fee, remoteAgent)
	case "live":
		return live(ctx, settings, trader, dash, venue, remoteAgent)
	}
	return fmt.Errorf("invalid mode %q: backtest, paper or live (a real account needs -mt5-live)", *mode)
}

// checkInputs fails on a flag set that belongs to other strategies of the
// agent than name: the inputs of each strategy are refused with the others.
func checkInputs(list []Strategy, name string, set map[string]bool) error {
	for flagName := range set {
		var owners []string
		for _, s := range list {
			if slices.Contains(s.Inputs, flagName) {
				owners = append(owners, s.Name)
			}
		}
		if len(owners) > 0 && !slices.Contains(owners, name) {
			slices.Sort(owners)
			return fmt.Errorf("-%s is an input of -strategy %s", flagName, strings.Join(owners, " and "))
		}
	}
	return nil
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

func paper(ctx context.Context, settings xtrader.Settings, strategy strategy.Strategy,
	dash dashboard, venue venue, fee float64, agent *remote.Agent) error {

	feed, err := venue.connect(ctx, settings.Pairs, false)
	if err != nil {
		return err
	}
	if err := venue.checkSizes(feed); err != nil {
		return err
	}

	storage, err := storage.FromMemory()
	if err != nil {
		return err
	}

	wallet, err := newPaperWallet(ctx, settings.Pairs, feed, fee)
	if err != nil {
		return err
	}
	chart, err := dash.chart(strategy, wallet)
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

func live(ctx context.Context, settings xtrader.Settings, strategy strategy.Strategy,
	dash dashboard, venue venue, agent *remote.Agent) error {

	broker, err := venue.connect(ctx, settings.Pairs, true)
	if err != nil {
		return err
	}
	if err := venue.checkSizes(broker); err != nil {
		return err
	}

	// the orders of the bot, kept by the controller
	orders, err := agent.Orders(ctx)
	if err != nil {
		return err
	}
	chart, err := dash.chart(strategy, nil, accountOptions(broker)...)
	if err != nil {
		return err
	}

	options := append(subscribe(chart), xtrader.WithStorage(orders))
	bot, err := xtrader.NewBot(ctx, settings, broker, strategy, options...)
	if err != nil {
		return err
	}

	apply := func(agentapi.Runtime) {}
	if chart != nil {
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

// sizesByPair maps the positive sizes of the flag name to the pairs: one for
// all of them, or one per pair in the same order. It returns nil without any.
func sizesByPair(name string, pairs, sizes []string) (map[string]float64, error) {
	if len(sizes) == 0 {
		return nil, nil
	}
	if len(sizes) != 1 && len(sizes) != len(pairs) {
		return nil, fmt.Errorf("%s: one size for all the pairs or one per pair, not %d for %d pairs", name,
			len(sizes), len(pairs))
	}

	values := make(map[string]float64, len(pairs))
	for i, pair := range pairs {
		size := sizes[0]
		if len(sizes) > 1 {
			size = sizes[i]
		}
		value, err := strconv.ParseFloat(size, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			return nil, fmt.Errorf("%s: invalid size %q for %s", name, size, pair)
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

	options := []ui.Option{ui.WithStrategyIndicators(strategy)}
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
	return http.ListenAndServe(d.addr, chart.Handler())
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
