package exchange

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// m5fProtected opens count positions of 1 lot of the bot, long, and protects
// them together with an OCO order: stop loss 4280, take profit 4300.
func m5fProtected(t *testing.T, f *m5fFake, m *MT5, count int) (positions []int64, tp, sl model.Order) {
	t.Helper()
	for range count {
		long, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
		require.NoError(t, err)
		positions = append(positions, long.ExchangeID)
	}
	legs, err := m.CreateOrderOCO(model.SideTypeSell, m5fPair, float64(count), 4300, 4280, 4280)
	require.NoError(t, err)
	tp, sl = m5fLegs(t, legs)
	for _, ticket := range positions {
		p, ok := f.position(ticket)
		require.True(t, ok)
		require.Equal(t, 4280.0, p.sl)
		require.Equal(t, 4300.0, p.tp)
	}
	return positions, tp, sl
}

// m5fLevelsOf returns the levels of the positions in the fake.
func m5fLevelsOf(t *testing.T, f *m5fFake, positions []int64) [][2]float64 {
	t.Helper()
	levels := make([][2]float64, 0, len(positions))
	for _, ticket := range positions {
		p, ok := f.position(ticket)
		require.True(t, ok, "position %d", ticket)
		levels = append(levels, [2]float64{p.sl, p.tp})
	}
	return levels
}

// m5fProtectionError returns the *MT5ProtectionError of err, its positions in
// the order of positions.
func m5fProtectionError(t *testing.T, err error, positions []int64) *MT5ProtectionError {
	t.Helper()
	var report *MT5ProtectionError
	require.True(t, errors.As(err, &report), "an *MT5ProtectionError: %v", err)
	require.Equal(t, m5fPair, report.Pair)
	require.NoError(t, report.Unread)
	require.Len(t, report.Positions, len(positions))
	for i, ticket := range positions {
		require.Equal(t, ticket, report.Positions[i].Position)
	}
	return report
}

// A stop loss moved on several positions failing at the second one: the first
// one gets its level back, so that every position stays at the level of the
// leg the bot follows, and the error tells the levels of each, read again.
func TestMT5UpdateProtection_PartialFailureSetsBackTheLevels(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	positions, _, sl := m5fProtected(t, f, m, 3)

	f.clearRequests()
	f.resultAt(2, mt5CodeReject, "Rejected")
	_, err := m.UpdateProtection(sl, 4282)
	require.Error(t, err)
	var refused *MT5Error
	require.True(t, errors.As(err, &refused), "the failure of the change: %v", err)
	require.Equal(t, mt5CodeReject, refused.Retcode)

	want := [2]float64{4280, 4300}
	require.Equal(t, [][2]float64{want, want, want}, m5fLevelsOf(t, f, positions), "back to the levels of before")
	requests := f.tradeRequests(mt5ActionSLTP)
	require.Len(t, requests, 3, "moved, refused, set back; the third never moved")
	require.Equal(t, 4282.0, requests[0]["sl"])
	require.Equal(t, 4280.0, requests[2]["sl"])
	require.Equal(t, float64(positions[0]), requests[2]["position"])
	require.NotEmpty(t, f.requestsTo("GET", "/positions"), "the account read again")

	report := m5fProtectionError(t, err, positions)
	require.True(t, report.Kept())
	for _, position := range report.Positions {
		require.Equal(t, 4280.0, position.StopLoss)
		require.Equal(t, 4300.0, position.TakeProfit)
		require.NoError(t, position.Restore)
	}
	require.Equal(t, 4280.0, m5fOrderOf(t, m, sl.ExchangeID).Price, "the leg where it was")
	require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, sl.ExchangeID).Status)
}

// The levels of a position can't be set back: the error tells which position
// is left at the new level, and which ones kept theirs.
func TestMT5UpdateProtection_SettingBackFailsIsReported(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	positions, _, sl := m5fProtected(t, f, m, 3)

	f.resultAt(2, mt5CodeReject, "Rejected")
	f.resultAt(3, mt5CodeMarketClosed, "Market closed") // setting back the first one
	_, err := m.UpdateProtection(sl, 4282)
	report := m5fProtectionError(t, err, positions)
	require.False(t, report.Kept())

	first := report.Positions[0]
	require.False(t, first.Kept())
	require.Error(t, first.Restore)
	require.Equal(t, 4282.0, first.StopLoss, "left at the new level, read again")
	require.Equal(t, 4280.0, first.PreviousStopLoss)
	require.True(t, report.Positions[1].Kept())
	require.True(t, report.Positions[2].Kept())
	require.Contains(t, err.Error(), "CHANGED")
	require.Equal(t, [2]float64{4282, 4300}, m5fLevelsOf(t, f, positions)[0])
}

