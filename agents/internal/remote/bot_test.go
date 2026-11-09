package remote

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/order"
)

// fakeBot is a bot started and stopped as order.Controller is.
type fakeBot struct {
	mu     sync.Mutex
	status order.Status
	stops  int
}

func (b *fakeBot) Status() order.Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status
}

func (b *fakeBot) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.status == order.StatusRunning {
		b.status, b.stops = order.StatusStopped, b.stops+1
	}
}

func (b *fakeBot) start() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status = order.StatusRunning
}

// A bot a user left stopped stopped as soon as it starts with its agent, once;
// started by a user afterwards, left running; nothing done on a bot never
// stopped.
func TestKeepBotStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bot := &fakeBot{}
	agent := &Agent{Config: agentapi.Config{Name: "orb", BotStopped: true}}
	agent.KeepBotStopped(ctx, bot)
	time.Sleep(20 * time.Millisecond) // not started yet: left alone
	assert.Equal(t, order.Status(""), bot.Status())
	bot.start()
	require.Eventually(t, func() bool { return bot.Status() == order.StatusStopped }, time.Second, time.Millisecond)

	bot.start() // a user starts it
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, order.StatusRunning, bot.Status(), "started by a user: left running")
	bot.mu.Lock()
	assert.Equal(t, 1, bot.stops)
	bot.mu.Unlock()

	running := &fakeBot{}
	(&Agent{Config: agentapi.Config{Name: "vwap"}}).KeepBotStopped(ctx, running)
	running.start()
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, order.StatusRunning, running.Status(), "never stopped by a user")
}
