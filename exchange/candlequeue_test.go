package exchange

import (
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/testdata/mocks"
)

// B9: a consumer of the candles stuck, its queue growing, is warned about,
// once a while, rather than its memory growing silently: its candles are kept
// all the same, none dropped, taken in their order once it goes on.
func TestDataFeedStart_ConsumerBehindIsWarnedAbout(t *testing.T) {
	exchange := &mocks.Exchange{}
	candles := make(chan model.Candle)
	errs := make(chan error)
	exchange.On("CandlesSubscription", mock.Anything, "BTCUSDT", "1h").Return(candles, errs)

	const sent = 2*candleQueueWarn + 10
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	stuck := newCandleRecorder(sent)
	other := newCandleRecorder(sent)
	feed := NewDataFeed(exchange)
	feed.Subscribe("BTCUSDT", "1h", func(candle model.Candle) {
		<-release
		stuck.consume(candle)
	}, false)
	feed.Subscribe("BTCUSDT", "1h", other.consume, false)

	hook := logtest.NewGlobal()
	defer hook.Reset()
	feed.Start(false)
	behind := func() int {
		var warned int
		for _, entry := range hook.AllEntries() {
			if entry.Level == logrus.WarnLevel &&
				strings.Contains(entry.Message, "data feed BTCUSDT 1h: a consumer is") {
				warned++
			}
		}
		return warned
	}

	for i := 1; i < candleQueueWarn; i++ {
		candles <- testCandle(i, true)
	}
	require.Zero(t, behind(), "warned below the bound")
	for i := candleQueueWarn; i <= sent; i++ {
		candles <- testCandle(i, true)
	}
	require.Len(t, other.wait(t), sent, "the other consumer takes them as they come")
	require.Equal(t, 1, behind(), "warned once a while: %v", hook.AllEntries())

	close(release)
	got := stuck.wait(t)
	require.Len(t, got, sent, "none dropped")
	for i, candle := range got {
		require.Equal(t, testCandle(i+1, true), candle, "in their order")
	}
}
