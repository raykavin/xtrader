// Package backtest runs a strategy of an agent over the candles of a period,
// as the controller asks for it (see Config), on a paper wallet and a feed of
// files only: never a broker. It models the costs and the uncertainties of
// the fills (see exchange.PaperExecution), the margin of a leverage, the
// volume steps of the symbols, the breakeven and trailing stop of the agent
// and its daily limits, on the clock of the candles, without any future data
// (see exchange.BacktestFeed); and writes a report of the run (see Report).
package backtest

import (
	"time"

	"github.com/raykavin/xtrader/agentapi"
)

// Config is the backtest a run of an agent makes, see agentapi.BacktestConfig.
type Config = agentapi.BacktestConfig

// Symbol is what a backtest knows of a symbol, see agentapi.BacktestSymbol.
type Symbol = agentapi.BacktestSymbol

// ReadConfig reads the config of a backtest from path, validated.
func ReadConfig(path string) (Config, error) { return agentapi.ReadBacktestConfig(path) }

// from returns the start of the trading period of c.
func from(c Config) time.Time { return time.Unix(c.From, 0).UTC() }

// to returns the end of the trading period of c.
func to(c Config) time.Time { return time.Unix(c.To, 0).UTC() }
