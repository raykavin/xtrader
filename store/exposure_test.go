package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

// The exposure limit of the agents: off while never set; set with who and its
// assets, an asset's own limit over the one of every asset, an own 0 lifting
// it there; refused out of its bounds and for an asset MT5 wouldn't name, the
// one set kept; an empty one clearing the assets and turning it off.
func TestExposure_Limits(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	limits, found, err := s.ExposureLimits(ctx)
	require.NoError(t, err)
	assert.False(t, found)
	assert.False(t, limits.On())
	assert.Equal(t, 0, limits.LimitOf("XAUUSDm"))

	_, err = s.SetExposureLimits(ctx, ui.ExposureSettings{MaxAgents: 2,
		Pairs: map[string]int{"XAUUSDm": 1, "US500.cash": 0}}, "admin")
	require.NoError(t, err)
	limits, found, err = s.ExposureLimits(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, limits.On())
	assert.Equal(t, 1, limits.LimitOf("XAUUSDm"), "its own")
	assert.Equal(t, 0, limits.LimitOf("US500.cash"), "lifted there")
	assert.Equal(t, 2, limits.LimitOf("BTCUSDm"), "the one of every asset")
	settings := limits.Settings()
	assert.Equal(t, "admin", settings.UpdatedBy)
	assert.NotZero(t, settings.UpdatedAt)

	for _, refused := range []ui.ExposureSettings{
		{MaxAgents: -1},
		{MaxAgents: 101},
		{Pairs: map[string]int{"XAU USDm": 1}},
		{Pairs: map[string]int{"": 1}},
		{Pairs: map[string]int{"XAUUSDm": 101}},
	} {
		_, err = s.SetExposureLimits(ctx, refused, "admin")
		assert.ErrorIs(t, err, ErrInvalid, "%+v", refused)
	}
	limits, _, err = s.ExposureLimits(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, limits.MaxAgents, "kept")

	_, err = s.SetExposureLimits(ctx, ui.ExposureSettings{Pairs: map[string]int{"XAUUSDm": 1}}, "admin")
	require.NoError(t, err)
	limits, _, err = s.ExposureLimits(ctx)
	require.NoError(t, err)
	assert.True(t, limits.On(), "an asset's own limit alone")
	assert.Equal(t, 0, limits.LimitOf("BTCUSDm"))

	_, err = s.SetExposureLimits(ctx, ui.ExposureSettings{}, "admin")
	require.NoError(t, err)
	limits, _, err = s.ExposureLimits(ctx)
	require.NoError(t, err)
	assert.False(t, limits.On())
	assert.Nil(t, limits.Settings().Pairs, "cleared")
}
