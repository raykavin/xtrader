package llm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// R5: Stop, as the bot exits, ends the request to the model in progress
// rather than waiting for its reply (Config.Timeout): the candle in progress
// ends at once, no order placed, and nothing kept of the request, the stop no
// failure of the model, neither a decision nor an entry of the journal; the
// requests after fail at once.
func TestTrader_StopEndsTheRequestInProgress(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) { c.Timeout = time.Hour }, 0)
	gate := &gateProvider{asked: make(chan Input, 2), release: make(chan struct{})}
	r.trader.provider = gate
	journal := &testJournal{}
	require.NoError(t, r.trader.SetJournal(journal))
	r.add(101)
	r.trader.Indicators(r.df)

	ended := make(chan struct{})
	go func() {
		defer close(ended)
		r.trader.OnCandle(r.df, r.broker)
	}()
	select {
	case <-gate.asked:
	case <-time.After(10 * time.Second):
		t.Fatal("the model never asked")
	}
	r.trader.Stop()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the candle in progress waits for the reply after Stop")
	}
	require.Zero(t, r.position(), "no order placed")
	decisions, err := r.trader.Decisions(testPair, 10)
	require.NoError(t, err)
	require.Empty(t, decisions, "a failure of the model kept")
	require.Empty(t, journal.entries)

	reply := r.trader.request(r.trader.refreshPrompt(r.df), Input{Symbol: testPair}, time.Now())
	require.ErrorIs(t, reply.err, context.Canceled, "a request after Stop")
}
