package ui

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

// panickyStrategy panics in its indicators on the candles closing at
// indicatorsAt, and drawing its levels with levels.
type panickyStrategy struct {
	indicatorsAt float64
	levels       bool
}

func (panickyStrategy) Timeframe() string                         { return "1h" }
func (panickyStrategy) WarmupPeriod() int                         { return 1 }
func (panickyStrategy) OnCandle(*model.Dataframe, service.Broker) {}

func (s panickyStrategy) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	if df.Close.Last(0) == s.indicatorsAt {
		var values []float64
		_ = values[len(df.Close)] // eg: an indicator on the data of the day
	}
	return []strategy.ChartIndicator{{GroupName: "close", Overlay: true, Warmup: 1, Time: df.Time,
		Metrics: []strategy.IndicatorMetric{{Name: "close", Values: df.Close}}}}
}

func (s panickyStrategy) ChartLevels(string) []Level {
	if s.levels {
		panic("boom")
	}
	return nil
}

// nextEvent returns the next event of events, failing after a second.
func nextEvent(t *testing.T, events chan sseEvent) sseEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("no event")
		return sseEvent{}
	}
}

// R10: a panic of the indicators of the strategy on the chart, which the feed
// of the bot calls with every candle, leaves the indicators out only, told on
// the events: the candle is kept and sent, the snapshot served, rather than
// the panic taking the agent down.
func TestChart_PanicOfTheIndicatorsLeavesThemOutOnly(t *testing.T) {
	c := newTestChart(t, WithStrategyIndicators(panickyStrategy{indicatorsAt: 12}))
	events := c.events.subscribe()
	defer c.events.unsubscribe(events)
	base := time.Unix(1_700_000_000, 0).UTC()

	c.OnCandle(candleAt("ETHUSDT", base, 11))
	event := nextEvent(t, events)
	require.Equal(t, "candle", event.name)
	require.Contains(t, string(event.data), `"name":"close"`, "the indicators")

	c.OnCandle(candleAt("ETHUSDT", base.Add(time.Hour), 12)) // panics
	event = nextEvent(t, events)
	require.Equal(t, "notification", event.name)
	var notification NotificationEvent
	require.NoError(t, json.Unmarshal(event.data, &notification))
	require.True(t, notification.Error)
	require.Contains(t, notification.Message, "Indicators of the strategy on the chart of ETHUSDT panicked on the "+
		"candle of 2023-11-14 23:13:20: runtime error: index out of range")
	event = nextEvent(t, events)
	require.Equal(t, "candle", event.name, "sent without its indicators")
	var candle CandleEvent
	require.NoError(t, json.Unmarshal(event.data, &candle))
	require.Equal(t, 12.0, candle.Candle.Close)
	require.Empty(t, candle.Indicators)

	srv := newTestServer(t, c)
	resp, body := get(t, srv.URL+"/api/ETHUSDT/snapshot")
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var snapshot Snapshot
	require.NoError(t, json.Unmarshal([]byte(body), &snapshot))
	require.Len(t, snapshot.Candles, 2, "the candle kept")
	require.Empty(t, snapshot.Indicators)

	c.OnCandle(candleAt("ETHUSDT", base.Add(2*time.Hour), 13))
	event = nextEvent(t, events)
	require.Equal(t, "candle", event.name, "the next candle, with its indicators")
	require.Contains(t, string(event.data), `"name":"close"`)
}

// R10: a panic building the event of a candle, eg: the strategy drawing its
// levels, leaves that event out, told once a while, the candles kept.
func TestChart_PanicOfACandleEventLeavesItOut(t *testing.T) {
	c := newTestChart(t, WithStrategyIndicators(panickyStrategy{levels: true}))
	events := c.events.subscribe()
	defer c.events.unsubscribe(events)
	base := time.Unix(1_700_000_000, 0).UTC()

	c.OnCandle(candleAt("ETHUSDT", base, 11))
	partial := candleAt("ETHUSDT", base.Add(time.Hour), 12)
	partial.Complete = false
	c.OnCandle(partial)
	c.OnCandle(candleAt("ETHUSDT", base.Add(time.Hour), 12.5))

	event := nextEvent(t, events)
	require.Equal(t, "notification", event.name)
	require.Contains(t, string(event.data), "the candle event of the chart of ETHUSDT panicked")
	select {
	case event := <-events:
		t.Fatalf("an event after the notice: %s %s", event.name, event.data)
	case <-time.After(50 * time.Millisecond):
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.candles["ETHUSDT"], 2, "the candles kept")
}
