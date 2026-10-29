package controller

import (
	"encoding/json"
	"math"
	"time"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/ui"
)

// backtestUsage is what the report of an LLM run tells of its model, in its
// extra: its calls and its estimated cost, nil when unknown.
type backtestUsage struct {
	Calls int      `json:"calls"`
	Cost  *float64 `json:"cost,omitempty"`
}

// summarizeBacktest returns the summary of report (see ui.BacktestSummary):
// the trades won and lost by their result after the fees, the profit factor
// and the payoff undefined (nil) without a loss or a win, Net over the
// largest fall nil without a fall; gaps the gaps of its candles.
func summarizeBacktest(report agentapi.BacktestReport, gaps int, model *backtestLLM) ui.BacktestSummary {
	s := ui.BacktestSummary{Currency: report.Currency, InitialBalance: report.Summary.InitialBalance,
		FinalEquity: report.Summary.FinalEquity, Net: report.Summary.Realized, OpenProfit: report.Summary.OpenProfit,
		OpenPositions: len(report.Open), Trades: len(report.Trades), Fees: report.Summary.Fees,
		MaxDrawdown: report.Summary.MaxDrawdown, MaxDrawdownPct: report.Summary.MaxDrawdownPct,
		Warnings: len(report.Warnings), Gaps: gaps, Refused: report.Summary.Refused}
	if s.InitialBalance > 0 {
		s.Return = s.Net / s.InitialBalance
	}
	var won, lost float64
	months := make(map[string]float64)
	for _, t := range report.Trades {
		switch {
		case t.Net > 0:
			s.Wins++
			won += t.Net
		case t.Net < 0:
			s.Losses++
			lost -= t.Net
		}
		months[time.Unix(t.ClosedAt, 0).UTC().Format("2006-01")] += t.Net
	}
	if s.Trades > 0 {
		s.WinRate = float64(s.Wins) / float64(s.Trades)
	}
	if s.Wins > 0 {
		s.AverageWin = won / float64(s.Wins)
	}
	if s.Losses > 0 {
		s.AverageLoss = -lost / float64(s.Losses)
	}
	if lost > 0 {
		factor := won / lost
		s.ProfitFactor = &factor
	}
	if s.Wins > 0 && s.Losses > 0 {
		payoff := s.AverageWin / -s.AverageLoss
		s.Payoff = &payoff
	}
	if s.MaxDrawdown > 0 {
		ratio := s.Net / s.MaxDrawdown
		s.ReturnOverDrawdown = &ratio
	}
	s.Months = len(months)
	for _, net := range months {
		if net > 0 {
			s.PositiveMonths++
		}
	}
	if model != nil && len(report.Extra) > 0 {
		var usage backtestUsage
		if err := json.Unmarshal(report.Extra, &usage); err == nil {
			s.LLMCalls = usage.Calls
			if usage.Cost != nil && !math.IsNaN(*usage.Cost) {
				s.LLMCost = usage.Cost
			}
		}
	}
	return s
}
