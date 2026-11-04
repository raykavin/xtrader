//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The exposure limit of the agents, as they claim their entries: off, every
// entry goes; with a rule for the asset, an agent already on it in the
// direction goes, the other direction counts apart, and past the limit an
// entry is refused with the agents on it; an asset without a rule free;
// agents claiming at the same second never pass it together, a place granted
// held until its order shows or a while passes; a pending entry takes a place;
// a rule changed or removed; an account that can't be read lets nothing in
// blind.
func TestExposure_Claims(t *testing.T) {
	s, _, _ := newDailySupervised(t) // trend, magic 101
	ctx := context.Background()
	s.liveAgent(t, "orb", 102, false)
	s.liveAgent(t, "sweeper", 103, false)
	g := s.managed.manager.exposure
	require.NotNil(t, g, "set by ServeSettings")
	clock := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return clock }
	id := func(name string) int64 {
		t.Helper()
		a, err := s.store.AgentByName(ctx, name)
		require.NoError(t, err)
		return a.ID
	}
	claim := func(name, pair, side string) (bool, string) {
		t.Helper()
		g.heldAt = time.Time{} // a read of now
		granted, reason, err := g.claim(ctx, id(name), pair, side)
		require.NoError(t, err)
		return granted, reason
	}
	set := func(settings ui.ExposureSettings) {
		t.Helper()
		_, err := s.store.SetExposureLimits(ctx, settings, "admin")
		require.NoError(t, err)
	}

	s.bridge.set(protected(1, 101)) // trend bought XAUUSDm
	granted, _ := claim("orb", "XAUUSDm", "BUY")
	assert.True(t, granted, "off: every entry goes")

	rules := map[string]int{"XAUUSDm": 1, "BTCUSDm": 1, "EURUSDm": 1}
	set(ui.ExposureSettings{Pairs: rules})
	granted, reason := claim("orb", "XAUUSDm", "BUY")
	assert.False(t, granted)
	assert.Equal(t, "Exposição máxima em XAUUSDm: 1 agente já está comprado (trend)", reason)
	granted, _ = claim("trend", "XAUUSDm", "BUY")
	assert.True(t, granted, "already on it")
	granted, _ = claim("orb", "XAUUSDm", "SELL")
	assert.True(t, granted, "the other direction apart")
	s.bridge.set(protected(1, 101), position(2, 102, "USOILm", false, 0.01, 0, 0))
	granted, _ = claim("sweeper", "USOILm", "BUY")
	assert.True(t, granted, "no rule for USOILm: free")
	s.bridge.set(protected(1, 101))
	s.agent(t, "papertrend", "trend", true, func(a *store.Agent) { a.Mode = "paper" })
	granted, _ = claim("papertrend", "XAUUSDm", "BUY")
	assert.True(t, granted, "in paper: no position on an account")
	assert.NotContains(t, g.claims[exposureKey{"XAUUSDm", "BUY"}], id("papertrend"), "no place held")

	// at the same second: the place granted held, then freed once it passes
	granted, _ = claim("orb", "BTCUSDm", "BUY")
	assert.True(t, granted)
	granted, reason = claim("sweeper", "BTCUSDm", "BUY")
	assert.False(t, granted, "orb holds the only place, its order not shown yet")
	assert.Contains(t, reason, "(orb)")
	clock = clock.Add(claimHeld + time.Second)
	granted, _ = claim("sweeper", "BTCUSDm", "BUY")
	assert.True(t, granted, "orb's place freed, its order never sent")

	// a pending entry takes a place; a pending order of an agent holding the
	// asset doesn't (it protects or adds to its position)
	s.bridge.set()
	s.bridge.mu.Lock()
	s.bridge.orders = []bridgeOrder{{Ticket: 30, Type: 2, Magic: 103, Symbol: "XAUUSDm", PriceOpen: 4100,
		VolumeCurrent: 0.01}}
	s.bridge.mu.Unlock()
	granted, reason = claim("trend", "XAUUSDm", "BUY")
	assert.False(t, granted)
	assert.Contains(t, reason, "(sweeper)")

	// a rule changed, then removed
	set(ui.ExposureSettings{Pairs: map[string]int{"XAUUSDm": 2, "EURUSDm": 1}})
	granted, _ = claim("trend", "XAUUSDm", "BUY")
	assert.True(t, granted, "2 places on XAUUSDm")
	set(ui.ExposureSettings{Pairs: map[string]int{"EURUSDm": 1}})
	granted, _ = claim("orb", "XAUUSDm", "BUY")
	assert.True(t, granted, "no rule for XAUUSDm anymore")

	// the account unread: nothing let in blind
	s.bridge.mu.Lock()
	s.bridge.status = http.StatusBadGateway
	s.bridge.mu.Unlock()
	granted, reason = claim("orb", "EURUSDm", "BUY")
	assert.False(t, granted)
	assert.Contains(t, reason, "Não foi possível conferir a exposição")
	s.bridge.mu.Lock()
	s.bridge.status = 0
	s.bridge.mu.Unlock()
}

// The exposure limit on the dashboard: set and read, refused out of its bounds,
// and told to every agent so that it claims its entries.
func TestExposure_Settings(t *testing.T) {
	s, _, _ := newDailySupervised(t)
	ctx := context.Background()
	call, _ := settingsSession(t, s.managed)
	resp, body := call(http.MethodPut, "/api/settings/exposure", ui.ExposureRequest{Settings: ui.ExposureSettings{
		Pairs: map[string]int{" XAUUSDm ": 1, "BTCUSDm": 2}}})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var got ui.ExposureResponse
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	assert.Equal(t, map[string]int{"XAUUSDm": 1, "BTCUSDm": 2}, got.Settings.Pairs, "trimmed")
	assert.Equal(t, "admin", got.Settings.UpdatedBy)

	resp, body = call(http.MethodGet, "/api/settings/exposure", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	a, err := s.store.AgentByName(ctx, "trend")
	require.NoError(t, err)
	entries, err := entriesOf(ctx, s.store, a)
	require.NoError(t, err)
	assert.True(t, entries.Exposure, "the agents told to claim their entries")

	for _, refused := range []ui.ExposureSettings{
		{Pairs: map[string]int{"XAU USD": 1}},
		{Pairs: map[string]int{"XAUUSDm": 0}},
		{Pairs: map[string]int{"XAUUSDm": 200}},
	} {
		resp, body = call(http.MethodPut, "/api/settings/exposure", ui.ExposureRequest{Settings: refused})
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
	}

	resp, _ = call(http.MethodPut, "/api/settings/exposure", ui.ExposureRequest{})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	entries, err = entriesOf(ctx, s.store, a)
	require.NoError(t, err)
	assert.False(t, entries.Exposure, "off: no claim")
}