// The protection of several positions canceled, failing at the third one: the
// two removed before get their levels back, none is left without protection
// while the bot still follows the legs.
func TestMT5Cancel_PartialFailureSetsBackTheProtection(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	positions, tp, sl := m5fProtected(t, f, m, 3)

	f.resultAt(3, mt5CodeReject, "Rejected")
	err := m.Cancel(tp)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrOrderNotOpen)

	want := [2]float64{4280, 4300}
	require.Equal(t, [][2]float64{want, want, want}, m5fLevelsOf(t, f, positions), "protected as before")
	report := m5fProtectionError(t, err, positions)
	require.True(t, report.Kept())
	require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, tp.ExchangeID).Status, "the legs stay")
	require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, sl.ExchangeID).Status)

	// canceled once it works
	require.NoError(t, m.Cancel(tp))
	none := [2]float64{0, 0}
	require.Equal(t, [][2]float64{none, none, none}, m5fLevelsOf(t, f, positions))
}

// An OCO order over several positions failing at the third one: the levels set
// on the two before are removed, none is protected without legs, and the error
// tells the levels of each.
func TestMT5OCO_PartialFailureRemovesTheLevelsSet(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	var positions []int64
	for range 3 {
		long, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
		require.NoError(t, err)
		positions = append(positions, long.ExchangeID)
	}

	f.resultAt(3, mt5CodeReject, "Rejected")
	_, err := m.CreateOrderOCO(model.SideTypeSell, m5fPair, 3, 4300, 4280, 4280)
	require.Error(t, err)

	none := [2]float64{0, 0}
	require.Equal(t, [][2]float64{none, none, none}, m5fLevelsOf(t, f, positions))
	report := m5fProtectionError(t, err, positions)
	require.True(t, report.Kept())

	// protected once it works
	legs, err := m.CreateOrderOCO(model.SideTypeSell, m5fPair, 3, 4300, 4280, 4280)
	require.NoError(t, err)
	m5fLegs(t, legs)
}

// A failure of the first position surely refused changed nothing: its error as
// it is, without reading the account again.
func TestMT5UpdateProtection_FirstRefusedChangesNothing(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	positions, _, sl := m5fProtected(t, f, m, 2)

	f.resultAt(1, mt5CodeInvalidStops, "Invalid stops")
	_, err := m.UpdateProtection(sl, 4282)
	require.ErrorIs(t, err, ErrInvalidProtection)
	var report *MT5ProtectionError
	require.False(t, errors.As(err, &report))
	want := [2]float64{4280, 4300}
	require.Equal(t, [][2]float64{want, want}, m5fLevelsOf(t, f, positions))
	require.Len(t, f.tradeRequests(mt5ActionSLTP), 3, "the 2 of the OCO order, then the one refused")
}

// A failure whose outcome isn't a sure refusal (an error of the bridge after
// the request was sent) may have changed its position: it is set back too.
func TestMT5UpdateProtection_FailureNotRefusedSetsBackItsPositionToo(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	positions, _, sl := m5fProtected(t, f, m, 2)

	f.clearRequests()
	f.afterNext("POST", "/order", func() {
		f.failNext("POST", "/order", http.StatusInternalServerError, `{"error": "order_send: Terminal: Call failed"}`)
	})
	_, err := m.UpdateProtection(sl, 4282)
	report := m5fProtectionError(t, err, positions)
	require.True(t, report.Kept())

	requests := f.tradeRequests(mt5ActionSLTP)
	require.Len(t, requests, 4, "moved, failed, then both set back")
	require.Equal(t, float64(positions[0]), requests[2]["position"])
	require.Equal(t, 4280.0, requests[2]["sl"])
	require.Equal(t, float64(positions[1]), requests[3]["position"])
	require.Equal(t, 4280.0, requests[3]["sl"])
	want := [2]float64{4280, 4300}
	require.Equal(t, [][2]float64{want, want}, m5fLevelsOf(t, f, positions))
}
