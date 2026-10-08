package tools_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/tools"
)

func TestNewTrailingStop(t *testing.T) {
	ts := tools.NewTrailingStop()

	require.NotNil(t, ts)
}

func TestTrailingStop_Start(t *testing.T) {
	ts := tools.NewTrailingStop()
	ts.Start(21.5, 13.0)

	require.True(t, ts.Active())
}

func TestTrailingStop_Stop(t *testing.T) {
	ts := tools.NewTrailingStop()
	ts.Start(21.5, 13.0)
	ts.Stop()

	require.False(t, ts.Active())
}

func TestTrailingStop_Update(t *testing.T) {
	ts := tools.NewTrailingStop()

	// not started
	require.False(t, ts.Update(12.0))

	current := 21.5
	stop := 13.0

	ts.Start(current, stop)

	// When the new value is higher than the current value, the TrailingStop is
	// not triggered and the stop value e summed up with the difference of the
	// two values.
	difference := 5.0
	require.False(t, ts.Update(current+difference))

	// So when called with the new stop value or a lower one, the TrailingStop
	// should be triggered.
	require.True(t, ts.Update(stop+difference))
	require.True(t, ts.Update(stop-difference))
}

// A stop above the price trails a SELL: it moves down with a lower price,
// never up, and is reached at or above it.
func TestTrailingStop_UpdateSell(t *testing.T) {
	ts := tools.NewTrailingStop()
	ts.Start(100, 110)

	require.False(t, ts.Update(105), "a higher price below the stop")
	require.False(t, ts.Update(90), "a better price moves the stop down to 100")
	require.False(t, ts.Update(95), "the stop isn't moved back up")
	require.False(t, ts.Update(99.9))
	require.True(t, ts.Update(100))
	require.True(t, ts.Update(120))
}

// A BUY is still trailed as before: the stop moves up with a higher price.
func TestTrailingStop_UpdateBuyNotTakenForASell(t *testing.T) {
	ts := tools.NewTrailingStop()
	ts.Start(100, 90)

	require.False(t, ts.Update(110), "the stop moved up to 100")
	require.False(t, ts.Update(105), "the stop isn't moved back down")
	require.True(t, ts.Update(100))
}

// The stop keeps its distance from the best price: a price back from a worse
// one, still below the best, doesn't move it.
func TestTrailingStop_UpdateFromTheBestPrice(t *testing.T) {
	ts := tools.NewTrailingStop()
	ts.Start(100, 90)

	require.False(t, ts.Update(95))
	require.False(t, ts.Update(100), "back to the best price: the stop stays at 90")
	require.False(t, ts.Update(91))
	require.True(t, ts.Update(90))

	ts.Start(100, 110)
	require.False(t, ts.Update(105))
	require.False(t, ts.Update(100), "back to the best price: the stop stays at 110")
	require.False(t, ts.Update(109))
	require.True(t, ts.Update(110))
}
