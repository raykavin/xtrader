package remote

import (
	"context"
	"time"

	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/tools/log"
)

// keepStoppedEvery is how often KeepBotStopped looks at the bot until it
// starts: its stop comes within it of the start of the order controller,
// before its candles, which its exchange sends after it.
const keepStoppedEvery = time.Millisecond

// BotSwitch is the bot of an agent, stopped by KeepBotStopped, see
// order.Controller.
type BotSwitch interface {
	Status() order.Status
	Stop()
}

// KeepBotStopped stops the bot of the agent once it starts, when a user left
// it stopped (see agentapi.Config.BotStopped): its process started again by a
// deploy, a reboot, a crash or Reiniciar never trades until a user starts it
// (Iniciar bot, /start), the bot starting with the agent (see xtrader.Run).
// Called before the bot runs, it returns at once; nothing without
// Config.BotStopped.
func (a *Agent) KeepBotStopped(ctx context.Context, bot BotSwitch) {
	if !a.Config.BotStopped {
		return
	}
	log.Warnf("%s: its bot was stopped by a user: it starts stopped, until a user starts it", a.Config.Name)
	go keepStopped(ctx, bot, a.Config.Name)
}

// keepStopped stops bot as soon as it runs, looking every keepStoppedEvery,
// until ctx is done.
func keepStopped(ctx context.Context, bot BotSwitch, name string) {
	ticker := time.NewTicker(keepStoppedEvery)
	defer ticker.Stop()
	for {
		if bot.Status() == order.StatusRunning {
			bot.Stop()
			log.Warnf("%s: the bot stopped, as a user left it: start it on the dashboard or by /start", name)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
