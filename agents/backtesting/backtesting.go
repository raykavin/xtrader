package main

import (
	"context"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
	"github.com/raykavin/xtrader/ui/indicator"
)

// This example shows how to use backtesting with XTrader
// Backtesting is a simulation of the strategy in historical data (from CSV)
func main() {
	ctx := context.Background()

	// bot settings (eg: pairs, telegram, etc)
	settings := xtrader.Settings{
		Pairs: []string{
			"BTCUSDT",
			"ETHUSDT",
		},
	}

	// initialize your strategy
	strategy := new(strategies.CrossEMA)

	// load historical data from CSV files
	csvFeed, err := exchange.NewCSVFeed(
		strategy.Timeframe(),
		exchange.PairFeed{
			Pair:      "BTCUSDT",
			File:      "testdata/btc-1h.csv",
			Timeframe: "1h",
		},
		exchange.PairFeed{
			Pair:      "ETHUSDT",
			File:      "testdata/eth-1h.csv",
			Timeframe: "1h",
		},
	)
	if err != nil {
		log.Fatal(err)
	}

	// initialize a database in memory
	storage, err := storage.FromMemory()
	if err != nil {
		log.Fatal(err)
	}

	// create a paper wallet for simulation, initializing with 10.000 USDT
	wallet := exchange.NewPaperWallet(
		ctx,
		"USDT",
		exchange.WithPaperAsset("USDT", 10000),
		// maker and taker fees charged on every fill, 0.1% of each
		exchange.WithPaperFee(0.001, 0.001),
		exchange.WithDataFeed(csvFeed),
	)

	// create a chart  with indicators from the strategy and a custom additional RSI indicator
	chart, err := ui.New(
		ui.WithStrategyIndicators(strategy),
		ui.WithCustomIndicators(
			indicator.RSI(14, "purple"),
		),
		ui.WithPaperWallet(wallet),
		ui.WithCandleWindow(0), // the whole backtest on the chart
	)
	if err != nil {
		log.Fatal(err)
	}

	// initializer XTrader with the objects created before
	bot, err := xtrader.NewBot(
		ctx,
		settings,
		wallet,
		strategy,
		xtrader.WithBacktest(wallet), // Required for Backtest mode
		xtrader.WithStorage(storage),

		// connect bot feed (candle and orders) to the chart
		xtrader.WithCandleSubscription(chart),
		xtrader.WithOrderSubscription(chart),
		xtrader.WithLogLevel(log.WarnLevel),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Initializer simulation
	err = bot.Run(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// Print bot results
	bot.Summary()

	// Serve the chart on localhost:5555, the API of the bot the web dashboard of
	// cmd/controller shows for an agent of its database at localhost:5555
	err = chart.Start()
	if err != nil {
		log.Fatal(err)
	}
}
