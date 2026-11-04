package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

// The exposure limit of the agents, by rules: off while never set; set with
// who and its rules, an asset without a rule without a limit; refused for a
// limit out of 1 to 100, an asset MT5 wouldn't name or too many assets, the
// ones set kept; no rule clearing them and turning it off.
func TestExposure_Limits(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	limits, found, err := s.ExposureLimits(ctx)
	require.NoError(t, err)
	assert.False(t, found)
	assert.False(t, limits.On())
	assert.Equal(t, 0, limits.LimitOf("XAUUSDm"))

	_, err = s.SetExposureLimits(ctx, ui.ExposureSettings{
		Pairs: map[string]int{"XAUUSDm": 1, "US500.cash": 3}}, "admin")
	require.NoError(t, err)
	limits, found, err = s.ExposureLimits(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, limits.On())
	assert.Equal(t, 1, limits.LimitOf("XAUUSDm"))
	assert.Equal(t, 3, limits.LimitOf("US500.cash"))
	assert.Equal(t, 0, limits.LimitOf("BTCUSDm"), "no rule: no limit")
	settings := limits.Settings()
	assert.Equal(t, "admin", settings.UpdatedBy)
	assert.NotZero(t, settings.UpdatedAt)

	many := make(map[string]int, maxExposurePairs+1)
	for i := range maxExposurePairs + 1 {
		many[fmt.Sprintf("PAIR%d", i)] = 1
	}
	for _, refused := range []ui.ExposureSettings{
		{Pairs: map[string]int{"XAU USDm": 1}},
		{Pairs: map[string]int{"": 1}},
		{Pairs: map[string]int{"XAUUSDm": 0}},
		{Pairs: map[string]int{"XAUUSDm": -1}},
		{Pairs: map[string]int{"XAUUSDm": 101}},
		{Pairs: many},
	} {
		_, err = s.SetExposureLimits(ctx, refused, "admin")
		assert.ErrorIs(t, err, ErrInvalid, "%d assets", len(refused.Pairs))
	}
	limits, _, err = s.ExposureLimits(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, limits.LimitOf("XAUUSDm"), "kept")

	_, err = s.SetExposureLimits(ctx, ui.ExposureSettings{}, "admin")
	require.NoError(t, err)
	limits, _, err = s.ExposureLimits(ctx)
	require.NoError(t, err)
	assert.False(t, limits.On())
	assert.Nil(t, limits.Settings().Pairs, "cleared")
}
