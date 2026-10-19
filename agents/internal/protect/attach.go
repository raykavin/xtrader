package protect

import (
	"context"
	"fmt"
	"time"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/agents/internal/remote"
	"github.com/raykavin/xtrader/ui"
)

// Attach protects the positions of bot, a bot of agent trading on timeframe:
// on every price it processes (see xtrader.CandleHook), the stop guard of its
// order controller on while the protection runs, the settings and the arms
// the controller tells, shown on chart (nil for none) and armed there by the
// supervisor. Live, the positions are named by their identifier on the MT5
// account (live true); on the paper wallet, by their entry in this run. The
// candles and the costs come from market: the broker live, the paper wallet
// in paper. Nothing without agent (a run apart from the controller), nil
// returned.
func Attach(ctx context.Context, bot *xtrader.XTrader, market Market, timeframe string, live bool,
	agent *remote.Agent, chart *ui.Chart) *Manager {
	if agent == nil {
		return nil
	}
	prefix := "mt5"
	if !live {
		prefix = fmt.Sprintf("paper:%x", time.Now().UnixNano())
	}
	manager := New(ctx, bot.Controller(), market, timeframe, prefix)
	bot.SetCandleHook(manager)
	bot.Controller().SetStopGuard(manager.Guarded)
	if chart != nil {
		chart.SetProtection(manager)
	}
	agent.Protect(manager)
	return manager
}
