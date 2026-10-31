package backtest

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/model"
)

// The statuses of a report and the reasons of the trades, see agentapi.
const (
	StatusSucceeded       = agentapi.BacktestStatusSucceeded
	StatusCanceled        = agentapi.BacktestStatusCanceled
	StatusFailed          = agentapi.BacktestStatusFailed
	StatusBudgetExhausted = agentapi.BacktestStatusBudgetExhausted

	ReasonMarket     = agentapi.BacktestReasonMarket
	ReasonLimit      = agentapi.BacktestReasonLimit
	ReasonStopEntry  = agentapi.BacktestReasonStopEntry
	ReasonStopLoss   = agentapi.BacktestReasonStopLoss
	ReasonTakeProfit = agentapi.BacktestReasonTakeProfit
	ReasonDailyLimit = agentapi.BacktestReasonDailyLimit
	ReasonStopOut    = agentapi.BacktestReasonStopOut
	ReasonHours      = agentapi.BacktestReasonHours
)

// The report of a backtest and its parts, see agentapi.BacktestReport.
type (
	Report       = agentapi.BacktestReport
	Summary      = agentapi.BacktestSummary
	Trade        = agentapi.BacktestTrade
	OpenPosition = agentapi.BacktestOpen
	Point        = agentapi.BacktestPoint
)

// maxEquityPoints bounds the points of the equity of a report, evenly kept.
const maxEquityPoints = 2000

// entryReason returns the reason of an entry by its order.
func entryReason(o model.Order) string {
	switch o.Type {
	case model.OrderTypeLimit, model.OrderTypeLimitMaker:
		return ReasonLimit
	case model.OrderTypeStopLoss, model.OrderTypeStopLossLimit:
		return ReasonStopEntry
	}
	return ReasonMarket
}

// exitReason returns the reason of an exit by its order, the ones the backtest
// sent itself told in own (by order ID).
func exitReason(o model.Order, own map[int64]string) string {
	if reason, ok := own[o.ID]; ok {
		return reason
	}
	switch o.Type {
	case model.OrderTypeStopLoss, model.OrderTypeStopLossLimit:
		return ReasonStopLoss
	case model.OrderTypeLimitMaker, model.OrderTypeTakeProfit, model.OrderTypeTakeProfitLimit:
		return ReasonTakeProfit
	case model.OrderTypeLimit:
		return ReasonLimit
	}
	return ReasonMarket
}

// open is a position being rebuilt, see trades.
type open struct {
	side           string
	quantity, peak float64 // held now, and the most held
	entryValue     float64 // the sum of the entries: units x price
	entryUnits     float64
	exitValue      float64
	exitUnits      float64
	gross, fees    float64
	opened         time.Time
	entryReason    string
}

// trades rebuilds the trades of orders, filled, from flat back to flat, by
// pair: the ones closed at or after from, and the positions open after the
// last order, see Trade. own tells the reasons of the orders of the backtest
// itself.
func trades(orders []*model.Order, from time.Time, own map[int64]string) ([]Trade, map[string]*open) {
	sorted := slices.Clone(orders)
	slices.SortStableFunc(sorted, func(a, b *model.Order) int {
		if c := a.UpdatedAt.Compare(b.UpdatedAt); c != 0 {
			return c
		}
		return int(a.ID - b.ID)
	})
	positions := make(map[string]*open)
	var closed []Trade
	for _, o := range sorted {
		if o.Status != model.OrderStatusTypeFilled || o.Quantity <= 0 || o.Price <= 0 {
			continue
		}
		side := string(o.Side)
		remaining := o.Quantity
		feePerUnit := o.Fee / o.Quantity
		p := positions[o.Pair]
		if p != nil && p.side != side {
			// a reduction, or a close and a reversal
			units := math.Min(remaining, p.quantity)
			average := p.entryValue / p.entryUnits
			sign := 1.0
			if p.side == string(model.SideTypeSell) {
				sign = -1
			}
			p.gross += sign * (o.Price - average) * units
			p.fees += feePerUnit * units
			p.exitValue += o.Price * units
			p.exitUnits += units
			p.quantity -= units
			remaining -= units
			if p.quantity <= p.peak*1e-9 {
				if !o.UpdatedAt.Before(from) {
					closed = append(closed, Trade{Pair: o.Pair, Side: p.side, Quantity: p.peak,
						OpenedAt: p.opened.Unix(), ClosedAt: o.UpdatedAt.Unix(), Entry: average,
						Exit: p.exitValue / p.exitUnits, Gross: p.gross, Fees: p.fees, Net: p.gross - p.fees,
						EntryReason: p.entryReason, ExitReason: exitReason(*o, own)})
				}
				delete(positions, o.Pair)
				p = nil
			}
		}
		if remaining <= o.Quantity*1e-9 {
			continue
		}
		if p == nil {
			p = &open{side: side, opened: o.UpdatedAt, entryReason: entryReason(*o)}
			positions[o.Pair] = p
		}
		p.quantity += remaining
		p.peak = math.Max(p.peak, p.quantity)
		p.entryValue += o.Price * remaining
		p.entryUnits += remaining
		p.fees += feePerUnit * remaining
	}
	return closed, positions
}

// downsample keeps maxEquityPoints of points at most, evenly, the last one
// kept.
func downsample(points []Point) []Point {
	if len(points) <= maxEquityPoints {
		return points
	}
	step := float64(len(points)-1) / float64(maxEquityPoints-1)
	kept := make([]Point, 0, maxEquityPoints)
	for i := range maxEquityPoints {
		kept = append(kept, points[int(math.Round(float64(i)*step))])
	}
	return kept
}

// drawdown returns the most points fell from a peak, and its share of that
// peak.
func drawdown(points []Point) (float64, float64) {
	peak := math.Inf(-1)
	var worst, share float64
	for _, p := range points {
		peak = math.Max(peak, p.Value)
		if fall := peak - p.Value; fall > worst {
			worst = fall
			if peak > 0 {
				share = fall / peak
			}
		}
	}
	return worst, share
}

// writeReport writes r to path, at once: a reader never sees half of it.
func writeReport(r Report, path string) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("backtest: the report: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".report-*")
	if err != nil {
		return fmt.Errorf("backtest: the report: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("backtest: the report: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("backtest: the report: %w", err)
	}
	return os.Rename(tmp.Name(), path)
}
