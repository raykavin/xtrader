package ui

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The catalog holds every permission once, "area:action", each area seen
// and/or changed, labeled; a manage implies the view of its area when it has
// one.
func TestPermissionAreas_EveryPermissionOnce(t *testing.T) {
	all := AllPermissions()
	format := regexp.MustCompile(`^[a-z0-9-]+:[a-z]+$`)
	seen := map[string]bool{}
	for _, p := range all {
		assert.Regexp(t, format, p)
		assert.False(t, seen[p], "%s once", p)
		seen[p] = true
	}
	for _, p := range []string{PermDashboardView, PermTradingOperate, PermSupervisorChat, PermPerformanceView,
		PermBacktestsView, PermBacktestsManage, PermAgentsView, PermAgentsManage, PermMT5AccountsView,
		PermMT5AccountsManage, PermLimitsView, PermLimitsManage, PermNewsView, PermNewsManage, PermPromptsView,
		PermPromptsManage, PermSupervisorView, PermSupervisorManage, PermTelegramView, PermTelegramManage,
		PermUsersView, PermUsersManage} {
		assert.True(t, seen[p], "%s in the catalog", p)
	}
	assert.Len(t, all, 22)
	for _, area := range PermissionAreas() {
		require.NotEmpty(t, area.Label, area.Key)
		require.NotEmpty(t, area.Hint, area.Key)
		require.True(t, area.View != "" || area.Manage != "", area.Key)
		if area.View != "" {
			assert.NotEmpty(t, area.ViewLabel, area.Key)
		}
		if area.Manage != "" {
			assert.NotEmpty(t, area.ManageLabel, area.Key)
		}
		if area.View != "" && area.Manage != "" {
			assert.Equal(t, []string{area.View, area.Manage}, Effective([]string{area.Manage}), area.Key)
		}
	}
	assert.True(t, KnownPermission(PermAgentsManage))
	assert.False(t, KnownPermission("agents:delete"))
}

// The effective permissions: the implied ones added, in the order of the
// catalog, the unknown ones dropped.
func TestEffective_AddsTheImplied(t *testing.T) {
	assert.Equal(t, []string{}, Effective(nil))
	assert.Equal(t, []string{PermDashboardView, PermTradingOperate}, Effective([]string{PermTradingOperate}))
	assert.Equal(t, []string{PermDashboardView, PermSupervisorChat}, Effective([]string{PermSupervisorChat}))
	assert.Equal(t, []string{PermAgentsView, PermAgentsManage, PermUsersView},
		Effective([]string{PermUsersView, PermAgentsManage, "made:up", PermAgentsManage}))
	assert.Equal(t, AllPermissions(), Effective(AllPermissions()))
}
